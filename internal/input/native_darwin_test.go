//go:build darwin && (amd64 || arm64)

package input

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMacKeyPositionsUnique(t *testing.T) {
	positions := map[uint16]string{}
	for code, key := range macKeys {
		if previous := positions[key]; previous != "" {
			t.Errorf("%s and %s share a native key position", previous, code)
		}
		positions[key] = code
		if !keyCodes[code] {
			t.Errorf("native key %s is not accepted by the common model", code)
		}
	}
	for _, unsupported := range []string{"CapsLock", "PrintScreen", "Pause", "ScrollLock", "F21", "F24"} {
		if slices.Contains(SupportedCodes(), unsupported) {
			t.Errorf("advertised unsupported macOS key %s", unsupported)
		}
	}
}

func TestMacSideButtonsAndDragAreSuppressed(t *testing.T) {
	e, output := testEngine(t, []string{"Meta", "KeyC"}, []string{"Shift"})
	driver := newNativeDriver(e).(*macDriver)
	button := int64(3)
	driver.api = &macAPI{getInteger: func(_ uintptr, field uint32) int64 {
		if field == 3 {
			return button
		}
		return 0
	}}
	if result := driver.handleEvent(25, 1); result != 0 {
		t.Fatal("back down not suppressed")
	}
	if result := driver.handleEvent(27, 1); result != 0 {
		t.Fatal("mapped side drag not suppressed")
	}
	if result := driver.handleEvent(26, 1); result != 0 {
		t.Fatal("back up not suppressed")
	}
	assertEvents(t, output, "Meta down", "KeyC down", "KeyC up", "Meta up")
	if result := driver.handleEvent(27, 1); result != 1 {
		t.Fatal("unowned side drag suppressed")
	}
	button = 4
	if result := driver.handleEvent(25, 1); result != 0 {
		t.Fatal("forward down not suppressed")
	}
	if result := driver.handleEvent(26, 1); result != 0 {
		t.Fatal("forward up not suppressed")
	}
	button = 2
	if result := driver.handleEvent(25, 1); result != 1 {
		t.Fatal("middle click suppressed")
	}
}

func TestMacSyntheticEventsDoNotAffectPhysicalState(t *testing.T) {
	e, _ := testEngine(t, []string{"KeyA"}, nil)
	driver := newNativeDriver(e).(*macDriver)
	driver.api = &macAPI{getInteger: func(_ uintptr, field uint32) int64 {
		if field == 42 {
			return macInputTag
		}
		return 0
	}}
	if result := driver.handleEvent(10, 1); result != 1 {
		t.Fatal("tagged keyboard event did not pass through")
	}
	if driver.physicallyDown("KeyA") {
		t.Fatal("synthetic input changed hardware state")
	}
}

func TestMacPhysicalModifierSidesAreIndependent(t *testing.T) {
	e, _ := testEngine(t, []string{"Shift"}, nil)
	driver := newNativeDriver(e).(*macDriver)
	key := int64(56)
	flags := uint64(1<<17 | 0x04)
	driver.api = &macAPI{
		getInteger: func(_ uintptr, field uint32) int64 {
			if field == 9 {
				return key
			}
			return 0
		},
		getFlags: func(uintptr) uint64 { return flags },
	}
	driver.handleEvent(12, 1)
	if driver.physicallyDown("Shift") {
		t.Fatal("right Shift must not count as mapped left Shift")
	}
	key, flags = 56, 1<<17|0x02
	driver.handleEvent(12, 1)
	if !driver.physicallyDown("Shift") {
		t.Fatal("left Shift was not tracked")
	}
	flags = 1<<17 | 0x04
	driver.handleEvent(12, 1)
	if driver.physicallyDown("Shift") {
		t.Fatal("left Shift release was hidden by held right Shift")
	}
}

func TestMacGeneratedFlagsCombineHardwareAndHeldModifiers(t *testing.T) {
	driver := newNativeDriver(nil).(*macDriver)
	var flags uint64
	var tag int64
	var keyCode uint16
	var down bool
	var posted, released bool
	driver.api = &macAPI{
		preflightPost:  func() bool { return true },
		createKeyEvent: func(_ uintptr, key uint16, pressed bool) uintptr { keyCode, down = key, pressed; return 123 },
		flagsState: func(state int32) uint64 {
			if state != 1 {
				t.Errorf("queried non-hardware state %d", state)
			}
			return 1<<16 | 1<<18
		},
		setFlags: func(event uintptr, value uint64) { flags = value },
		setInteger: func(event uintptr, field uint32, value int64) {
			if field != 42 {
				t.Errorf("tag field = %d", field)
			}
			tag = value
		},
		postEvent: func(location uint32, event uintptr) {
			if location != 1 {
				t.Errorf("posting into hardware event stream instead of session")
			}
			posted = true
		},
		release: func(uintptr) { released = true },
	}
	if err := driver.sendKey("KeyC", true, []string{"Meta", "Shift"}); err != nil {
		t.Fatal(err)
	}
	if flags != 1<<16|1<<17|1<<18|1<<20 {
		t.Errorf("flags = %#x", flags)
	}
	if tag != macInputTag || keyCode != 8 || !down || !posted || !released {
		t.Fatal("incorrect generated native keyboard event")
	}
}

func TestMacRevokedPostingPermissionFailsOpen(t *testing.T) {
	driver := newNativeDriver(nil).(*macDriver)
	driver.api = &macAPI{preflightPost: func() bool { return false }}
	if err := driver.sendKey("KeyC", true, nil); err == nil || !strings.Contains(err.Error(), "Accessibility") {
		t.Fatalf("expected permission failure, got %v", err)
	}
}

func TestMacUnavailableKeyRejectedWhilePaused(t *testing.T) {
	e := New(nil)
	if err := e.Apply(Config{Enabled: false, Back: Binding{Enabled: true, Keys: []string{"F24"}}}); err == nil {
		t.Fatal("accepted a platform-unsupported key while recording/paused")
	}
}

func TestMacImmediateStopDoesNotNeedStartedRunLoop(t *testing.T) {
	e := New(nil)
	driver := e.driver.(*macDriver)
	driver.api = &macAPI{
		trusted: func() bool { return true }, preflightListen: func() bool { return true },
		keyState:      func(int32, uint16) bool { return false },
		createTap:     func(uint32, uint32, uint32, uint64, uintptr, uintptr) uintptr { return 1 },
		createSource:  func(uintptr, uintptr, int64) uintptr { return 2 },
		loopCurrent:   func() uintptr { return 3 },
		loopAddSource: func(uintptr, uintptr, uintptr) {}, loopRemoveSource: func(uintptr, uintptr, uintptr) {},
		loopRunMode: func(uintptr, float64, bool) int32 { time.Sleep(time.Millisecond); return 3 },
		// Deliberately do not wake the loop: the stop channel must suffice.
		loopStop: func(uintptr) {}, enableTap: func(uintptr, bool) {}, release: func(uintptr) {},
	}
	for range 10 {
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		if err := e.Stop(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMacNativeHookLifecycle(t *testing.T) {
	if os.Getenv("MOUSE_MAPPER_NATIVE_TEST") != "1" {
		t.Skip("set MOUSE_MAPPER_NATIVE_TEST=1 after granting macOS input permissions")
	}
	e := New(nil)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Running {
		t.Fatal("native input service did not start")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
}
