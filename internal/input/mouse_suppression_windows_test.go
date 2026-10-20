//go:build windows

package input

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
	"golang.org/x/sys/windows"
)

const windowsMouseProbeTag = 0x4D4D5052

type windowsMouseProbeEvent struct {
	message uintptr
	event   windowsMouseEvent
}

// The older hooks are independent of the mapper under test. They consume only
// the probe's mouse input and tagged F23/F24 output, even if mapping regresses.
type windowsMouseProbeSink struct {
	thread           uint32
	mouseCallback    uintptr
	keyboardCallback uintptr
	mouse            chan windowsMouseProbeEvent
	keys             chan windowsKeyboardEvent
	done             chan struct{}
	err              error
}

func startWindowsMouseProbeSink(t *testing.T) *windowsMouseProbeSink {
	t.Helper()
	// Holding this named object rejects another process using the same private
	// marker. No mutex ownership or OS-thread affinity is needed for the lease.
	name := windows.StringToUTF16Ptr(`Local\MouseMapper.NativeMouseProbe.4D4D5052`)
	lease, err := windows.CreateMutex(nil, false, name)
	if lease != 0 {
		t.Cleanup(func() {
			if err := windows.CloseHandle(lease); err != nil {
				t.Error(err)
			}
		})
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		t.Fatal("another native mouse probe owns the private marker; run these tests serially")
	}
	if err != nil {
		t.Fatal(err)
	}
	sink := &windowsMouseProbeSink{
		mouse: make(chan windowsMouseProbeEvent, 16), keys: make(chan windowsKeyboardEvent, 16),
		done: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(sink.done)
		var message windowsMessage
		peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 0)
		sink.thread = windows.GetCurrentThreadId()
		pass := &windowsDriver{}
		sink.mouseCallback = windows.NewCallback(func(code int32, message uintptr, event *windowsMouseEvent) uintptr {
			if code >= 0 && event != nil && event.ExtraInfo == windowsMouseProbeTag {
				select {
				case sink.mouse <- windowsMouseProbeEvent{message: message, event: *event}:
				default:
				}
				return 1
			}
			return pass.next(code, message, unsafe.Pointer(event))
		})
		sink.keyboardCallback = windows.NewCallback(func(code int32, message uintptr, event *windowsKeyboardEvent) uintptr {
			if code >= 0 && event != nil && event.ExtraInfo == inputTag &&
				(event.VKCode == 0x86 || event.VKCode == 0x87 || event.ScanCode == 0x6E || event.ScanCode == 0x76) {
				select {
				case sink.keys <- *event:
				default:
				}
				return 1
			}
			return pass.next(code, message, unsafe.Pointer(event))
		})
		module, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
		mouseHook, _, err := setWindowsHookExW.Call(14, sink.mouseCallback, module, 0)
		if mouseHook == 0 {
			ready <- windowsCallError("install test mouse guard", err)
			return
		}
		defer unhookWindowsHookEx.Call(mouseHook)
		keyboardHook, _, err := setWindowsHookExW.Call(13, sink.keyboardCallback, module, 0)
		if keyboardHook == 0 {
			ready <- windowsCallError("install test keyboard guard", err)
			return
		}
		defer unhookWindowsHookEx.Call(keyboardHook)
		ready <- nil
		for {
			result, _, err := getMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if result == 0 {
				return
			}
			if int32(result) == -1 {
				sink.err = windowsCallError("pump test input guards", err)
				return
			}
		}
	}()
	if err := <-ready; err != nil {
		<-sink.done
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.stop(t) })
	return sink
}

func (sink *windowsMouseProbeSink) stop(t *testing.T) {
	t.Helper()
	select {
	case <-sink.done:
	default:
		posted, _, err := postThreadMessageW.Call(uintptr(sink.thread), 0x0012, 0, 0)
		if posted == 0 {
			t.Error(windowsCallError("stop test input guards", err))
			return
		}
		select {
		case <-sink.done:
		case <-time.After(3 * time.Second):
			t.Error("test input guards did not stop")
			return
		}
	}
	if sink.err != nil {
		t.Error(sink.err)
	}
}

func windowsMouseProbePacket(button uint32, down bool) []byte {
	size, offset, extra := 28, 4, 20
	if unsafe.Sizeof(uintptr(0)) == 8 {
		size, offset, extra = 40, 8, 24
	}
	packet := make([]byte, size)
	binary.LittleEndian.PutUint32(packet[offset+8:], button)
	flags := uint32(0x0100)
	if down {
		flags = 0x0080
	}
	binary.LittleEndian.PutUint32(packet[offset+12:], flags)
	if size == 40 {
		binary.LittleEndian.PutUint64(packet[offset+extra:], windowsMouseProbeTag)
	} else {
		binary.LittleEndian.PutUint32(packet[offset+extra:], windowsMouseProbeTag)
	}
	return packet
}

func sendWindowsMouseProbe(t *testing.T, sink *windowsMouseProbeSink, button uint32, transitions ...bool) {
	t.Helper()
	if (button != 1 && button != 2) || len(transitions) == 0 {
		t.Fatal("native mouse probe requires a side button and explicit down/up transitions")
	}
	select {
	case <-sink.done:
		t.Fatal("test input guard stopped; no mouse input was sent")
	default:
	}
	packets := windowsMouseProbePacket(button, transitions[0])
	size := len(packets)
	for _, down := range transitions[1:] {
		packets = append(packets, windowsMouseProbePacket(button, down)...)
	}
	sent, _, err := sendInput.Call(uintptr(len(transitions)), uintptr(unsafe.Pointer(&packets[0])), uintptr(size))
	runtime.KeepAlive(packets)
	if sent != uintptr(len(transitions)) {
		t.Fatalf("send test mouse transitions: %d/%d events (%v)", sent, len(transitions), windowsCallError("SendInput", err))
	}
}

func receiveWindowsProbe[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("the isolated native input sink did not receive its expected event")
		var zero T
		return zero
	}
}

func TestWindowsMouseProbePacketLayout(t *testing.T) {
	size, offset, extra := 28, 4, 20
	if unsafe.Sizeof(uintptr(0)) == 8 {
		size, offset, extra = 40, 8, 24
	}
	for _, down := range []bool{true, false} {
		packet := windowsMouseProbePacket(2, down)
		flags := uint32(0x0100)
		if down {
			flags = 0x0080
		}
		if len(packet) != size || binary.LittleEndian.Uint32(packet) != 0 ||
			binary.LittleEndian.Uint32(packet[offset+8:]) != 2 ||
			binary.LittleEndian.Uint32(packet[offset+12:]) != flags ||
			binary.LittleEndian.Uint32(packet[offset+extra:]) != windowsMouseProbeTag {
			t.Fatalf("invalid test MOUSEINPUT packet: %x", packet)
		}
	}
}

func verifyWindowsMouseProbeSink(t *testing.T, sink *windowsMouseProbeSink) {
	t.Helper()
	// First exercise the guard trampolines without sending any OS input.
	for _, message := range []uintptr{0x020B, 0x020C} {
		event := windowsMouseEvent{MouseData: 1 << 16, Flags: 1, ExtraInfo: windowsMouseProbeTag}
		result, _, _ := syscall.SyscallN(sink.mouseCallback, 0, message, uintptr(unsafe.Pointer(&event)))
		if result != 1 {
			t.Fatalf("mouse safety guard returned %d", result)
		}
		receiveWindowsProbe(t, sink.mouse)
	}
	for _, key := range []struct{ vk, scan uint32 }{{0x86, 0x6E}, {0x87, 0x76}} {
		for _, message := range []uintptr{0x0100, 0x0101} {
			event := windowsKeyboardEvent{VKCode: key.vk, ScanCode: key.scan, Flags: 0x10, ExtraInfo: inputTag}
			result, _, _ := syscall.SyscallN(sink.keyboardCallback, 0, message, uintptr(unsafe.Pointer(&event)))
			if result != 1 {
				t.Fatalf("keyboard safety guard returned %d", result)
			}
			receiveWindowsProbe(t, sink.keys)
		}
	}
	// With no mapper installed, both sides of each probe must reach the guard.
	for _, button := range []uint32{1, 2} {
		sendWindowsMouseProbe(t, sink, button, true, false)
		for _, message := range []uintptr{0x020B, 0x020C} {
			event := receiveWindowsProbe(t, sink.mouse)
			if event.message != message || event.event.MouseData>>16 != button || event.event.Flags&1 == 0 {
				t.Fatalf("unexpected native guard event: %+v", event)
			}
		}
	}
	for _, key := range []struct {
		code string
		vk   uint32
	}{{"F23", 0x86}, {"F24", 0x87}} {
		for _, down := range []bool{true, false} {
			if err := (&windowsDriver{}).sendKey(key.code, down, nil); err != nil {
				t.Fatal(err)
			}
			event := receiveWindowsProbe(t, sink.keys)
			if event.VKCode != key.vk || event.Flags&0x10 == 0 || (event.Flags&0x80 == 0) != down {
				t.Fatalf("unexpected guarded %s event: %+v", key.code, event)
			}
		}
	}
}

type windowsMouseProbeFocus struct {
	checks   chan uint32
	switches atomic.Int32
}

func (focus *windowsMouseProbeFocus) Valid() bool {
	select {
	case focus.checks <- windows.GetCurrentThreadId():
	default:
	}
	return true
}

func (focus *windowsMouseProbeFocus) Switch(context.Context, string) error {
	focus.switches.Add(1)
	return errors.New("native mouse probe must never switch an input method")
}

func startWindowsMouseProbeMapper(t *testing.T, config Config) (*Engine, *windowsDriver, *windowsMouseProbeFocus) {
	t.Helper()
	e := New(nil)
	focus := &windowsMouseProbeFocus{checks: make(chan uint32, 16)}
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	driver := e.driver.(*windowsDriver)
	// Never remap real user input while this integration test is running.
	driver.mouseCallback = windows.NewCallback(func(code int32, message uintptr, event *windowsMouseEvent) uintptr {
		if event != nil && event.ExtraInfo == windowsMouseProbeTag {
			return driver.handleMouse(code, message, event)
		}
		return driver.next(code, message, unsafe.Pointer(event))
	})
	driver.keyboardCallback = windows.NewCallback(func(code int32, message uintptr, event *windowsKeyboardEvent) uintptr {
		if event != nil && event.ExtraInfo == inputTag {
			return driver.handleKeyboard(code, message, event)
		}
		return driver.next(code, message, unsafe.Pointer(event))
	})
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	// Stop and release mapped keys while the independent keyboard guard is live.
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
		if calls := focus.switches.Load(); calls != 0 {
			t.Errorf("native mouse probe tried to switch an input method %d times", calls)
		}
	})
	return e, driver, focus
}

func flushWindowsMouseProbe(t *testing.T, driver *windowsDriver) {
	t.Helper()
	if err := driver.runOnInputThread(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func assertWindowsMouseProbeQuiet(t *testing.T, sink *windowsMouseProbeSink, duration time.Duration) {
	t.Helper()
	select {
	case <-sink.done:
		t.Fatal("the isolated native input guard stopped unexpectedly")
	case event := <-sink.mouse:
		t.Fatalf("mapped side button leaked to the downstream guard: %+v", event)
	case event := <-sink.keys:
		t.Fatalf("side button emitted an unexpected mapped key event: %+v", event)
	case <-time.After(duration):
	}
}

// Opt-in only. An independent, continuously pumping guard is installed first
// and tested before the mapper. No window is opened, focused, moved or clicked;
// every generated mouse event and F23/F24 event is consumed by a test-owned hook.
func TestWindowsNativeSideButtonsAreSuppressed(t *testing.T) {
	if os.Getenv("MOUSE_MAPPER_NATIVE_MOUSE_TEST") != "1" {
		t.Skip("set MOUSE_MAPPER_NATIVE_MOUSE_TEST=1 for guarded native side-button validation")
	}
	sink := startWindowsMouseProbeSink(t)
	verifyWindowsMouseProbeSink(t, sink)
	config := Config{Enabled: true, Back: Binding{Enabled: true, Keys: []string{"F24"}}, Forward: Binding{Enabled: true, Keys: []string{"F24"}}}
	_, driver, _ := startWindowsMouseProbeMapper(t, config)
	for _, button := range []uint32{1, 2} {
		if !t.Run(fmt.Sprintf("button-%d", button), func(t *testing.T) {
			sendWindowsMouseProbe(t, sink, button, true, false)
			for _, up := range []bool{false, true} {
				event := receiveWindowsProbe(t, sink.keys)
				if event.VKCode != 0x87 || event.Flags&0x10 == 0 || (event.Flags&0x80 != 0) != up {
					t.Fatalf("unexpected mapped F24 event: %+v", event)
				}
			}
			// Wait for the mouse callback to finish, including any CallNextHookEx.
			flushWindowsMouseProbe(t, driver)
			select {
			case event := <-sink.mouse:
				t.Fatalf("mapped side button leaked to the downstream guard: %+v", event)
			default:
			}
			select {
			case event := <-sink.keys:
				t.Fatalf("side button emitted an extra mapped key event: %+v", event)
			default:
			}
		}) {
			return
		}
	}
}

// This uses the real hook thread and timer, but a fake Valid-only focus. Both
// short and long outputs are harmless tagged function keys caught by the guard.
func TestWindowsNativeLongPressUsesHookThread(t *testing.T) {
	if os.Getenv("MOUSE_MAPPER_NATIVE_MOUSE_TEST") != "1" {
		t.Skip("set MOUSE_MAPPER_NATIVE_MOUSE_TEST=1 for guarded native long-press validation")
	}
	sink := startWindowsMouseProbeSink(t)
	verifyWindowsMouseProbeSink(t, sink)
	const thresholdMS = 500
	threshold := time.Duration(thresholdMS) * time.Millisecond
	binding := Binding{Enabled: true, Keys: []string{"F24"}, LongPress: &LongPressBinding{Keys: []string{"F23"}, ThresholdMS: thresholdMS}}
	e, driver, focus := startWindowsMouseProbeMapper(t, Config{Enabled: true, Back: binding, Forward: binding})
	for _, button := range []uint32{1, 2} {
		for _, long := range []bool{false, true} {
			if !t.Run(fmt.Sprintf("button-%d/long-%t", button, long), func(t *testing.T) {
				started := time.Now()
				sendWindowsMouseProbe(t, sink, button, true)
				flushWindowsMouseProbe(t, driver)
				assertWindowsMouseProbeQuiet(t, sink, 25*time.Millisecond)
				wantVK := uint32(0x86)
				if !long {
					wantVK = 0x87
					sendWindowsMouseProbe(t, sink, button, false)
				}
				for _, up := range []bool{false, true} {
					event := receiveWindowsProbe(t, sink.keys)
					if event.VKCode != wantVK || event.Flags&0x10 == 0 || (event.Flags&0x80 != 0) != up {
						t.Fatalf("unexpected mapped key event (want VK %#x, up=%t): %+v", wantVK, up, event)
					}
				}
				if long && time.Since(started) < threshold {
					t.Fatal("long action fired before the configured threshold")
				}
				checkedOn := receiveWindowsProbe(t, focus.checks)
				if err := driver.runOnInputThread(context.Background(), func() error {
					if actual := windows.GetCurrentThreadId(); checkedOn != actual {
						return fmt.Errorf("timed action ran on thread %d, want hook thread %d", checkedOn, actual)
					}
					e.mu.Lock()
					defer e.mu.Unlock()
					name := Back
					if button == 2 {
						name = Forward
					}
					_, held := e.active[name]
					press := e.timedPresses[name]
					if held != long || (long && (press == nil || !press.decided)) || (!long && press != nil) || len(e.references) != 0 {
						return fmt.Errorf("timed tap retained incorrect state: held=%t, press=%+v, key owners=%d", held, press, len(e.references))
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				// A held long press cannot repeat; a released short press cannot
				// turn into a long press when its cancelled deadline passes.
				assertWindowsMouseProbeQuiet(t, sink, threshold+50*time.Millisecond)
				if long {
					sendWindowsMouseProbe(t, sink, button, false)
					flushWindowsMouseProbe(t, driver)
					assertWindowsMouseProbeQuiet(t, sink, 50*time.Millisecond)
				}
				e.mu.Lock()
				remaining := len(e.active) + len(e.timedPresses) + len(e.references)
				e.mu.Unlock()
				if remaining != 0 {
					t.Fatal("released side button retained a timer or input ownership")
				}
				select {
				case thread := <-focus.checks:
					t.Fatalf("action was decided more than once (thread %d)", thread)
				default:
				}
			}) {
				return
			}
		}
	}
}
