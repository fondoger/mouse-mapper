package desktop

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/input"
)

type nativeActionDraft struct {
	keys        []string
	inputMethod string
	methodName  string
}

type nativeBindingDraft struct {
	actions     [2]nativeActionDraft
	longEnabled bool
	threshold   string
}

func newNativeBindingDraft(binding input.Binding) nativeBindingDraft {
	draft := nativeBindingDraft{threshold: strconv.Itoa(input.DefaultLongPressThresholdMS)}
	draft.actions[0] = nativeActionDraft{slices.Clone(binding.Keys), binding.InputMethod, binding.InputMethodName}
	if long := binding.LongPress; long != nil {
		draft.longEnabled = true
		draft.actions[1] = nativeActionDraft{slices.Clone(long.Keys), long.InputMethod, long.InputMethodName}
		if long.ThresholdMS != 0 {
			draft.threshold = strconv.Itoa(long.ThresholdMS)
		}
	}
	return draft
}

func (v *nativeView) currentEditorAction() nativeActionDraft {
	name := v.editInputMethodName
	if v.editInputMethod == "" {
		name = ""
	}
	return nativeActionDraft{slices.Clone(v.editKeys), v.editInputMethod, name}
}

func (v *nativeView) loadEditorAction() {
	action := v.editDraft.actions[v.editAction]
	v.editKeys = slices.Clone(action.keys)
	v.editInputMethod, v.editInputMethodName = action.inputMethod, action.methodName
	v.syncManualQuery()
}

func (v *nativeView) switchEditorAction(action int) {
	if action == v.editAction || action < 0 || action > 1 || action == 1 && !v.editDraft.longEnabled {
		return
	}
	// The visible recorder is reused, but each action owns an independent copy.
	// Switching tabs must not share key slices with the saved configuration.
	v.editDraft.actions[v.editAction] = v.currentEditorAction()
	v.editAction = action
	v.loadEditorAction()
	v.editError, v.captured = "", false
	v.stopCapture()
}

func (v *nativeView) setLongPressEnabled(on bool) {
	if on == v.editDraft.longEnabled {
		return
	}
	if on {
		v.editDraft.longEnabled = true
		v.switchEditorAction(1)
	} else {
		v.switchEditorAction(0)
		v.editDraft.longEnabled = false
	}
}

func parseLongPressThreshold(value string) (int, error) {
	threshold, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || threshold < input.MinLongPressThresholdMS || threshold > input.MaxLongPressThresholdMS {
		return 0, fmt.Errorf("长按阈值请输入 %d-%d 之间的整数（毫秒）。", input.MinLongPressThresholdMS, input.MaxLongPressThresholdMS)
	}
	return threshold, nil
}

func (v *nativeView) editorBinding() (input.Binding, error) {
	actions := v.editDraft.actions
	actions[v.editAction] = v.currentEditorAction()
	short := actions[0]
	binding := input.Binding{Enabled: true, Keys: slices.Clone(short.keys), InputMethod: short.inputMethod, InputMethodName: short.methodName}
	if v.editDraft.longEnabled {
		threshold, err := parseLongPressThreshold(v.editDraft.threshold)
		if err != nil {
			return input.Binding{}, err
		}
		long := actions[1]
		binding.LongPress = &input.LongPressBinding{
			Keys: slices.Clone(long.keys), InputMethod: long.inputMethod, InputMethodName: long.methodName, ThresholdMS: threshold,
		}
	}
	return input.NormalizeBinding(binding)
}

func (v *nativeView) longPressEditor(c *ui.Context) {
	ui.Row(c).Gap(10).Children(func() {
		action := v.editAction
		if ui.Segmented(c.Key("action"), &action, "单击动作", "长按动作").Label("编辑单击或长按动作").Grow(1).Disabled(v.busy).Changed() {
			v.switchEditorAction(action)
		}
		ui.Text(c, "长按阈值").FontSize(10).TextColor(muted)
		field := ui.TextInput(c.Key("threshold"), &v.editDraft.threshold).Label("长按阈值（毫秒）").
			Width(66).FontSize(11).Padding(7, 8).Placeholder("500").Tooltip("100-5000 毫秒").Disabled(v.busy)
		if field.Focused() {
			v.stopCapture()
		}
		if field.Changed() {
			v.editError = ""
		}
		if _, err := parseLongPressThreshold(v.editDraft.threshold); err != nil {
			field.BorderColor(ui.Hex("#bf4b46"))
		}
		ui.Text(c, "ms").FontSize(10).TextColor(muted)
	})
}

func (v *nativeView) mappingActionSummary(c *ui.Context, label string, keys []string, method, methodName string) {
	ui.Column(c).Gap(4).FillWidth().MinWidth(0).Children(func() {
		ui.Row(c).Gap(10).FillWidth().MinWidth(0).Children(func() {
			ui.Text(c, label).Width(100).FontSize(10).TextColor(muted)
			chord := describeKeys(keys, v.state.Status.Platform)
			ui.Text(c, chord).FontSize(14).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Tooltip(chord)
		})
		if method != "" {
			text := "先切换到 " + v.inputMethodName(method, methodName)
			ui.Text(c, text).FontSize(10).TextColor(blue).SingleLine().MinWidth(0).FillWidth().Tooltip(text)
		}
	})
}
