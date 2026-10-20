package desktop

import (
	"errors"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

func (m *uiMapper) RefreshInputMethods() State { return m.GetState() }

func uiInputMethods() []inputmethod.Method {
	return []inputmethod.Method{
		{ID: "test:wechat", Name: "微信输入法"},
		{ID: "test:doubao", Name: "豆包输入法"},
	}
}

func TestNativeEditorSavesInputMethodBeforeShortcut(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	tt.Key(ui.Ctrl|ui.Shift, ui.KeySpace)
	click(t, tt, "选择触发前的输入法")
	click(t, tt, "微信输入法")
	if !tt.HasText("已是目标则不切换") || !tt.HasText("切换失败时不发送按键") {
		t.Fatal("the editor did not explain the exact-current skip and fail-closed behavior")
	}
	click(t, tt, "保存快捷键")
	binding := m.state.Settings.Back
	if binding.InputMethod != "test:wechat" || binding.InputMethodName != "微信输入法" || !slices.Equal(binding.Keys, []string{"Control", "Shift", "Space"}) {
		t.Fatalf("wrong shortcut/preparation saved: %+v", binding)
	}
	if m.state.Settings.Forward.InputMethod != "" || m.state.Recording || !tt.HasText("先切换到 微信输入法") {
		t.Fatal("selection changed another binding, left mapping paused, or missed the card summary")
	}
}

func TestNativeEditorPreservesInputMethodWhenOnlyKeysChange(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	m.state.Settings.Back.InputMethod, m.state.Settings.Back.InputMethodName = "test:wechat", "微信输入法"
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	tt.Key(ui.Ctrl, ui.KeyL)
	click(t, tt, "保存快捷键")
	if m.state.Settings.Back.InputMethod != "test:wechat" || !slices.Equal(m.state.Settings.Back.Keys, []string{"Control", "KeyL"}) {
		t.Fatal("editing only the shortcut lost its saved input-method target")
	}
}

func TestNativeInputMethodKeyboardSelectionDoesNotRecordNavigation(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	tt.Key(ui.Ctrl, ui.KeyK)
	click(t, tt, "选择触发前的输入法")
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	click(t, tt, "保存快捷键")
	if m.state.Settings.Back.InputMethod != "test:wechat" || !slices.Equal(m.state.Settings.Back.Keys, []string{"Control", "KeyK"}) {
		t.Fatalf("input-method navigation overwrote the recorded chord: %+v", m.state.Settings.Back)
	}
}

func TestNativeEditorCanDisableInputMethodSwitch(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	m.state.Settings.Forward.InputMethod, m.state.Settings.Forward.InputMethodName = "test:doubao", "豆包输入法"
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑前进键")
	click(t, tt, "选择触发前的输入法")
	click(t, tt, "不切换（保持当前输入法）")
	if !tt.HasText("保持当前输入法，直接发送快捷键") {
		t.Fatal("opting out still implied an input-method switch")
	}
	click(t, tt, "保存快捷键")
	if m.state.Settings.Forward.InputMethod != "" || m.state.Settings.Forward.InputMethodName != "" {
		t.Fatal("opting out did not clear the target and its display name")
	}
}

func TestNativeMissingInputMethodDoesNotLoseSavedSelection(t *testing.T) {
	m := newUIMapper()
	m.state.Settings.Back.InputMethod, m.state.Settings.Back.InputMethodName = "test:uninstalled", "Previous IME"
	m.state.InputMethodsError = "temporarily unavailable"
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	if !tt.HasText("Previous IME（未检测到）") {
		t.Fatal("missing saved input method was silently replaced with opt-out")
	}
	tt.Key(ui.Ctrl, ui.KeyL)
	click(t, tt, "保存快捷键")
	if m.state.Settings.Back.InputMethod != "test:uninstalled" || m.state.Settings.Back.InputMethodName != "Previous IME" {
		t.Fatal("a discovery error erased a saved input-method selection")
	}
}

func TestNativeInputMethodCancelAndFailedSavePreserveSettings(t *testing.T) {
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	click(t, tt, "选择触发前的输入法")
	click(t, tt, "微信输入法")
	m.saveErr = errors.New("test write failed")
	click(t, tt, "保存快捷键")
	if m.state.Settings.Back.InputMethod != "" || !m.state.Recording {
		t.Fatal("failed save changed the old binding or dismissed the editor")
	}
	click(t, tt, "取消")
	if m.state.Settings.Back.InputMethod != "" || m.state.Recording {
		t.Fatal("cancellation persisted the input-method choice or left mapping paused")
	}
}
