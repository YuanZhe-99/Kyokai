// Package localapi is the protocol between the GUI (and `kyokai-svc ctl`)
// and the engine, adapted from Easytier-MyGo. The engine owns the device
// identity and the host role; clients read state and send commands.
//
// Transport: a Unix domain socket (AF_UNIX also on Windows),
// newline-delimited JSON. A request names the protocol version; a mismatch
// fails instead of guessing. Most requests are one request and one
// response; a session (RequestSessionAttach) keeps its connection open and
// receives pushed status until either side closes it.
package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ProtocolVersion is bumped when a request or response changes meaning.
const ProtocolVersion = 2

// SocketEnv overrides the socket path (tests, development).
const SocketEnv = "KYOKAI_SOCKET"

// UserSocketPath is the No privileges engine's per-user socket:
// $KYOKAI_SOCKET, else the user's temporary directory (%TEMP% on Windows,
// a per-user folder under $TMPDIR on macOS) + kyokai/engine[-profile].sock.
func UserSocketPath(profile string) string {
	if p := os.Getenv(SocketEnv); p != "" {
		return p
	}
	name := "engine.sock"
	if profile != "" {
		name = "engine-" + profile + ".sock"
	}
	return filepath.Join(userRuntimeDir(), "kyokai", name)
}

// RequestType enumerates the engine's commands.
type RequestType string

const (
	RequestStatus               RequestType = "status"                 // identity summary, never a secret
	RequestPasswordReveal       RequestType = "password.reveal"        // the temporary password, for the host's own GUI
	RequestPasswordRegenerate   RequestType = "password.regenerate"    // a new temporary password
	RequestNetworkGet           RequestType = "network.get"            // the first network's settings without the secret (an older GUI, ctl)
	RequestNetworkSet           RequestType = "network.set"            // the first network's new settings (Request.Network)
	RequestNetworkProfiles      RequestType = "network.profiles"       // every network kept, without the secrets
	RequestNetworkProfileSet    RequestType = "network.profile.set"    // add or change a network (Request.Profile)
	RequestNetworkProfileRemove RequestType = "network.profile.remove" // forget a network (Request.Profile's ID)
	RequestNetworkDirect        RequestType = "network.direct"         // try direct paths in every network (Request.Direct)
	RequestPermanentSet         RequestType = "password.set_permanent" // set or remove the permanent password (Request.Permanent)
	RequestConnect              RequestType = "session.connect"        // start a session with a device (Request.Connect)
	RequestIdentify             RequestType = "session.identify"       // which device answers at an address (Request.Address → Response.DeviceID or Response.Failure)
	RequestDeviceRemove         RequestType = "device.remove"          // take a host off Home: end its sessions from here, forget its key and saved password (Request.DeviceID)
	RequestClose                RequestType = "session.close"          // end a session (Request.ID)
	RequestDecide               RequestType = "request.decide"         // answer a host confirmation (Request.Decide)
	RequestTrustedList          RequestType = "trusted.list"           // the trusted devices
	RequestTrustedRemove        RequestType = "trusted.remove"         // forget a trusted device (Request.Key)
	RequestHostForget           RequestType = "host.forget"            // forget a pinned host key (Request.DeviceID)
	RequestForgetPassword       RequestType = "password.forget_saved"  // forget the password saved for a host (Request.DeviceID)
	RequestIdentityExport       RequestType = "identity.export"        // an encrypted identity backup (Request.Backup.Passphrase)
	RequestIdentityImport       RequestType = "identity.import"        // restore an identity backup (Request.Backup)
	RequestIdentityReset        RequestType = "identity.reset"         // a new identity and Device ID
	RequestNameSet              RequestType = "device.set_name"        // this device's friendly name (Request.Name; "" follows the computer's name)
	RequestAccessSettings       RequestType = "access.settings"        // which passwords open this host (Request.PasswordMode), whether a password admits without asking (Request.WithoutAsking) and connections by address (Request.ByAddress)
	RequestAgentRestart         RequestType = "helper.agent_restart"   // the helper's session agent starts anew (macOS counts new privacy grants only then)
	RequestStartOver            RequestType = "secrets.start_over"     // the saved identity cannot be unlocked: move it aside and keep a new one
	RequestPermission           RequestType = "system.permission"      // ask the system for a privacy permission (Request.Permission: "screen", "accessibility", "input-monitoring" or "keychain"; macOS)
	RequestPasswordGrants       RequestType = "access.password_grants" // what a correct password grants without asking (Request.Grants)
	RequestProtect              RequestType = "protect.set"            // protected mode on (needs a permanent password) or off (with it) (Request.Protect); while on, the engine serves only the status, the AI requests, confirmations, disconnecting, quitting and what the GUI sends as it attaches
	RequestLogs                 RequestType = "logs.set"               // keep no logs, crash traces or connection history (Request.LogsOff), or keep them again; the GUI sends it as it attaches and when the setting changes
	RequestCaptureShow          RequestType = "capture.show"           // whether captures show Kyokai's windows (Request.Show); hidden until a GUI says otherwise
	RequestInputKyokai          RequestType = "input.kyokai"           // whether a person's remote input may reach Kyokai's windows (Request.KyokaiInput); refused until a GUI says otherwise
	RequestSoundSource          RequestType = "sound.source"           // the sound to share (Request.SoundSource): "" what the computer plays, else an input device's id
	RequestSoundDevices         RequestType = "sound.devices"          // the input devices that may be shared (Response.SoundDevices)
	RequestBackground           RequestType = "helper.background"      // the helper serves from boot (Request.Background) or only while a GUI is attached
	RequestStoreExport          RequestType = "store.export"           // the whole store sealed for this device's other engine (Request.Backup.Passphrase → Response.Document)
	RequestStoreImport          RequestType = "store.import"           // take over a store this device's other engine sealed (Request.Backup)
	RequestStoreClear           RequestType = "store.clear"            // forget the moved store and stop: the other engine holds it now
	RequestHelperRemove         RequestType = "helper.remove"          // the helper removes itself, its state included (macOS), after the store moved out
	RequestHelperUpdate         RequestType = "helper.update"          // the helper installs the signed bundle in Request.From and restarts
	RequestAIModels             RequestType = "ai.models.list"         // the models a source lists (Request.Source → Response.AIModels), only when asked
	RequestMedia                RequestType = "session.media"          // a controller session's media (Request.ID); the connection then carries packets
	RequestSessionAttach        RequestType = "session.attach"         // streaming; see package doc
	RequestAISettingsGet        RequestType = "ai.settings.get"        // the AI settings and which keys are stored
	RequestAISettingsSet        RequestType = "ai.settings.set"        // change the AI settings (Request.AISettings)
	RequestAIKeySet             RequestType = "ai.key.set"             // store or remove a provider key (Request.AIKey)
	RequestAITaskStart          RequestType = "ai.task.start"          // start an AI task (Request.AITask)
	RequestAIDraft              RequestType = "ai.draft.set"           // the shared draft of the next instruction (Request.AITask.Text)
	RequestAITaskReply          RequestType = "ai.task.reply"          // continue a finished task (Request.ID, Request.AITask.Text)
	RequestAITaskPause          RequestType = "ai.task.pause"          // pause a task (Request.ID)
	RequestAITaskResume         RequestType = "ai.task.resume"         // resume a paused task (Request.ID)
	RequestAITaskCancel         RequestType = "ai.task.cancel"         // cancel a task (Request.ID)
	RequestAITaskDecide         RequestType = "ai.task.decide"         // answer an approval (Request.ID, Request.AITask.Approve)
	RequestAITasks              RequestType = "ai.tasks"               // every task, newest first
	RequestAIHistoryClear       RequestType = "ai.history.clear"       // forget the finished tasks
	RequestAIStop               RequestType = "ai.stop"                // the emergency stop: cancel every task
	RequestShutdown             RequestType = "shutdown"
)

// ErrorCode classifies a failed request for clients that react to it.
type ErrorCode string

const (
	CodeUnknown  ErrorCode = "unknown-request"
	CodeInternal ErrorCode = "internal"
	CodeInvalid  ErrorCode = "invalid" // the request's parameters were refused; Error says why
)

// Request is one line from a client.
type Request struct {
	Version int         `json:"version"`
	Type    RequestType `json:"type"`
	// Network carries the settings of RequestNetworkSet.
	Network *NetworkSettings `json:"network,omitempty"`
	// Profile is the network RequestNetworkProfileSet adds or changes, or
	// (its ID) the one RequestNetworkProfileRemove forgets.
	Profile *NetworkChange `json:"profile,omitempty"`
	// Direct is RequestNetworkDirect's: try direct paths in every network.
	Direct *bool `json:"direct,omitempty"`
	// Permanent sets (a password) or removes (nil Password) the permanent
	// password.
	Permanent *PermanentPassword `json:"permanent,omitempty"`
	Connect   *ConnectRequest    `json:"connect,omitempty"`
	Decide    *Decision          `json:"decide,omitempty"`
	// ID names a session (RequestClose).
	ID uint64 `json:"id,omitempty"`
	// Key names a trusted device by its device key (RequestTrustedRemove).
	Key string `json:"key,omitempty"`
	// DeviceID names a host whose pinned key to forget (RequestHostForget).
	DeviceID string `json:"device_id,omitempty"`
	// Name is the friendly name of RequestNameSet.
	Name *string `json:"name,omitempty"`
	// Source names an AI source (RequestAIModels).
	Source string `json:"source,omitempty"`
	// From is the app folder whose signed bundle RequestHelperUpdate
	// installs.
	From string `json:"from,omitempty"`
	// PasswordMode ("temporary", "permanent", "both") and WithoutAsking
	// are RequestAccessSettings' (empty or nil: unchanged).
	PasswordMode  string `json:"password_mode,omitempty"`
	WithoutAsking *bool  `json:"without_asking,omitempty"`
	// ByAddress replaces whether and from where this host accepts
	// connections by address (RequestAccessSettings).
	ByAddress *ByAddress `json:"by_address,omitempty"`
	// Address is the IP address or domain name RequestIdentify dials.
	Address string `json:"address,omitempty"`
	// Permission names what RequestPermission asks the system for.
	Permission string `json:"permission,omitempty"`
	// Grants are the permissions of RequestPasswordGrants (empty: none).
	Grants *[]string `json:"grants,omitempty"`
	// Protect is RequestProtect's.
	Protect *ProtectRequest `json:"protect,omitempty"`
	// LogsOff is RequestLogs': true keeps no logs, crash traces or
	// connection history (and deletes the logs written).
	LogsOff *bool `json:"logs_off,omitempty"`
	// Show is RequestCaptureShow's: true lets captures see Kyokai's
	// windows ("Hide Kyokai from capture" off).
	Show *bool `json:"show,omitempty"`
	// KyokaiInput is RequestInputKyokai's: true lets a person's remote input
	// reach Kyokai's windows ("Hide Kyokai from capture" off; never the
	// host's AI, nor while a confirmation or an approval waits).
	KyokaiInput *bool `json:"kyokai_input,omitempty"`
	// SoundSource is RequestSoundSource's: "" for what the computer
	// plays, else an input device's id.
	SoundSource *string `json:"sound_source,omitempty"`
	// Background chooses Background service (true) or Follow GUI
	// (RequestBackground).
	Background *bool `json:"background,omitempty"`
	// Backup carries the passphrase (and for an import the document).
	Backup *IdentityBackup `json:"backup,omitempty"`
	// AISettings carries the fields RequestAISettingsSet changes.
	AISettings *AISettings    `json:"ai_settings,omitempty"`
	AIKey      *AIKey         `json:"ai_key,omitempty"`
	AITask     *AITaskRequest `json:"ai_task,omitempty"`
}

// AISettings are the host's AI settings. In a request, empty fields stay
// as they are; Keys is only in a response.
type AISettings struct {
	// Sources are the providers set up on this host.
	Sources []AISource `json:"sources,omitempty"`
	// Model is the one tasks use.
	Model *AIModelRef `json:"model,omitempty"`
	// Approval is "flagged" or "every-action".
	Approval string `json:"approval,omitempty"`
	// Keys says which sources have a stored key (a response only); the
	// keys never leave the engine.
	Keys map[string]bool `json:"keys,omitempty"`
}

// AISource is a model provider: a template's endpoint or the user's own,
// and the models used from it (doc/en-us/computer-use.md, "Providers").
type AISource struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Template string `json:"template"`
	// Protocol is "anthropic", "openai-responses" or "openai-chat".
	Protocol string `json:"protocol"`
	// URL is the base URL (`https://api.anthropic.com/v1`).
	URL    string    `json:"url"`
	Models []AIModel `json:"models"`
}

// AIModel is a model of a source: the id it expects and the name it lists,
// if any.
type AIModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// AIModelRef names one model of one source.
type AIModelRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// AIKey stores (Key set) or removes (Key nil) a source's API key.
type AIKey struct {
	Source string  `json:"source"`
	Key    *string `json:"key"`
}

// AITaskRequest starts a task (Key, Instruction), continues one (Text) or
// answers its approval (Approve).
type AITaskRequest struct {
	// Key makes the start idempotent: a second start with it returns the
	// first task.
	Key         string `json:"key,omitempty"`
	Instruction string `json:"instruction,omitempty"`
	Text        string `json:"text,omitempty"`
	Approve     bool   `json:"approve,omitempty"`
}

// AITask is one AI task on this host.
type AITask struct {
	ID          uint64 `json:"id"`
	Instruction string `json:"instruction"`
	// Origin is the Device ID of the controller that started the task,
	// empty for this computer's own user.
	Origin string `json:"origin,omitempty"`
	// Source is the AI source the task uses.
	Source string `json:"source"`
	Model  string `json:"model"`
	// State is "running", "approval", "paused", "done", "failed" or
	// "cancelled".
	State string `json:"state"`
	// Paused says why: "host-user", "local-input", "controller",
	// "take-over", "disconnected" or "sleep".
	Paused string `json:"paused,omitempty"`
	// Error says why the task failed; safe to show.
	Error    string      `json:"error,omitempty"`
	Text     string      `json:"text"`
	Progress []string    `json:"progress"`
	Steps    []AIStep    `json:"steps"`
	Approval *AIApproval `json:"approval,omitempty"`
	Created  int64       `json:"created"` // Unix seconds
	Updated  int64       `json:"updated"`
}

// AIStep is one action of a task and what became of it.
type AIStep struct {
	At int64 `json:"at"`
	// Action is the action ({"kind": "click", …}; doc/en-us/computer-use.md),
	// null when the model's proposal could not be read.
	Action json.RawMessage `json:"action"`
	// Outcome is "ok", "failed", "declined", "not-executed" or "paused".
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// AIApproval is an action waiting for the host user.
type AIApproval struct {
	Action json.RawMessage `json:"action"`
	// Flagged are the provider's safety notes for the turn, if any.
	Flagged []string `json:"flagged"`
}

// AIActive is the task in progress, as the status shows it.
type AIActive struct {
	ID     uint64 `json:"id"`
	State  string `json:"state"`
	Paused string `json:"paused,omitempty"`
	// Origin is the controller that started it, empty for this computer's
	// own user.
	Origin string `json:"origin,omitempty"`
}

// IdentityBackup is an identity backup document and its passphrase.
type IdentityBackup struct {
	Document   string `json:"document,omitempty"`
	Passphrase string `json:"passphrase"`
}

// PermanentPassword is the request body of RequestPermanentSet.
type PermanentPassword struct {
	Password *string `json:"password"`
}

// ConnectRequest asks the engine to start a session with a device.
type ConnectRequest struct {
	DeviceID string `json:"device_id"`
	// Password is the host's password; empty uses the one saved for the
	// host, or else asks the host's user without one.
	Password string   `json:"password"`
	Want     []string `json:"want,omitempty"`
	// Remember keeps the password for this host once it accepts the login.
	Remember bool `json:"remember,omitempty"`
	// Address dials this IP address or domain name (with or without a
	// port) instead of the network; the device there must prove to be
	// DeviceID.
	Address string `json:"address,omitempty"`
}

// ProtectRequest turns protected mode on, or off with the permanent
// password.
type ProtectRequest struct {
	On       bool   `json:"on"`
	Password string `json:"password,omitempty"`
}

// ByAddress is whether a host accepts connections by address (TCP on
// this computer's own addresses, port 47310) and the source address
// ranges it accepts them from.
type ByAddress struct {
	On     bool     `json:"on"`
	Ranges []string `json:"ranges"`
	// Port is where it listens, 0 when it does not (status only).
	Port int `json:"port,omitempty"`
	// Error says why it could not listen (status only).
	Error string `json:"error,omitempty"`
}

// Decision answers a host confirmation; Permissions are the granted ones.
type Decision struct {
	ID          uint64   `json:"id"`
	Allow       bool     `json:"allow"`
	Permissions []string `json:"permissions,omitempty"`
}

// Permission names, as the wire and the core spell them.
const (
	PermViewScreen      = "view-screen"
	PermControlInput    = "control-input"
	PermRunAITask       = "run-ai-task"
	PermApproveAIAction = "approve-ai-action"
	PermClipboard       = "clipboard"
	PermFiles           = "files"
	PermHearSound       = "hear-sound"
)

// AllPermissions in the order the interface lists them.
var AllPermissions = []string{PermViewScreen, PermControlInput, PermRunAITask, PermApproveAIAction, PermClipboard, PermFiles, PermHearSound}

// Response is one line from the engine: an answer, or a pushed frame of a
// session.
type Response struct {
	Version  int       `json:"version"`
	OK       bool      `json:"ok"`
	Code     ErrorCode `json:"code,omitempty"`
	Error    string    `json:"error,omitempty"`
	Status   *Status   `json:"status,omitempty"`
	Password string    `json:"password,omitempty"`
	// Network answers RequestNetworkGet and RequestNetworkSet.
	Network *NetworkSettings `json:"network,omitempty"`
	// Networks answers the requests about the networks kept.
	Networks *NetworkProfiles `json:"networks,omitempty"`
	// ID answers RequestConnect: the new session's id.
	ID uint64 `json:"id,omitempty"`
	// DeviceID answers RequestIdentify: the device at the address, else
	// Failure says why there is none: "unreachable", "incompatible",
	// "broken" (not a Kyokai device) or "self" (this device).
	DeviceID string `json:"device_id,omitempty"`
	Failure  string `json:"failure,omitempty"`
	// Trusted answers RequestTrustedList.
	Trusted []TrustedDevice `json:"trusted,omitempty"`
	// Document answers RequestIdentityExport: the encrypted backup.
	Document string `json:"document,omitempty"`
	// SoundDevices answers RequestSoundDevices.
	SoundDevices []SoundDevice `json:"sound_devices,omitempty"`
	// AISettings answers the AI settings and key requests.
	AISettings *AISettings `json:"ai_settings,omitempty"`
	// AITasks answers RequestAITasks.
	AITasks []AITask `json:"ai_tasks,omitempty"`
	// AIModels are the models a source listed (RequestAIModels).
	AIModels []AIModel `json:"ai_models,omitempty"`
}

// TrustedDevice is a controller this host accepted before.
type TrustedDevice struct {
	Key      string   `json:"key"`
	DeviceID string   `json:"device_id"`
	First    int64    `json:"first"` // Unix seconds
	Last     int64    `json:"last"`
	Granted  []string `json:"granted"`
	// About is what the controller last said about itself.
	About About `json:"about"`
}

// About is what a device says about itself after a login: a friendly
// name, its maker and model, and its system ("windows", "macos"). It is a
// claim of the other device, shown beside its Device ID, never proof.
type About struct {
	Name  string `json:"name"`
	Maker string `json:"maker"`
	Model string `json:"model"`
	OS    string `json:"os"`
}

// HostRequest is a controller waiting for this host's user to confirm.
type HostRequest struct {
	ID       uint64 `json:"id"`
	DeviceID string `json:"device_id"`
	// Known: the controller's key was accepted before. NewKey: its Device ID
	// is known with another key.
	Known  bool `json:"known"`
	NewKey bool `json:"new_key"`
	// Password: the controller proved a password and asks for more than
	// a password grants; false: it asks without one.
	Password bool     `json:"password"`
	Want     []string `json:"want"`
	// About is what the controller says about itself.
	About About `json:"about"`
}

// RecentDevice is a host this device connected to.
type RecentDevice struct {
	DeviceID string `json:"device_id"`
	Last     int64  `json:"last"` // Unix seconds
	// About is what the host said about itself in the last session.
	About About `json:"about"`
	// SavedPassword says whether a password is saved for the host (never
	// the password).
	SavedPassword bool `json:"saved_password,omitempty"`
	// Address is the address the last session dialed, empty over the
	// network.
	Address string `json:"address,omitempty"`
}

// SessionInfo is one session of this device, live or recently finished.
type SessionInfo struct {
	ID   uint64 `json:"id"`
	Role string `json:"role"` // "host" or "controller"
	// DeviceID is the other device.
	DeviceID string `json:"device_id"`
	// State is "connecting", "confirming", "connected", "reconnecting",
	// "closed" or "failed".
	State string `json:"state"`
	// Failure says why a session failed: a name, or {"refused": reason}.
	Failure     json.RawMessage `json:"failure,omitempty"`
	Granted     []string        `json:"granted"`
	PeerVersion string          `json:"peer_version"`
	PeerSource  string          `json:"peer_source"`
	// Peer is what the other device said about itself after the login.
	Peer About `json:"peer"`
}

// SoundDevice is an input device the host may share instead of what it
// plays: its system id and name.
type SoundDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NetworkSettings are the first EasyTier network the engine keeps, as an
// older GUI and ctl set it (RequestNetworkGet, RequestNetworkSet).
type NetworkSettings struct {
	// Servers are standard EasyTier server URLs, tried in order.
	Servers []string `json:"servers"`
	Name    string   `json:"name"`
	// Secret is set only in a request; nil keeps the stored secret.
	Secret *string `json:"secret,omitempty"`
	// HasSecret and WeakSecret describe the stored secret in a response.
	HasSecret  bool `json:"has_secret,omitempty"`
	WeakSecret bool `json:"weak_secret,omitempty"`
	// Direct tries hole-punched paths and UPnP (Windows Firewall may ask);
	// nil in a request keeps the current choice. The helper always does.
	Direct *bool `json:"direct,omitempty"`
}

// NetworkProfiles are the EasyTier networks the engine keeps, joined or
// not, and whether every one tries direct paths (always in the helper).
type NetworkProfiles struct {
	Direct   bool             `json:"direct"`
	Profiles []NetworkProfile `json:"profiles"`
}

// NetworkProfile is one network kept: its ID, the name shown for it (empty:
// its network name), its servers and EasyTier network name, its secret as
// HasSecret and WeakSecret only, and whether this device joins it.
type NetworkProfile struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Servers    []string `json:"servers"`
	Name       string   `json:"name"`
	HasSecret  bool     `json:"has_secret"`
	WeakSecret bool     `json:"weak_secret"`
	Joined     bool     `json:"joined"`
}

// NetworkChange adds a network (no ID: Servers and Name needed, joined
// unless Joined says otherwise) or changes one, where nil or empty fields
// keep their values (the secret too).
type NetworkChange struct {
	ID      string   `json:"id,omitempty"`
	Label   *string  `json:"label,omitempty"`
	Servers []string `json:"servers,omitempty"`
	Name    *string  `json:"name,omitempty"`
	// Secret travels only towards the engine.
	Secret *string `json:"secret,omitempty"`
	Joined *bool   `json:"joined,omitempty"`
}

// NetworkNode is how EasyTier nodes are doing: all of them together, or one
// network's.
type NetworkNode struct {
	// State is "off", "connecting", "online" or "error".
	State string `json:"state"`
	// Error says why the node stopped (State "error").
	Error string `json:"error,omitempty"`
	// Address is the node's virtual IPv4 address once assigned.
	Address string `json:"address,omitempty"`
	// NAT is the detected NAT type ("unknown" until STUN answers).
	NAT string `json:"nat,omitempty"`
	// ListenPort is where other devices' nodes connect to this one
	// directly (direct connections on), 0 when it does not listen.
	ListenPort int             `json:"listen_port,omitempty"`
	Devices    []NetworkDevice `json:"devices,omitempty"`
}

// NetworkStatus is the engine's EasyTier nodes as they are now: all of
// them together — online when one is, each device once by its best path,
// the address of the first network online — and each network's.
type NetworkStatus struct {
	NetworkNode
	Networks []NetworkState `json:"networks,omitempty"`
}

// NetworkState is one kept network's node: State "off" while it is not
// joined.
type NetworkState struct {
	ID string `json:"id"`
	// Label is the name it is shown by (its network name when it has none).
	Label  string `json:"label"`
	Joined bool   `json:"joined"`
	NetworkNode
}

// NetworkDevice is another Kyokai device the network routes to. Its
// DeviceID is what the device announces: a hint, never proof of identity.
type NetworkDevice struct {
	DeviceID  string `json:"device_id"`
	Path      string `json:"path"` // "direct" or "relay"
	LatencyMS int    `json:"latency_ms"`
}

// Status is the engine's state: product and ABI version, the device
// identity and how secrets are kept. It never holds a secret.
type Status struct {
	Version string `json:"version"`
	// Build is the engine's build within its version (version.Build).
	Build       string `json:"build,omitempty"`
	ABI         int    `json:"abi"`
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"fingerprint"`
	StateDir    string `json:"state_dir"`
	// RunMode is "user" (No privileges) or "helper" (the privileged helper);
	// Background says whether the helper serves from boot (Background
	// service) or only while a GUI is attached (Follow GUI).
	RunMode    string `json:"run_mode"`
	Background bool   `json:"background,omitempty"`
	// Persistence is "os-store", "memory-only", "unreadable" or "denied"
	// (macOS: the Keychain refused the key).
	Persistence string `json:"persistence"`
	// PasswordSerial changes whenever the temporary password does.
	PasswordSerial uint64        `json:"password_serial"`
	Network        NetworkStatus `json:"network"`
	// PermanentPassword says whether a permanent password is set.
	PermanentPassword bool `json:"permanent_password"`
	// PasswordGrants are the permissions a correct password grants without
	// asking.
	// Permissions are the system's privacy permissions where it has them
	// (macOS), nil elsewhere.
	Permissions    *Permissions `json:"permissions,omitempty"`
	PasswordGrants []string     `json:"password_grants"`
	// PasswordMode is which passwords open this host ("temporary",
	// "permanent", "both"); WithoutAsking whether an accepted password
	// admits without confirmation.
	PasswordMode  string `json:"password_mode"`
	WithoutAsking bool   `json:"without_asking"`
	// ByAddress is whether and from where this host accepts connections
	// by address, and whether it listens.
	ByAddress ByAddress `json:"by_address"`
	// LogsOff: this engine keeps no logs, crash traces or connection
	// history (Settings, No logs).
	LogsOff bool `json:"logs_off,omitempty"`
	// Protected: protected mode is on; the GUI offers only the AI page
	// until the permanent password turns it off.
	Protected bool          `json:"protected,omitempty"`
	Requests  []HostRequest `json:"requests"`
	Sessions  []SessionInfo `json:"sessions"`
	// Recent are the hosts this device connected to, newest first.
	Recent []RecentDevice `json:"recent"`
	// Name is the friendly name chosen here ("" follows the computer's
	// name); About is what this device tells its peers.
	Name  string `json:"name"`
	About About  `json:"about"`
	// AI is the AI task in progress, nil when there is none.
	AI *AIActive `json:"ai,omitempty"`
	// AIDraft is the shared draft of the next AI instruction, as anyone
	// (this device's user or a controller) typed it last.
	AIDraft string    `json:"ai_draft,omitempty"`
	Started time.Time `json:"started"`
}

// Permissions are the macOS privacy permissions the engine needs, as
// granted now.
type Permissions struct {
	// Screen is Screen Recording (capture); a grant counts after Kyokai
	// restarts.
	Screen bool `json:"screen"`
	// Accessibility lets the engine inject input.
	Accessibility bool `json:"accessibility"`
	// InputMonitoring lets the engine notice the host user's own keyboard
	// and mouse, which pause AI tasks and carry the emergency stop.
	InputMonitoring bool `json:"input_monitoring"`
	// Keychain: the Keychain did not refuse the secrets' key; asking for
	// "keychain" opens the store again.
	Keychain bool `json:"keychain"`
}

// ErrNotRunning reports that no engine listens on the socket.
var ErrNotRunning = errors.New("the engine is not running")

// RequestError is a request the engine refused.
type RequestError struct {
	Code    ErrorCode
	Message string
}

func (e *RequestError) Error() string { return e.Message }

// Client talks to one engine socket. Safe for concurrent use.
type Client struct {
	SocketPath string
	Timeout    time.Duration
}

// NewClient returns a client for path.
func NewClient(path string) *Client {
	return &Client{SocketPath: path, Timeout: 15 * time.Second}
}

// Do sends one request and returns the response.
// Returns: ErrNotRunning (wrapped) when nothing listens; a *RequestError
// when the engine refused the request.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := writeRequest(conn, req); err != nil {
		return nil, err
	}
	resp, err := readResponse(bufio.NewReaderSize(conn, maxResponseSize))
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return resp, err
}

// Attach opens a session. fn receives every pushed status until ctx ends,
// the engine goes away, or fn returns an error.
// Returns: the reason the session ended.
func (c *Client) Attach(ctx context.Context, fn func(*Status) error) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := writeRequest(conn, Request{Type: RequestSessionAttach}); err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, maxResponseSize)
	for {
		resp, err := readResponse(r)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if resp.Status != nil {
			if err := fn(resp.Status); err != nil {
				return err
			}
		}
	}
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		var opErr *net.OpError
		if errors.Is(err, os.ErrNotExist) || (errors.As(err, &opErr) && opErr.Op == "dial") {
			return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
		}
		return nil, err
	}
	return conn, nil
}

// maxResponseSize bounds one response line.
const maxResponseSize = 1 << 20

func writeRequest(conn net.Conn, req Request) error {
	req.Version = ProtocolVersion
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(payload, '\n'))
	return err
}

func readResponse(r *bufio.Reader) (*Response, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Version != ProtocolVersion {
		return nil, fmt.Errorf("protocol version mismatch: engine %d, client %d", resp.Version, ProtocolVersion)
	}
	if !resp.OK {
		return &resp, &RequestError{Code: resp.Code, Message: resp.Error}
	}
	return &resp, nil
}

// Fail builds an error response.
func Fail(code ErrorCode, format string, args ...any) Response {
	return Response{OK: false, Code: code, Error: fmt.Sprintf(format, args...)}
}

// MaxPacket is the largest media packet either side accepts.
const MaxPacket = 32 << 20

// WritePacket writes one media packet: a 4-byte big-endian length, then
// the bytes.
func WritePacket(w io.Writer, p []byte) error {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(p)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

// ReadPacket reads one media packet into buf (grown when it is too small).
// Returns: the packet, a slice of the (possibly new) buffer.
func ReadPacket(r io.Reader, buf []byte) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(head[:]))
	if n > MaxPacket {
		return nil, fmt.Errorf("media packet of %d bytes exceeds the limit", n)
	}
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// Media opens a controller session's media stream: after the engine's
// answer the connection carries packets (ReadPacket / WritePacket) until
// either side closes it.
func (c *Client) Media(ctx context.Context, session uint64) (net.Conn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeRequest(conn, Request{Type: RequestMedia, ID: session}); err != nil {
		conn.Close()
		return nil, err
	}
	// Read the answer byte by byte: what follows it is packets, which a
	// buffered reader would swallow.
	var line []byte
	one := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(c.Timeout))
	for len(line) < maxResponseSize {
		if _, err := conn.Read(one); err != nil {
			conn.Close()
			return nil, err
		}
		if one[0] == '\n' {
			break
		}
		line = append(line, one[0])
	}
	_ = conn.SetReadDeadline(time.Time{})
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		conn.Close()
		return nil, err
	}
	if !resp.OK {
		conn.Close()
		return nil, &RequestError{Code: resp.Code, Message: resp.Error}
	}
	return conn, nil
}
