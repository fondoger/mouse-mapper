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

func (v *nativeView) inputMethodOptions() []inputmethod.Method {
	options := []inputmethod.Method{{Name: "不切换（保持当前输入法）"}}
	options = append(options, v.state.InputMethods...)
	if v.editInputMethod != "" && !slices.ContainsFunc(options, func(method inputmethod.Method) bool { return method.ID == v.editInputMethod }) {
		options = append(options, inputmethod.Method{
			ID: v.editInputMethod, Name: v.inputMethodName(v.editInputMethod, v.editInputMethodName) + "（未检测到）",
		})
	}
	return options
}

func (v *nativeView) inputMethodChoice(c *ui.Context) {
	ui.Column(c).Gap(7).Padding(12, 14).Radius(11).Background(canvas).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, "触发前切换到的输入法").FontSize(11).FontWeight(600).Grow(1)
			ui.Text(c, "可选").FontSize(10).TextColor(muted)
			if provider, ok := v.mapper.(inputMethodProvider); ok {
				if ui.ButtonBase(c.Key("refresh")).Label("刷新输入法列表").Padding(3, 5).TextColor(blue).Disabled(v.busy).Children(func() {
					ui.Text(c, "刷新").FontSize(10)
				}).Clicked() {
					v.stopCapture()
					v.perform(func() (State, error) { return provider.RefreshInputMethods(), nil }, nil)
				}
			}
		})
		options := v.inputMethodOptions()
		selected := options[0].Name
		for _, method := range options {
			if method.ID == v.editInputMethod {
				selected = method.Name
			}
		}
		selector := ui.SelectBase(c.Key("select"), &v.editInputMethod)
		selector.Trigger.Label("选择触发前的输入法").FillWidth().MinWidth(0).Gap(8).Padding(8, 11).Radius(7).Border(1, line).Background(white).Disabled(v.busy).Children(func() {
			ui.Text(c, selected).FontSize(11).Grow(1).MinWidth(0).SingleLine().Tooltip(selected)
			ui.Icon(c, iconChevron).FontSize(12).TextColor(muted)
		})
		selector.Popup(func(panel ui.Element) {
			panel.Width(selector.Trigger.Bounds().W).Padding(5).Gap(2).MaxHeight(230).Background(white).Border(1, line).Radius(9)
			ui.Scroll(c).MaxHeight(220).FillWidth().Children(func() {
				for _, method := range options {
					item := selector.Item(method.ID).Label(method.Name).FillWidth().MinWidth(0).Padding(8, 11).Radius(5)
					if item.Highlighted() || method.ID == v.editInputMethod {
						item.Background(blueWash).TextColor(blue)
					}
					item.Children(func() { ui.Text(c, method.Name).FontSize(11).SingleLine().Tooltip(method.Name) })
				}
			})
		})
		if selector.Trigger.Changed() {
			v.stopCapture()
			v.editInputMethodName = ""
			for _, method := range v.state.InputMethods {
				if method.ID == v.editInputMethod {
					v.editInputMethodName = method.Name
				}
			}
		}
		message, color := "保持当前输入法，直接发送快捷键。选择目标后，已选中时不会重复切换。", muted
		if v.state.InputMethodsError != "" {
			message, color = "读取输入法失败，可重试或保持不切换。"+v.state.InputMethodsError, ui.Hex("#bf4b46")
		} else if len(v.state.InputMethods) == 0 {
			message = "未检测到可用输入法。请先在系统中启用，再点击刷新。"
		} else if v.editInputMethod != "" {
			message = "已是目标则不切换；否则先切换再触发，不轮换其他输入法。切换失败时不发送按键。"
		}
		ui.Text(c, message).FontSize(10).TextColor(color).MaxLines(3).Tooltip(message).Selectable()
	})
}
