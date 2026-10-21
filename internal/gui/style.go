package gui

import (
	_ "embed"

	"github.com/egoist/mygo/plugins/loading"
	"github.com/egoist/mygo/ui"
)

// The sidebar's icons: original line drawings in one style (24×24,
// stroke 2), drawn in the text colour.
var (
	//go:embed assets/icons/home.svg
	homeSVG []byte
	//go:embed assets/icons/network.svg
	networkSVG []byte
	//go:embed assets/icons/ai.svg
	aiSVG []byte
	//go:embed assets/icons/permissions.svg
	permissionsSVG []byte
	//go:embed assets/icons/security.svg
	securitySVG []byte
	//go:embed assets/icons/general.svg
	generalSVG []byte
	//go:embed assets/icons/about.svg
	aboutSVG []byte
	//go:embed assets/icons/mark.svg
	markSVG []byte

	navHome        = ui.MustParseSVG(homeSVG)
	navNetwork     = ui.MustParseSVG(networkSVG)
	navAI          = ui.MustParseSVG(aiSVG)
	navPermissions = ui.MustParseSVG(permissionsSVG)
	navSecurity    = ui.MustParseSVG(securitySVG)
	navGeneral     = ui.MustParseSVG(generalSVG)
	navAbout       = ui.MustParseSVG(aboutSVG)
	// appMark is the app's mark (a ring split by a boundary) for the
	// sidebar's title.
	appMark = ui.MustParseSVG(markSVG)
)

// The viewer toolbar's icons, in the same style.
var (
	//go:embed assets/icons/display.svg
	displaySVG []byte
	//go:embed assets/icons/quality.svg
	qualitySVG []byte
	//go:embed assets/icons/keyboard.svg
	keyboardSVG []byte
	//go:embed assets/icons/actions.svg
	actionsSVG []byte
	//go:embed assets/icons/sound.svg
	soundSVG []byte
	//go:embed assets/icons/sound-off.svg
	soundOffSVG []byte
	//go:embed assets/icons/pointer.svg
	pointerSVG []byte
	//go:embed assets/icons/clipboard.svg
	clipboardSVG []byte
	//go:embed assets/icons/full-screen.svg
	fullScreenSVG []byte
	//go:embed assets/icons/exit-full-screen.svg
	exitFullScreenSVG []byte
	//go:embed assets/icons/grip.svg
	gripSVG []byte
	//go:embed assets/icons/fold.svg
	foldSVG []byte
	//go:embed assets/icons/unfold.svg
	unfoldSVG []byte

	barDisplay        = ui.MustParseSVG(displaySVG)
	barQuality        = ui.MustParseSVG(qualitySVG)
	barKeyboard       = ui.MustParseSVG(keyboardSVG)
	barActions        = ui.MustParseSVG(actionsSVG)
	barSound          = ui.MustParseSVG(soundSVG)
	barSoundOff       = ui.MustParseSVG(soundOffSVG)
	barPointer        = ui.MustParseSVG(pointerSVG)
	barClipboard      = ui.MustParseSVG(clipboardSVG)
	barFullScreen     = ui.MustParseSVG(fullScreenSVG)
	barExitFullScreen = ui.MustParseSVG(exitFullScreenSVG)
	barGrip           = ui.MustParseSVG(gripSVG)
	barFold           = ui.MustParseSVG(foldSVG)
	barUnfold         = ui.MustParseSVG(unfoldSVG)
)

// Layout and type scale (doc/en-us/gui.md: body 14, titles 20–24, nothing
// below 12).
const (
	sizeSmall     float32 = 12
	sizeBody      float32 = 14
	sizeHeading   float32 = 16
	sizeTitle     float32 = 22
	sizeDeviceID  float32 = 34
	sidebarW      float32 = 168
	navIconSize   float32 = 16 // the sidebar's icons
	cardRadius    float32 = 12
	cardPad       float32 = 16
	gap           float32 = 12
	contentMaxW   float32 = 760
	inputAreaH    float32 = 72  // a multi-line input of about three lines
	aiPanelW      float32 = 320 // the viewer's AI panel
	clipPanelW    float32 = 300 // the viewer's clipboard panel
	tabTitleMaxW  float32 = 220 // a viewer tab's title
	tabStripH     float32 = 38  // the tab strip, which is the windows' title bar
	thisDeviceW   float32 = 300 // Home's This device column
	deviceCardW   float32 = 236 // a recent device's card on Home
	onlineDot     float32 = 8   // a device's online dot
	connectPanelW float32 = 420 // a connection tab's panel before the picture
	pointerR      float32 = 6   // the host's pointer marker in the viewer
	barTop        float32 = 8   // the viewer toolbar, from the picture's top edge
	barMargin     float32 = 8   // the least room it keeps from the picture's sides
	barRadius     float32 = 10  // its corners
	barIconSize   float32 = 16  // its icons
	avatarSize    float32 = 36  // a device's round avatar
	logoSize      float32 = 22  // a vendor logo inside it (about 64%)
	busySize      float32 = 16  // a loading indicator beside text
	busySizeLarge float32 = 28  // a loading indicator on its own
)

// Where the macOS traffic lights sit in the tab strip: the close button's
// top-left corner, which centres the 14-point buttons in tabStripH.
const (
	trafficX = 14
	trafficY = 12
)

// busyMark is the loading indicator shown while something is under way:
// loading.dev's Arc (MyGo's loading plugin) in the theme's accent, named
// for assistive technology.
// Inputs: label says what is loading; size is busySize beside text or
// busySizeLarge on its own.
func busyMark(c *ui.Context, label string, size float32) ui.Element {
	return busyMarkIn(c, label, size, c.Theme().Accent)
}

// busyMarkIn is busyMark in another colour, such as white on the viewer's black
// stage.
func busyMarkIn(c *ui.Context, label string, size float32, col ui.Color) ui.Element {
	return loading.Arc(c, loading.Options{Size: size, Color: col}).Label(label)
}

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

// labelled puts a switch and its visible label side by side (a switch's
// Label names it only for assistive technology).
func labelled(c *ui.Context, control func(), label string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		control()
		ui.Text(c, label).FontSize(sizeSmall)
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
