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
	return input.Binding{Enabled: true, Keys: []string{"Control", "KeyC"}, InputMethod: "test:wechat", InputMethodName: "微信输入法",
		LongPress: &input.LongPressBinding{Keys: []string{"Alt", "Space"}, InputMethod: "test:doubao", InputMethodName: "豆包输入法", ThresholdMS: 650}}
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
			if v.longPressConfigured() || tt.HasText("手动选择") || tt.HasText("或从常用操作开始") {
				t.Fatal("new editor must have an empty optional long row and no old modes")
			}
			click(t, tt, "快捷键录制区域")
			tt.Key(0, ui.KeyEnter)
			click(t, tt, "选择触发前的输入法")
			click(t, tt, "微信输入法")
			click(t, tt, "长按快捷键录制区域")
			tt.Key(0, ui.KeyEscape)
			click(t, tt, "选择长按触发前的输入法")
			click(t, tt, "豆包输入法")
			changeThreshold(t, tt, "725")
			click(t, tt, "保持按下直到侧键松开")
			if !reflect.DeepEqual(m.state.Settings, before) {
				t.Fatal("editing saved a partial draft")
			}
			click(t, tt, "保存快捷键")
			binding := m.state.Settings.Back
			if button == input.Forward {
				binding = m.state.Settings.Forward
			}
			if !slices.Equal(binding.Keys, []string{"Enter"}) || binding.InputMethod != "test:wechat" || binding.LongPress == nil ||
				!slices.Equal(binding.LongPress.Keys, []string{"Escape"}) || binding.LongPress.InputMethod != "test:doubao" ||
				binding.LongPress.ThresholdMS != 725 || !binding.LongPress.Hold || v.editing || m.state.Recording {
				t.Fatalf("wrong actions saved: %+v / %+v", binding, binding.LongPress)
			}
			click(t, tt, label)
			if !v.editDraft.hold || !v.longPressConfigured() {
				t.Fatal("reopening lost the hold option")
			}
		})
	}
}

func TestNativeLongPressEmptyAndClearedRowsDisableLongAction(t *testing.T) {
	for _, existing := range []bool{false, true} {
		m := newUIMapper()
		if existing {
			m.state.Settings.Back = longPressFixture()
		}
		short := slices.Clone(m.state.Settings.Back.Keys)
		v := testView(m)
		tt := ui.NewTester(v.main, 880, 640)
		click(t, tt, "编辑后退键")
		if existing {
			click(t, tt, "清空长按快捷键")
		}
		v.editDraft.threshold = "invalid but disabled"
		click(t, tt, "保存快捷键")
		if v.editing || m.state.Settings.Back.LongPress != nil || !slices.Equal(m.state.Settings.Back.Keys, short) {
			t.Fatal("empty long row must save as no long mapping without changing the short action")
		}
	}
}

func TestNativeLongPressCancelAndFailedSavePreserveDrafts(t *testing.T) {
	m := newUIMapper()
	m.state.Settings.Back = longPressFixture()
	before := config.Clone(m.state.Settings)
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	click(t, tt, "快捷键录制区域")
	tt.Key(ui.Ctrl, ui.KeyX)
	click(t, tt, "长按快捷键录制区域")
	tt.Key(ui.Shift, ui.KeyL)
	click(t, tt, "保持按下直到侧键松开")
	m.saveErr = errors.New("mock settings write failed")
	click(t, tt, "保存快捷键")
	if !v.editing || !reflect.DeepEqual(m.state.Settings, before) || !v.editDraft.hold || v.editError != m.saveErr.Error() {
		t.Fatal("failed Save changed saved settings or discarded the draft")
	}
	if !slices.Equal(v.editorAction(0).keys, []string{"Control", "KeyX"}) || !slices.Equal(v.editorAction(1).keys, []string{"Shift", "KeyL"}) {
		t.Fatal("rows overwrote each other's draft")
	}
	click(t, tt, "取消")
	if !reflect.DeepEqual(m.state.Settings, before) || m.state.Recording {
		t.Fatal("Cancel persisted changes")
	}
	m.saveErr = nil
	click(t, tt, "编辑后退键")
	if v.editDraft.hold {
		t.Fatal("Cancel retained unsaved hold mode")
	}
}

func TestNativeLongPressThresholdValidation(t *testing.T) {
	for _, value := range []string{"", "0", "99", "5001", "-500", "500.5", "1e3", "abc", "99999999999999999999"} {
		if _, err := parseLongPressThreshold(value); err == nil {
			t.Errorf("accepted invalid threshold %q", value)
		}
	}
	for _, value := range []string{"100", "500", "725", "5000", " 800 "} {
		if _, err := parseLongPressThreshold(value); err != nil {
			t.Errorf("rejected threshold %q: %v", value, err)
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
		v.saveEditor()
		if !v.editing || !reflect.DeepEqual(m.state.Settings, before) || !strings.Contains(v.editError, "100-5000") {
			t.Fatalf("invalid threshold %q bypassed validation", value)
		}
	}
	changeThreshold(t, tt, "875")
	click(t, tt, "保存快捷键")
	if v.editing || m.state.Settings.Back.LongPress.ThresholdMS != 875 {
		t.Fatal("corrected threshold did not save")
	}
}

func TestNativeLongPressRecorderKeepsModifierChordsIndependent(t *testing.T) {
	for _, platform := range []string{"windows", "darwin"} {
		m, v, tt := recordingEditor(t, platform)
		tt.HoldModifiers(ui.Ctrl | ui.Shift | ui.Super)
		tt.HoldModifiers(ui.Super)
		tt.HoldModifiers(0)
		click(t, tt, "长按快捷键录制区域")
		tt.HoldModifiers(ui.Alt | ui.Super)
		tt.HoldModifiers(ui.Super)
		tt.HoldModifiers(0)
		assertRecording(t, v, "Alt", "Meta")
		click(t, tt, "快捷键录制区域")
		assertRecording(t, v, "Control", "Shift", "Meta")
		click(t, tt, "保存快捷键")
		if !slices.Equal(m.state.Settings.Back.Keys, []string{"Control", "Shift", "Meta"}) ||
			!slices.Equal(m.state.Settings.Back.LongPress.Keys, []string{"Alt", "Meta"}) {
			t.Fatal("rows lost modifier-only chords")
		}
	}
}

func TestNativeLongPressEditorActionsStayVisibleAtMinimumSize(t *testing.T) {
	m := newUIMapper()
	m.state.Settings.Back = longPressFixture()
	m.state.InputMethodsError = strings.Repeat("Input source unavailable. ", 10)
	v := testView(m)
	tt := ui.NewTester(v.main, 880, 640)
	click(t, tt, "编辑后退键")
	for _, label := range []string{"保存快捷键", "取消", "长按阈值（毫秒）", "快捷键录制区域", "长按快捷键录制区域"} {
		bounds, found := tt.Find(label)
		if !found || bounds.W < 20 || bounds.H < 16 || bounds.Y+bounds.H > 616 {
			t.Fatalf("%s clipped: %+v", label, bounds)
		}
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
	m.state.Settings.Forward.Keys = []string{"Enter"}
	m.state.Settings.Forward.LongPress = &input.LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 500}
	v := testView(m)
	tt := ui.NewTester(v.main, 880, 640)
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-mappings-minimum.png"))
	click(t, tt, "编辑前进键")
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-editor-rows.png"))
	click(t, tt, "保持按下直到侧键松开")
	writeSnapshot(t, tt, filepath.Join(directory, "longpress-editor-hold.png"))
}
