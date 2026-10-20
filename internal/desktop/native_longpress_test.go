package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/config"
	"github.com/fondoger/mouse-mapper/internal/input"
)

func changeThreshold(t *testing.T, tt *ui.Tester, value string) {
	t.Helper()
	click(t, tt, "长按阈值（毫秒）")
	modifier := ui.Ctrl
	if runtime.GOOS == "darwin" {
		modifier = ui.Super
	}
	tt.Key(modifier, ui.KeyA)
	tt.Type(value)
}

func longPressFixture() input.Binding {
	return input.Binding{
		Enabled: true, Keys: []string{"Control", "KeyC"}, InputMethod: "test:wechat", InputMethodName: "微信输入法",
		LongPress: &input.LongPressBinding{
			Keys: []string{"Alt", "Space"}, InputMethod: "test:doubao", InputMethodName: "豆包输入法", ThresholdMS: 650,
		},
	}
}

func TestNativeLongPressRecordsBothActionsAndInputMethods(t *testing.T) {
	for _, button := range []string{input.Back, input.Forward} {
		t.Run(button, func(t *testing.T) {
			m := newUIMapper()
			m.state.InputMethods = uiInputMethods()
			before := config.Clone(m.state.Settings)
			v := testView(m)
			tt := ui.NewTester(v.main, 1080, 760)
			label := "编辑后退键"
			if button == input.Forward {
				label = "编辑前进键"
			}
			click(t, tt, label)
			tt.Key(ui.Ctrl|ui.Shift, ui.KeySpace)
			shortKeys := []string{"Control", "Shift", "Space"}
			click(t, tt, "选择触发前的输入法")
			click(t, tt, "微信输入法")
			click(t, tt, "启用长按映射")
			if !v.editDraft.longEnabled || v.editAction != 1 || !tt.Focused("快捷键录制区域") {
				t.Fatal("enabling long press did not open its independent recorder")
			}
			tt.Key(ui.Alt|ui.Shift, ui.KeyV)
			longKeys := []string{"Alt", "Shift", "KeyV"}
			click(t, tt, "选择触发前的输入法")
			click(t, tt, "豆包输入法")
			changeThreshold(t, tt, "725")
			assertRecording(t, v, longKeys...)
			click(t, tt, "单击动作")
			assertRecording(t, v, shortKeys...)
			if v.editInputMethod != "test:wechat" {
				t.Fatal("switching actions lost the short action's input method")
			}
			click(t, tt, "长按动作")
			assertRecording(t, v, longKeys...)
			if v.editInputMethod != "test:doubao" || v.editDraft.threshold != "725" {
				t.Fatal("switching actions lost the long action's preparation or threshold")
			}
			if !reflect.DeepEqual(m.state.Settings, before) {
				t.Fatal("editing persisted a partial action before Save")
			}
			click(t, tt, "保存快捷键")
			binding, other, expectedOther := m.state.Settings.Back, m.state.Settings.Forward, before.Forward
			if button == input.Forward {
				binding, other, expectedOther = m.state.Settings.Forward, m.state.Settings.Back, before.Back
			}
			if !slices.Equal(binding.Keys, shortKeys) || binding.InputMethod != "test:wechat" || binding.InputMethodName != "微信输入法" ||
				binding.LongPress == nil || !slices.Equal(binding.LongPress.Keys, longKeys) || binding.LongPress.InputMethod != "test:doubao" ||
				binding.LongPress.InputMethodName != "豆包输入法" || binding.LongPress.ThresholdMS != 725 {
				t.Fatalf("wrong short/long actions saved: %+v, long=%+v", binding, binding.LongPress)
			}
			if !reflect.DeepEqual(other, expectedOther) || m.state.Recording || v.editing {
				t.Fatal("saving changed the other side button or did not resume mappings")
			}
			if !tt.HasText("长按 · 725 ms") || !tt.HasText("先切换到 微信输入法") || !tt.HasText("先切换到 豆包输入法") {
				t.Fatal("mapping card omitted its two actions, IMEs or threshold")
			}
			v.editKeys[0] = "Meta"
			v.editDraft.actions[0].keys[0] = "Alt"
			if !slices.Equal(binding.Keys, shortKeys) || !slices.Equal(binding.LongPress.Keys, longKeys) {
				t.Fatal("the saved configuration retained mutable UI draft slices")
			}
		})
	}
}

func TestNativeLongPressCancelPreservesBothSavedActions(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	m.state.Settings.Back = longPressFixture()
	before := config.Clone(m.state.Settings)
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	tt.Key(ui.Ctrl, ui.KeyX)
	click(t, tt, "选择触发前的输入法")
	click(t, tt, "豆包输入法")
	click(t, tt, "长按动作")
	tt.Key(ui.Shift, ui.KeyL)
	click(t, tt, "选择触发前的输入法")
	click(t, tt, "微信输入法")
	changeThreshold(t, tt, "950")
	click(t, tt, "启用长按映射")
	assertRecording(t, v, "Control", "KeyX")
	click(t, tt, "启用长按映射")
	assertRecording(t, v, "Shift", "KeyL")
	if v.editInputMethod != "test:wechat" || v.editDraft.threshold != "950" {
		t.Fatal("temporarily disabling long press erased the unsaved long action")
	}
	if !reflect.DeepEqual(m.state.Settings, before) {
		t.Fatal("a draft mutated saved keys, IMEs or threshold before Save")
	}
	click(t, tt, "取消")
	if !reflect.DeepEqual(m.state.Settings, before) || m.state.Recording || v.editing {
		t.Fatal("Cancel persisted a draft or left recording active")
	}
	click(t, tt, "编辑后退键")
	assertRecording(t, v, before.Back.Keys...)
	click(t, tt, "长按动作")
	assertRecording(t, v, before.Back.LongPress.Keys...)
	if v.editInputMethod != before.Back.LongPress.InputMethod || v.editDraft.threshold != "650" {
		t.Fatal("reopened editor retained cancelled long-action changes")
	}
}

func TestNativeLongPressDisablingPreservesShortAction(t *testing.T) {
	for _, button := range []string{input.Back, input.Forward} {
		t.Run(button, func(t *testing.T) {
			m := newUIMapper()
			m.state.InputMethods = uiInputMethods()
			label := "编辑后退键"
			if button == input.Back {
				m.state.Settings.Back = longPressFixture()
			} else {
				label = "编辑前进键"
				m.state.Settings.Forward = longPressFixture()
			}
			v := testView(m)
			tt := ui.NewTester(v.main, 1080, 760)
			click(t, tt, label)
			click(t, tt, "长按动作")
			tt.Key(ui.Shift, ui.KeyZ)
			click(t, tt, "选择触发前的输入法")
			click(t, tt, "不切换（保持当前输入法）")
			click(t, tt, "启用长按映射")
			if v.editDraft.longEnabled || v.editAction != 0 || v.editInputMethod != "test:wechat" {
				t.Fatal("disabling long press did not restore the original short action")
			}
			assertRecording(t, v, "Control", "KeyC")
			click(t, tt, "保存快捷键")
			binding := m.state.Settings.Back
			if button == input.Forward {
				binding = m.state.Settings.Forward
			}
			want := longPressFixture()
			want.LongPress = nil
			if !reflect.DeepEqual(binding, want) {
				t.Fatalf("disabling long press overwrote the short action: %+v", binding)
			}
		})
	}
}

func TestNativeLongPressFailedSaveIsAtomicAndRetainsBothDrafts(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	m.state.Settings.Back = longPressFixture()
	before := config.Clone(m.state.Settings)
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	tt.Key(ui.Ctrl, ui.KeyX)
	click(t, tt, "选择触发前的输入法")
	click(t, tt, "不切换（保持当前输入法）")
	click(t, tt, "长按动作")
	tt.Key(ui.Shift, ui.KeyL)
	changeThreshold(t, tt, "900")
	m.saveErr = errors.New("mock settings write failed")
	click(t, tt, "保存快捷键")
	if !v.editing || !m.state.Recording || !reflect.DeepEqual(m.state.Settings, before) || v.editError != m.saveErr.Error() {
		t.Fatal("failed Save committed part of the binding or dismissed the draft")
	}
	click(t, tt, "单击动作")
	assertRecording(t, v, "Control", "KeyX")
	if v.editInputMethod != "" {
		t.Fatal("failed Save lost the short action's unsaved input-method opt-out")
	}
	click(t, tt, "长按动作")
	assertRecording(t, v, "Shift", "KeyL")
	m.saveErr = nil
	click(t, tt, "保存快捷键")
	binding := m.state.Settings.Back
	if v.editing || m.state.Recording || !slices.Equal(binding.Keys, []string{"Control", "KeyX"}) ||
		binding.InputMethod != "" || binding.InputMethodName != "" || binding.LongPress.ThresholdMS != 900 ||
		!slices.Equal(binding.LongPress.Keys, []string{"Shift", "KeyL"}) || binding.LongPress.InputMethod != "test:doubao" {
		t.Fatalf("retry did not commit both retained drafts: %+v, long=%+v", binding, binding.LongPress)
	}
}

func TestNativeLongPressThresholdValidation(t *testing.T) {
	for _, value := range []string{"", "0", "99", "5001", "-500", "500.5", "1e3", "abc", "99999999999999999999"} {
		if _, err := parseLongPressThreshold(value); err == nil {
			t.Errorf("accepted invalid threshold %q", value)
		}
	}
	for _, test := range []struct {
		value string
		want  int
	}{{"100", 100}, {"500", 500}, {"725", 725}, {"5000", 5000}, {" 800 ", 800}} {
		got, err := parseLongPressThreshold(test.value)
		if err != nil || got != test.want {
			t.Errorf("threshold %q = %d, %v", test.value, got, err)
		}
	}
	m := newUIMapper()
	m.state.Settings.Back = longPressFixture()
	before := config.Clone(m.state.Settings)
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	for _, value := range []string{"0", "99", "5001", "500.5", "abc", ""} {
		changeThreshold(t, tt, value)
		click(t, tt, "保存快捷键")
		if !v.editing || !m.state.Recording || !reflect.DeepEqual(m.state.Settings, before) {
			t.Fatalf("invalid threshold %q saved or dismissed the editor", value)
		}
		v.saveEditor()
		if !strings.Contains(v.editError, "100-5000") || !reflect.DeepEqual(m.state.Settings, before) {
			t.Fatalf("invalid threshold %q was not rejected at the save boundary", value)
		}
	}
	changeThreshold(t, tt, "875")
	assertRecording(t, v, before.Back.Keys...)
	click(t, tt, "保存快捷键")
	if v.editing || m.state.Settings.Back.LongPress.ThresholdMS != 875 {
		t.Fatal("correcting the threshold did not make Save usable again")
	}
}

func TestNativeLongPressRequiresAnIndependentShortcut(t *testing.T) {
	m := newUIMapper()
	before := config.Clone(m.state.Settings)
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	click(t, tt, "启用长按映射")
	if v.editDraft.threshold != "500" || len(v.editKeys) != 0 {
		t.Fatal("new long action should use the default threshold and require explicit keys")
	}
	click(t, tt, "保存快捷键")
	if !v.editing || !reflect.DeepEqual(m.state.Settings, before) {
		t.Fatal("an empty long action was accepted")
	}
	click(t, tt, "单击动作")
	click(t, tt, "保存快捷键")
	if !v.editing || !reflect.DeepEqual(m.state.Settings, before) {
		t.Fatal("switching away from the empty action bypassed validation")
	}
	click(t, tt, "启用长按映射")
	click(t, tt, "保存快捷键")
	if v.editing || !reflect.DeepEqual(m.state.Settings, before) {
		t.Fatal("disabling the empty optional action did not preserve the old mapping")
	}
}

func TestNativeLongPressRecorderKeepsModifierChordsIndependent(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			m, v, tt := recordingEditor(t, platform)
			tt.HoldModifiers(ui.Ctrl | ui.Shift | ui.Super)
			tt.HoldModifiers(ui.Ctrl | ui.Super)
			tt.HoldModifiers(ui.Super)
			tt.HoldModifiers(0)
			click(t, tt, "启用长按映射")
			tt.HoldModifiers(ui.Alt | ui.Super)
			tt.HoldModifiers(ui.Super)
			tt.HoldModifiers(0)
			assertRecording(t, v, "Alt", "Meta")
			click(t, tt, "单击动作")
			assertRecording(t, v, "Control", "Shift", "Meta")
			click(t, tt, "长按动作")
			assertRecording(t, v, "Alt", "Meta")
			click(t, tt, "保存快捷键")
			if !slices.Equal(m.state.Settings.Back.Keys, []string{"Control", "Shift", "Meta"}) ||
				!slices.Equal(m.state.Settings.Back.LongPress.Keys, []string{"Alt", "Meta"}) {
				t.Fatal("action transitions or modifier releases replaced one of the recorded chords")
			}
		})
	}
}

func TestNativeLongPressEditorActionsStayVisibleAtMinimumSize(t *testing.T) {
	for _, mode := range []string{"record", "manual", "record-error", "manual-error"} {
		t.Run(mode, func(t *testing.T) {
			m := newUIMapper()
			m.state.InputMethods = uiInputMethods()
			m.state.Settings.Back = longPressFixture()
			m.state.Settings.Back.LongPress.Keys = []string{"Control", "Alt", "Shift", "Meta", "Space"}
			if strings.Contains(mode, "error") {
				m.state.InputMethodsError = strings.Repeat("Input source unavailable. ", 10)
			}
			v := testView(m)
			tt := ui.NewTester(v.main, 880, 640)
			click(t, tt, "编辑后退键")
			click(t, tt, "长按动作")
			if strings.Contains(mode, "manual") {
				click(t, tt, "手动选择")
			}
			for _, label := range []string{"保存快捷键", "取消", "长按阈值（毫秒）", "单击动作", "长按动作"} {
				bounds, found := tt.Find(label)
				if !found || bounds.W < 20 || bounds.H < 16 || bounds.X < 0 || bounds.X+bounds.W > 880 || bounds.Y+bounds.H > 616 {
					t.Fatalf("%s is clipped at the minimum window size: %+v found=%v", label, bounds, found)
				}
			}
			click(t, tt, "保存快捷键")
			if v.editing || m.state.Recording || m.state.Settings.Back.LongPress == nil {
				t.Fatal("the visible Save action could not persist the long mapping")
			}
		})
	}
}

func TestNativeLongPressManualEscapeDoesNotDismissEditor(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	click(t, tt, "启用长按映射")
	click(t, tt, "手动选择")
	click(t, tt, "选择按键")
	modifier := ui.Ctrl
	if runtime.GOOS == "darwin" {
		modifier = ui.Super
	}
	tt.Key(modifier, ui.KeyA)
	tt.Type("Esc")
	tt.Key(0, ui.KeyEnter)
	assertRecording(t, v, "Escape")
	click(t, tt, "保存快捷键")
	if m.state.Settings.Back.LongPress == nil || !slices.Equal(m.state.Settings.Back.LongPress.Keys, []string{"Escape"}) {
		t.Fatal("the long-action manual picker could not assign Escape")
	}
}

func TestNativeLongPressSnapshots(t *testing.T) {
	directory := os.Getenv("MOUSE_MAPPER_UI_SNAPSHOTS")
	if directory == "" {
		t.Skip("set MOUSE_MAPPER_UI_SNAPSHOTS to write native UI snapshots")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	m.state.Settings.Back = longPressFixture()
	m.state.Settings.Forward.Keys = []string{"Enter"}
	m.state.Settings.Forward.LongPress = &input.LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 500}
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-mappings.png"))
	tt.SetSize(880, 640)
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-mappings-minimum.png"))
	click(t, tt, "编辑前进键")
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-editor-short-minimum.png"))
	click(t, tt, "长按动作")
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-editor-long-minimum.png"))
	click(t, tt, "手动选择")
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-editor-manual-minimum.png"))
}
