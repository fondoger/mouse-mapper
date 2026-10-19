//go:build windows

package desktop

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
)

var (
	overlayUser32           = syscall.NewLazyDLL("user32.dll")
	overlayGDI32            = syscall.NewLazyDLL("gdi32.dll")
	overlayGetWindowStyle   = overlayUser32.NewProc("GetWindowLongPtrW")
	overlaySetWindowStyle   = overlayUser32.NewProc("SetWindowLongPtrW")
	overlayGetCursorPos     = overlayUser32.NewProc("GetCursorPos")
	overlayMonitorFromPoint = overlayUser32.NewProc("MonitorFromPoint")
	overlayGetMonitorInfo   = overlayUser32.NewProc("GetMonitorInfoW")
	overlayGetMonitorDPI    = syscall.NewLazyDLL("shcore.dll").NewProc("GetDpiForMonitor")
	overlaySetWindowPos     = overlayUser32.NewProc("SetWindowPos")
	overlayGetWindowRect    = overlayUser32.NewProc("GetWindowRect")
	overlaySetWindowRgn     = overlayUser32.NewProc("SetWindowRgn")
	overlayCreateRoundRgn   = overlayGDI32.NewProc("CreateRoundRectRgn")
	overlayDeleteObject     = overlayGDI32.NewProc("DeleteObject")
)

type overlayPoint struct{ X, Y int32 }
type overlayRect struct{ Left, Top, Right, Bottom int32 }
type overlayMonitorInfo struct {
	Size    uint32
	Monitor overlayRect
	Work    overlayRect
	Flags   uint32
}

func configureOverlay(window *mygo.Window) error {
	handle := window.NativeHandle()
	index := ^uintptr(19) // GWL_EXSTYLE = -20.
	style, _, _ := overlayGetWindowStyle.Call(handle, index)
	// ShowInactive avoids focus on show; NOACTIVATE also prevents later focus.
	const nonInteractive = 0x08000000 | 0x00080000 | 0x00000020
	_, _, err := overlaySetWindowStyle.Call(handle, index, style|nonInteractive)
	actual, _, _ := overlayGetWindowStyle.Call(handle, index)
	if actual&nonInteractive != nonInteractive {
		return fmt.Errorf("configure non-interactive shortcut window: %w", err)
	}
	return nil
}

func overlayWorkArea() (mygo.Rectangle, uint32, error) {
	var cursor overlayPoint
	if ok, _, err := overlayGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok == 0 {
		return mygo.Rectangle{}, 0, fmt.Errorf("read cursor monitor: %w", err)
	}
	point := uintptr(uint32(cursor.X)) | uintptr(uint32(cursor.Y))<<32
	monitor, _, _ := overlayMonitorFromPoint.Call(point, 2) // MONITOR_DEFAULTTONEAREST.
	info := overlayMonitorInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, err := overlayGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return mygo.Rectangle{}, 0, fmt.Errorf("read monitor work area: %w", err)
	}
	var dpiX, dpiY uint32
	if err := overlayGetMonitorDPI.Find(); err != nil {
		return mygo.Rectangle{}, 0, fmt.Errorf("find monitor DPI API: %w", err)
	}
	if hr, _, _ := overlayGetMonitorDPI.Call(monitor, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY))); int32(hr) < 0 || dpiX == 0 {
		return mygo.Rectangle{}, 0, fmt.Errorf("read monitor DPI: HRESULT %#x", uint32(hr))
	}
	work := info.Work
	return mygo.Rectangle{X: int(work.Left), Y: int(work.Top), Width: int(work.Right - work.Left), Height: int(work.Bottom - work.Top)}, dpiX, nil
}

func overlayPixels(dips int, dpi uint32) int { return (dips*int(dpi) + 48) / 96 }

func placeOverlay(window *mygo.Window, width, height int) error {
	area, dpi, err := overlayWorkArea()
	if err != nil {
		return err
	}
	frame := overlayFrame(area, overlayPixels(width, dpi), overlayPixels(height, dpi), overlayPixels(overlayMargin, dpi))
	handle := window.NativeHandle()
	// MyGo 0.5.5's DisableMove also cancels programmatic WM_WINDOWPOSCHANGING.
	// Use physical monitor coordinates to avoid its mixed-DPI DIP conversion,
	// and only permit movement during our own non-activating placement.
	window.SetMovable(true)
	defer window.SetMovable(false)
	for attempt := 0; attempt < 2; attempt++ {
		if ok, _, err := overlaySetWindowPos.Call(handle, ^uintptr(0), uintptr(frame.X), uintptr(frame.Y), uintptr(frame.Width), uintptr(frame.Height), 0x0210); ok == 0 {
			return fmt.Errorf("place shortcut window: %w", err)
		}
		var actual overlayRect
		if ok, _, err := overlayGetWindowRect.Call(handle, uintptr(unsafe.Pointer(&actual))); ok == 0 {
			return fmt.Errorf("read shortcut window bounds: %w", err)
		}
		if actual == (overlayRect{int32(frame.X), int32(frame.Y), int32(frame.X + frame.Width), int32(frame.Y + frame.Height)}) {
			return roundOverlay(handle, frame.Width, frame.Height, overlayPixels(overlayRadius, dpi))
		}
		// Moving between monitors may let WM_DPICHANGED apply a suggested
		// rectangle. Reapply the exact physical frame after that transition.
	}
	return fmt.Errorf("the shortcut window did not accept its bottom-right position")
}

func roundOverlay(handle uintptr, width, height, radius int) error {
	region, _, err := overlayCreateRoundRgn.Call(0, 0, uintptr(width+1), uintptr(height+1), uintptr(radius*2), uintptr(radius*2))
	if region == 0 {
		return fmt.Errorf("create rounded shortcut outline: %w", err)
	}
	if ok, _, err := overlaySetWindowRgn.Call(handle, region, 1); ok == 0 {
		overlayDeleteObject.Call(region)
		return fmt.Errorf("apply rounded shortcut outline: %w", err)
	}
	// SetWindowRgn transfers ownership of the region to Windows.
	return nil
}
