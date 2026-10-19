package desktop

import (
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func recordingEditor(t *testing.T, platform string) (*uiMapper, *nativeView, *ui.Tester) {
	t.Helper()
	m := newUIMapper()
	m.state.Status.Platform = platform
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	if !tt.Focused("快捷键录制区域") {
		t.Fatal("the native recorder did not take focus")
	}
	return m, v, tt
}

func assertRecording(t *testing.T, v *nativeView, keys ...string) {
	t.Helper()
	if !slices.Equal(v.editKeys, keys) {
		t.Fatalf("recorded keys = %v, want %v", v.editKeys, keys)
	}
}

var recordingReleaseOrders = [][3]ui.Modifiers{
	{ui.Ctrl, ui.Shift, ui.Super}, {ui.Ctrl, ui.Super, ui.Shift},
	{ui.Shift, ui.Ctrl, ui.Super}, {ui.Shift, ui.Super, ui.Ctrl},
	{ui.Super, ui.Ctrl, ui.Shift}, {ui.Super, ui.Shift, ui.Ctrl},
}

func TestNativeRecordingModifierOnlyReleasesKeepChord(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		for order, releases := range recordingReleaseOrders {
			t.Run(fmt.Sprintf("%s/release-%d", platform, order), func(t *testing.T) {
				m, v, tt := recordingEditor(t, platform)
				mods := ui.Ctrl | ui.Shift | ui.Super
				tt.HoldModifiers(ui.Ctrl)
				tt.HoldModifiers(ui.Ctrl | ui.Shift)
				tt.HoldModifiers(mods)
				assertRecording(t, v, "Control", "Shift", "Meta")
				for _, released := range releases {
					mods &^= released
					tt.HoldModifiers(mods)
					assertRecording(t, v, "Control", "Shift", "Meta")
				}
				click(t, tt, "保存快捷键")
				if !slices.Equal(m.state.Settings.Back.Keys, []string{"Control", "Shift", "Meta"}) {
					t.Fatalf("saved incomplete modifier chord: %v", m.state.Settings.Back.Keys)
				}
				click(t, tt, "编辑后退键")
				assertRecording(t, v, "Control", "Shift", "Meta")
				click(t, tt, "取消")
			})
		}
	}
}

func TestNativeRecordingModifierOnlyCanBeReplacedAfterRelease(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			_, v, tt := recordingEditor(t, platform)
			tt.HoldModifiers(ui.Ctrl | ui.Shift | ui.Super)
			tt.HoldModifiers(0)
			tt.HoldModifiers(ui.Alt)
			tt.HoldModifiers(ui.Alt | ui.Super)
			tt.HoldModifiers(ui.Alt)
			tt.HoldModifiers(0)
			assertRecording(t, v, "Alt", "Meta")
		})
	}
}

func TestNativeRecordingNeverUnionsNonOverlappingModifiers(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			_, v, tt := recordingEditor(t, platform)
			for _, mods := range []ui.Modifiers{ui.Ctrl, ui.Shift, ui.Super} {
				tt.HoldModifiers(mods)
				tt.HoldModifiers(0)
				if !slices.Equal(v.editKeys, modifierKeys(mods)) {
					t.Fatalf("sequential modifiers became a chord: %v", v.editKeys)
				}
			}
			// Shift and Alt never overlap, even though Control stays held.
			tt.HoldModifiers(ui.Ctrl)
			tt.HoldModifiers(ui.Ctrl | ui.Shift)
			tt.HoldModifiers(ui.Ctrl)
			tt.HoldModifiers(ui.Ctrl | ui.Alt)
			tt.HoldModifiers(ui.Ctrl)
			tt.HoldModifiers(0)
			assertRecording(t, v, "Control", "Alt")
		})
	}
}

func TestNativeRecordingOrdinaryChordSurvivesModifierReleases(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		for order, releases := range recordingReleaseOrders {
			t.Run(fmt.Sprintf("%s/release-%d", platform, order), func(t *testing.T) {
				_, v, tt := recordingEditor(t, platform)
				mods := ui.Ctrl | ui.Shift | ui.Super
				tt.HoldModifiers(mods)
				tt.Key(mods, ui.KeyC)
				for _, released := range releases {
					mods &^= released
					tt.HoldModifiers(mods)
					assertRecording(t, v, "Control", "Shift", "Meta", "KeyC")
				}
				tt.HoldModifiers(ui.Alt)
				tt.HoldModifiers(0)
				assertRecording(t, v, "Alt")
			})
		}
	}
}

func TestNativeRecordingOrdinaryKeyCanBeReplacedWithModifiersHeld(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			_, v, tt := recordingEditor(t, platform)
			tt.HoldModifiers(ui.Ctrl)
			tt.Key(ui.Ctrl, ui.KeyC)
			assertRecording(t, v, "Control", "KeyC")
			tt.Key(ui.Ctrl, ui.KeyV)
			assertRecording(t, v, "Control", "KeyV")
			tt.HoldModifiers(ui.Ctrl | ui.Shift)
			tt.HoldModifiers(ui.Shift)
			tt.HoldModifiers(0)
			assertRecording(t, v, "Control", "KeyV")
		})
	}
}

func TestNativeRecordingRepeatDoesNotReplaceNewerChord(t *testing.T) {
	_, v, tt := recordingEditor(t, "windows")
	tt.HoldModifiers(ui.Ctrl)
	for _, event := range []ui.InputEvent{
		{Kind: ui.InputKeyDown, Key: ui.KeyC, Mods: ui.Ctrl},
		{Kind: ui.InputKeyDown, Key: ui.KeyV, Mods: ui.Ctrl},
		{Kind: ui.InputKeyUp, Key: ui.KeyV, Mods: ui.Ctrl},
		{Kind: ui.InputKeyDown, Key: ui.KeyC, Mods: ui.Ctrl, Repeat: true},
	} {
		if !v.recordInput(event) {
			t.Fatalf("the recorder did not consume %+v", event)
		}
	}
	tt.Frame()
	assertRecording(t, v, "Control", "KeyV")
	tt.KeyUp(ui.KeyC)
	tt.HoldModifiers(ui.Alt)
	tt.HoldModifiers(0)
	assertRecording(t, v, "Alt")
}

func TestNativeRecordingOrdinaryKeyKeepsGestureUntilAllReleased(t *testing.T) {
	_, v, tt := recordingEditor(t, "windows")
	tt.KeyDown(ui.KeyC)
	tt.HoldModifiers(ui.Ctrl)
	tt.HoldModifiers(ui.Ctrl | ui.Shift)
	tt.HoldModifiers(0)
	assertRecording(t, v, "KeyC")
	tt.KeyUp(ui.KeyC)
	tt.HoldModifiers(ui.Alt)
	tt.HoldModifiers(0)
	assertRecording(t, v, "Alt")
}

func TestNativeRecordingTracksAllHeldOrdinaryKeys(t *testing.T) {
	_, v, tt := recordingEditor(t, "windows")
	tt.KeyDown(ui.KeyC)
	tt.KeyDown(ui.KeyV)
	tt.KeyUp(ui.KeyV)
	tt.HoldModifiers(ui.Alt)
	tt.HoldModifiers(0)
	assertRecording(t, v, "KeyV")
	tt.KeyUp(ui.KeyC)
	tt.HoldModifiers(ui.Shift)
	tt.HoldModifiers(0)
	assertRecording(t, v, "Shift")
}

func TestNativeRecordingReleaseUsesPhysicalKey(t *testing.T) {
	_, v, tt := recordingEditor(t, "windows")
	v.recordInput(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyZ, PhysicalKey: ui.KeyY})
	v.recordInput(ui.InputEvent{Kind: ui.InputKeyUp, PhysicalKey: ui.KeyY})
	assertRecording(t, v, "KeyY")
	tt.HoldModifiers(ui.Ctrl)
	tt.HoldModifiers(0)
	assertRecording(t, v, "Control")
}

func TestNativeRecordingResetsAfterCaptureTransitions(t *testing.T) {
	for _, transition := range []struct {
		name  string
		reset func(*testing.T, *ui.Tester)
	}{
		{"restart", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "快捷键录制区域")
		}},
		{"mode", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "手动选择")
			click(t, tt, "键盘录制")
		}},
		{"preset", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "粘贴")
			click(t, tt, "快捷键录制区域")
		}},
		{"editor", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "取消")
			click(t, tt, "编辑后退键")
		}},
		{"window-focus", func(t *testing.T, tt *ui.Tester) {
			tt.SetFocused(false)
			tt.SetFocused(true)
		}},
	} {
		t.Run(transition.name, func(t *testing.T) {
			_, v, tt := recordingEditor(t, "windows")
			tt.KeyDown(ui.KeyC)
			transition.reset(t, tt)
			// No key-up arrives after capture loses the original gesture.
			tt.HoldModifiers(ui.Ctrl | ui.Shift | ui.Super)
			tt.HoldModifiers(ui.Ctrl | ui.Shift)
			tt.HoldModifiers(ui.Ctrl)
			tt.HoldModifiers(0)
			assertRecording(t, v, "Control", "Shift", "Meta")
		})
	}
}
