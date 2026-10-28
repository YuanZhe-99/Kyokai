package gui

import "github.com/ebitengine/purego/objc"

// LaunchedAsLoginItem reports whether macOS started Kyokai as a login item:
// the Apple Event AppKit is handling opens the app and marks it so.
// Notes: valid only inside applicationDidFinishLaunching:, which AppKit sends
// while it handles the launch's open-application event and where MyGo runs
// WhenReady's functions. MyGo's own answer (App.WasOpenedAtLogin) is taken in
// applicationWillFinishLaunching:, before that event, and is always no.
func LaunchedAsLoginItem() bool {
	const (
		kAEOpenApplication       = 'o'<<24 | 'a'<<16 | 'p'<<8 | 'p'
		keyAEPropData            = 'p'<<24 | 'r'<<16 | 'd'<<8 | 't'
		keyAELaunchedAsLogInItem = 'l'<<24 | 'g'<<16 | 'i'<<8 | 't'
	)
	mgr := objc.ID(objc.GetClass("NSAppleEventManager")).Send(objc.RegisterName("sharedAppleEventManager"))
	ev := mgr.Send(objc.RegisterName("currentAppleEvent"))
	if ev == 0 || objc.Send[uint32](ev, objc.RegisterName("eventID")) != kAEOpenApplication {
		return false
	}
	prop := ev.Send(objc.RegisterName("paramDescriptorForKeyword:"), uint32(keyAEPropData))
	return prop != 0 && objc.Send[uint32](prop, objc.RegisterName("enumCodeValue")) == keyAELaunchedAsLogInItem
}
