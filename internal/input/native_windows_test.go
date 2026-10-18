//go:build windows

package input

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsNativeStructureLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		if size := unsafe.Sizeof(windowsMouseEvent{}); size != 32 {
			t.Errorf("MSLLHOOKSTRUCT size = %d, want 32", size)
		}
		if offset := unsafe.Offsetof(windowsMouseEvent{}.ExtraInfo); offset != 24 {
			t.Errorf("MSLLHOOKSTRUCT dwExtraInfo offset = %d, want 24", offset)
		}
		if size := unsafe.Sizeof(windowsKeyboardEvent{}); size != 24 {
			t.Errorf("KBDLLHOOKSTRUCT size = %d, want 24", size)
		}
		if offset := unsafe.Offsetof(windowsKeyboardEvent{}.ExtraInfo); offset != 16 {
			t.Errorf("KBDLLHOOKSTRUCT dwExtraInfo offset = %d, want 16", offset)
		}
		if size := unsafe.Sizeof(windowsMessage{}); size != 48 {
			t.Errorf("MSG size = %d, want 48", size)
		}
	}
}

func TestWindowsSendInputPacketLayout(t *testing.T) {
	size, offset, extraOffset := 28, 4, 12
	if unsafe.Sizeof(uintptr(0)) == 8 {
		size, offset, extraOffset = 40, 8, 16
	}
	for _, test := range []struct {
		code  string
		down  bool
		flags uint32
	}{
		{"KeyC", true, 0x8}, {"KeyC", false, 0xA},
		{"ArrowLeft", true, 0x9}, {"ArrowLeft", false, 0xB},
		{"Pause", true, 0}, {"Pause", false, 2},
	} {
		packet := windowsKeyPacket(windowsKeys[test.code], test.down)
		if len(packet) != size {
			t.Fatalf("INPUT size = %d, want %d", len(packet), size)
		}
		if kind := binary.LittleEndian.Uint32(packet); kind != 1 {
			t.Errorf("INPUT type = %d, want INPUT_KEYBOARD", kind)
		}
		if flags := binary.LittleEndian.Uint32(packet[offset+4:]); flags != test.flags {
			t.Errorf("%s down=%v flags = %#x, want %#x", test.code, test.down, flags, test.flags)
		}
		if test.code == "Pause" {
			if vk := binary.LittleEndian.Uint16(packet[offset:]); vk != 0x13 {
				t.Errorf("Pause virtual key = %#x", vk)
			}
		} else if scan := binary.LittleEndian.Uint16(packet[offset+2:]); scan != windowsKeys[test.code].scan {
			t.Errorf("%s scancode = %#x", test.code, scan)
		}
		if tag := binary.LittleEndian.Uint32(packet[offset+extraOffset:]); tag != inputTag {
			t.Errorf("dwExtraInfo = %#x, want %#x", tag, inputTag)
		}
	}
}

func TestWindowsKeyPositionsUnique(t *testing.T) {
	positions := map[int]string{}
	for code, key := range windowsKeys {
		if previous := positions[key.index()]; previous != "" {
			t.Errorf("%s and %s share a scan position", previous, code)
		}
		positions[key.index()] = code
	}
}

func TestWindowsFunctionKeyScancodesMatchOS(t *testing.T) {
	for n := 1; n <= 24; n++ {
		code := fmt.Sprintf("F%d", n)
		scan, _, _ := mapVirtualKeyW.Call(uintptr(0x6F+n), 4)
		if scan != uintptr(windowsKeys[code].scan) {
			t.Errorf("%s scan = %#x, Windows reports %#x", code, windowsKeys[code].scan, scan)
		}
	}
}

// This calls the real Windows callback trampoline with native struct pointers,
// but sends output to a fake driver. It does not inject system input.
func TestWindowsCallbackABI(t *testing.T) {
	e, output := testEngine(t, []string{"Control", "KeyC"}, []string{"Shift"})
	driver := newNativeDriver(e).(*windowsDriver)
	callback := windows.NewCallback(driver.handleMouse)
	event := windowsMouseEvent{MouseData: 1 << 16}
	result, _, _ := syscall.SyscallN(callback, 0, 0x020B, uintptr(unsafe.Pointer(&event)))
	if result != 1 {
		t.Fatalf("mouse down callback = %d, want suppression", result)
	}
	result, _, _ = syscall.SyscallN(callback, 0, 0x020C, uintptr(unsafe.Pointer(&event)))
	if result != 1 {
		t.Fatalf("mouse up callback = %d, want suppression", result)
	}
	assertEvents(t, output, "Control down", "KeyC down", "KeyC up", "Control up")
	event.MouseData = 2 << 16
	result, _, _ = syscall.SyscallN(callback, 0, 0x020B, uintptr(unsafe.Pointer(&event)))
	if result != 1 {
		t.Fatalf("forward callback = %d, want suppression", result)
	}
	keyboard := windows.NewCallback(driver.handleKeyboard)
	key := windowsKeyboardEvent{VKCode: 0xA0, ScanCode: 0x2A, Flags: 0x80}
	result, _, _ = syscall.SyscallN(keyboard, 0, 0x0101, uintptr(unsafe.Pointer(&key)))
	if result != 1 {
		t.Fatalf("physical modifier release callback = %d, want ownership transfer", result)
	}
	e.mouseButton(Forward, false)
}

func TestWindowsInjectedInputIsNotRemapped(t *testing.T) {
	e, output := testEngine(t, []string{"KeyA"}, nil)
	driver := newNativeDriver(e).(*windowsDriver)
	for _, event := range []windowsMouseEvent{{MouseData: 1 << 16, Flags: 1}, {MouseData: 1 << 16, ExtraInfo: inputTag}} {
		driver.handleMouse(0, 0x020B, &event)
	}
	assertEvents(t, output)
	for _, event := range []windowsKeyboardEvent{{ScanCode: 0x1E, Flags: 0x10}, {ScanCode: 0x1E, ExtraInfo: inputTag}} {
		driver.handleKeyboard(0, 0x0100, &event)
	}
	if driver.physicallyDown("KeyA") {
		t.Fatal("injected key changed physical state")
	}
}

func TestWindowsPhysicalModifierSidesAreIndependent(t *testing.T) {
	e, _ := testEngine(t, []string{"Control"}, nil)
	driver := newNativeDriver(e).(*windowsDriver)
	right := windowsKeyboardEvent{VKCode: 0xA3, ScanCode: 0x1D, Flags: 1}
	driver.handleKeyboard(0, 0x0100, &right)
	if driver.physicallyDown("Control") {
		t.Fatal("right Control must not be treated as synthetic left Control")
	}
	left := windowsKeyboardEvent{VKCode: 0xA2, ScanCode: 0x1D}
	driver.handleKeyboard(0, 0x0100, &left)
	if !driver.physicallyDown("Control") {
		t.Fatal("left Control physical down was not tracked")
	}
}

func TestWindowsRejectSecureAttentionSequence(t *testing.T) {
	e := New(nil)
	for _, enabled := range []bool{true, false} {
		if err := e.Apply(Config{Enabled: enabled, Back: Binding{Enabled: true, Keys: []string{"Control", "Alt", "Delete"}}}); err == nil {
			t.Fatal("accepted synthetic Control+Alt+Delete")
		}
	}
}

// Opt-in integration test: the test-owned low-level keyboard sink consumes both
// tagged F24 events before they reach any application. No mouse movement,
// foreground-window changes, or unconsumed keyboard events are generated.
func TestWindowsNativeHookAndSendInput(t *testing.T) {
	if os.Getenv("MOUSE_MAPPER_NATIVE_TEST") != "1" {
		t.Skip("set MOUSE_MAPPER_NATIVE_TEST=1 to exercise the Windows input APIs")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e := New(nil)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	driver := e.driver.(*windowsDriver)
	seen := make(chan windowsKeyboardEvent, 4)
	callback := windows.NewCallback(func(code int32, message uintptr, event *windowsKeyboardEvent) uintptr {
		if code >= 0 && event != nil && event.ExtraInfo == inputTag && event.ScanCode == uint32(windowsKeys["F24"].scan) {
			select {
			case seen <- *event:
			default:
			}
			return 1
		}
		return driver.next(code, message, unsafe.Pointer(event))
	})
	module, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	hook, _, hookErr := setWindowsHookExW.Call(13, callback, module, 0)
	if hook == 0 {
		t.Fatal(windowsCallError("install test-owned input sink", hookErr))
	}
	defer unhookWindowsHookEx.Call(hook)
	var message windowsMessage
	peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 0)
	results := make(chan error, 2)
	go func() {
		results <- driver.sendKey("F24", true, nil)
		results <- driver.sendKey("F24", false, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	var events []windowsKeyboardEvent
	var sends int
	for time.Now().Before(deadline) && (len(events) < 2 || sends < 2) {
		peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 1)
		select {
		case event := <-seen:
			events = append(events, event)
		case err := <-results:
			sends++
			if err != nil {
				t.Fatal(err)
			}
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if len(events) != 2 || sends != 2 {
		t.Fatalf("native sink received %d events; %d SendInput calls returned", len(events), sends)
	}
	if events[0].Flags&0x10 == 0 || events[1].Flags&0x10 == 0 {
		t.Error("Windows did not mark SendInput events as injected")
	}
	if events[0].VKCode != 0x87 || events[1].VKCode != 0x87 {
		t.Errorf("F24 events have VK codes %#x/%#x, want %#x", events[0].VKCode, events[1].VKCode, 0x87)
	}
	if events[0].Flags&0x80 != 0 || events[1].Flags&0x80 == 0 {
		t.Error("native down/up order was not preserved")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Status().Running {
		t.Fatal("native hook remained running after Stop")
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
}
