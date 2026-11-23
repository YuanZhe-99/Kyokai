package gui

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"github.com/YuanZhe-99/Kyokai/internal/androidhost"
)

// Mobile Material Design 3 geometry and spacing tokens.
const (
	mobileWideMinW          float32 = 600
	mobileKeyboardThreshold float32 = 120
	mobileBottomBarH        float32 = 76
	mobileFloatDockH        float32 = 62
	mobileRailW             float32 = 84
	mobileTopBarH           float32 = 56
	mobileCardRadius        float32 = 20
	mobileSheetRadius       float32 = 28
	mobileBtnRadius         float32 = 24
	mobileChipRadius        float32 = 16
	mobileTileRadius        float32 = 16
	mobileNavPillW          float32 = 64
	mobileNavPillH          float32 = 32
	mobileNavPillRadius     float32 = 16
)

// mobileColors holds the Material Design 3 tonal palette and keyboard surface
// hierarchy used across Android controller views.
type mobileColors struct {
	Dark bool

	Primary              ui.Color
	OnPrimary            ui.Color
	PrimaryContainer     ui.Color
	OnPrimaryContainer   ui.Color
	SecondaryContainer   ui.Color
	OnSecondaryContainer ui.Color

	Surface                 ui.Color
	SurfaceContainerLowest  ui.Color
	SurfaceContainerLow     ui.Color
	SurfaceContainer        ui.Color
	SurfaceContainerHigh    ui.Color
	SurfaceContainerHighest ui.Color

	OnSurface        ui.Color
	OnSurfaceVariant ui.Color
	Outline          ui.Color
	OutlineVariant   ui.Color

	Error            ui.Color
	OnError          ui.Color
	ErrorContainer   ui.Color
	OnErrorContainer ui.Color

	Success ui.Color
	Warning ui.Color

	// On-screen keyboard tonal hierarchy (tray darker than functional keys,
	// character keys distinctly brighter/elevated in both light and dark modes).
	KbTray        ui.Color
	KbKey         ui.Color
	KbKeyPressed  ui.Color
	KbFuncKey     ui.Color
	KbFuncPressed ui.Color
	KbKeyShadow   ui.Color
}

// argbColor unpacks an Android 0xAARRGGBB color integer into ui.Color.
func argbColor(c uint32) ui.Color {
	return ui.Color{
		A: uint8(c >> 24),
		R: uint8(c >> 16),
		G: uint8(c >> 8),
		B: uint8(c),
	}
}

// resolveMobileColors returns the active Material Design 3 tonal palette for
// dark, using Android 12+ Material You system colors when pal.Available is
// true, or Kyokai's jade-teal (#04342C seed) Material 3 palette otherwise.
func resolveMobileColors(dark bool, pal androidhost.MaterialPalette) mobileColors {
	if pal.Available {
		if dark {
			surf := argbColor(pal.SurfaceDark)
			low := argbColor(pal.SurfaceContainerLowDark)
			cont := argbColor(pal.SurfaceContainerDark)
			high := argbColor(pal.SurfaceContainerHighDark)
			highest := argbColor(pal.SurfaceContainerHighestDark)
			lowest := argbColor(pal.SurfaceContainerLowestDark)
			primary := argbColor(pal.PrimaryDark)
			return mobileColors{
				Dark:                    true,
				Primary:                 primary,
				OnPrimary:               argbColor(pal.OnPrimaryDark),
				PrimaryContainer:        argbColor(pal.PrimaryContainerDark),
				OnPrimaryContainer:      argbColor(pal.OnPrimaryContainerDark),
				SecondaryContainer:      argbColor(pal.SecondaryContainerDark),
				OnSecondaryContainer:    argbColor(pal.OnSecondaryContainerDark),
				Surface:                 surf,
				SurfaceContainerLowest:  lowest,
				SurfaceContainerLow:     low,
				SurfaceContainer:        cont,
				SurfaceContainerHigh:    high,
				SurfaceContainerHighest: highest,
				OnSurface:               argbColor(pal.OnSurfaceDark),
				OnSurfaceVariant:        argbColor(pal.OnSurfaceVariantDark),
				Outline:                 argbColor(pal.OutlineDark),
				OutlineVariant:          argbColor(pal.OutlineVariantDark),
				Error:                   ui.Hex("#FFB4AB"),
				OnError:                 ui.Hex("#690005"),
				ErrorContainer:          ui.Hex("#93000A"),
				OnErrorContainer:        ui.Hex("#FFDAD6"),
				Success:                 ui.Hex("#6DD58C"),
				Warning:                 ui.Hex("#FFB74D"),
				KbTray:                  lowest,
				KbKey:                   highest,
				KbKeyPressed:            highest.Mix(primary, 0.24),
				KbFuncKey:               cont,
				KbFuncPressed:           high,
				KbKeyShadow:             ui.RGBA(0, 0, 0, 0.45),
			}
		}
		primary := argbColor(pal.PrimaryLight)
		cont := argbColor(pal.SurfaceContainerLight)
		high := argbColor(pal.SurfaceContainerHighLight)
		highest := argbColor(pal.SurfaceContainerHighestLight)
		lowest := argbColor(pal.SurfaceContainerLowestLight)
		return mobileColors{
			Dark:                    false,
			Primary:                 primary,
			OnPrimary:               argbColor(pal.OnPrimaryLight),
			PrimaryContainer:        argbColor(pal.PrimaryContainerLight),
			OnPrimaryContainer:      argbColor(pal.OnPrimaryContainerLight),
			SecondaryContainer:      argbColor(pal.SecondaryContainerLight),
			OnSecondaryContainer:    argbColor(pal.OnSecondaryContainerLight),
			Surface:                 argbColor(pal.SurfaceLight),
			SurfaceContainerLowest:  lowest,
			SurfaceContainerLow:     argbColor(pal.SurfaceContainerLowLight),
			SurfaceContainer:        cont,
			SurfaceContainerHigh:    high,
			SurfaceContainerHighest: highest,
			OnSurface:               argbColor(pal.OnSurfaceLight),
			OnSurfaceVariant:        argbColor(pal.OnSurfaceVariantLight),
			Outline:                 argbColor(pal.OutlineLight),
			OutlineVariant:          argbColor(pal.OutlineVariantLight),
			Error:                   ui.Hex("#BA1A1A"),
			OnError:                 ui.Hex("#FFFFFF"),
			ErrorContainer:          ui.Hex("#FFDAD6"),
			OnErrorContainer:        ui.Hex("#410002"),
			Success:                 ui.Hex("#146C2E"),
			Warning:                 ui.Hex("#B25E00"),
			KbTray:                  high,
			KbKey:                   lowest,
			KbKeyPressed:            highest.Mix(primary, 0.14),
			KbFuncKey:               highest.Mix(argbColor(pal.OutlineVariantLight), 0.35),
			KbFuncPressed:           highest.Mix(primary, 0.24),
			KbKeyShadow:             ui.RGBA(0, 0, 0, 0.16),
		}
	}

	if dark {
		return mobileColors{
			Dark:                    true,
			Primary:                 ui.Hex("#7FD8C5"),
			OnPrimary:               ui.Hex("#00372E"),
			PrimaryContainer:        ui.Hex("#005044"),
			OnPrimaryContainer:      ui.Hex("#A6F2E2"),
			SecondaryContainer:      ui.Hex("#334B46"),
			OnSecondaryContainer:    ui.Hex("#CCE8E0"),
			Surface:                 ui.Hex("#0F1513"),
			SurfaceContainerLowest:  ui.Hex("#0A0F0E"),
			SurfaceContainerLow:     ui.Hex("#171D1B"),
			SurfaceContainer:        ui.Hex("#1B211F"),
			SurfaceContainerHigh:    ui.Hex("#252B29"),
			SurfaceContainerHighest: ui.Hex("#303634"),
			OnSurface:               ui.Hex("#DEE4E1"),
			OnSurfaceVariant:        ui.Hex("#BFC9C5"),
			Outline:                 ui.Hex("#899390"),
			OutlineVariant:          ui.Hex("#3F4946"),
			Error:                   ui.Hex("#FFB4AB"),
			OnError:                 ui.Hex("#690005"),
			ErrorContainer:          ui.Hex("#93000A"),
			OnErrorContainer:        ui.Hex("#FFDAD6"),
			Success:                 ui.Hex("#6DD58C"),
			Warning:                 ui.Hex("#FFB74D"),
			KbTray:                  ui.Hex("#0B0F0E"),
			KbKey:                   ui.Hex("#2B3230"),
			KbKeyPressed:            ui.Hex("#3E4845"),
			KbFuncKey:               ui.Hex("#1C2321"),
			KbFuncPressed:           ui.Hex("#2C3633"),
			KbKeyShadow:             ui.RGBA(0, 0, 0, 0.45),
		}
	}

	return mobileColors{
		Dark:                    false,
		Primary:                 ui.Hex("#0E6B5C"),
		OnPrimary:               ui.Hex("#FFFFFF"),
		PrimaryContainer:        ui.Hex("#A6F2E2"),
		OnPrimaryContainer:      ui.Hex("#00201A"),
		SecondaryContainer:      ui.Hex("#CCE8E0"),
		OnSecondaryContainer:    ui.Hex("#07201B"),
		Surface:                 ui.Hex("#F5FBF9"),
		SurfaceContainerLowest:  ui.Hex("#FFFFFF"),
		SurfaceContainerLow:     ui.Hex("#EFF5F3"),
		SurfaceContainer:        ui.Hex("#E9F0EE"),
		SurfaceContainerHigh:    ui.Hex("#E2EAE8"),
		SurfaceContainerHighest: ui.Hex("#DCE4E2"),
		OnSurface:               ui.Hex("#171D1B"),
		OnSurfaceVariant:        ui.Hex("#3F4946"),
		Outline:                 ui.Hex("#6F7976"),
		OutlineVariant:          ui.Hex("#BFC9C5"),
		Error:                   ui.Hex("#BA1A1A"),
		OnError:                 ui.Hex("#FFFFFF"),
		ErrorContainer:          ui.Hex("#FFDAD6"),
		OnErrorContainer:        ui.Hex("#410002"),
		Success:                 ui.Hex("#146C2E"),
		Warning:                 ui.Hex("#B25E00"),
		KbTray:                  ui.Hex("#DCE3E2"),
		KbKey:                   ui.Hex("#FFFFFF"),
		KbKeyPressed:            ui.Hex("#D9E4E2"),
		KbFuncKey:               ui.Hex("#C8D3D1"),
		KbFuncPressed:           ui.Hex("#B4C2C0"),
		KbKeyShadow:             ui.RGBA(0, 0, 0, 0.16),
	}
}

// mobileUITheme adapts MyGo's base theme to Material 3 tokens so even shared
// desktop sub-views (Networks, Sign-in Keys, About) render harmoniously on mobile.
func mobileUITheme(base *ui.Theme, mc mobileColors) *ui.Theme {
	r := *base
	r.Dark = mc.Dark
	r.Background = mc.Surface
	r.Surface = mc.SurfaceContainerLow
	r.SurfaceHover = mc.SurfaceContainerHigh
	r.SurfacePressed = mc.SurfaceContainerHighest
	r.Border = mc.OutlineVariant
	r.Text = mc.OnSurface
	r.TextMuted = mc.OnSurfaceVariant
	r.Accent = mc.Primary
	r.AccentHover = mc.Primary
	r.AccentPressed = mc.Primary
	r.AccentText = mc.OnPrimary
	r.Selection = mc.Primary.Alpha(0.25)
	r.Focus = mc.Primary.Alpha(0.55)
	r.Danger = mc.Error
	r.Warning = mc.Warning
	r.Success = mc.Success
	r.Radius = 16
	r.Spacing = 4
	return &r
}

// mobilePaletteFor returns the Material 3 palette for the active context theme.
func (a *App) mobilePaletteFor(c *ui.Context) mobileColors {
	var pal androidhost.MaterialPalette
	if a != nil && a.mobile != nil {
		pal = a.mobile.palette
	}
	return resolveMobileColors(c.Theme().Dark, pal)
}

// m3TopBar renders a Material 3 TopAppBar with circular ripple action slots.
func m3TopBar(c *ui.Context, mc mobileColors, o mobile.TopBarOptions) {
	bg := mc.Surface
	if o.Background != nil {
		bg = *o.Background
	}
	leftPad := float32(16)
	if o.Leading != nil {
		leftPad = 6
	}
	rightPad := float32(16)
	if o.Trailing != nil {
		rightPad = 10
	}
	ui.Row(c).FillWidth().Height(mobileTopBarH).Padding(0, rightPad, 0, leftPad).Gap(6).
		Background(bg).Shrink(0).AlignItems(ui.Center).Role(ui.RoleGroup).Children(func() {
		if o.Leading != nil {
			o.Leading()
		}
		ui.Text(c, o.Title).FontSize(22).FontWeight(600).TextColor(mc.OnSurface).SingleLine().Grow(1).MinWidth(0).
			Role(ui.RoleHeading).Level(1)
		if o.Trailing != nil {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(o.Trailing)
		}
	})
}

// m3IconButton renders a circular 44dp Material 3 icon button with Ink ripple.
func m3IconButton(c *ui.Context, key string, icon *ui.SVG, label string, fg, bg ui.Color) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Size(44, 44).Radius(22).Background(bg).Center().Clip().Label(label)
	mobile.Ink(b)
	b.Children(func() {
		ui.Icon(c, icon).FontSize(20).TextColor(fg)
	})
	return b
}

// m3FilledButton renders a full-pill Material 3 primary filled button.
func m3FilledButton(c *ui.Context, mc mobileColors, key, label string, disabled bool) ui.Element {
	bg, fg := mc.Primary, mc.OnPrimary
	if disabled {
		bg = mc.OnSurface.Alpha(0.12)
		fg = mc.OnSurface.Alpha(0.38)
	}
	b := ui.ButtonBase(c.Key(key)).Height(48).PaddingX(20).Radius(mobileBtnRadius).Center().Clip().
		Background(bg).Disabled(disabled).Label(label)
	mobile.Ink(b)
	b.Children(func() {
		ui.Text(c, label).FontSize(14).FontWeight(600).TextColor(fg).SingleLine()
	})
	return b
}

// m3TonalButton renders a full-pill Material 3 secondary tonal button.
func m3TonalButton(c *ui.Context, mc mobileColors, key, label string, disabled bool) ui.Element {
	bg, fg := mc.SecondaryContainer, mc.OnSecondaryContainer
	if disabled {
		bg = mc.OnSurface.Alpha(0.12)
		fg = mc.OnSurface.Alpha(0.38)
	}
	b := ui.ButtonBase(c.Key(key)).Height(46).PaddingX(16).Radius(mobileBtnRadius).Center().Clip().
		Background(bg).Disabled(disabled).Label(label)
	mobile.Ink(b)
	b.Children(func() {
		ui.Text(c, label).FontSize(14).FontWeight(600).TextColor(fg).SingleLine()
	})
	return b
}

// m3OutlinedButton renders a full-pill Material 3 outlined button.
func m3OutlinedButton(c *ui.Context, mc mobileColors, key, label string, fg ui.Color, disabled bool) ui.Element {
	if fg == (ui.Color{}) {
		fg = mc.Primary
	}
	b := ui.ButtonBase(c.Key(key)).Height(46).PaddingX(16).Radius(mobileBtnRadius).Center().Clip().
		Background(mc.SurfaceContainerLow).Border(1, mc.OutlineVariant).Disabled(disabled).Label(label)
	mobile.Ink(b)
	b.Children(func() {
		ui.Text(c, label).FontSize(14).FontWeight(600).TextColor(fg).SingleLine()
	})
	return b
}

// m3Card returns a soft Material 3 rounded container card.
func m3Card(c *ui.Context, mc mobileColors, key string) ui.Element {
	return ui.Column(c.Key(key)).FillWidth().Padding(16).Gap(10).Radius(mobileCardRadius).
		Background(mc.SurfaceContainerLow).Border(1, mc.OutlineVariant.Alpha(0.45))
}

// m3TonalCard returns an elevated-tone Material 3 container card without heavy borders.
func m3TonalCard(c *ui.Context, mc mobileColors, key string) ui.Element {
	return ui.Column(c.Key(key)).FillWidth().Padding(16).Gap(10).Radius(mobileCardRadius).
		Background(mc.SurfaceContainer)
}

// m3Segmented renders a Material 3 SegmentedButton pill with leading checkmark on
// the active segment and Ink ripples. It returns true when *selected changed.
func m3Segmented(c *ui.Context, mc mobileColors, key string, selected *int, labels ...string) bool {
	parts := ui.SegmentedBase(c.Key(key), selected, len(labels))
	track := parts.Track.FillWidth().Height(42).Radius(21).Background(mc.SurfaceContainerLow).
		Border(1, mc.OutlineVariant).Clip()
	track.Children(func() {
		for i, label := range labels {
			if i > 0 {
				ui.Box(c).Width(1).FillHeight().Background(mc.OutlineVariant)
			}
			on := i == *selected
			seg := parts.Segment(i).Grow(1).Basis(0).MinWidth(0).FillHeight().PaddingX(8).Center().Clip()
			fg := mc.OnSurfaceVariant
			if on {
				seg.Background(mc.SecondaryContainer)
				fg = mc.OnSecondaryContainer
			}
			mobile.Ink(seg)
			seg.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Justify(ui.Center).Gap(5).Children(func() {
					if on {
						ui.Icon(c, rowCheck).FontSize(15).TextColor(fg)
					}
					weight := 500
					if on {
						weight = 600
					}
					ui.Text(c, label).FontSize(13).FontWeight(weight).TextColor(fg).SingleLine()
				})
			})
		}
	})
	return track.Changed()
}

// m3Switch renders Material's animated Switch widget with haptics and returns
// whether its state changed in this frame.
func m3Switch(c *ui.Context, key string, on *bool, label string) bool {
	sw := mobile.Switch(c.Key(key), on).Label(label)
	return sw.Changed()
}

// m3StatusChip renders a pill-shaped status badge with a leading colored dot,
// optionally clickable with an Ink ripple.
func m3StatusChip(c *ui.Context, mc mobileColors, key, text string, col ui.Color, clickable bool) ui.Element {
	t := c.Theme()
	bg := col.Alpha(0.14)
	if mc.Dark {
		bg = col.Alpha(0.18)
	}
	fg := textTone(t, col)
	if col == mc.OnSurfaceVariant || col == t.TextMuted {
		bg = mc.SurfaceContainerHigh
		fg = mc.OnSurfaceVariant
	}
	var pill ui.Element
	if clickable {
		pill = ui.ButtonBase(c.Key(key)).Height(30).Padding(0, 12).Radius(15).Background(bg).Center().Clip().Label(text)
		mobile.Ink(pill)
	} else {
		pill = ui.Row(c).Height(28).Padding(0, 10).Radius(14).
			Background(bg).AlignItems(ui.Center)
	}
	pill.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Size(7, 7).Radius(3.5).Background(col).Shrink(0)
			ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(fg).SingleLine().MaxWidth(homeCardMinW / 2)
		})
	})
	return pill
}
