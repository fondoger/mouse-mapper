package desktop

import (
	"math"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	overlayHeight   = 38
	overlayMargin   = 16
	overlayRadius   = 12
	overlayFontSize = 13
	overlayFont     = "Manrope, PingFang SC, Microsoft YaHei"
)

func newShortcutOverlay(view *nativeView) (*mygo.Window, error) {
	background := "#0000"
	if runtime.GOOS == "windows" {
		background = "#ffffff"
	}
	window := mygo.NewWindow(mygo.WindowOptions{
		Title: "Mouse Mapper Shortcut", Content: ui.View(view.overlay), Width: 112, Height: overlayHeight,
		Hidden: true, Frameless: true, Transparent: runtime.GOOS != "windows", BackgroundColor: background,
		AlwaysOnTop: true, SkipTaskbar: true, DisableResize: true, DisableMove: true,
		DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true, DisableShadow: true,
		Panel: true,
	})
	window.SetMenu(mygo.NewMenu(nil))
	window.SetIgnoreMouseEvents(true)
	window.SetVisibleOnAllWorkspaces(true)
	return window, configureOverlay(window)
}

func overlayBackground() ui.Color {
	// MyGo's Windows native surface is opaque without a composition backdrop.
	// The HWND's rounded region clips the opaque canvas instead of showing a
	// black rectangle wherever the UI would otherwise paint transparent pixels.
	if runtime.GOOS == "windows" {
		return white
	}
	return clear
}

func overlaySize(keys []string, platform string) (int, int) {
	prepareInterfaceFont()
	label := describeKeys(keys, platform)
	textWidth, _ := ui.ShapeText(label, ui.Font{Family: overlayFont, Size: overlayFontSize, Weight: 600}, 0).Size()
	// Two insets, the mouse glyph and its gap, plus a small text rounding guard.
	return max(112, int(math.Ceil(float64(textWidth)))+24+14+9+4), overlayHeight
}

func overlayFrame(area mygo.Rectangle, width, height, margin int) mygo.Rectangle {
	marginX := min(margin, max(0, (area.Width-1)/2))
	marginY := min(margin, max(0, (area.Height-1)/2))
	width = min(width, max(1, area.Width-2*marginX))
	height = min(height, max(1, area.Height-2*marginY))
	return mygo.Rectangle{
		X: area.X + area.Width - marginX - width, Y: area.Y + area.Height - marginY - height,
		Width: width, Height: height,
	}
}
