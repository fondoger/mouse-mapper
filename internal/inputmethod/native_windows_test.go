//go:build windows

package inputmethod

import (
	"context"
	"errors"
	"os"
	"runtime"
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

func TestWindowsInputMethodCOMEnumerationABI(t *testing.T) {
	manager := &winCOM{VTable: &[24]uintptr{}}
	enumerator := &winCOM{VTable: &[24]uintptr{}}
	reads, releases := 0, 0
	manager.VTable[6] = windows.NewCallback(func(obj *winCOM, language uintptr, output **winCOM) uintptr {
		if obj != manager || language != 0 {
			return 0x80070057
		}
		*output = enumerator
		return 0
	})
	// Unlike IEnumUnknown, this TSF enumerator has Clone before Next.
	enumerator.VTable[4] = windows.NewCallback(func(obj *winCOM, count uintptr, profile *winProfile, fetched *uint32) uintptr {
		if obj != enumerator || count != 1 {
			return 0x80070057
		}
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
	profile := winProfile{Kind: winProfileTIP, Language: 0x804, Class: winInputProfilesCLSID, Profile: winProfileManagerIID}
	var result uintptr
	var called bool
	manager.VTable[3] = windows.NewCallback(func(obj *winCOM, kind, language uintptr, class, guid *windows.GUID, layout, flags uintptr) uintptr {
		called = obj == manager && kind == winProfileTIP && language == 0x804 &&
			*class == profile.Class && *guid == profile.Profile && layout == 0 && flags == 0x20000004
		return result
	})
	service := &winProfiles{manager: manager}
	if err := service.activate(&profile); err != nil || !called {
		t.Fatalf("activation ABI: called=%v error=%v", called, err)
	}
	result = 1
	if err := service.activate(&profile); err == nil {
		t.Fatal("S_FALSE for a disabled profile must not be treated as successful activation")
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
