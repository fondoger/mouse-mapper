package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/input"
)

func recordingEditor(t *testing.T, platform string) (*uiMapper, *nativeView, *ui.Tester) {
	t.Helper()
	m := newUIMapper()
	m.state.Status.Platform = platform
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	if v.capture {
		t.Fatal("opening the editor armed the recorder without a click")
	}
	click(t, tt, "快捷键录制区域")
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

func TestNativeRecordingRequiresExplicitActivation(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	original := slices.Clone(v.editKeys)
	tt.Key(ui.Ctrl|ui.Shift, ui.KeyK)
	tt.HoldModifiers(ui.Ctrl | ui.Super)
	tt.HoldModifiers(0)
	assertRecording(t, v, original...)
	if v.capture || tt.HasText("按下快捷键") {
		t.Fatal("opening the editor started listening")
	}
	click(t, tt, "快捷键录制区域")
	if !v.capture || !tt.HasText("按下快捷键") || !tt.Focused("快捷键录制区域") {
		t.Fatal("activating the field did not focus it and show its prompt")
	}
	assertRecording(t, v, original...)
	// Merely activating and leaving a field must not erase the draft.
	click(t, tt, "为后退键设置快捷键")
	if v.capture || tt.HasText("按下快捷键") {
		t.Fatal("clicking non-focusable dialog content did not stop recording")
	}
	tt.Key(ui.Ctrl, ui.KeyX)
	assertRecording(t, v, original...)
	click(t, tt, "保存快捷键")
	if !slices.Equal(m.state.Settings.Back.Keys, original) || m.state.Recording {
		t.Fatal("an empty recording changed the saved shortcut or left mappings paused")
	}
}

func TestNativeRecordingSpecialKeysDoNotDismissOrNavigate(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		for _, button := range []string{input.Back, input.Forward} {
			for _, long := range []bool{false, true} {
				for _, key := range []ui.Key{ui.KeyEscape, ui.KeyEnter, ui.KeyTab, ui.KeySpace, ui.KeyBackspace} {
					t.Run(fmt.Sprintf("%s/%s/long-%v/%s", platform, button, long, physicalCode(key)), func(t *testing.T) {
						m := newUIMapper()
						m.state.Status.Platform = platform
						v := testView(m)
						tt := ui.NewTester(v.main, 880, 640)
						label := "编辑后退键"
						if button == input.Forward {
							label = "编辑前进键"
						}
						click(t, tt, label)
						field := "快捷键录制区域"
						if long {
							field = "长按快捷键录制区域"
						}
						click(t, tt, field)
						for range 2 {
							tt.Key(0, key)
							assertRecording(t, v, physicalCode(key))
							if !v.editing || !m.state.Recording || !tt.Focused(field) {
								t.Fatal("a recorded key dismissed the editor or moved focus")
							}
						}
						click(t, tt, "保存快捷键")
						binding := m.state.Settings.Back
						if button == input.Forward {
							binding = m.state.Settings.Forward
						}
						keys := binding.Keys
						if long {
							if binding.LongPress == nil {
								t.Fatal("long action was not saved")
							}
							keys = binding.LongPress.Keys
						}
						if !slices.Equal(keys, []string{physicalCode(key)}) || v.editing || m.state.Recording {
							t.Fatalf("special key did not save/resume mapping: %v", keys)
						}
					})
				}
			}
		}
	}
}

func TestNativeRecordingStopsUntilClickedAgain(t *testing.T) {
	for _, transition := range []string{"action", "window-focus", "input-method", "threshold", "blank"} {
		t.Run(transition, func(t *testing.T) {
			m := newUIMapper()
			m.state.Settings.Back = longPressFixture()
			m.state.InputMethods = uiInputMethods()
			v := testView(m)
			tt := ui.NewTester(v.main, 1080, 760)
			click(t, tt, "编辑后退键")
			click(t, tt, "快捷键录制区域")
			tt.KeyDown(ui.KeyC)
			switch transition {
			case "action":
				click(t, tt, "长按快捷键录制区域")
				click(t, tt, "为后退键设置快捷键")
			case "window-focus":
				tt.SetFocused(false)
				tt.SetFocused(true)
			case "input-method":
				click(t, tt, "选择触发前的输入法")
				click(t, tt, "豆包输入法")
			case "threshold":
				changeThreshold(t, tt, "750")
			case "blank":
				click(t, tt, "为后退键设置快捷键")
			}
			if v.capture || len(v.recorder.held) != 0 || v.recorder.mods != 0 {
				t.Fatal("leaving the field retained recording/held-key state")
			}
			before := slices.Clone(v.editKeys)
			tt.HoldModifiers(ui.Ctrl | ui.Shift)
			tt.HoldModifiers(0)
			tt.Key(0, ui.KeyZ)
			assertRecording(t, v, before...)
			click(t, tt, "快捷键录制区域")
			if !tt.HasText("按下快捷键") {
				t.Fatal("reactivating the field did not restore the prompt")
			}
			tt.Key(0, ui.KeyEscape)
			assertRecording(t, v, "Escape")
		})
	}
}

func TestNativeRecordingModifiersHeldBeforeClickDoNotReplacePrompt(t *testing.T) {
	_, v, tt := recordingEditor(t, "windows")
	click(t, tt, "为后退键设置快捷键")
	before := slices.Clone(v.editKeys)
	if err := tt.ClickWith(ui.Ctrl|ui.Shift, "快捷键录制区域"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("按下快捷键") {
		t.Fatal("previously held modifiers replaced the prompt")
	}
	tt.HoldModifiers(ui.Ctrl)
	tt.HoldModifiers(0)
	assertRecording(t, v, before...)
	tt.HoldModifiers(ui.Alt)
	tt.HoldModifiers(0)
	assertRecording(t, v, "Alt")
}

func TestNativeRecordingKeyboardFocusDoesNotArmUntilActivated(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	for attempts := 0; attempts < 20 && !tt.Focused("快捷键录制区域"); attempts++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("快捷键录制区域") || v.capture {
		t.Fatal("Tab must focus the field without starting capture")
	}
	before := slices.Clone(v.editKeys)
	tt.Key(0, ui.KeyK)
	assertRecording(t, v, before...)
	tt.Key(0, ui.KeyEnter)
	if !v.capture || !tt.HasText("按下快捷键") {
		t.Fatal("keyboard activation did not arm the field")
	}
	tt.Key(ui.Ctrl|ui.Shift, ui.KeyEscape)
	assertRecording(t, v, "Control", "Shift", "Escape")
	click(t, tt, "取消")
	if m.state.Recording || !slices.Equal(m.state.Settings.Back.Keys, before) {
		t.Fatal("Cancel saved the captured draft or left mappings paused")
	}
}

func TestNativeRecordingFieldSnapshots(t *testing.T) {
	directory := os.Getenv("MOUSE_MAPPER_UI_SNAPSHOTS")
	if directory == "" {
		t.Skip("set MOUSE_MAPPER_UI_SNAPSHOTS to write native UI snapshots")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"windows", "darwin"} {
		m := newUIMapper()
		m.state.Status.Platform = platform
		m.state.InputMethods = uiInputMethods()
		v := testView(m)
		tt := ui.NewTester(v.main, 880, 640)
		click(t, tt, "编辑前进键")
		writeSnapshot(t, tt, filepath.Join(directory, platform+"-recorder-idle.png"))
		click(t, tt, "快捷键录制区域")
		writeSnapshot(t, tt, filepath.Join(directory, platform+"-recorder-armed.png"))
		tt.Key(0, ui.KeyEscape)
		writeSnapshot(t, tt, filepath.Join(directory, platform+"-recorder-escape.png"))
		click(t, tt, "长按快捷键录制区域")
		tt.HoldModifiers(ui.Ctrl | ui.Shift | ui.Super)
		tt.HoldModifiers(0)
		writeSnapshot(t, tt, filepath.Join(directory, platform+"-recorder-long-modifiers.png"))
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
		{"other-row", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "长按快捷键录制区域")
			click(t, tt, "快捷键录制区域")
		}},
		{"clear", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "清空单击快捷键")
			click(t, tt, "快捷键录制区域")
		}},
		{"editor", func(t *testing.T, tt *ui.Tester) {
			click(t, tt, "取消")
			click(t, tt, "编辑后退键")
			click(t, tt, "快捷键录制区域")
		}},
		{"window-focus", func(t *testing.T, tt *ui.Tester) {
			tt.SetFocused(false)
			tt.SetFocused(true)
			click(t, tt, "快捷键录制区域")
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
