package gui

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"github.com/YuanZhe-99/Kyokai/internal/androidhost"
	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// mobileSettingsPage enumerates the sub-pages inside the mobile Settings tab.
type mobileSettingsPage int

const (
	mobSetDevice mobileSettingsPage = iota
	mobSetNetworks
	mobSetKeys
	mobSetInput
	mobSetAppearance
	mobSetDiagnostics
	mobSetAbout
)

// mobileState holds all Android controller UI and runtime state.
type mobileState struct {
	host       androidhost.Host
	info       androidhost.DeviceInfo
	palette    androidhost.MaterialPalette
	decoders   []androidhost.DecoderCap
	postureSub io.Closer
	touchSub   io.Closer
	taskSub    io.Closer
	posture    androidhost.Posture

	engineMu       sync.Mutex
	engineStarted  bool
	engineStartErr error
	engineDone     chan struct{}

	// Navigation shell.
	navTab        int    // 0 Devices, 1 Sessions, 2 Settings
	selectedDev   string // Device ID open in Device detail
	settingsPage  mobileSettingsPage
	settingsOpen  bool   // true when a Settings sub-page is pushed on compact phones
	activeSession string // tabKey of full-screen session ("" when in shell)
	filterMode    int    // 0 All, 1 Online, 2 Offline

	// Active viewer interaction state (per session key) & native touch routing.
	views        map[string]*mobileViewerUI
	terms        map[string]*mobileTermSession
	touchMu      sync.Mutex
	touchHandler func(androidhost.RawTouch)
	lastTouchCfg [17]float64

	// Sheets and dialogs.
	clipSheetOpen bool
	aiSheetOpen   bool
	aiSheetDevice string
	aiHistOpen    bool
	aiHistDevice  string
	typeTextOpen  bool
	typeTextValue string
	menuSheetOpen bool
	routeAddAddr  string
	devNameEdit   string
	devNameDirty  bool
	aliasDev      string
	aliasEdit     string
	aliasDirty    bool

	// clipboard sync deduplication & display wake lock.
	clipMu       sync.Mutex
	lastClipText string
	lastClipMark string
	inFront      bool
	lastSvcText  string
	awakeRelease func()
}

type mobileViewerUI struct {
	pad           *Touchpad
	kb            *MobileKeyboard
	kbOpen        bool
	kbAutoOpened  bool
	autoKBGeom    string
	zoom          mobile.ZoomState
	curX, curY    float32 // normalized 0..1 local cursor position
	curInit       bool
	barFolded     bool
	dragHandle    ui.Point
	barOffset     ui.Point
	barOffsetInit bool
	barAreaW      float32
	barAreaH      float32
	lastBarRect   ui.Rect
	dipScale      float32
}

func (a *App) initMobile() {
	ms := &mobileState{
		views: map[string]*mobileViewerUI{},
		terms: map[string]*mobileTermSession{},
	}
	if h, err := androidhost.NewHost(); err == nil {
		ms.host = h
		if info, err := h.Info(); err == nil {
			ms.info = info
		}
		ms.palette = h.DynamicPalette()
		ms.decoders = h.Decoders()
		ms.postureSub = h.WatchPosture(func(p androidhost.Posture) {
			a.update(func() {
				ms.posture = p
			})
		})
		ms.touchSub = h.WatchTouch(func(rt androidhost.RawTouch) {
			ms.touchMu.Lock()
			fn := ms.touchHandler
			ms.touchMu.Unlock()
			if fn != nil {
				fn(rt)
			}
		})
		ms.taskSub = h.WatchTaskRemoved(func() {
			a.mobileOnTaskRemoved()
		})
	}
	// Wipe stale received clipboard files from previous cold starts.
	cacheDir := ms.info.CacheDir
	if cacheDir == "" {
		cacheDir, _ = os.UserCacheDir()
	}
	if cacheDir != "" {
		_ = os.RemoveAll(filepath.Join(cacheDir, "received"))
	}
	a.mobile = ms
}

func (a *App) ensureInProcessEngine(ctx context.Context, socket string) error {
	client := localapi.NewClient(socket)
	if _, err := client.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil {
		return nil
	}
	ms := a.mobile
	if ms == nil {
		return errors.New("mobile state not initialized")
	}
	ms.engineMu.Lock()
	if !ms.engineStarted {
		ms.engineStarted = true
		ms.engineStartErr = nil
		done := make(chan struct{})
		ms.engineDone = done
		var masterKey, cacheDir, defaultName string
		var decNames []string
		if ms.host != nil {
			masterKey = ms.host.MasterKey()
			cacheDir = ms.info.CacheDir
			defaultName = strings.TrimSpace(ms.info.Manufacturer + " " + ms.info.Model)
			for _, d := range ms.decoders {
				decNames = append(decNames, d.Codec)
			}
		}
		if len(decNames) == 0 {
			decNames = []string{"h264"}
		}
		if cacheDir == "" {
			cacheDir, _ = os.UserCacheDir()
		}
		if len([]rune(defaultName)) > 64 {
			defaultName = string([]rune(defaultName)[:64])
		}
		opts := engine.Options{
			Version:        version.Version,
			Socket:         socket,
			StateDir:       a.stateDir,
			Profile:        a.profile,
			CoreLib:        core.LibraryName(),
			Logf:           engine.Logger(filepath.Join(a.stateDir, "logs", "engine.log")),
			MasterKey:      masterKey,
			CacheDir:       cacheDir,
			Decoders:       decNames,
			DefaultName:    defaultName,
			ControllerOnly: true,
		}
		go func() {
			defer close(done)
			err := engine.Run(ctx, opts)
			if err != nil && ctx.Err() == nil {
				ms.engineMu.Lock()
				ms.engineStarted = false
				ms.engineStartErr = err
				ms.engineMu.Unlock()
				a.update(func() { a.engineErr = err.Error() })
			}
		}()
	}
	ms.engineMu.Unlock()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil {
			return nil
		}
		ms.engineMu.Lock()
		startErr := ms.engineStartErr
		ms.engineMu.Unlock()
		if startErr != nil {
			return startErr
		}
		time.Sleep(80 * time.Millisecond)
	}
	return errors.New("the in-process engine did not start")
}

func (a *App) watchMobileWindow(w *mygo.Window) {
	w.OnFocus(func() {
		a.SetVisible(true)
		a.mobileForeground(true)
	})
	w.OnBlur(func() {
		a.mobileForeground(false)
	})
	w.OnShow(func() {
		a.SetVisible(true)
		a.mobileForeground(true)
	})
	w.OnHide(func() {
		a.SetVisible(false)
		a.mobileForeground(false)
	})
	mygo.Power.OnSuspend(func() {
		a.mobileEnterBackground()
	})
	mygo.Power.OnResume(func() {
		a.mobileEnterForeground()
	})
}

// mobileEnterBackground runs when the Android activity moves to the
// background: drops visibility and host status watches, releases any screen
// wake lock, and disconnects open sessions unless KeepBackgroundSessions is
// enabled.
func (a *App) mobileEnterBackground() {
	if a.mobile == nil {
		return
	}
	a.SetVisible(false)
	a.mobileForeground(false)
	a.mu.Lock()
	rel := a.mobile.awakeRelease
	a.mobile.awakeRelease = nil
	keep := a.prefs.KeepBackgroundSessions
	a.mu.Unlock()
	if rel != nil {
		rel()
	}
	if !keep {
		a.mobileDisconnectAllSessions()
		return
	}
	a.mobileSyncSessions()
}

// mobileEnterForeground runs when the Android activity returns to the
// foreground: restores visibility and refreshes session service state.
func (a *App) mobileEnterForeground() {
	if a.mobile == nil {
		return
	}
	a.SetVisible(true)
	a.mobileForeground(true)
	a.mobileSyncSessions()
}

// mobileDisconnectAllSessions closes every open desktop, service, SSH and
// AI-link controller session on mobile and clears session-bound sheets.
func (a *App) mobileDisconnectAllSessions() {
	ms := a.mobile
	a.mu.Lock()
	viewers := make([]*viewer, 0, len(a.viewers))
	for _, v := range a.viewers {
		viewers = append(viewers, v)
	}
	services := make([]*serviceTab, 0, len(a.services))
	for _, s := range a.services {
		services = append(services, s)
	}
	links := make([]*aiLink, 0, len(a.aiLinks))
	for k, l := range a.aiLinks {
		links = append(links, l)
		delete(a.aiLinks, k)
	}
	var termKeys []string
	if ms != nil {
		for k := range ms.terms {
			termKeys = append(termKeys, k)
		}
		ms.activeSession = ""
		ms.clipSheetOpen = false
		ms.aiSheetOpen = false
		ms.typeTextOpen = false
		ms.menuSheetOpen = false
	}
	connected := a.connected
	quitting := a.quitting
	a.mu.Unlock()

	for _, v := range viewers {
		v.disconnect()
	}
	for _, s := range services {
		s.disconnect()
	}
	for _, k := range termKeys {
		a.mobileCloseSSH(k)
	}
	for _, l := range links {
		l.mu.Lock()
		conn := l.conn
		l.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		if id := l.sid.Load(); id != 0 {
			a.dropSession(id)
			go a.closeSession(id)
		}
	}
	if connected && !quitting {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, _ = localapi.NewClient(a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestCloseControllers})
		}()
	}
	a.mobileSyncSessions()
	a.homeVisibilityChanged()
}

// mobileOnTaskRemoved runs when the user dismisses Kyokai from Android's
// Recents screen: disconnects every session, stops the in-process engine
// (and its EasyTier network), and waits briefly for cleanup before exit.
func (a *App) mobileOnTaskRemoved() {
	a.MarkQuitting()
	ms := a.mobile
	if ms != nil {
		a.mu.Lock()
		ms.lastSvcText = ""
		rel := ms.awakeRelease
		ms.awakeRelease = nil
		a.mu.Unlock()
		if rel != nil {
			// Release off the calling path so Android's main-thread onTaskRemoved wait cannot deadlock.
			go rel()
		}
	}
	a.mobileDisconnectAllSessions()
	_ = a.writePrefs()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	client := localapi.NewClient(a.sock())
	_, _ = client.Do(ctx, localapi.Request{Type: localapi.RequestCloseControllers})
	_, _ = client.Do(ctx, localapi.Request{Type: localapi.RequestShutdown})
	cancel()
	a.cancel()

	if ms != nil {
		ms.engineMu.Lock()
		done := ms.engineDone
		ms.engineMu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (a *App) mobileForeground(front bool) {
	if a.mobile == nil {
		return
	}
	a.mobile.clipMu.Lock()
	a.mobile.inFront = front
	a.mobile.clipMu.Unlock()

	// Update front status on the active viewer.
	a.mu.Lock()
	active := a.mobile.activeSession
	a.mu.Unlock()
	for _, t := range a.allTabs() {
		t.setFront(front && t.tabKey() == active)
	}
	if front {
		go a.mobileSyncClipboardToCore()
	}
}

func (a *App) mobileSyncClipboardToCore() {
	if a.mobile == nil {
		return
	}
	// Check image first, then text.
	if a.mobile.host != nil {
		a.mobile.clipMu.Lock()
		unless := a.mobile.lastClipMark
		a.mobile.clipMu.Unlock()
		ci := a.mobile.host.ClipboardImage(unless)
		if ci.Mark != "" && len(ci.PNG) > 0 {
			a.mobile.clipMu.Lock()
			a.mobile.lastClipMark = ci.Mark
			a.mobile.clipMu.Unlock()
			b64 := base64.StdEncoding.EncodeToString(ci.PNG)
			_, _ = a.requestWith(localapi.Request{Type: localapi.RequestAndroidClipboard, ClipboardPNG: b64})
			return
		}
	}
	txt := mygo.Clipboard.ReadText()
	if txt == "" {
		return
	}
	a.mobile.clipMu.Lock()
	same := txt == a.mobile.lastClipText
	a.mobile.lastClipText = txt
	a.mobile.clipMu.Unlock()
	if !same {
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestAndroidClipboard, ClipboardText: &txt})
	}
}

func (a *App) mobileHandleHostClipboard(p clipPacket) []string {
	if p.Text != "" {
		if a.mobile != nil {
			a.mobile.clipMu.Lock()
			a.mobile.lastClipText = p.Text
			a.mobile.clipMu.Unlock()
		}
		clipboardWrite(p.Text)
	}
	if len(p.Paths) == 0 {
		return nil
	}
	cacheDir := ""
	if a.mobile != nil {
		cacheDir = a.mobile.info.CacheDir
	}
	if cacheDir == "" {
		cacheDir, _ = os.UserCacheDir()
	}
	if cacheDir == "" {
		return p.Paths
	}
	dstDir := filepath.Join(cacheDir, "received", strconv.FormatInt(time.Now().UnixNano(), 36))
	if os.MkdirAll(dstDir, 0o700) != nil {
		return p.Paths
	}
	var kept []string
	for _, src := range p.Paths {
		dst := filepath.Join(dstDir, filepath.Base(src))
		if os.Rename(src, dst) == nil {
			kept = append(kept, dst)
		} else {
			kept = append(kept, src)
		}
	}
	return kept
}

func (a *App) mobileSetHostPNG(png []byte) {
	if a.mobile == nil || a.mobile.host == nil || len(png) == 0 {
		return
	}
	mark := a.mobile.host.SetClipboardImage(png)
	a.mobile.clipMu.Lock()
	a.mobile.lastClipMark = mark
	a.mobile.clipMu.Unlock()
}

// collectShareFiles recursively collects regular files from paths so folders
// received over clipboard can be shared via mygo.Share.
func collectShareFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		_ = filepath.WalkDir(p, func(sub string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				out = append(out, sub)
			}
			return nil
		})
	}
	return out
}

// mobileKeepAwake keeps the screen on while viewing a remote session; tests replace it.
var mobileKeepAwake = func() func() { return mygo.Power.KeepAwake("Remote session", true) }

// mobileSyncSessions updates the Android foreground service notification and
// records any observed AI task summaries into MobileAIHistory.
func (a *App) mobileSyncSessions() {
	if a.mobile == nil {
		return
	}
	count := a.mobileOpenSessionCount()
	var titles []string
	if count > 0 {
		titles = a.mobileOpenSessionTitles()
	}

	a.mu.Lock()
	keep := a.prefs.KeepBackgroundSessions
	visible := a.visible
	viewing := a.mobile.activeSession != "" && visible
	quitting := a.quitting
	noLogs := a.prefs.NoLogs
	links := make([]*aiLink, 0, len(a.aiLinks))
	for _, l := range a.aiLinks {
		links = append(links, l)
	}
	var text string
	if keep && count > 0 {
		text = fmt.Sprintf("%d active session(s)", count)
		if len(titles) > 0 {
			text = strings.Join(titles, ", ")
		}
	}
	var svcHost androidhost.Host
	if !quitting && a.mobile.host != nil && text != a.mobile.lastSvcText {
		a.mobile.lastSvcText = text
		svcHost = a.mobile.host
	}
	var rel func()
	needAwake := false
	if !quitting {
		if viewing && a.mobile.awakeRelease == nil {
			needAwake = true
		} else if !viewing && a.mobile.awakeRelease != nil {
			rel = a.mobile.awakeRelease
			a.mobile.awakeRelease = nil
		}
	}
	a.mu.Unlock()

	if svcHost != nil {
		svcHost.SetSessions(text)
	}
	if rel != nil {
		rel()
	}
	if needAwake {
		if acquired := mobileKeepAwake(); acquired != nil {
			a.mu.Lock()
			if a.mobile != nil && a.mobile.awakeRelease == nil && !a.quitting && a.visible && a.mobile.activeSession != "" {
				a.mobile.awakeRelease = acquired
				acquired = nil
			}
			a.mu.Unlock()
			if acquired != nil {
				acquired()
			}
		}
	}
	if !noLogs {
		for _, l := range links {
			snap := l.snapshot()
			if snap.task != nil {
				a.recordMobileAIHistory(l.device, snap.task)
			}
		}
	}
}

func (a *App) recordMobileAIHistory(device string, task *localapi.AITask) {
	if device == "" || task == nil {
		return
	}
	a.mu.Lock()
	if a.prefs.NoLogs {
		a.mu.Unlock()
		return
	}
	p := a.prefs
	a.mu.Unlock()

	sum := MobileAITaskSummary{
		ID:          task.ID,
		Instruction: task.Instruction,
		State:       task.State,
		Steps:       len(task.Steps),
		InputTokens: int(task.Tokens.Input),
		OutTokens:   int(task.Tokens.Output),
		Updated:     task.Updated,
	}
	if sum.Updated == 0 {
		sum.Updated = time.Now().Unix()
	}

	a.mu.Lock()
	cur := slices.Clone(p.MobileAIHistory[device])
	idx := slices.IndexFunc(cur, func(x MobileAITaskSummary) bool { return x.ID == sum.ID })
	if idx >= 0 && cur[idx] == sum {
		a.mu.Unlock()
		return
	}
	if idx >= 0 {
		cur[idx] = sum
	} else {
		cur = append([]MobileAITaskSummary{sum}, cur...)
	}
	slices.SortStableFunc(cur, func(x, y MobileAITaskSummary) int { return cmp.Compare(y.Updated, x.Updated) })
	if len(cur) > 100 {
		cur = cur[:100]
	}
	hist := make(map[string][]MobileAITaskSummary, len(p.MobileAIHistory)+1)
	for k, v := range p.MobileAIHistory {
		hist[k] = v
	}
	hist[device] = cur
	p.MobileAIHistory = hist
	a.mu.Unlock()
	a.setPrefs(p)
}

func (a *App) addMobileRecentTarget(target string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	list := slices.DeleteFunc(slices.Clone(p.MobileRecentTargets), func(s string) bool { return s == target })
	list = append([]string{target}, list...)
	if len(list) > 8 {
		list = list[:8]
	}
	p.MobileRecentTargets = list
	a.setPrefs(p)
}

func (a *App) mobileOpenSessionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.viewers) + len(a.services) + len(a.aiLinks)
	if a.mobile != nil {
		n += len(a.mobile.terms)
	}
	return n
}

func (a *App) mobileOpenSessionTitles() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	type item struct{ key, title string }
	var items []item
	for k, v := range a.viewers {
		items = append(items, item{k, v.title})
	}
	for k, s := range a.services {
		items = append(items, item{k, s.title})
	}
	if a.mobile != nil {
		for k, ts := range a.mobile.terms {
			items = append(items, item{k, ts.name + " · SSH"})
		}
	}
	slices.SortFunc(items, func(x, y item) int { return cmp.Compare(x.key, y.key) })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.title
	}
	return out
}

func (a *App) mobileUpdateTouchCapture(
	capture bool,
	stage, pad, ex1, ex2 ui.Rect,
	handler func(androidhost.RawTouch),
) {
	ms := a.mobile
	if ms == nil || ms.host == nil {
		return
	}
	ms.touchMu.Lock()
	if capture {
		ms.touchHandler = handler
	} else {
		ms.touchHandler = nil
	}
	var cfg [17]float64
	if capture {
		cfg = [17]float64{
			1,
			float64(stage.X), float64(stage.Y), float64(stage.X + stage.W), float64(stage.Y + stage.H),
			float64(pad.X), float64(pad.Y), float64(pad.X + pad.W), float64(pad.Y + pad.H),
			float64(ex1.X), float64(ex1.Y), float64(ex1.X + ex1.W), float64(ex1.Y + ex1.H),
			float64(ex2.X), float64(ex2.Y), float64(ex2.X + ex2.W), float64(ex2.Y + ex2.H),
		}
	}
	same := cfg == ms.lastTouchCfg
	ms.lastTouchCfg = cfg
	ms.touchMu.Unlock()
	if same {
		return
	}
	ms.host.SetTouchBounds(
		capture,
		cfg[1], cfg[2], cfg[3], cfg[4],
		cfg[5], cfg[6], cfg[7], cfg[8],
		cfg[9], cfg[10], cfg[11], cfg[12],
		cfg[13], cfg[14], cfg[15], cfg[16],
	)
}

// wakeHost wakes a host's display: via an already-open desktop session with
// control-input if available, else via a short-lived control-input session.
func (a *App) wakeHost(device string) {
	if v := a.viewerFor(device); v != nil {
		v.mu.Lock()
		ctrl := v.control && v.phase.State == "connected"
		v.mu.Unlock()
		if ctrl {
			v.sendControl(map[string]any{"type": "action", "action": "wake"})
			a.update(func() { a.setToast(L.MobileWakeSent(), false) })
			return
		}
	}
	a.update(func() { a.setToast(L.MobileWaking(), false) })
	go func() {
		resp, err := a.requestWith(localapi.Request{
			Type: localapi.RequestConnect,
			Connect: &localapi.ConnectRequest{
				DeviceID: device,
				Want:     []string{localapi.PermControlInput},
			},
		})
		if err != nil {
			a.update(func() { a.setToast(L.MobileWakeFailed(), true) })
			return
		}
		sid := resp.ID
		a.dropSession(sid)
		defer a.closeSession(sid)

		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			stResp, err := a.request(localapi.RequestStatus)
			if err == nil && stResp != nil && stResp.Status != nil {
				for _, s := range stResp.Status.Sessions {
					if s.ID == sid {
						if s.State == "connected" {
							conn, err := localapi.NewClient(a.sock()).Media(a.ctx, sid)
							if err == nil {
								pkt := append([]byte{packetControl}, `{"type":"action","action":"wake"}`...)
								_ = localapi.WritePacket(conn, pkt)
								time.Sleep(150 * time.Millisecond)
								_ = conn.Close()
								a.update(func() { a.setToast(L.MobileWakeSent(), false) })
								return
							}
						}
						if s.State == "closed" || s.State == "failed" {
							a.update(func() { a.setToast(L.MobileWakeFailed(), true) })
							return
						}
					}
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		a.update(func() { a.setToast(L.MobileWakeFailed(), true) })
	}()
}
