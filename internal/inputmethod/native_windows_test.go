//go:build windows

package inputmethod

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsInputMethodStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the app targets 64-bit Windows")
	}
	for _, test := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"TF_INPUTPROCESSORPROFILE", unsafe.Sizeof(winProfile{}), 88},
		{"langid", unsafe.Offsetof(winProfile{}.Language), 4},
		{"clsid", unsafe.Offsetof(winProfile{}.Class), 8},
		{"guidProfile", unsafe.Offsetof(winProfile{}.Profile), 24},
		{"catid", unsafe.Offsetof(winProfile{}.Category), 40},
		{"hklSubstitute", unsafe.Offsetof(winProfile{}.Substitute), 56},
		{"dwCaps", unsafe.Offsetof(winProfile{}.Caps), 64},
		{"hkl", unsafe.Offsetof(winProfile{}.Layout), 72},
		{"dwFlags", unsafe.Offsetof(winProfile{}.Flags), 80},
		{"GUITHREADINFO", unsafe.Sizeof(winGUIThreadInfo{}), 72},
		{"hwndFocus", unsafe.Offsetof(winGUIThreadInfo{}.Focus), 16},
		{"rcCaret", unsafe.Offsetof(winGUIThreadInfo{}.CaretRect), 56},
		{"MSG", unsafe.Sizeof(winThreadMessage{}), 48},
		{"MSG wParam", unsafe.Offsetof(winThreadMessage{}.WParam), 16},
		{"MSG point", unsafe.Offsetof(winThreadMessage{}.Point), 36},
	} {
		if test.got != test.want {
			t.Errorf("%s = %d, want %d", test.name, test.got, test.want)
		}
	}
}

func TestWindowsInputMethodIdentity(t *testing.T) {
	profile := winProfile{Kind: winProfileTIP, Language: 0x804,
		Class: winInputProfilesCLSID, Profile: winProfileManagerIID,
		Category: winKeyboardCategoryGUID, Flags: winProfileEnabled}
	id := winProfileIdentity(profile, "unused")
	want := "windows:tip:0804:{33c53a50-f456-4884-b049-85fd643ecfed}:{71c6e74c-0f28-11d8-a82a-00065b84435c}"
	if id != want {
		t.Fatalf("TIP identity = %q, want %q", id, want)
	}
	if err := ValidateSelection(id, "Example input method"); err != nil {
		t.Fatal(err)
	}
	profile.Layout = 0xf0200804
	profile.Substitute = 0xf0200804
	if changed := winProfileIdentity(profile, "different"); changed != id {
		t.Fatalf("transient keyboard handles changed the stable identity: %q", changed)
	}
	profile.Profile.Data1++
	if winProfileIdentity(profile, "unused") == id {
		t.Fatal("two TIPs sharing a language must have distinct identities")
	}
	if got := winProfileIdentity(winProfile{Kind: winProfileKeyboard}, "000A0409"); got != "windows:layout:000a0409" {
		t.Fatalf("keyboard identity = %q", got)
	}
}

func TestWindowsInputMethodFriendlyName(t *testing.T) {
	if got := winFriendlyProfileName("WeType"); got != "\u5fae\u4fe1\u8f93\u5165\u6cd5 (WeType)" {
		t.Fatalf("WeType label = %q", got)
	}
	for _, name := range []string{"Example WeType helper", "US", "Other input method"} {
		if got := winFriendlyProfileName(name); got != name {
			t.Fatalf("unrelated official name %q was rewritten as %q", name, got)
		}
	}
}

func TestWindowsInputMethodProfileFiltering(t *testing.T) {
	for _, test := range []struct {
		profile winProfile
		want    bool
	}{
		{winProfile{Kind: winProfileTIP, Category: winKeyboardCategoryGUID, Flags: winProfileEnabled}, true},
		{winProfile{Kind: winProfileTIP, Category: winKeyboardCategoryGUID}, false},
		{winProfile{Kind: winProfileTIP, Flags: winProfileEnabled}, false},
		{winProfile{Kind: winProfileKeyboard, Flags: winProfileEnabled, Layout: 0x04090409}, true},
		{winProfile{Kind: winProfileKeyboard, Flags: winProfileEnabled}, false},
		{winProfile{Kind: winProfileKeyboard, Flags: winProfileEnabled | winProfileSubstituted, Layout: 0x08040804}, false},
	} {
		if got := winSelectable(test.profile); got != test.want {
			t.Errorf("winSelectable(%+v) = %v, want %v", test.profile, got, test.want)
		}
	}
}

func TestWindowsInputMethodProfileComparison(t *testing.T) {
	a := winProfile{Kind: winProfileTIP, Language: 0x804, Class: winInputProfilesCLSID, Profile: winProfileManagerIID}
	b := a
	b.Profile.Data1++
	if winSameProfile(a, b) {
		t.Fatal("same-language profiles are not interchangeable")
	}
	b = a
	b.Layout = 0xf0200804
	b.Flags = 7
	if !winSameProfile(a, b) {
		t.Fatal("transient handles and flags must not change TIP identity")
	}
	if winSameProfile(winProfile{Kind: winProfileKeyboard, Language: 0x409, Layout: 0x04090409},
		winProfile{Kind: winProfileKeyboard, Language: 0x409, Layout: 0xf0020409}) {
		t.Fatal("same-language keyboard layouts are not interchangeable")
	}
}

func TestWindowsInputMethodLanguageLayout(t *testing.T) {
	loaded := []uintptr{0x04090409, 0x08040804}
	for _, test := range []struct {
		profile winProfile
		want    uintptr
	}{
		{winProfile{Kind: winProfileKeyboard, Layout: 0xf0020409}, 0xf0020409},
		{winProfile{Kind: winProfileTIP, Language: 0x804}, 0x08040804},
		{winProfile{Kind: winProfileTIP, Language: 0x804, Substitute: 0xe0010804}, 0xe0010804},
		{winProfile{Kind: winProfileTIP, Language: 0x411}, 0},
	} {
		if got := winLanguageLayout(test.profile, loaded); got != test.want {
			t.Errorf("language layout for %+v = %x, want %x", test.profile, got, test.want)
		}
	}
	info := winLayoutIdentity(0x04090409, map[uint32]winLayoutInfo{0x04090409: {id: "00000409", name: "US"}})
	if info.id != "00000409" || info.name != "US" {
		t.Errorf("stable keyboard layout identity = %+v", info)
	}
}

//go:noinline
func growWinCOMCallbackStack(depth int) byte {
	var frame [2048]byte
	frame[0], frame[len(frame)-1] = byte(depth), byte(depth+1)
	if depth > 0 {
		frame[len(frame)/2] = growWinCOMCallbackStack(depth - 1)
	}
	runtime.KeepAlive(&frame)
	return frame[0] ^ frame[len(frame)/2] ^ frame[len(frame)-1]
}

func TestWindowsCOMCallKeepsPointerArgumentsStable(t *testing.T) {
	const wantValue = uint64(0x123456789abcdef0)
	const wantResult = uintptr(0x80070057)
	callback := windows.NewCallback(func(input *uint64, output **uint64) uintptr {
		_ = growWinCOMCallbackStack(32)
		runtime.GC()
		*input ^= 0xff
		*output = input
		return wantResult
	})
	type observation struct {
		result uintptr
		value  uint64
		alias  bool
	}
	for range 8 {
		finished := make(chan observation, 1)
		// A fresh goroutine starts with a small stack, which the callback grows.
		go func() {
			input := wantValue
			var output *uint64
			result := winCOMCall(callback, uintptr(unsafe.Pointer(&input)), uintptr(unsafe.Pointer(&output)))
			finished <- observation{result: result, value: input, alias: output == &input}
		}()
		got := <-finished
		if got.result != wantResult || got.value != wantValue^0xff || !got.alias {
			t.Fatalf("native callback lost pointer-backed arguments: %+v", got)
		}
	}
}

func TestWindowsInputMethodCOMEnumerationABI(t *testing.T) {
	manager := &winCOM{VTable: &[24]uintptr{}}
	enumerator := &winCOM{VTable: &[24]uintptr{}}
	reads, releases := 0, 0
	manager.VTable[6] = windows.NewCallback(func(obj *winCOM, language uintptr, output **winCOM) uintptr {
		if obj != manager || language != 0 {
			return 0x80070057
		}
		_ = growWinCOMCallbackStack(16)
		runtime.GC()
		*output = enumerator
		return 0
	})
	// Unlike IEnumUnknown, this TSF enumerator has Clone before Next.
	enumerator.VTable[4] = windows.NewCallback(func(obj *winCOM, count uintptr, profile *winProfile, fetched *uint32) uintptr {
		if obj != enumerator || count != 1 {
			return 0x80070057
		}
		_ = growWinCOMCallbackStack(16)
		runtime.GC()
		reads++
		if reads > 1 {
			*fetched = 0
			return 1
		}
		*profile = winProfile{Kind: winProfileTIP, Language: 0x804, Class: winInputProfilesCLSID}
		*fetched = 1
		return 0
	})
	enumerator.VTable[2] = windows.NewCallback(func(obj *winCOM) uintptr {
		if obj == enumerator {
			releases++
		}
		return 0
	})
	profiles, err := (&winProfiles{manager: manager}).enumerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 || releases != 1 || len(profiles) != 1 || profiles[0].Class != winInputProfilesCLSID {
		t.Fatalf("incorrect TSF enumeration: profiles=%+v reads=%d releases=%d", profiles, reads, releases)
	}
}

func TestWindowsInputMethodCOMActivationABI(t *testing.T) {
	manager := &winCOM{VTable: &[24]uintptr{}}
	expected := winProfile{Kind: winProfileTIP, Language: 0x804, Class: winInputProfilesCLSID, Profile: winProfileManagerIID}
	var result uintptr
	var called bool
	manager.VTable[3] = windows.NewCallback(func(obj *winCOM, kind, language uintptr, class, guid *windows.GUID, layout, flags uintptr) uintptr {
		_ = growWinCOMCallbackStack(16)
		runtime.GC()
		called = obj == manager && kind == winProfileTIP && language == 0x804 &&
			*class == expected.Class && *guid == expected.Profile && layout == 0 && flags == 0x20000004
		return result
	})
	service := &winProfiles{manager: manager}
	profile := expected
	if err := service.activate(&profile); err != nil || !called {
		t.Fatalf("activation ABI: called=%v error=%v", called, err)
	}
	result = 1
	if err := service.activate(&profile); err == nil {
		t.Fatal("S_FALSE for a disabled profile must not be treated as successful activation")
	}
}

// All switching orchestration tests use this in-memory API. They never call
// real input hooks, foreground helpers, COM activation, or keyboard APIs.
type fakeWinSwitch struct {
	current          winProfile
	profiles         []winProfile
	focused          bool
	workerLayout     uintptr
	foregroundLayout uintptr
	keepCurrent      bool
	readErr          error
	openErr          error
	enumerateErr     error
	activateErr      error
	requestErr       error
	workerErr        error
	calls            []string
	activations      []winProfile
	workerChanges    []uintptr
	requests         []uintptr
	onCall           func(string)
}

func newFakeWinSwitch() *fakeWinSwitch {
	target := winProfile{Kind: winProfileTIP, Language: 0x804, Class: winInputProfilesCLSID,
		Profile: winProfileManagerIID, Category: winKeyboardCategoryGUID, Flags: winProfileEnabled}
	current := target
	current.Class.Data1++
	current.Profile.Data1++
	current.Layout = 0x08040804
	return &fakeWinSwitch{
		current: current, profiles: []winProfile{target}, focused: true,
		workerLayout: 0x08040804, foregroundLayout: 0x08040804,
	}
}

func (fake *fakeWinSwitch) call(name string) {
	fake.calls = append(fake.calls, name)
	if fake.onCall != nil {
		fake.onCall(name)
	}
}

func (fake *fakeWinSwitch) count(name string) int {
	count := 0
	for _, call := range fake.calls {
		if call == name {
			count++
		}
	}
	return count
}

func (fake *fakeWinSwitch) targetID() string {
	return winProfileIdentity(fake.profiles[0], "")
}

func (fake *fakeWinSwitch) selectCurrent(profile winProfile) {
	fake.current = profile
	fake.current.Layout = fake.foregroundLayout
}

func (fake *fakeWinSwitch) api() winSwitchAPI {
	return winSwitchAPI{
		valid: func() bool {
			fake.call("valid")
			return fake.focused
		},
		foregroundProfile: func(context.Context) (winProfile, error) {
			fake.call("read")
			return fake.current, fake.readErr
		},
		openProfiles: func() (winSwitchProfiles, error) {
			fake.call("open")
			return fake, fake.openErr
		},
		layoutCatalog: func() map[uint32]winLayoutInfo {
			fake.call("catalog")
			return map[uint32]winLayoutInfo{
				0x04090409: {id: "00000409", name: "US"},
				0x08040804: {id: "00000804", name: "Chinese"},
			}
		},
		loadedLayouts: func() []uintptr {
			fake.call("loaded")
			return []uintptr{0x04090409, 0x08040804}
		},
		threadLayout: func(thread uint32) uintptr {
			fake.call("layout")
			if thread == 0 {
				return fake.workerLayout
			}
			return fake.foregroundLayout
		},
		activateKeyboard: func(layout uintptr) error {
			fake.workerChanges = append(fake.workerChanges, layout)
			if fake.workerErr == nil {
				fake.workerLayout = layout
			}
			fake.call("worker")
			return fake.workerErr
		},
		requestLayout: func(_ context.Context, layout uintptr) error {
			fake.requests = append(fake.requests, layout)
			if fake.requestErr == nil {
				fake.foregroundLayout = layout
				fake.current.Layout = layout
			}
			fake.call("request")
			return fake.requestErr
		},
		pumpMessages: func() { fake.call("pump") },
	}
}

func (fake *fakeWinSwitch) enumerate(context.Context) ([]winProfile, error) {
	fake.call("enumerate")
	return fake.profiles, fake.enumerateErr
}

func (fake *fakeWinSwitch) activate(profile *winProfile) error {
	fake.activations = append(fake.activations, *profile)
	if fake.activateErr == nil && !fake.keepCurrent {
		fake.selectCurrent(*profile)
	}
	fake.call("activate")
	return fake.activateErr
}

func (fake *fakeWinSwitch) close() { fake.call("close") }

func (fake *fakeWinSwitch) assertReadOnly(t *testing.T, reads int) {
	t.Helper()
	if got := fake.count("read"); got != reads {
		t.Fatalf("foreground reads = %d, want %d; calls=%v", got, reads, fake.calls)
	}
	for _, call := range fake.calls {
		if call != "valid" && call != "read" {
			t.Fatalf("unnecessary operation %q on the read-only path: %v", call, fake.calls)
		}
	}
	if len(fake.activations) != 0 || len(fake.requests) != 0 || len(fake.workerChanges) != 0 {
		t.Fatalf("read-only path mutated input state: %+v", fake)
	}
}

func TestWindowsSwitchAlreadySelectedTIPDoesNotAttemptSelection(t *testing.T) {
	fake := newFakeWinSwitch()
	fake.selectCurrent(fake.profiles[0])
	id := fake.targetID()
	// Neither a different worker language nor unavailable enumeration matters
	// when the target thread already has the exact requested GUID pair.
	fake.workerLayout = 0x04090409
	fake.openErr = errors.New("enumeration must not open")
	if err := winSwitchInputMethod(context.Background(), id, 42, fake.api()); err != nil {
		t.Fatal(err)
	}
	fake.assertReadOnly(t, 1)
}

func TestWindowsSwitchDifferentTIPSharingLayoutActivatesExactProfile(t *testing.T) {
	fake := newFakeWinSwitch()
	if err := winSwitchInputMethod(context.Background(), fake.targetID(), 42, fake.api()); err != nil {
		t.Fatal(err)
	}
	if fake.count("open") != 1 || fake.count("enumerate") != 1 || fake.count("close") != 1 ||
		len(fake.activations) != 1 || !winSameProfile(fake.activations[0], fake.profiles[0]) {
		t.Fatalf("different same-HKL TIP did not activate exactly once: %+v", fake)
	}
	if fake.count("read") < 3 || slices.Index(fake.calls, "read") > slices.Index(fake.calls, "open") {
		t.Fatalf("must query the target before enumeration and confirm after selection: %v", fake.calls)
	}
	if len(fake.workerChanges) != 0 || len(fake.requests) != 0 || fake.count("catalog") != 0 {
		t.Fatalf("same-language TIP selection changed keyboard layouts: %+v", fake)
	}
}

func TestWindowsSwitchReadsCurrentTIPOnEveryTrigger(t *testing.T) {
	fake := newFakeWinSwitch()
	original := fake.current
	fake.selectCurrent(fake.profiles[0])
	id := fake.targetID()
	api := fake.api()
	if err := winSwitchInputMethod(context.Background(), id, 42, api); err != nil {
		t.Fatal(err)
	}
	fake.assertReadOnly(t, 1)
	// A manual same-HKL IME switch between triggers must invalidate the skip.
	fake.current = original
	if err := winSwitchInputMethod(context.Background(), id, 42, api); err != nil {
		t.Fatal(err)
	}
	if len(fake.activations) != 1 {
		t.Fatalf("manual IME change was hidden by cached state: %v", fake.calls)
	}
	reads := fake.count("read")
	if err := winSwitchInputMethod(context.Background(), id, 42, api); err != nil {
		t.Fatal(err)
	}
	if fake.count("read") != reads+1 || len(fake.activations) != 1 || fake.count("open") != 1 {
		t.Fatalf("already selected trigger repeated a selection attempt: %v", fake.calls)
	}
}

func TestWindowsSwitchRejectsUnknownForegroundEvidence(t *testing.T) {
	helperFailure := errors.New("target helper unavailable")
	for _, test := range []struct {
		name   string
		change func(*fakeWinSwitch)
		want   error
	}{
		{"helper error", func(fake *fakeWinSwitch) { fake.readErr = helperFailure }, helperFailure},
		{"unknown kind", func(fake *fakeWinSwitch) { fake.current.Kind = 0 }, ErrUnavailable},
		{"missing class", func(fake *fakeWinSwitch) { fake.current.Class = windows.GUID{} }, ErrUnavailable},
		{"missing profile", func(fake *fakeWinSwitch) { fake.current.Profile = windows.GUID{} }, ErrUnavailable},
		{"wrong category", func(fake *fakeWinSwitch) { fake.current.Category = windows.GUID{} }, ErrUnavailable},
		{"missing layout", func(fake *fakeWinSwitch) { fake.current.Layout = 0 }, ErrUnavailable},
		{"incoherent layout", func(fake *fakeWinSwitch) { fake.current.Layout = 0x04090409 }, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeWinSwitch()
			fake.selectCurrent(fake.profiles[0])
			test.change(fake)
			if err := winSwitchInputMethod(context.Background(), fake.targetID(), 42, fake.api()); !errors.Is(err, test.want) {
				t.Fatalf("unknown foreground evidence = %v, want %v", err, test.want)
			}
			fake.assertReadOnly(t, 1)
		})
	}
}

func TestWindowsSwitchGuardsBeforeAndAfterExactRead(t *testing.T) {
	for _, during := range []string{"before", "valid", "read"} {
		for _, cancelInstead := range []bool{false, true} {
			name := during + "/focus"
			if cancelInstead {
				name = during + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				fake := newFakeWinSwitch()
				fake.selectCurrent(fake.profiles[0])
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				loseTarget := func() { fake.focused = false }
				want := ErrFocusChanged
				if cancelInstead {
					loseTarget = cancel
					want = context.Canceled
				}
				if during == "before" {
					loseTarget()
				} else {
					fake.onCall = func(name string) {
						if name == during {
							loseTarget()
						}
					}
				}
				if err := winSwitchInputMethod(ctx, fake.targetID(), 42, fake.api()); !errors.Is(err, want) {
					t.Fatalf("invalid trigger = %v, want %v", err, want)
				}
				reads := 0
				if during == "read" {
					reads = 1
				}
				fake.assertReadOnly(t, reads)
			})
		}
	}
}

func TestWindowsSwitchRechecksGuardsAroundNativeOperations(t *testing.T) {
	for _, during := range []string{"open", "enumerate", "loaded", "worker", "activate", "layout", "pump"} {
		for _, cancelInstead := range []bool{false, true} {
			name := during + "/focus"
			if cancelInstead {
				name = during + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				fake := newFakeWinSwitch()
				fake.workerLayout = 0x04090409
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := ErrFocusChanged
				if cancelInstead {
					want = context.Canceled
				}
				fake.onCall = func(name string) {
					if name != during || during == "layout" && len(fake.activations) == 0 {
						return
					}
					if cancelInstead {
						cancel()
					} else {
						fake.focused = false
					}
				}
				if err := winSwitchInputMethod(ctx, fake.targetID(), 42, fake.api()); !errors.Is(err, want) {
					t.Fatalf("stale switch = %v, want %v; calls=%v", err, want, fake.calls)
				}
				wantActivations := 0
				if during == "activate" || during == "layout" || during == "pump" {
					wantActivations = 1
				}
				if len(fake.activations) != wantActivations || len(fake.requests) != 0 || fake.count("close") != 1 || fake.count("read") != 1 {
					t.Fatalf("continued selection after guard failure: %+v", fake)
				}
				if fake.workerLayout != 0x04090409 {
					t.Fatalf("failed switch did not restore the worker layout: %x", fake.workerLayout)
				}
			})
		}
	}
}

func TestWindowsSwitchConfirmsTheActualTargetThreadNotWorker(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeWinSwitch)
		want   error
	}{
		{"different same-HKL TIP", func(fake *fakeWinSwitch) { fake.keepCurrent = true }, context.DeadlineExceeded},
		{"helper read error", func(fake *fakeWinSwitch) { fake.readErr = ErrUnavailable }, ErrUnavailable},
		{"unknown identity", func(fake *fakeWinSwitch) { fake.current.Kind = 0 }, ErrUnavailable},
		{"incoherent layout", func(fake *fakeWinSwitch) { fake.current.Layout = 0x04090409 }, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeWinSwitch()
			original := fake.current
			fake.onCall = func(name string) {
				if name == "activate" {
					test.change(fake)
					if fake.keepCurrent {
						fake.current = original
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := winSwitchInputMethod(ctx, fake.targetID(), 42, fake.api()); !errors.Is(err, test.want) {
				t.Fatalf("unconfirmed foreground selection = %v, want %v", err, test.want)
			}
			if len(fake.activations) != 1 || len(fake.requests) != 0 || fake.count("read") < 2 || fake.count("close") != 1 {
				t.Fatalf("unconfirmed selection retried or bypassed the target-thread query: %+v", fake)
			}
		})
	}
}

func TestWindowsSwitchPreparesAndRestoresWorkerLanguage(t *testing.T) {
	fake := newFakeWinSwitch()
	fake.workerLayout = 0x04090409
	fake.foregroundLayout = 0x04090409
	fake.current = winProfile{Kind: winProfileKeyboard, Language: 0x409, Layout: 0x04090409}
	if err := winSwitchInputMethod(context.Background(), fake.targetID(), 42, fake.api()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.workerChanges, []uintptr{0x08040804, 0x04090409}) ||
		!slices.Equal(fake.requests, []uintptr{0x08040804}) || len(fake.activations) != 1 || fake.count("close") != 1 {
		t.Fatalf("cross-language selection did not prepare, confirm and restore correctly: %+v", fake)
	}
}

func TestWindowsSwitchNativeFailuresDoNotRetry(t *testing.T) {
	failure := errors.New("mock native failure")
	for _, during := range []string{"open", "enumerate", "worker", "activate", "request"} {
		t.Run(during, func(t *testing.T) {
			fake := newFakeWinSwitch()
			fake.workerLayout = 0x04090409
			fake.foregroundLayout = 0x04090409
			fake.current = winProfile{Kind: winProfileKeyboard, Language: 0x409, Layout: 0x04090409}
			switch during {
			case "open":
				fake.openErr = failure
			case "enumerate":
				fake.enumerateErr = failure
			case "worker":
				fake.workerErr = failure
			case "activate":
				fake.activateErr = failure
			case "request":
				fake.requestErr = failure
			}
			if err := winSwitchInputMethod(context.Background(), fake.targetID(), 42, fake.api()); !errors.Is(err, failure) {
				t.Fatalf("native failure = %v", err)
			}
			if fake.count(during) != 1 || fake.count("read") != 1 || len(fake.activations) > 1 || len(fake.requests) > 1 {
				t.Fatalf("native failure was retried: %+v", fake)
			}
			if fake.workerLayout != 0x04090409 {
				t.Fatal("worker language not restored after native failure")
			}
		})
	}
}

func TestWindowsSwitchKeyboardLayoutDoesNotRequireTIPHelper(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "different", true: "already selected"}[selected], func(t *testing.T) {
			fake := newFakeWinSwitch()
			fake.profiles = []winProfile{{Kind: winProfileKeyboard, Language: 0x409, Layout: 0x04090409, Flags: winProfileEnabled}}
			fake.readErr = errors.New("TIP helper must not run for a keyboard layout")
			if selected {
				fake.foregroundLayout = 0x04090409
			}
			if err := winSwitchInputMethod(context.Background(), "windows:layout:00000409", 42, fake.api()); err != nil {
				t.Fatal(err)
			}
			wantRequests := 1
			if selected {
				wantRequests = 0
			}
			if fake.count("read") != 0 || len(fake.activations) != 0 || len(fake.workerChanges) != 0 ||
				len(fake.requests) != wantRequests || fake.count("catalog") != 1 || fake.count("close") != 1 {
				t.Fatalf("keyboard-layout path invoked TIP operations: %+v", fake)
			}
		})
	}
}

func TestWindowsSwitchKeyboardLayoutChecksGuardAfterNativeRead(t *testing.T) {
	fake := newFakeWinSwitch()
	fake.profiles = []winProfile{{Kind: winProfileKeyboard, Language: 0x409, Layout: 0x04090409, Flags: winProfileEnabled}}
	fake.foregroundLayout = 0x04090409
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.onCall = func(name string) {
		if name == "layout" {
			cancel()
		}
	}
	if err := winSwitchInputMethod(ctx, "windows:layout:00000409", 42, fake.api()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled layout query reported success: %v", err)
	}
	if len(fake.requests) != 0 || len(fake.activations) != 0 || fake.count("read") != 0 {
		t.Fatal("cancelled layout query changed input state")
	}
}

func TestWindowsSelectionStability(t *testing.T) {
	var stability winSelectionStability
	start := time.Unix(1, 0)
	for _, test := range []struct {
		name    string
		after   time.Duration
		matched bool
		ready   bool
	}{
		{"unmatched", 0, false, false},
		{"first match", time.Millisecond, true, false},
		{"too early", 60 * time.Millisecond, true, false},
		{"match lost", 61 * time.Millisecond, false, false},
		{"new first match", 62 * time.Millisecond, true, false},
		{"reset interval not elapsed", 121 * time.Millisecond, true, false},
		{"stable", 122 * time.Millisecond, true, true},
	} {
		if got := stability.ready(test.matched, start.Add(test.after)); got != test.ready {
			t.Errorf("%s: ready = %v, want %v", test.name, got, test.ready)
		}
	}
}

func TestWindowsWaitForInputMethod(t *testing.T) {
	valid := func() bool { return true }
	started := time.Now()
	if err := winWaitForSelection(context.Background(), valid, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < winSelectionStableTime {
		t.Fatalf("selection returned before the stability interval: %s", elapsed)
	}
	if err := winWaitForSelection(context.Background(), func() bool { return false }, func() bool { return true }); !errors.Is(err, ErrFocusChanged) {
		t.Fatalf("changed foreground = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := winWaitForSelection(ctx, valid, func() bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled action = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := winWaitForSelection(ctx, valid, func() bool { return false }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unacknowledged selection = %v", err)
	}
	stillFocused := true
	if err := winWaitForSelection(context.Background(), func() bool { return stillFocused }, func() bool {
		stillFocused = false
		return true
	}); !errors.Is(err, ErrFocusChanged) {
		t.Fatalf("focus changed during confirmation = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	if err := winWaitForSelection(ctx, valid, func() bool {
		cancel()
		return true
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during confirmation = %v", err)
	}
}

func TestWindowsWaitForInputMethodRechecksDuringStability(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		checks := 0
		err := winWaitForSelection(ctx, func() bool { return true }, func() bool {
			checks++
			if checks == 2 {
				cancel()
			}
			return true
		})
		if !errors.Is(err, context.Canceled) || checks != 2 {
			t.Fatalf("cancellation while stabilizing: checks=%d error=%v", checks, err)
		}
	})
	t.Run("focus change", func(t *testing.T) {
		focused := true
		checks := 0
		err := winWaitForSelection(context.Background(), func() bool { return focused }, func() bool {
			checks++
			if checks == 2 {
				focused = false
			}
			return true
		})
		if !errors.Is(err, ErrFocusChanged) || checks != 2 {
			t.Fatalf("focus changed while stabilizing: checks=%d error=%v", checks, err)
		}
	})
	t.Run("caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		err := winWaitForSelection(ctx, func() bool { return true }, func() bool { return true })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline before selection became stable: %v", err)
		}
	})
}

// This diagnostic is opt-in and read-only: it never activates a profile,
// sends a keyboard event, changes foreground focus, or writes the registry.
func TestWindowsReadOnlyInputMethodDiscovery(t *testing.T) {
	if os.Getenv("MOUSE_MAPPER_IME_DISCOVERY_TEST") != "1" {
		t.Skip("set MOUSE_MAPPER_IME_DISCOVERY_TEST=1 for read-only TSF discovery")
	}
	methods, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) == 0 {
		t.Fatal("no enabled keyboard or text-service profiles found")
	}
	seen := map[string]bool{}
	for _, method := range methods {
		if err := ValidateSelection(method.ID, method.Name); err != nil {
			t.Fatal(err)
		}
		if seen[method.ID] || strings.TrimSpace(method.Name) == "" {
			t.Fatalf("invalid discovered method: %+v", method)
		}
		seen[method.ID] = true
		t.Logf("%s: %s", method.Name, method.ID)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	service, err := winOpenProfiles()
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()
	profiles, err := service.enumerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if winSelectable(profile) {
			t.Logf("profile kind=%d lang=%04x clsid=%s guid=%s substitute=%x layout=%x flags=%x", profile.Kind, profile.Language,
				profile.Class.String(), profile.Profile.String(), profile.Substitute, profile.Layout, profile.Flags)
		}
	}
	active, err := service.active()
	t.Logf("helper active=%+v err=%v helper layout=%x loaded=%x", active, err, winThreadLayout(0), winLoadedLayouts())
	if focus, err := CaptureFocus(); err == nil {
		t.Logf("foreground layout=%x", winThreadLayout(focus.(*winFocus).thread))
	}
}
