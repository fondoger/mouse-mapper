//go:build windows

package input

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const inputTag = 0x4D4D4150

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	setWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	unhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	callNextHookEx      = user32.NewProc("CallNextHookEx")
	getMessageW         = user32.NewProc("GetMessageW")
	peekMessageW        = user32.NewProc("PeekMessageW")
	postThreadMessageW  = user32.NewProc("PostThreadMessageW")
	getAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	mapVirtualKeyW      = user32.NewProc("MapVirtualKeyW")
	sendInput           = user32.NewProc("SendInput")
)

type windowsKey struct {
	scan     uint16
	extended bool
	virtual  uint16
}

func (key windowsKey) index() int {
	if key.extended {
		return int(key.scan) + 256
	}
	return int(key.scan)
}

var windowsKeys = func() map[string]windowsKey {
	m := map[string]windowsKey{}
	add := func(code string, scan uint16) { m[code] = windowsKey{scan: scan} }
	for _, item := range []struct {
		code string
		scan uint16
	}{
		{"Control", 0x1D}, {"Alt", 0x38}, {"Shift", 0x2A},
		{"KeyA", 0x1E}, {"KeyB", 0x30}, {"KeyC", 0x2E}, {"KeyD", 0x20}, {"KeyE", 0x12},
		{"KeyF", 0x21}, {"KeyG", 0x22}, {"KeyH", 0x23}, {"KeyI", 0x17}, {"KeyJ", 0x24},
		{"KeyK", 0x25}, {"KeyL", 0x26}, {"KeyM", 0x32}, {"KeyN", 0x31}, {"KeyO", 0x18},
		{"KeyP", 0x19}, {"KeyQ", 0x10}, {"KeyR", 0x13}, {"KeyS", 0x1F}, {"KeyT", 0x14},
		{"KeyU", 0x16}, {"KeyV", 0x2F}, {"KeyW", 0x11}, {"KeyX", 0x2D}, {"KeyY", 0x15}, {"KeyZ", 0x2C},
		{"Digit0", 0x0B}, {"Digit1", 0x02}, {"Digit2", 0x03}, {"Digit3", 0x04}, {"Digit4", 0x05},
		{"Digit5", 0x06}, {"Digit6", 0x07}, {"Digit7", 0x08}, {"Digit8", 0x09}, {"Digit9", 0x0A},
		{"Escape", 0x01}, {"Tab", 0x0F}, {"Backspace", 0x0E}, {"Enter", 0x1C}, {"Space", 0x39},
		{"CapsLock", 0x3A}, {"ScrollLock", 0x46},
		{"Minus", 0x0C}, {"Equal", 0x0D}, {"BracketLeft", 0x1A}, {"BracketRight", 0x1B},
		{"Backslash", 0x2B}, {"Semicolon", 0x27}, {"Quote", 0x28}, {"Backquote", 0x29},
		{"Comma", 0x33}, {"Period", 0x34}, {"Slash", 0x35}, {"IntlBackslash", 0x56},
		{"Numpad0", 0x52}, {"Numpad1", 0x4F}, {"Numpad2", 0x50}, {"Numpad3", 0x51},
		{"Numpad4", 0x4B}, {"Numpad5", 0x4C}, {"Numpad6", 0x4D}, {"Numpad7", 0x47},
		{"Numpad8", 0x48}, {"Numpad9", 0x49}, {"NumpadAdd", 0x4E}, {"NumpadSubtract", 0x4A},
		{"NumpadMultiply", 0x37}, {"NumpadDecimal", 0x53},
	} {
		add(item.code, item.scan)
	}
	for _, item := range []struct {
		code string
		scan uint16
	}{
		{"Meta", 0x5B}, {"ArrowLeft", 0x4B}, {"ArrowRight", 0x4D}, {"ArrowUp", 0x48}, {"ArrowDown", 0x50},
		{"Home", 0x47}, {"End", 0x4F}, {"PageUp", 0x49}, {"PageDown", 0x51}, {"Insert", 0x52}, {"Delete", 0x53},
		{"NumpadDivide", 0x35}, {"NumpadEnter", 0x1C}, {"PrintScreen", 0x37},
	} {
		m[item.code] = windowsKey{scan: item.scan, extended: true}
	}
	for n := 1; n <= 10; n++ {
		add(fmt.Sprintf("F%d", n), uint16(0x3A+n))
	}
	add("F11", 0x57)
	add("F12", 0x58)
	for n := 13; n <= 23; n++ {
		add(fmt.Sprintf("F%d", n), uint16(0x64+n-13))
	}
	add("F24", 0x76)
	// Pause has an E1-prefixed scan sequence; SendInput's VK path handles it.
	m["Pause"] = windowsKey{scan: 0x45, virtual: 0x13}
	return m
}()

type windowsPoint struct{ X, Y int32 }
type windowsMouseEvent struct {
	Point     windowsPoint
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}
type windowsKeyboardEvent struct {
	VKCode    uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}
type windowsMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   windowsPoint
	Private uint32
}

type windowsDriver struct {
	engine           *Engine
	mu               sync.Mutex
	thread           uint32
	done             chan struct{}
	mouseHook        uintptr
	keyboardHook     uintptr
	mouseCallback    uintptr
	keyboardCallback uintptr
	physical         [512]atomic.Bool
	codes            map[int]string
}

func newNativeDriver(engine *Engine) nativeDriver {
	d := &windowsDriver{engine: engine, codes: map[int]string{}}
	for code, key := range windowsKeys {
		d.codes[key.index()] = code
	}
	return d
}

func (d *windowsDriver) supports(code string) bool { _, ok := windowsKeys[code]; return ok }

func (d *windowsDriver) start() error {
	d.mu.Lock()
	previous := d.done
	d.mu.Unlock()
	if previous != nil {
		select {
		case <-previous:
		case <-time.After(3 * time.Second):
			return errors.New("the previous Windows input hook is still stopping")
		}
	}
	d.mu.Lock()
	d.done = make(chan struct{})
	done := d.done
	d.mu.Unlock()
	ready := make(chan error, 1)
	go d.run(ready)
	if err := <-ready; err != nil {
		<-done
		return err
	}
	return nil
}

func (d *windowsDriver) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d.mu.Lock()
	d.thread = windows.GetCurrentThreadId()
	done := d.done
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.thread = 0
		d.mu.Unlock()
		close(done)
	}()
	// Force creation of the message queue before another thread can post Quit.
	var message windowsMessage
	peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 0)
	if d.mouseCallback == 0 {
		d.mouseCallback = windows.NewCallback(d.handleMouse)
		d.keyboardCallback = windows.NewCallback(d.handleKeyboard)
	}
	module, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	hook, _, err := setWindowsHookExW.Call(14, d.mouseCallback, module, 0)
	if hook == 0 {
		ready <- windowsCallError("install mouse hook", err)
		return
	}
	d.mouseHook = hook
	defer unhookWindowsHookEx.Call(hook)
	hook, _, err = setWindowsHookExW.Call(13, d.keyboardCallback, module, 0)
	if hook == 0 {
		ready <- windowsCallError("install keyboard state hook", err)
		return
	}
	d.keyboardHook = hook
	defer unhookWindowsHookEx.Call(hook)
	// Install the state hook before seeding: a key released during startup
	// must either appear released in this snapshot or arrive in the queue.
	for _, key := range windowsKeys {
		scan := uintptr(key.scan)
		if key.extended {
			scan |= 0xE000
		}
		vk := uintptr(key.virtual)
		if vk == 0 {
			vk, _, _ = mapVirtualKeyW.Call(scan, 3)
		}
		state, _, _ := getAsyncKeyState.Call(vk)
		d.physical[key.index()].Store(state&0x8000 != 0)
	}
	ready <- nil
	for {
		result, _, messageErr := getMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			d.engine.nativeFailed(windowsCallError("read input messages", messageErr))
			return
		}
		if result == 0 {
			return
		}
	}
}

func (d *windowsDriver) handleMouse(code int32, message uintptr, event *windowsMouseEvent) uintptr {
	if code < 0 || (message != 0x020B && message != 0x020C) || event == nil {
		return d.next(code, message, unsafe.Pointer(event))
	}
	if event.Flags&1 != 0 || event.ExtraInfo == inputTag {
		return d.next(code, message, unsafe.Pointer(event))
	}
	var button string
	switch event.MouseData >> 16 {
	case 1:
		button = Back
	case 2:
		button = Forward
	default:
		return d.next(code, message, unsafe.Pointer(event))
	}
	if d.engine.mouseButton(button, message == 0x020B) {
		return 1
	}
	return d.next(code, message, unsafe.Pointer(event))
}

func (d *windowsDriver) handleKeyboard(code int32, message uintptr, event *windowsKeyboardEvent) uintptr {
	if code < 0 || event == nil {
		return d.next(code, message, unsafe.Pointer(event))
	}
	if event.Flags&0x10 != 0 || event.ExtraInfo == inputTag {
		return d.next(code, message, unsafe.Pointer(event))
	}
	if message != 0x0100 && message != 0x0101 && message != 0x0104 && message != 0x0105 {
		return d.next(code, message, unsafe.Pointer(event))
	}
	index := int(event.ScanCode & 255)
	if event.Flags&1 != 0 {
		index += 256
	}
	down := message == 0x0100 || message == 0x0104
	d.physical[index].Store(down)
	if mappedCode := d.codes[index]; mappedCode != "" && d.engine.physicalKey(mappedCode, down) {
		return 1
	}
	return d.next(code, message, unsafe.Pointer(event))
}

func (d *windowsDriver) next(code int32, message uintptr, event unsafe.Pointer) uintptr {
	result, _, _ := callNextHookEx.Call(0, uintptr(code), message, uintptr(event))
	return result
}

func (d *windowsDriver) stop() error {
	d.mu.Lock()
	thread, done := d.thread, d.done
	d.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	if thread != 0 {
		result, _, err := postThreadMessageW.Call(uintptr(thread), 0x0012, 0, 0)
		if result == 0 {
			select {
			case <-done:
				return nil
			default:
				return windowsCallError("stop input hook", err)
			}
		}
	}
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("timed out stopping the Windows input hook")
	}
}

func (d *windowsDriver) sendKey(code string, down bool, _ []string) error {
	key, ok := windowsKeys[code]
	if !ok {
		return fmt.Errorf("unsupported Windows key %s", code)
	}
	packet := windowsKeyPacket(key, down)
	sent, _, err := sendInput.Call(1, uintptr(unsafe.Pointer(&packet[0])), uintptr(len(packet)))
	runtime.KeepAlive(packet)
	if sent != 1 {
		return fmt.Errorf("cannot send %s: Windows may be blocking input to an elevated or secure window (%w)", code, windowsCallError("SendInput", err))
	}
	return nil
}

func windowsKeyPacket(key windowsKey, down bool) []byte {
	size, offset := 28, 4
	if unsafe.Sizeof(uintptr(0)) == 8 {
		size, offset = 40, 8
	}
	// INPUT contains a pointer-aligned union whose size differs on 32/64-bit.
	packet := make([]byte, size)
	binary.LittleEndian.PutUint32(packet, 1)
	flags := uint32(0x0008)
	if key.extended {
		flags |= 0x0001
	}
	if !down {
		flags |= 0x0002
	}
	if key.virtual != 0 {
		flags &^= 0x0008
		binary.LittleEndian.PutUint16(packet[offset:], key.virtual)
	} else {
		binary.LittleEndian.PutUint16(packet[offset+2:], key.scan)
	}
	binary.LittleEndian.PutUint32(packet[offset+4:], flags)
	if size == 40 {
		binary.LittleEndian.PutUint64(packet[offset+16:], inputTag)
	} else {
		binary.LittleEndian.PutUint32(packet[offset+12:], inputTag)
	}
	return packet
}

func (d *windowsDriver) physicallyDown(code string) bool {
	key, ok := windowsKeys[code]
	return ok && d.physical[key.index()].Load()
}

func (d *windowsDriver) openPermissions() error {
	return errors.New("Windows needs no input permission; applications running as administrator and secure desktop screens may reject simulated keys")
}

func windowsCallError(action string, err error) error {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return fmt.Errorf("%s failed", action)
	}
	return fmt.Errorf("%s: %w", action, err)
}
