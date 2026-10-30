package gui

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/egoist/mygo/ui"
)

// The device Access shows chosen: the saved one, else the one picked
// before it is offered, else the one found; a saved one that is not
// there now stays chosen, marked as such.
func TestKvmChoice(t *testing.T) {
	ports := []localapi.NanokvmPort{{ID: "/dev/video0", Name: "Card"}, {ID: "/dev/video2", Name: "Camera"}}
	for _, c := range []struct {
		saved, picked, suggested string
		want                     string
		extra                    bool
	}{
		{"/dev/video2", "/dev/video0", "/dev/video0", "/dev/video2", false},
		{"", "/dev/video2", "/dev/video0", "/dev/video2", false},
		{"", "", "/dev/video0", "/dev/video0", false},
		{"", "", "", "", false},
		{"/dev/video9", "", "/dev/video0", "/dev/video9", true},
	} {
		id, ids, labels := kvmChoice(ports, c.saved, c.picked, c.suggested)
		if id != c.want || len(ids) != len(labels) || (len(ids) == 3) != c.extra || !slices.Contains(append(ids, ""), id) {
			t.Errorf("%+v: %q %v %v", c, id, ids, labels)
		}
	}
}

// A device's desktop and its NanoKVM are two connections, side by side,
// each with its own tab and settings.
func TestNanokvmViewerBesideTheDesktop(t *testing.T) {
	a := &App{stateDir: t.TempDir(), viewers: map[string]*viewer{}}
	desk := a.newViewer("482916735", localapi.About{}, nil, false)
	desk.setCodec("av1")
	desk.keepSettings()
	kvm := a.newViewer("482916735", localapi.About{}, []string{localapi.PermUseNanokvm}, true)
	if kvm.codec != "auto" {
		t.Errorf("the NanoKVM pane took the desktop's codec %q", kvm.codec)
	}
	if desk.tabKey() == kvm.tabKey() {
		t.Fatalf("one key for both: %q", desk.tabKey())
	}
	if a.viewerFor("482916735") != desk || a.kvmViewerFor("482916735") != kvm {
		t.Fatal("each is found as its own")
	}
	kvm.close()
	if a.viewerFor("482916735") != desk || a.kvmViewerFor("482916735") != nil {
		t.Fatal("closing the NanoKVM pane leaves the desktop")
	}
}

// The + picker lists this computer first for a NanoKVM USB once its
// devices are chosen here, and not for other kinds.
func TestPickThisComputerNanokvm(t *testing.T) {
	first := func(kind string, own bool) string {
		rows := pickRows(kind, "", own, nil, nil, nil, nil, ui.Color{}, ui.Color{}, ui.Color{})
		if len(rows) == 0 {
			return ""
		}
		return rows[0].key
	}
	if first(serviceNanokvm, true) != selfID || first(serviceNanokvm, false) != "" || first(serviceSSH, true) != "" {
		t.Error("this computer's NanoKVM row")
	}
}

// This computer's own NanoKVM pane is named as this computer, and the
// engine's "in use" answer turns into Take over.
func TestOwnNanokvmPane(t *testing.T) {
	a := &App{stateDir: t.TempDir(), viewers: map[string]*viewer{}, status: &localapi.Status{DeviceID: "482916735",
		Nanokvm: &localapi.Nanokvm{Video: "/dev/video0", Serial: "/dev/ttyUSB0"}}}
	if !a.ownKvmReady() {
		t.Fatal("devices chosen: ready")
	}
	own := a.newViewer("482916735", localapi.About{Name: "Desk"}, []string{localapi.PermUseNanokvm}, true)
	other := a.newViewer("123456789", localapi.About{Name: "Desk"}, []string{localapi.PermUseNanokvm}, true)
	if !own.own || other.own || own.title == other.title {
		t.Errorf("own %v %q, other %v %q", own.own, own.title, other.own, other.title)
	}
	a.status.Nanokvm.Serial = ""
	if a.ownKvmReady() {
		t.Error("no serial port: not ready")
	}
}

// Type the clipboard splits long text into the host's text events at
// character boundaries, losing nothing.
func TestTextPieces(t *testing.T) {
	text := strings.Repeat("ab鏡界", 5)
	pieces := textPieces(text, 7)
	if strings.Join(pieces, "") != text {
		t.Fatalf("lost text: %q", pieces)
	}
	for _, p := range pieces {
		if len(p) > 7 || !utf8.ValidString(p) {
			t.Errorf("piece %q", p)
		}
	}
	if textPieces("", 7) != nil {
		t.Error("nothing to type: no pieces")
	}
}
