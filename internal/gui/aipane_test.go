package gui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// fakePane is a pane source with a 2×1 picture that records the events.
type fakePane struct {
	events   []inputEvent
	released bool
	empty    bool
}

func (f *fakePane) paneSize() (int, int) {
	if f.empty {
		return 0, 0
	}
	return 2, 1
}

func (f *fakePane) panePicture() (*image.RGBA, error) {
	if f.empty {
		return nil, errors.New("the pane shows no picture yet")
	}
	p := image.NewRGBA(image.Rect(0, 0, 2, 1))
	p.Set(1, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	return p, nil
}

func (f *fakePane) paneInput(e inputEvent) error {
	f.events = append(f.events, e)
	return nil
}

func (f *fakePane) paneRelease() { f.released = true }

// TestPaneAnswers: a served pane answers the engine's requests from its
// source: the size, the picture as a PNG, an input event, release, and a
// refusal for no picture or an unknown request.
func TestPaneAnswers(t *testing.T) {
	src := &fakePane{}
	ask := func(req string) map[string]any {
		var out map[string]any
		if err := json.Unmarshal(answerPane(src, json.RawMessage(req)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := ask(`{"op":"size"}`); got["width"] != 2.0 || got["height"] != 1.0 {
		t.Errorf("size: %v", got)
	}
	raw, _ := base64.StdEncoding.DecodeString(ask(`{"op":"capture"}`)["png"].(string))
	pic, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := pic.At(1, 0).RGBA(); r>>8 != 200 || g>>8 != 100 || b>>8 != 50 {
		t.Errorf("picture: %v", pic.At(1, 0))
	}
	ask(`{"op":"input","event":{"kind":"pointer","action":"down","button":0,"x":0.5,"y":0.5}}`)
	if len(src.events) != 1 || src.events[0].Action != "down" || src.events[0].X != 0.5 {
		t.Errorf("input: %+v", src.events)
	}
	ask(`{"op":"release"}`)
	if !src.released {
		t.Error("not released")
	}
	if got := ask(`{"op":"format-disk"}`); got["error"] == nil {
		t.Errorf("an unknown request was answered: %v", got)
	}
	src.empty = true
	if got := ask(`{"op":"capture"}`); got["error"] == nil {
		t.Errorf("a picture with none: %v", got)
	}
}

// TestPaneKeys: the executor's key names reach the remote keys, the
// modifiers included.
func TestPaneKeys(t *testing.T) {
	for name, want := range map[string]remoteKey{"ctrl": keyCtrl, "shift": keyShift, "meta": keyMeta, "enter": remoteKeysByName["enter"]} {
		if k, ok := paneKey(name); !ok || k != want {
			t.Errorf("%s: %+v", name, k)
		}
	}
	if _, ok := paneKey("no-such-key"); ok {
		t.Error("an unknown key was found")
	}
}
