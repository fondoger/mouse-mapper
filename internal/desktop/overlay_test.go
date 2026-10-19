package desktop

import (
	"math"
	"runtime"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/input"
)

func TestOverlayFrameUsesBottomRightWorkArea(t *testing.T) {
	for _, tc := range []struct {
		name string
		area mygo.Rectangle
		want mygo.Rectangle
	}{
		{"primary", mygo.Rectangle{Width: 1920, Height: 1040}, mygo.Rectangle{X: 1792, Y: 986, Width: 112, Height: 38}},
		{"left-monitor", mygo.Rectangle{X: -1920, Y: -200, Width: 1920, Height: 1080}, mygo.Rectangle{X: -128, Y: 826, Width: 112, Height: 38}},
		{"menu-and-dock", mygo.Rectangle{X: 1440, Y: 25, Width: 1280, Height: 820}, mygo.Rectangle{X: 2592, Y: 791, Width: 112, Height: 38}},
		{"small-work-area", mygo.Rectangle{X: -100, Y: 70, Width: 60, Height: 50}, mygo.Rectangle{X: -84, Y: 86, Width: 28, Height: 18}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlayFrame(tc.area, 112, 38, 16); got != tc.want {
				t.Fatalf("frame = %+v; want %+v", got, tc.want)
			}
		})
	}
}

func TestOverlayIsCompactAndFitsEveryShortcut(t *testing.T) {
	width, height := overlaySize([]string{"Control", "KeyC"}, "windows")
	if width > 130 || height > 40 || width*height >= 350*104/5 {
		t.Fatalf("ordinary shortcut is not compact: %dx%d", width, height)
	}
	for _, platform := range []string{"windows", "darwin"} {
		for _, key := range input.SupportedCodes() {
			keys := []string{"Control", "Alt", "Shift", "Meta", key}
			width, height := overlaySize(keys, platform)
			textWidth, _ := ui.ShapeText(describeKeys(keys, platform), ui.Font{Family: overlayFont, Size: overlayFontSize, Weight: 600}, 0).Size()
			if width < int(math.Ceil(float64(textWidth)))+47 || width > 360 || height != overlayHeight {
				t.Errorf("%s %s: text %.1f cannot fit compact %dx%d", platform, key, textWidth, width, height)
			}
		}
	}
}

func TestOverlayOnlyShowsShortcutOnOneLine(t *testing.T) {
	v := testView(newUIMapper())
	keys := []string{"Control", "Shift", "KeyK"}
	v.acceptShortcut(ShortcutEvent{Button: input.Back, Keys: keys, Sequence: 1, Duration: 1200})
	width, height := overlaySize(keys, runtime.GOOS)
	tt := ui.NewTester(v.overlay, width, height)
	if !tt.HasText(describeKeys(keys, runtime.GOOS)) || len(tt.Texts()) != 1 {
		t.Fatalf("overlay has extra labels or missing shortcut: %v", tt.Texts())
	}
}
