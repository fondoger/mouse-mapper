package desktop

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeEditorActionsStayVisibleAtMinimumSize(t *testing.T) {
	for _, mode := range []string{"record", "record-error"} {
		t.Run(mode, func(t *testing.T) {
			m := newUIMapper()
			m.state.InputMethods = uiInputMethods()
			if strings.Contains(mode, "error") {
				m.state.InputMethodsError = strings.Repeat("The input service could not be initialized; ", 8)
			}
			v := testView(m)
			tt := ui.NewTester(v.main, 880, 640)
			click(t, tt, "编辑后退键")
			for _, label := range []string{"保存快捷键", "取消"} {
				bounds, found := tt.Find(label)
				if !found || bounds.W < 20 || bounds.H < 18 || bounds.Y+bounds.H > 616 {
					t.Fatalf("%s is clipped or below the minimum-sized dialog: %+v found=%v", label, bounds, found)
				}
			}
			click(t, tt, "保存快捷键")
			if v.editing || m.state.Recording {
				t.Fatal("the visible Save action was not usable or did not resume mapping")
			}
		})
	}
}

func TestNativeLongInputMethodNameDoesNotHideEditorOrPopup(t *testing.T) {
	for range 1 {
		m := newUIMapper()
		m.state.InputMethods = uiInputMethods()
		m.state.InputMethods[0].Name = "International Input Method " + strings.Repeat("ProfessionalEdition", 9)
		m.state.Settings.Back.InputMethod = m.state.InputMethods[0].ID
		m.state.Settings.Back.InputMethodName = m.state.InputMethods[0].Name
		v := testView(m)
		tt := ui.NewTester(v.main, 880, 640)
		click(t, tt, "编辑后退键")
		if !v.editing {
			t.Fatal("the long mapping-card label pushed the Edit button out of reach")
		}
		bounds, found := tt.Find("选择触发前的输入法")
		if !found || bounds.W > 480 || bounds.X < 0 || bounds.X+bounds.W > 880 {
			t.Fatalf("long selected name expanded the input-method selector: %+v", bounds)
		}
		click(t, tt, "选择触发前的输入法")
		bounds, found = tt.Find("豆包输入法")
		if !found || bounds.W < 20 || bounds.H < 14 || bounds.X < 0 || bounds.X+bounds.W > 880 || bounds.Y+bounds.H > 640 {
			t.Fatalf("long option expanded the popup or hid its other choices: %+v", bounds)
		}
		click(t, tt, "豆包输入法")
		click(t, tt, "保存快捷键")
		if m.state.Settings.Back.InputMethod != "test:doubao" || v.editing {
			t.Fatal("the constrained picker or fixed Save action was not usable")
		}
	}
}
