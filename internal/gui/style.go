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

// The shell's icons, in the same style: the sidebar's controls, the kinds of
// pane, and a pane header's buttons.
var (
	//go:embed assets/icons/sidebar.svg
	sidebarSVG []byte
	//go:embed assets/icons/plus.svg
	plusSVG []byte
	//go:embed assets/icons/search.svg
	searchSVG []byte
	//go:embed assets/icons/gear.svg
	gearSVG []byte
	//go:embed assets/icons/sun.svg
	sunSVG []byte
	//go:embed assets/icons/moon.svg
	moonSVG []byte
	//go:embed assets/icons/auto.svg
	autoSVG []byte
	//go:embed assets/icons/split-right.svg
	splitRightSVG []byte
	//go:embed assets/icons/split-down.svg
	splitDownSVG []byte
	//go:embed assets/icons/maximize.svg
	maximizeSVG []byte
	//go:embed assets/icons/restore.svg
	restoreSVG []byte
	//go:embed assets/icons/close.svg
	closeSVG []byte
	//go:embed assets/icons/terminal.svg
	terminalSVG []byte
	//go:embed assets/icons/remote-desktop.svg
	remoteDesktopSVG []byte
	//go:embed assets/icons/vnc.svg
	vncSVG []byte

	shellSidebar   = ui.MustParseSVG(sidebarSVG)
	shellPlus      = ui.MustParseSVG(plusSVG)
	shellSearch    = ui.MustParseSVG(searchSVG)
	shellGear      = ui.MustParseSVG(gearSVG)
	shellSun       = ui.MustParseSVG(sunSVG)
	shellMoon      = ui.MustParseSVG(moonSVG)
	shellAuto      = ui.MustParseSVG(autoSVG)
	paneSplitRight = ui.MustParseSVG(splitRightSVG)
	paneSplitDown  = ui.MustParseSVG(splitDownSVG)
	paneMaximize   = ui.MustParseSVG(maximizeSVG)
	paneRestore    = ui.MustParseSVG(restoreSVG)
	paneClose      = ui.MustParseSVG(closeSVG)
	kindDesktop    = barDisplay
	kindSSH        = ui.MustParseSVG(terminalSVG)
	kindRDP        = ui.MustParseSVG(remoteDesktopSVG)
	kindVNC        = ui.MustParseSVG(vncSVG)
)

// The icons of Settings' rows (settings.go), in the same style.
var (
	//go:embed assets/icons/power.svg
	powerSVG []byte
	//go:embed assets/icons/refresh.svg
	refreshSVG []byte
	//go:embed assets/icons/lock.svg
	lockSVG []byte
	//go:embed assets/icons/eye.svg
	eyeSVG []byte
	//go:embed assets/icons/eye-off.svg
	eyeOffSVG []byte
	//go:embed assets/icons/mouse.svg
	mouseSVG []byte
	//go:embed assets/icons/folder.svg
	folderSVG []byte
	//go:embed assets/icons/check.svg
	checkSVG []byte
	//go:embed assets/icons/pulse.svg
	pulseSVG []byte
	//go:embed assets/icons/tray.svg
	traySVG []byte
	//go:embed assets/icons/archive.svg
	archiveSVG []byte
	//go:embed assets/icons/tag.svg
	tagSVG []byte
	//go:embed assets/icons/id-card.svg
	idCardSVG []byte
	//go:embed assets/icons/plug.svg
	plugSVG []byte
	//go:embed assets/icons/log.svg
	logSVG []byte
	//go:embed assets/icons/user.svg
	userSVG []byte
	//go:embed assets/icons/server.svg
	serverSVG []byte
	//go:embed assets/icons/download.svg
	downloadSVG []byte
	//go:embed assets/icons/more.svg
	moreSVG []byte

	rowPower    = ui.MustParseSVG(powerSVG)
	rowRefresh  = ui.MustParseSVG(refreshSVG)
	rowLock     = ui.MustParseSVG(lockSVG)
	rowEye      = ui.MustParseSVG(eyeSVG)
	rowEyeOff   = ui.MustParseSVG(eyeOffSVG)
	rowMouse    = ui.MustParseSVG(mouseSVG)
	rowFolder   = ui.MustParseSVG(folderSVG)
	rowCheck    = ui.MustParseSVG(checkSVG)
	rowPulse    = ui.MustParseSVG(pulseSVG)
	rowTray     = ui.MustParseSVG(traySVG)
	rowArchive  = ui.MustParseSVG(archiveSVG)
	rowTag      = ui.MustParseSVG(tagSVG)
	rowID       = ui.MustParseSVG(idCardSVG)
	rowPlug     = ui.MustParseSVG(plugSVG)
	rowLog      = ui.MustParseSVG(logSVG)
	rowUser     = ui.MustParseSVG(userSVG)
	rowServer   = ui.MustParseSVG(serverSVG)
	rowDownload = ui.MustParseSVG(downloadSVG)
	rowMore     = ui.MustParseSVG(moreSVG)
	rowGlobe    = statusIconNetwork
)

// The window buttons the view draws on Windows (windowButtons): Windows
// 11's glyphs, 10 points with lines of one.
var (
	//go:embed assets/icons/window-minimize.svg
	winMinimizeSVG []byte
	//go:embed assets/icons/window-maximize.svg
	winMaximizeSVG []byte
	//go:embed assets/icons/window-restore.svg
	winRestoreSVG []byte
	//go:embed assets/icons/window-close.svg
	winCloseSVG []byte

	winMinimize = ui.MustParseSVG(winMinimizeSVG)
	winMaximize = ui.MustParseSVG(winMaximizeSVG)
	winRestore  = ui.MustParseSVG(winRestoreSVG)
	winClose    = ui.MustParseSVG(winCloseSVG)
)

// paneShadow is a pane's shadow over the window's base.
var paneShadow = ui.RGBA(16, 24, 40, 0.07)

// The Settings sheet's dimming over the window, and its shadow.
var (
	sheetScrim  = ui.RGBA(0, 0, 0, 0.4)
	sheetShadow = ui.RGBA(0, 0, 0, 0.3)
)

// closeHover is the window's close button under the pointer, white on
// Windows' red.
var (
	closeHover     = ui.RGB(196, 43, 28)
	closeHoverText = ui.RGB(255, 255, 255)
)

// A host's status tiles: their icons, in the same style, and their
// colours, one per metric, the second part of a history in its own.
var (
	//go:embed assets/icons/cpu.svg
	cpuSVG []byte
	//go:embed assets/icons/memory.svg
	memorySVG []byte
	//go:embed assets/icons/gpu.svg
	gpuSVG []byte
	//go:embed assets/icons/disk.svg
	diskSVG []byte
	//go:embed assets/icons/globe.svg
	globeSVG []byte
	//go:embed assets/icons/battery.svg
	batterySVG []byte

	statusIconCPU     = ui.MustParseSVG(cpuSVG)
	statusIconMemory  = ui.MustParseSVG(memorySVG)
	statusIconGPU     = ui.MustParseSVG(gpuSVG)
	statusIconDisk    = ui.MustParseSVG(diskSVG)
	statusIconNetwork = ui.MustParseSVG(globeSVG)
	statusIconBattery = ui.MustParseSVG(batterySVG)

	statusCPU        = ui.Hex("#3b82f6") // programs' share; the system's in statusCPUSystem
	statusCPUSystem  = ui.Hex("#ef4444")
	statusMemory     = ui.Hex("#22a35a")
	statusGPU        = ui.Hex("#e5484d")
	statusDisk       = ui.Hex("#f08c1a") // reads; writes in statusDiskWrite
	statusDiskWrite  = ui.Hex("#3b82f6")
	statusNetwork    = ui.Hex("#14a3a3") // received; sent in statusNetworkUp
	statusNetworkUp  = ui.Hex("#3b82f6")
	statusBattery    = ui.Hex("#22a35a")
	statusBatteryLow = ui.Hex("#f08c1a")
)

// Layout and type scale (doc/en-us/gui.md: body 14, titles 20–24, nothing
// below 12).
const (
	sizeSmall      float32 = 12
	sizeBody       float32 = 14
	sizeHeading    float32 = 16
	sizeTitle      float32 = 22
	sizeDeviceID   float32 = 34
	navIconSize    float32 = 16 // the sidebar's icons
	cardRadius     float32 = 12
	cardPad        float32 = 16
	gap            float32 = 12
	contentMaxW    float32 = 760
	inputAreaH     float32 = 72  // a multi-line input of about three lines
	aiPanelW       float32 = 320 // the viewer's AI panel
	clipPanelW     float32 = 300 // the viewer's clipboard panel
	shellSidebarW  float32 = 248 // the sidebar beside the panes
	railW          float32 = 56  // the sidebar folded to its icons
	sbRowH         float32 = 32  // a row of the sidebar
	sbTopH         float32 = 40  // its top row: hide the sidebar and +
	paneMargin     float32 = 8   // between the panes, and from the window's edges
	paneHeaderH    float32 = 44  // a pane's header
	paneRadius     float32 = 14  // a pane's corners
	paneButton     float32 = 28  // a button in a pane's header or the sidebar
	winButtonW     float32 = 40  // a window button the view draws on Windows
	winButtonH     float32 = 30
	winGlyph       float32 = 10  // its glyph
	settingsNavW   float32 = 240 // the Settings sheet's sections
	sheetX         float32 = 64  // the Settings sheet, from the window's sides
	sheetTop       float32 = 48  // and from its top
	sheetBottom    float32 = 32  // and its bottom
	sheetHeaderH   float32 = 52  // its header
	settingsPageW  float32 = 720 // a Settings page's column
	sizePageTitle  float32 = 20  // its title
	listRadius     float32 = 12  // a list of rows on it
	rowMinH        float32 = 54  // a row
	tileSize       float32 = 32  // the tile a row starts with
	permTileH      float32 = 60  // a permission's toggle tile
	moreW          float32 = 360 // a row's longer explanation (How it works)
	splitMenuW     float32 = 280 // a pane header's split menu
	thisDeviceW    float32 = 300 // Home's This device column
	deviceCardW    float32 = 236 // a recent device's card on Home
	onlineDot      float32 = 8   // a device's online dot
	connectPanelW  float32 = 420 // a connection tab's panel before the picture
	pointerR       float32 = 6   // the host's pointer marker in the viewer
	barTop         float32 = 8   // the viewer toolbar, from the picture's top edge
	barMargin      float32 = 8   // the least room it keeps from the picture's sides
	barRadius      float32 = 10  // its corners
	barIconSize    float32 = 16  // its icons
	avatarSize     float32 = 36  // a device's round avatar
	logoSize       float32 = 22  // a vendor logo inside it (about 64%)
	busySize       float32 = 16  // a loading indicator beside text
	busySizeLarge  float32 = 28  // a loading indicator on its own
	chipRadius     float32 = 10  // a chip (a saved sign-in, a state)
	homeMaxW       float32 = 1120
	homeCardMinW   float32 = 300 // a device's card on Home, at least
	homeColumns    int     = 3   // Home's device cards side by side, at most
	tileGap        float32 = 8   // between a card's status tiles
	sizeMetric     float32 = 34  // a status figure in a host's details
	statusIconSize float32 = 14  // a status tile's icon
	tileRadius     float32 = 10  // a status tile's corners
	statusTileMinW float32 = 280 // a status tile in a host's details, at least
	dotSize        float32 = 4   // a dot of a status history
	dotPitch       float32 = 6   // from one dot to the next
	dotInset       float32 = 6   // around the dots
	dotRowsCard    int     = 4   // a history's rows on a device card
	dotRowsFull    int     = 6   // and in a host's details
	noticeBottom   float32 = 24  // the viewer's notices over the picture, from its bottom edge
)

// Where the macOS traffic lights sit: the close button's top-left corner,
// which centres the 14-point buttons on the top pane's header, at the
// sidebar's left edge.
const (
	trafficX = 18
	trafficY = int(paneMargin + (paneHeaderH-14)/2)
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
