//go:build windows

package desktop

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/input"
)

// Native overlay verification is opt-in and never starts the input service.
// It briefly shows only test-owned, non-activating windows at the screen edge.
func TestMain(m *testing.M) {
	if os.Getenv("MOUSE_MAPPER_NATIVE_OVERLAY_TEST") != "1" {
		os.Exit(m.Run())
	}
	mygo.App.SetName("Mouse Mapper Overlay Test")
	mygo.App.OnWindowAllClosed(func() {})
	mygo.Theme.SetSource(mygo.ThemeLight)
	code := 1
	mygo.App.WhenReady(func() {
		go func() {
			code = m.Run()
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func TestNativeOverlayCompositionAndPlacement(t *testing.T) {
	if os.Getenv("MOUSE_MAPPER_NATIVE_OVERLAY_TEST") != "1" {
		t.Skip("set MOUSE_MAPPER_NATIVE_OVERLAY_TEST=1 for the real non-interactive Windows overlay check")
	}
	var overlay, backdrop *mygo.Window
	var frame mygo.Rectangle
	var setupErr error
	var dpi uint32
	keys := []string{"Control", "KeyC"}
	width, height := overlaySize(keys, "windows")
	mygo.RunOnMain(func() {
		area, scale, err := overlayWorkArea()
		if err != nil {
			setupErr = err
			return
		}
		dpi = scale
		frame = overlayFrame(area, overlayPixels(width, dpi), overlayPixels(height, dpi), overlayPixels(overlayMargin, dpi))
		backdrop = mygo.NewWindow(mygo.WindowOptions{
			Title: "Overlay Test Backdrop", Width: width + 24, Height: height + 24,
			Hidden: true, Frameless: true, Panel: true, AlwaysOnTop: true, SkipTaskbar: true, DisableShadow: true, DisableResize: true,
			BackgroundColor: "#225566", Content: ui.View(func(c *ui.Context) { c.Root().Background(ui.Hex("#225566")) }),
		})
		handle := backdrop.NativeHandle()
		style, _, _ := overlayGetWindowStyle.Call(handle, ^uintptr(19))
		overlaySetWindowStyle.Call(handle, ^uintptr(19), style|0x08000000)
		overlaySetWindowPos.Call(handle, ^uintptr(0), uintptr(frame.X-12), uintptr(frame.Y-12), uintptr(frame.Width+24), uintptr(frame.Height+24), 0x0210)
		backdrop.ShowInactive()
		view := testView(newUIMapper())
		view.acceptShortcut(ShortcutEvent{Button: input.Back, Keys: keys, Sequence: 1, Duration: 5000})
		overlay, setupErr = newShortcutOverlay(view)
		if setupErr == nil {
			setupErr = placeOverlay(overlay, width, height)
		}
		if setupErr == nil {
			overlay.ShowInactive()
		}
	})
	t.Cleanup(func() {
		if overlay != nil {
			overlay.Destroy()
		}
		if backdrop != nil {
			backdrop.Destroy()
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}

	var actual overlayRect
	overlayGetWindowRect.Call(overlay.NativeHandle(), uintptr(unsafe.Pointer(&actual)))
	var backdropBounds overlayRect
	overlayGetWindowRect.Call(backdrop.NativeHandle(), uintptr(unsafe.Pointer(&backdropBounds)))
	t.Logf("overlay physical=%+v backdrop=%+v", actual, backdropBounds)
	want := overlayRect{int32(frame.X), int32(frame.Y), int32(frame.X + frame.Width), int32(frame.Y + frame.Height)}
	if actual != want {
		t.Fatalf("native overlay bounds %+v; want bottom-right work-area bounds %+v", actual, want)
	}
	if overlay.IsMovable() {
		t.Fatal("overlay remained user-movable after programmatic placement")
	}

	capture := mygo.Rectangle{X: frame.X - 8, Y: frame.Y - 8, Width: frame.Width + 16, Height: frame.Height + 16}
	var screen *image.RGBA
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(80 * time.Millisecond)
		mygo.RunOnMain(func() {
			screen, err = captureOverlayScreen(capture)
		})
		if err != nil {
			t.Fatal(err)
		}
		if closeOverlayColor(screen.RGBAAt(9, 9), color.RGBA{0x22, 0x55, 0x66, 255}) &&
			closeOverlayColor(screen.RGBAAt(8+frame.Width/2, 8+overlayPixels(5, dpi)), color.RGBA{255, 255, 255, 255}) {
			break
		}
	}
	snapshot := os.Getenv("MOUSE_MAPPER_OVERLAY_SNAPSHOT")
	if snapshot == "" {
		snapshot = filepath.Join(t.TempDir(), "overlay-composed.png")
	}
	if err := writeOverlayScreen(snapshot, screen); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual Windows-composed overlay: %s (%dx%d px, %d DPI)", snapshot, frame.Width, frame.Height, dpi)
	for _, point := range []image.Point{{9, 9}, {8 + frame.Width - 2, 9}, {9, 8 + frame.Height - 2}, {8 + frame.Width - 2, 8 + frame.Height - 2}} {
		// DWM may leave a faint menu shadow below a panel even with its
		// client shadow disabled; the original solid black rectangle fails.
		if got := screen.RGBAAt(point.X, point.Y); !nearOverlayColor(got, color.RGBA{0x22, 0x55, 0x66, 255}, 20) {
			t.Errorf("rounded corner %v shows %v instead of the backdrop (black-square regression)", point, got)
		}
	}
	if got := screen.RGBAAt(8+frame.Width/2, 8+overlayPixels(5, dpi)); !closeOverlayColor(got, color.RGBA{255, 255, 255, 255}) {
		t.Errorf("native shortcut pill did not compose an opaque white background: %v", got)
	}
	if overlay.IsFocused() || backdrop.IsFocused() {
		t.Error("a non-activating test window acquired keyboard focus")
	}
	point := uintptr(uint32(frame.X+frame.Width/2)) | uintptr(uint32(frame.Y+frame.Height/2))<<32
	under, _, _ := overlayUser32.NewProc("WindowFromPoint").Call(point)
	root, _, _ := overlayUser32.NewProc("GetAncestor").Call(under, 2)
	if root != backdrop.NativeHandle() {
		t.Errorf("overlay did not let hit testing reach the test backdrop: got %#x, want %#x", root, backdrop.NativeHandle())
	}
}

func closeOverlayColor(got, want color.RGBA) bool {
	return nearOverlayColor(got, want, 4)
}

func nearOverlayColor(got, want color.RGBA, tolerance int) bool {
	delta := func(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }
	return delta(got.R, want.R) <= tolerance && delta(got.G, want.G) <= tolerance && delta(got.B, want.B) <= tolerance
}

func writeOverlayScreen(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func captureOverlayScreen(frame mygo.Rectangle) (*image.RGBA, error) {
	// Capture the composed desktop, not CapturePage's offscreen CPU rendering.
	// The small crop contains only our opaque test backdrop and the overlay.
	overlayDwmFlush()
	dc, _, err := overlayUser32.NewProc("GetDC").Call(0)
	if dc == 0 {
		return nil, err
	}
	defer overlayUser32.NewProc("ReleaseDC").Call(0, dc)
	memory, _, err := overlayGDI32.NewProc("CreateCompatibleDC").Call(dc)
	if memory == 0 {
		return nil, err
	}
	defer overlayGDI32.NewProc("DeleteDC").Call(memory)
	bitmap, _, err := overlayGDI32.NewProc("CreateCompatibleBitmap").Call(dc, uintptr(frame.Width), uintptr(frame.Height))
	if bitmap == 0 {
		return nil, err
	}
	defer overlayDeleteObject.Call(bitmap)
	selectObject := overlayGDI32.NewProc("SelectObject")
	old, _, _ := selectObject.Call(memory, bitmap)
	ok, _, err := overlayGDI32.NewProc("BitBlt").Call(memory, 0, 0, uintptr(frame.Width), uintptr(frame.Height), dc, uintptr(frame.X), uintptr(frame.Y), 0x40cc0020)
	selectObject.Call(memory, old)
	if ok == 0 {
		return nil, err
	}
	header := struct {
		Size          uint32
		Width, Height int32
		Planes, Bits  uint16
		Compression   uint32
		ImageSize     uint32
		X, Y          int32
		Used, Needed  uint32
	}{Size: 40, Width: int32(frame.Width), Height: -int32(frame.Height), Planes: 1, Bits: 32}
	img := image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	lines, _, err := overlayGDI32.NewProc("GetDIBits").Call(dc, bitmap, 0, uintptr(frame.Height), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&header)), 0)
	if int(lines) != frame.Height {
		return nil, fmt.Errorf("capture composed overlay: %w", err)
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 255
	}
	return img, nil
}

func overlayDwmFlush() {
	// mygo's native compositor has returned by the time DWM presents a frame.
	mygo.RunOnMain(func() {
		syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmFlush").Call()
	})
}
