//go:build windows

package desktop

import (
	"syscall"

	"github.com/egoist/mygo"
)

func configureOverlay(window *mygo.Window) error {
	user32 := syscall.NewLazyDLL("user32.dll")
	getStyle := user32.NewProc("GetWindowLongPtrW")
	setStyle := user32.NewProc("SetWindowLongPtrW")
	handle := window.NativeHandle()
	index := ^uintptr(19) // GWL_EXSTYLE = -20.
	style, _, _ := getStyle.Call(handle, index)
	// ShowInactive avoids focus on show; NOACTIVATE also prevents later focus.
	_, _, _ = setStyle.Call(handle, index, style|0x08000000)
	return nil
}
