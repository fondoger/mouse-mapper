//go:build darwin && (amd64 || arm64)

package input

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
)

const macInputTag int64 = 0x4D4D4150

var macKeys = map[string]uint16{
	"KeyA": 0, "KeyS": 1, "KeyD": 2, "KeyF": 3, "KeyH": 4, "KeyG": 5,
	"KeyZ": 6, "KeyX": 7, "KeyC": 8, "KeyV": 9, "IntlBackslash": 10, "KeyB": 11,
	"KeyQ": 12, "KeyW": 13, "KeyE": 14, "KeyR": 15, "KeyY": 16, "KeyT": 17,
	"Digit1": 18, "Digit2": 19, "Digit3": 20, "Digit4": 21, "Digit6": 22, "Digit5": 23,
	"Equal": 24, "Digit9": 25, "Digit7": 26, "Minus": 27, "Digit8": 28, "Digit0": 29,
	"BracketRight": 30, "KeyO": 31, "KeyU": 32, "BracketLeft": 33, "KeyI": 34, "KeyP": 35,
	"Enter": 36, "KeyL": 37, "KeyJ": 38, "Quote": 39, "KeyK": 40, "Semicolon": 41,
	"Backslash": 42, "Comma": 43, "Slash": 44, "KeyN": 45, "KeyM": 46, "Period": 47,
	"Tab": 48, "Space": 49, "Backquote": 50, "Backspace": 51, "Escape": 53,
	"Meta": 55, "Shift": 56, "Alt": 58, "Control": 59,
	"F17": 64, "NumpadDecimal": 65, "NumpadMultiply": 67, "NumpadAdd": 69,
	"NumpadDivide": 75, "NumpadEnter": 76, "NumpadSubtract": 78, "F18": 79, "F19": 80,
	"Numpad0": 82, "Numpad1": 83, "Numpad2": 84, "Numpad3": 85, "Numpad4": 86,
	"Numpad5": 87, "Numpad6": 88, "Numpad7": 89, "F20": 90, "Numpad8": 91, "Numpad9": 92,
	"F5": 96, "F6": 97, "F7": 98, "F3": 99, "F8": 100, "F9": 101, "F11": 103,
	"F13": 105, "F16": 106, "F14": 107, "F10": 109, "F12": 111, "F15": 113,
	"Insert": 114, "Home": 115, "PageUp": 116, "Delete": 117, "F4": 118, "End": 119,
	"F2": 120, "PageDown": 121, "F1": 122, "ArrowLeft": 123, "ArrowRight": 124, "ArrowDown": 125, "ArrowUp": 126,
}

var macModifierFlags = map[string]uint64{"Shift": 1 << 17, "Control": 1 << 18, "Alt": 1 << 19, "Meta": 1 << 20}

type macAPI struct {
	trusted          func() bool
	preflightListen  func() bool
	preflightPost    func() bool
	requestListen    func() bool
	createTap        func(uint32, uint32, uint32, uint64, uintptr, uintptr) uintptr
	enableTap        func(uintptr, bool)
	isTapEnabled     func(uintptr) bool
	createSource     func(uintptr, uintptr, int64) uintptr
	loopCurrent      func() uintptr
	loopAddSource    func(uintptr, uintptr, uintptr)
	loopRemoveSource func(uintptr, uintptr, uintptr)
	loopRunMode      func(uintptr, float64, bool) int32
	loopStop         func(uintptr)
	release          func(uintptr)
	createString     func(uintptr, string, uint32) uintptr
	getInteger       func(uintptr, uint32) int64
	setInteger       func(uintptr, uint32, int64)
	getFlags         func(uintptr) uint64
	setFlags         func(uintptr, uint64)
	keyState         func(int32, uint16) bool
	flagsState       func(int32) uint64
	createKeyEvent   func(uintptr, uint16, bool) uintptr
	postEvent        func(uint32, uintptr)
	defaultMode      uintptr
}

type macDriver struct {
	engine   *Engine
	mu       sync.Mutex
	loadMu   sync.Mutex
	api      *macAPI
	loop     uintptr
	tap      uintptr
	done     chan struct{}
	stopLoop chan struct{}
	callback uintptr
	physical [128]atomic.Bool
	codes    map[uint16]string
}

func newNativeDriver(engine *Engine) nativeDriver {
	d := &macDriver{engine: engine, codes: map[uint16]string{}}
	for code, key := range macKeys {
		d.codes[key] = code
	}
	return d
}

func (d *macDriver) supports(code string) bool { _, ok := macKeys[code]; return ok }

func (d *macDriver) load() error {
	d.loadMu.Lock()
	defer d.loadMu.Unlock()
	if d.api != nil {
		return nil
	}
	cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("load CoreGraphics: %w", err)
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("load CoreFoundation: %w", err)
	}
	ax, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("load ApplicationServices: %w", err)
	}
	api := &macAPI{}
	for _, symbol := range []struct {
		target  any
		library uintptr
		name    string
	}{
		{&api.trusted, ax, "AXIsProcessTrusted"},
		{&api.preflightListen, cg, "CGPreflightListenEventAccess"}, {&api.requestListen, cg, "CGRequestListenEventAccess"},
		{&api.preflightPost, cg, "CGPreflightPostEventAccess"},
		{&api.createTap, cg, "CGEventTapCreate"}, {&api.enableTap, cg, "CGEventTapEnable"}, {&api.isTapEnabled, cg, "CGEventTapIsEnabled"},
		{&api.createSource, cf, "CFMachPortCreateRunLoopSource"},
		{&api.loopCurrent, cf, "CFRunLoopGetCurrent"}, {&api.loopRunMode, cf, "CFRunLoopRunInMode"}, {&api.loopStop, cf, "CFRunLoopStop"},
		{&api.loopAddSource, cf, "CFRunLoopAddSource"}, {&api.loopRemoveSource, cf, "CFRunLoopRemoveSource"},
		{&api.release, cf, "CFRelease"}, {&api.getInteger, cg, "CGEventGetIntegerValueField"}, {&api.setInteger, cg, "CGEventSetIntegerValueField"},
		{&api.createString, cf, "CFStringCreateWithCString"},
		{&api.getFlags, cg, "CGEventGetFlags"}, {&api.setFlags, cg, "CGEventSetFlags"},
		{&api.keyState, cg, "CGEventSourceKeyState"}, {&api.flagsState, cg, "CGEventSourceFlagsState"},
		{&api.createKeyEvent, cg, "CGEventCreateKeyboardEvent"}, {&api.postEvent, cg, "CGEventPost"},
	} {
		address, lookupErr := purego.Dlsym(symbol.library, symbol.name)
		if lookupErr != nil {
			return fmt.Errorf("load %s: %w", symbol.name, lookupErr)
		}
		purego.RegisterFunc(symbol.target, address)
	}
	// Run-loop modes use CFString equality, not pointer identity.
	api.defaultMode = api.createString(0, "kCFRunLoopDefaultMode", 0x08000100)
	if api.defaultMode == 0 {
		return errors.New("could not create the CoreFoundation run loop mode")
	}
	d.api = api
	return nil
}

func (d *macDriver) start() error {
	if err := d.load(); err != nil {
		return err
	}
	if !d.api.trusted() {
		return errors.New("allow Mouse Mapper in System Settings > Privacy & Security > Accessibility, then restart the app")
	}
	if !d.api.preflightListen() {
		return errors.New("allow Mouse Mapper in System Settings > Privacy & Security > Input Monitoring, then restart the app")
	}
	d.mu.Lock()
	previous := d.done
	d.mu.Unlock()
	if previous != nil {
		select {
		case <-previous:
		case <-time.After(3 * time.Second):
			return errors.New("the previous macOS input event tap is still stopping")
		}
	}
	d.mu.Lock()
	d.done = make(chan struct{})
	d.stopLoop = make(chan struct{})
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

func (d *macDriver) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d.mu.Lock()
	done := d.done
	stop := d.stopLoop
	d.mu.Unlock()
	defer close(done)
	api := d.api
	if d.callback == 0 {
		d.callback = purego.NewCallback(func(_ uintptr, eventType uint32, event uintptr, _ uintptr) uintptr {
			return d.handleEvent(eventType, event)
		})
	}
	const mask = uint64((1 << 10) | (1 << 11) | (1 << 12) | (1 << 25) | (1 << 26) | (1 << 27))
	tap := api.createTap(1, 0, 0, mask, d.callback, 0)
	if tap == 0 {
		ready <- errors.New("macOS refused the input event tap; enable Accessibility and Input Monitoring, then restart the app")
		return
	}
	defer api.release(tap)
	source := api.createSource(0, tap, 0)
	if source == 0 {
		ready <- errors.New("could not create the macOS input run-loop source")
		return
	}
	defer api.release(source)
	loop := api.loopCurrent()
	api.loopAddSource(loop, source, api.defaultMode)
	defer api.loopRemoveSource(loop, source, api.defaultMode)
	d.mu.Lock()
	d.loop, d.tap = loop, tap
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.loop, d.tap = 0, 0; d.mu.Unlock() }()
	api.enableTap(tap, true)
	// Subscribe before sampling to avoid missing a physical modifier release
	// between the initial snapshot and installation of the event tap.
	for i := range d.physical {
		d.physical[i].Store(api.keyState(1, uint16(i)))
	}
	ready <- nil
	defer api.enableTap(tap, false)
	for {
		select {
		case <-stop:
			return
		default:
		}
		// A bounded mode run also handles Stop arriving just before the first
		// run; CFRunLoopStop alone does not stop a loop that is not running yet.
		if result := api.loopRunMode(api.defaultMode, 0.25, false); result == 1 {
			d.engine.nativeFailed(errors.New("the macOS input event source closed; restart Mouse Mapper"))
			return
		}
	}
}

func (d *macDriver) handleEvent(eventType uint32, event uintptr) uintptr {
	api := d.api
	if eventType == 0xFFFFFFFE || eventType == 0xFFFFFFFF {
		// macOS may disable a tap after sleep or a slow callback. Release all
		// output before enabling it again instead of leaving a modifier held.
		d.engine.mu.Lock()
		_ = d.engine.releaseAllLocked()
		d.engine.mu.Unlock()
		d.mu.Lock()
		tap := d.tap
		loop := d.loop
		d.mu.Unlock()
		if tap != 0 {
			api.enableTap(tap, true)
			if !api.isTapEnabled(tap) {
				d.engine.nativeFailed(errors.New("macOS disabled mouse remapping; check Accessibility and Input Monitoring permissions"))
				d.mu.Lock()
				select {
				case <-d.stopLoop:
				default:
					close(d.stopLoop)
				}
				d.mu.Unlock()
				if loop != 0 {
					api.loopStop(loop)
				}
			}
		}
		return event
	}
	if event == 0 || api.getInteger(event, 42) == macInputTag {
		return event
	}
	if eventType == 25 || eventType == 26 || eventType == 27 {
		var button string
		switch api.getInteger(event, 3) {
		case 3:
			button = Back
		case 4:
			button = Forward
		default:
			return event
		}
		if eventType == 27 {
			if d.engine.mouseHeld(button) {
				return 0
			}
		} else if d.engine.mouseButton(button, eventType == 25) {
			return 0
		}
		return event
	}
	key := uint16(api.getInteger(event, 9))
	if key >= 128 {
		return event
	}
	down := eventType == 10
	if eventType == 12 {
		// Device-dependent flags distinguish left and right modifiers even
		// when the other side is held. Generic flags cannot do this.
		deviceFlag := map[uint16]uint64{55: 0x08, 56: 0x02, 58: 0x20, 59: 0x01, 54: 0x10, 60: 0x04, 61: 0x40, 62: 0x2000}[key]
		if deviceFlag == 0 {
			return event
		}
		down = api.getFlags(event)&deviceFlag != 0
	}
	d.physical[key].Store(down)
	if code := d.codes[key]; code != "" && d.engine.physicalKey(code, down) {
		return 0
	}
	return event
}

func (d *macDriver) stop() error {
	d.mu.Lock()
	loop, done := d.loop, d.done
	if d.stopLoop != nil {
		select {
		case <-d.stopLoop:
		default:
			close(d.stopLoop)
		}
	}
	d.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	if loop != 0 {
		d.api.loopStop(loop)
	}
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("timed out stopping the macOS input event tap")
	}
}

func (d *macDriver) sendKey(code string, down bool, heldModifiers []string) error {
	key, ok := macKeys[code]
	if !ok {
		return fmt.Errorf("unsupported macOS key %s", code)
	}
	api := d.api
	if api == nil {
		return errors.New("macOS input service is not running")
	}
	if !api.preflightPost() {
		return errors.New("macOS is blocking simulated keys; restore Mouse Mapper's Accessibility permission")
	}
	event := api.createKeyEvent(0, key, down)
	if event == 0 {
		return fmt.Errorf("could not create a macOS keyboard event for %s", code)
	}
	defer api.release(event)
	// Only hardware flags plus the engine's currently owned modifiers enter
	// the generated chord. This avoids stale synthetic Command/Option flags.
	flags := api.flagsState(1)
	for _, modifier := range heldModifiers {
		flags |= macModifierFlags[modifier]
	}
	api.setFlags(event, flags)
	api.setInteger(event, 42, macInputTag)
	api.postEvent(1, event)
	return nil
}

func (d *macDriver) physicallyDown(code string) bool {
	key, ok := macKeys[code]
	return ok && d.physical[key].Load()
}

func (d *macDriver) openPermissions() error {
	if err := d.load(); err != nil {
		return err
	}
	d.api.requestListen()
	return exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility").Run()
}
