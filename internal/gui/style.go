package gui

import (
	_ "embed"
	"runtime"

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
	//go:embed assets/icons/copy.svg
	copySVG []byte
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
	rowCopy     = ui.MustParseSVG(copySVG)
	rowGlobe    = statusIconNetwork
)

// An AI session's icons: send and stop in the prompt box, pause and
// resume, rename in the header, approvals' shield, a menu chip's chevron,
// a step that did not run, and the suggestions' image and printer.
var (
	//go:embed assets/icons/send.svg
	sendSVG []byte
	//go:embed assets/icons/stop.svg
	stopSVG []byte
	//go:embed assets/icons/pause.svg
	pauseSVG []byte
	//go:embed assets/icons/play.svg
	playSVG []byte
	//go:embed assets/icons/pencil.svg
	pencilSVG []byte
	//go:embed assets/icons/shield.svg
	shieldSVG []byte
	//go:embed assets/icons/chevron-down.svg
	chevronDownSVG []byte
	//go:embed assets/icons/chevron-right.svg
	chevronRightSVG []byte
	//go:embed assets/icons/dash.svg
	dashSVG []byte
	//go:embed assets/icons/image.svg
	imageSVG []byte
	//go:embed assets/icons/printer.svg
	printerSVG []byte

	aiSend    = ui.MustParseSVG(sendSVG)
	aiStop    = ui.MustParseSVG(stopSVG)
	aiPause   = ui.MustParseSVG(pauseSVG)
	aiResume  = ui.MustParseSVG(playSVG)
	aiRename  = ui.MustParseSVG(pencilSVG)
	aiShield  = ui.MustParseSVG(shieldSVG)
	aiChevron = ui.MustParseSVG(chevronDownSVG)
	// aiChevronRight is a folded group of steps.
	aiChevronRight = ui.MustParseSVG(chevronRightSVG)
	aiDash         = ui.MustParseSVG(dashSVG)
	aiImage        = ui.MustParseSVG(imageSVG)
	aiPrinter      = ui.MustParseSVG(printerSVG)
)

// A warning, before the words it goes with (a connection request's).
var (
	//go:embed assets/icons/alert.svg
	alertSVG []byte

	rowAlert = ui.MustParseSVG(alertSVG)
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

// Words against WCAG AA's 4.5:1: in the light appearance MyGo's muted text
// (#71717a) reaches 4.4:1 on the surface Home's cards use, so a shade
// darker (5.2:1 there); a status colour or the accent as words moves this
// far toward black, or toward white in the dark appearance (textTone).
var (
	mygoMuted  = ui.Hex("#71717a")
	mutedLight = ui.Hex("#66666e")
)

const statusTextMix = 0.3

// readableTheme is a window's theme with the light appearance's muted text
// a shade darker, unless the system chose its own colours.
func readableTheme(t *ui.Theme) *ui.Theme {
	if t.Dark || t.TextMuted != mygoMuted {
		return t
	}
	r := *t
	r.TextMuted = mutedLight
	return &r
}

// textTone is a status colour — warning, success, danger — or the accent
// as the colour of words: 30% of the way toward black in the light
// appearance, toward white in the dark one, which reaches 4.5:1 on the
// page, on the surface and on the colour's own tint (a pill, a chosen
// tile); any other colour as it is.
func textTone(t *ui.Theme, col ui.Color) ui.Color {
	if col != t.Warning && col != t.Success && col != t.Danger && col != t.Accent {
		return col
	}
	var to float32 // black
	if t.Dark {
		to = 255
	}
	mix := func(v uint8) uint8 { return uint8(float32(v)*(1-statusTextMix) + to*statusTextMix) }
	return ui.Color{R: mix(col.R), G: mix(col.G), B: mix(col.B), A: col.A}
}

// The Settings sheet's dimming over the window, its shadow, and the
// window's buttons on the dimming: their glyphs and their face under the
// pointer.
var (
	sheetScrim  = ui.RGBA(0, 0, 0, 0.4)
	sheetShadow = ui.RGBA(0, 0, 0, 0.3)
	scrimGlyph  = ui.RGB(244, 245, 247)
	scrimHover  = ui.RGBA(255, 255, 255, 0.14)
)

// paletteScrim is the search palette's dimming, lighter than the Settings
// sheet's: the palette is gone with the next choice.
var paletteScrim = ui.RGBA(0, 0, 0, 0.18)

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
	sizeSmall       float32 = 12
	sizeBody        float32 = 14
	sizeHeading     float32 = 16
	sizeTitle       float32 = 22
	sizeDeviceID    float32 = 34
	navIconSize     float32 = 16 // the sidebar's icons
	cardRadius      float32 = 12
	cardPad         float32 = 16
	gap             float32 = 12
	inputAreaH      float32 = 72  // a multi-line input of about three lines
	aiColumnW       float32 = 720 // an AI session's conversation and box
	aiNewW          float32 = 680 // a new session's column
	aiMarkSize      float32 = 48  // the AI's round mark over it
	sizeAIAsk       float32 = 22  // what the AI should do, under it, and the mark's glyph
	aiSuggestH      float32 = 34  // a suggestion under its box
	aiRecentWhereW  float32 = 110 // a recent task's device
	aiRecentStateW  float32 = 128 // and its state
	aiBubbleMaxW    float32 = 520 // a message of yours
	aiBubbleRadius  float32 = 16  // its corners
	aiCardRadius    float32 = 12  // an approval waiting
	aiBoxRadius     float32 = 16  // the prompt box
	aiBoxMinH       float32 = 44  // its text, two lines
	aiBoxMaxH       float32 = 168 // and at most about seven, scrolling beyond
	aiSendSize      float32 = 32  // its round send or stop button
	aiChipH         float32 = 28  // a chip under it
	aiStepMark      float32 = 18  // a step's round mark
	aiStepGlyph     float32 = 11  // its glyph
	aiStepRowH      float32 = 28  // a step's row
	aiChevronSize   float32 = 12  // a menu chip's chevron
	aiRecentH       float32 = 40  // a recent task's row
	aiRenameW       float32 = 260 // a session's title being typed in the header
	clipPanelW      float32 = 300 // the viewer's clipboard panel
	shellSidebarW   float32 = 248 // the sidebar beside the panes
	railW           float32 = 56  // the sidebar folded to its icons (railWidth)
	railMacW        float32 = 72  // on macOS: the traffic lights and 9 on either side
	sbRowH          float32 = 32  // a row of the sidebar
	sbNameMaxW      float32 = 140 // its name, before the kind beside it gives way
	sbTopH          float32 = 40  // its top row: hide the sidebar and +
	paneMargin      float32 = 8   // between the panes, and from the window's edges
	paneHeaderH     float32 = 44  // a pane's header
	paneNarrowW     float32 = 560 // narrower, a header folds its split and maximize buttons into ⋯
	paneMinW        float32 = 360 // a pane in a group, at least: Home keeps one column of cards
	paneMinH        float32 = 280 // and its height
	mainMinW        float32 = 760 // the main window, at least, whatever the group shown
	mainMinH        float32 = 520 // and its height
	paneNameMaxW    float32 = 200 // a header's name, at most
	paneRadius      float32 = 14  // a pane's corners
	paneButton      float32 = 28  // a button in a pane's header or the sidebar
	winButtonW      float32 = 40  // a window button the view draws on Windows
	winButtonH      float32 = 30
	winGlyph        float32 = 10  // its glyph
	settingsNavW    float32 = 240 // the Settings sheet's sections
	sheetX          float32 = 64  // the Settings sheet, from the window's sides
	sheetTop        float32 = 48  // and from its top
	sheetBottom     float32 = 32  // and its bottom
	sheetHeaderH    float32 = 52  // its header
	settingsPageW   float32 = 720 // a Settings page's column
	sizePageTitle   float32 = 20  // its title
	listRadius      float32 = 12  // a list of rows on it
	rowMinH         float32 = 54  // a row
	tileSize        float32 = 32  // the tile a row starts with
	permTileH       float32 = 60  // a permission's toggle tile
	askTileMinW     float32 = 140 // one a connection request asks for, at least
	noticePadX      float32 = 16  // a card above the panes, from its sides
	noticePadY      float32 = 14  // and from its top and bottom
	emptyTop        float32 = 96  // what a pane shows in place of its content, from its top
	emptyW          float32 = 440 // and its words' width, at most
	routeRowH       float32 = 48  // a route in a device's Addresses editor
	unlockW         float32 = 240 // protected mode's permanent password field
	moreW           float32 = 360 // a row's longer explanation (How it works)
	splitMenuW      float32 = 280 // a pane header's split menu
	pickerW         float32 = 424 // the + picker
	pickerRowH      float32 = 44  // a device in it
	pickerRowsMaxH  float32 = 300 // its devices, scrolling beyond
	paletteW        float32 = 600 // the search palette
	paletteTop      float32 = 72  // from the window's top
	paletteMaxH     float32 = 380 // its results, scrolling beyond
	deviceCardW     float32 = 236 // a recent device's card on Home
	onlineDot       float32 = 8   // a device's online dot
	connectPanelW   float32 = 420 // a connection tab's panel before the picture, at most
	pointerR        float32 = 6   // the host's pointer marker in the viewer
	barTop          float32 = 8   // the viewer toolbar, from the picture's top edge
	barMargin       float32 = 8   // the least room it keeps from the picture's sides
	barRadius       float32 = 10  // its corners
	barIconSize     float32 = 16  // its icons
	avatarSize      float32 = 36  // a device's round avatar
	logoSize        float32 = 22  // a vendor logo inside it (about 64%)
	busySize        float32 = 16  // a loading indicator beside text
	busySizeLarge   float32 = 28  // a loading indicator on its own
	chipRadius      float32 = 10  // a chip (a saved sign-in, a state)
	homeMaxW        float32 = 1120
	homeCardMinW    float32 = 300 // a device's card on Home, at least
	homeCardH       float32 = 208 // and its height: the cards of a row match
	homeActionW     float32 = 32  // a service's button on it
	homeOtherH      float32 = 58  // a device in my networks never connected to
	homeTopGap      float32 = 14  // between This device and Connect to a device
	homeTwoColumnsW float32 = 640 // the room they need side by side
	openAsSegmentsW float32 = 440 // Connect's Open as as five segments, in the widest language with room to spare
	openAsMenuW     float32 = 200 // and as a menu in less
	homeColumns     int     = 3   // Home's device cards side by side, at most
	tileGap         float32 = 8   // between a card's status tiles
	sizeMetric      float32 = 34  // a status figure in a host's details
	statusIconSize  float32 = 14  // a status tile's icon
	tileRadius      float32 = 10  // a status tile's corners
	statusTileMinW  float32 = 280 // a status tile in a host's details, at least
	dotSize         float32 = 4   // a dot of a status history
	dotPitch        float32 = 6   // from one dot to the next
	dotInset        float32 = 6   // around the dots
	dotRowsCard     int     = 4   // a history's rows on a device card
	dotRowsFull     int     = 6   // and in a host's details
	noticeBottom    float32 = 24  // the viewer's notices over the picture, from its bottom edge
)

// Where the macOS traffic lights sit: the close button's top-left corner,
// which centres the 14-point buttons on the top pane's header — in the main
// window at the system's own inset from its edge, over the sidebar's top
// row or the rail; in a window of its own clear of its pane's edge, which
// starts paneMargin from the window's.
const (
	trafficMainX = 9
	trafficOwnX  = 18
	trafficY     = int(paneMargin + (paneHeaderH-14)/2)
)

// railWidth is the width of the sidebar folded to its icons: on macOS the
// traffic lights' with their inset on either side, so that they and the
// icons share one centre line.
func railWidth() float32 {
	if runtime.GOOS == "darwin" {
		return railMacW
	}
	return railW
}

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
