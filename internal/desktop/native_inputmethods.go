package desktop

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type inputMethodProvider interface {
	RefreshInputMethods() State
}

func (v *nativeView) inputMethodName(id, fallback string) string {
	for _, method := range v.state.InputMethods {
		if method.ID == id {
			return method.Name
		}
	}
	if fallback != "" {
		return fallback
	}
	return "已保存的输入法"
}

func (v *nativeView) inputMethodOptionsFor(action nativeActionDraft) []inputmethod.Method {
	options := []inputmethod.Method{{Name: "不切换（保持当前输入法）"}}
	options = append(options, v.state.InputMethods...)
	if action.inputMethod != "" && !slices.ContainsFunc(options, func(method inputmethod.Method) bool { return method.ID == action.inputMethod }) {
		options = append(options, inputmethod.Method{
			ID: action.inputMethod, Name: v.inputMethodName(action.inputMethod, action.methodName) + "（未检测到）",
		})
	}
	return options
}

func (v *nativeView) actionInputMethodChoice(c *ui.Context, action int) {
	draft := v.editorAction(action)
	options := v.inputMethodOptionsFor(draft)
	selected := options[0].Name
	for _, method := range options {
		if method.ID == draft.inputMethod {
			selected = method.Name
		}
	}
	label := "选择触发前的输入法"
	if action == 1 {
		label = "选择长按触发前的输入法"
	}
	selector := ui.SelectBase(c.Key("select"), &draft.inputMethod)
	selector.Trigger.Label(label).FillWidth().MinWidth(0).Height(42).Gap(8).Padding(8, 10).Radius(8).
		Border(1, line).Background(white).Disabled(v.busy).Children(func() {
		ui.Text(c, selected).FontSize(11).Grow(1).MinWidth(0).SingleLine().Tooltip(selected)
		ui.Icon(c, iconChevron).FontSize(12).TextColor(muted)
	})
	selector.Popup(func(panel ui.Element) {
		panel.Width(selector.Trigger.Bounds().W).Padding(5).Gap(2).MaxHeight(230).Background(white).Border(1, line).Radius(9)
		ui.Scroll(c).MaxHeight(220).FillWidth().Children(func() {
			for _, method := range options {
				item := selector.Item(method.ID).Label(method.Name).FillWidth().MinWidth(0).Padding(8, 11).Radius(5)
				if item.Highlighted() || method.ID == draft.inputMethod {
					item.Background(blueWash).TextColor(blue)
				}
				item.Children(func() { ui.Text(c, method.Name).FontSize(11).SingleLine().Tooltip(method.Name) })
			}
		})
	})
	if selector.Trigger.Changed() {
		v.stopCapture()
		draft.methodName = ""
		for _, method := range v.state.InputMethods {
			if method.ID == draft.inputMethod {
				draft.methodName = method.Name
			}
		}
		if action == v.editAction {
			v.editInputMethod, v.editInputMethodName = draft.inputMethod, draft.methodName
		} else {
			v.editDraft.actions[action] = draft
		}
	}
}

func (v *nativeView) inputMethodNotice(c *ui.Context) {
	ui.Column(c).Gap(6).FillWidth().Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			message := "保持当前输入法，直接发送快捷键。"
			if v.editorAction(0).inputMethod != "" || v.editorAction(1).inputMethod != "" {
				message = "已是目标则不切换；切换失败时不发送按键。"
			}
			ui.Text(c, message).FontSize(10).TextColor(muted).Grow(1).MinWidth(0)
			if provider, ok := v.mapper.(inputMethodProvider); ok {
				if ui.Button(c.Key("refresh"), "刷新输入法").Label("刷新输入法列表").FontSize(10).Disabled(v.busy).Clicked() {
					v.stopCapture()
					v.perform(func() (State, error) { return provider.RefreshInputMethods(), nil }, nil)
				}
			}
		})
		if v.state.InputMethodsError != "" {
			message := "读取输入法失败，可重试或保持不切换。" + v.state.InputMethodsError
			ui.Text(c, message).FontSize(10).TextColor(ui.Hex("#bf4b46")).MaxLines(2).Tooltip(message)
		}
	})
}
