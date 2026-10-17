package gui

import (
	"github.com/egoist/mygo/ui"
)

// Layout and type scale (doc/en-us/gui.md: body 14, titles 20–24, nothing
// below 12).
const (
	sizeSmall    float32 = 12
	sizeBody     float32 = 14
	sizeHeading  float32 = 16
	sizeTitle    float32 = 22
	sizeDeviceID float32 = 34
	sidebarW     float32 = 200
	cardRadius   float32 = 12
	cardPad      float32 = 16
	gap          float32 = 12
	contentMaxW  float32 = 760
	inputAreaH   float32 = 72  // a multi-line input of about three lines
	aiPanelW     float32 = 320 // the viewer's AI panel
	pointerR     float32 = 6   // the host's pointer marker in the viewer
	avatarSize   float32 = 36  // a device's round avatar
	logoSize     float32 = 22  // a vendor logo inside it (about 64%)
)

// card is a rounded panel.
func card(c *ui.Context) ui.Element {
	t := c.Theme()
	return ui.Column(c).Background(t.Surface).Radius(cardRadius).Padding(cardPad).Gap(gap).FillWidth().MaxWidth(contentMaxW).Border(1, t.Border)
}

// settingRow is a labelled row with a control on the right.
func settingRow(c *ui.Context, key, title, hint string, control func()) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, title)
			if hint != "" {
				ui.Text(c, hint).FontSize(sizeSmall).TextColor(t.TextMuted)
			}
		})
		control()
	})
}

// groupDigits writes a Device ID in groups of three: "482 916 735".
func groupDigits(id string) string {
	out := make([]byte, 0, len(id)+len(id)/3)
	for i := range len(id) {
		if i > 0 && (len(id)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, id[i])
	}
	return string(out)
}

// groupHex splits a fingerprint into groups of four for reading aloud.
func groupHex(fp string) string {
	out := make([]byte, 0, len(fp)+len(fp)/4)
	for i := range len(fp) {
		if i > 0 && i%4 == 0 {
			out = append(out, ' ')
		}
		out = append(out, fp[i])
	}
	return string(out)
}
