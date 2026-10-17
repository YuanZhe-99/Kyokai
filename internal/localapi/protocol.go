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
const ProtocolVersion = 1

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
	RequestStatus             RequestType = "status"                 // identity summary, never a secret
	RequestPasswordReveal     RequestType = "password.reveal"        // the temporary password, for the host's own GUI
	RequestPasswordRegenerate RequestType = "password.regenerate"    // a new temporary password
	RequestNetworkGet         RequestType = "network.get"            // network settings without the secret
	RequestNetworkSet         RequestType = "network.set"            // new network settings (Request.Network)
	RequestPermanentSet       RequestType = "password.set_permanent" // set or remove the permanent password (Request.Permanent)
	RequestConnect            RequestType = "session.connect"        // start a session with a device (Request.Connect)
	RequestClose              RequestType = "session.close"          // end a session (Request.ID)
	RequestDecide             RequestType = "request.decide"         // answer a host confirmation (Request.Decide)
	RequestTrustedList        RequestType = "trusted.list"           // the trusted devices
	RequestTrustedRemove      RequestType = "trusted.remove"         // forget a trusted device (Request.Key)
	RequestHostForget         RequestType = "host.forget"            // forget a pinned host key (Request.DeviceID)
	RequestIdentityExport     RequestType = "identity.export"        // an encrypted identity backup (Request.Backup.Passphrase)
	RequestIdentityImport     RequestType = "identity.import"        // restore an identity backup (Request.Backup)
	RequestIdentityReset      RequestType = "identity.reset"         // a new identity and Device ID
	RequestNameSet            RequestType = "device.set_name"        // this device's friendly name (Request.Name; "" follows the computer's name)
	RequestMedia              RequestType = "session.media"          // a controller session's media (Request.ID); the connection then carries packets
	RequestSessionAttach      RequestType = "session.attach"         // streaming; see package doc
	RequestAISettingsGet      RequestType = "ai.settings.get"        // the AI settings and which keys are stored
	RequestAISettingsSet      RequestType = "ai.settings.set"        // change the AI settings (Request.AISettings)
	RequestAIKeySet           RequestType = "ai.key.set"             // store or remove a provider key (Request.AIKey)
	RequestAITaskStart        RequestType = "ai.task.start"          // start an AI task (Request.AITask)
	RequestAITaskReply        RequestType = "ai.task.reply"          // continue a finished task (Request.ID, Request.AITask.Text)
	RequestAITaskPause        RequestType = "ai.task.pause"          // pause a task (Request.ID)
	RequestAITaskResume       RequestType = "ai.task.resume"         // resume a paused task (Request.ID)
	RequestAITaskCancel       RequestType = "ai.task.cancel"         // cancel a task (Request.ID)
	RequestAITaskDecide       RequestType = "ai.task.decide"         // answer an approval (Request.ID, Request.AITask.Approve)
	RequestAITasks            RequestType = "ai.tasks"               // every task, newest first
	RequestAIHistoryClear     RequestType = "ai.history.clear"       // forget the finished tasks
	RequestAIStop             RequestType = "ai.stop"                // the emergency stop: cancel every task
	RequestShutdown           RequestType = "shutdown"
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
	// Provider is "claude" or "openai".
	Provider    string `json:"provider,omitempty"`
	OpenAIModel string `json:"openai_model,omitempty"`
	ClaudeModel string `json:"claude_model,omitempty"`
	// Approval is "flagged" or "every-action".
	Approval string  `json:"approval,omitempty"`
	Keys     *AIKeys `json:"keys,omitempty"`
}

// AIKeys says which providers have a stored key; the keys never leave the
// engine.
type AIKeys struct {
	OpenAI bool `json:"openai"`
	Claude bool `json:"claude"`
}

// AIKey stores (Key set) or removes (Key nil) a provider's API key.
type AIKey struct {
	Provider string  `json:"provider"`
	Key      *string `json:"key"`
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
	Origin   string `json:"origin,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// State is "running", "approval", "paused", "done", "failed" or
	// "cancelled".
	State string `json:"state"`
	// Paused says why: "host-user", "local-input", "controller",
	// "take-over" or "disconnected".
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
	DeviceID string   `json:"device_id"`
	Password string   `json:"password"`
	Want     []string `json:"want,omitempty"`
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
)

// AllPermissions in the order the interface lists them.
var AllPermissions = []string{PermViewScreen, PermControlInput, PermRunAITask, PermApproveAIAction, PermClipboard}

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
	// ID answers RequestConnect: the new session's id.
	ID uint64 `json:"id,omitempty"`
	// Trusted answers RequestTrustedList.
	Trusted []TrustedDevice `json:"trusted,omitempty"`
	// Document answers RequestIdentityExport: the encrypted backup.
	Document string `json:"document,omitempty"`
	// AISettings answers the AI settings and key requests.
	AISettings *AISettings `json:"ai_settings,omitempty"`
	// AITasks answers RequestAITasks.
	AITasks []AITask `json:"ai_tasks,omitempty"`
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
	Known  bool     `json:"known"`
	NewKey bool     `json:"new_key"`
	Want   []string `json:"want"`
	// About is what the controller says about itself (after the password
	// check).
	About About `json:"about"`
}

// RecentDevice is a host this device connected to.
type RecentDevice struct {
	DeviceID string `json:"device_id"`
	Last     int64  `json:"last"` // Unix seconds
	// About is what the host said about itself in the last session.
	About About `json:"about"`
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

// NetworkSettings are the EasyTier network the engine joins.
type NetworkSettings struct {
	// Servers are standard EasyTier server URLs, tried in order.
	Servers []string `json:"servers"`
	Name    string   `json:"name"`
	// Secret is set only in a request; nil keeps the stored secret.
	Secret *string `json:"secret,omitempty"`
	// HasSecret and WeakSecret describe the stored secret in a response.
	HasSecret  bool `json:"has_secret,omitempty"`
	WeakSecret bool `json:"weak_secret,omitempty"`
}

// NetworkStatus is the engine's EasyTier node as it is now.
type NetworkStatus struct {
	// State is "off", "connecting", "online" or "error".
	State string `json:"state"`
	// Error says why the node stopped (State "error").
	Error string `json:"error,omitempty"`
	// Address is the node's virtual IPv4 address once assigned.
	Address string `json:"address,omitempty"`
	// NAT is the detected NAT type ("unknown" until STUN answers).
	NAT     string          `json:"nat,omitempty"`
	Devices []NetworkDevice `json:"devices,omitempty"`
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
	Version     string `json:"version"`
	ABI         int    `json:"abi"`
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"fingerprint"`
	StateDir    string `json:"state_dir"`
	// RunMode is "user" (No privileges) or "helper" (the privileged helper).
	RunMode string `json:"run_mode"`
	// Persistence is "os-store", "memory-only" or "unreadable".
	Persistence string `json:"persistence"`
	// PasswordSerial changes whenever the temporary password does.
	PasswordSerial uint64        `json:"password_serial"`
	Network        NetworkStatus `json:"network"`
	// PermanentPassword says whether a permanent password is set.
	PermanentPassword bool          `json:"permanent_password"`
	Requests          []HostRequest `json:"requests"`
	Sessions          []SessionInfo `json:"sessions"`
	// Recent are the hosts this device connected to, newest first.
	Recent []RecentDevice `json:"recent"`
	// Name is the friendly name chosen here ("" follows the computer's
	// name); About is what this device tells its peers.
	Name  string `json:"name"`
	About About  `json:"about"`
	// AI is the AI task in progress, nil when there is none.
	AI      *AIActive `json:"ai,omitempty"`
	Started time.Time `json:"started"`
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
