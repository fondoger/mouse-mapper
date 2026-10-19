package desktop

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/input"
)

var modifierNames = []string{"Control", "Alt", "Shift", "Meta"}

func keyLabel(code, platform string) string {
	if platform == "darwin" {
		if symbol := map[string]string{"Control": "⌃", "Alt": "⌥", "Shift": "⇧", "Meta": "⌘"}[code]; symbol != "" {
			return symbol
		}
	}
	if symbol := map[string]string{
		"Control": "Ctrl", "Alt": "Alt", "Shift": "Shift", "Meta": "Win",
		"Enter": "↵", "Escape": "Esc", "Backspace": "⌫", "Tab": "Tab",
		"Space": "Space", "Delete": "Del", "Insert": "Ins", "PageUp": "PgUp", "PageDown": "PgDn",
		"ArrowUp": "↑", "ArrowDown": "↓", "ArrowLeft": "←", "ArrowRight": "→",
		"Minus": "−", "Equal": "=", "BracketLeft": "[", "BracketRight": "]", "Backslash": "\\",
		"Semicolon": ";", "Quote": "'", "Backquote": "`", "Comma": ",", "Period": ".", "Slash": "/",
		"CapsLock": "Caps", "PrintScreen": "PrtSc", "ScrollLock": "ScrLk", "Pause": "Pause",
		"NumpadAdd": "Num +", "NumpadSubtract": "Num −", "NumpadMultiply": "Num ×",
		"NumpadDivide": "Num ÷", "NumpadDecimal": "Num .", "NumpadEnter": "Num ↵", "IntlBackslash": "Intl \\",
	}[code]; symbol != "" {
		return symbol
	}
	if strings.HasPrefix(code, "Key") {
		return strings.TrimPrefix(code, "Key")
	}
	if strings.HasPrefix(code, "Digit") {
		return strings.TrimPrefix(code, "Digit")
	}
	if strings.HasPrefix(code, "Numpad") {
		return "Num " + strings.TrimPrefix(code, "Numpad")
	}
	return code
}

func describeKeys(keys []string, platform string) string {
	labels := make([]string, len(keys))
	for i, key := range keys {
		labels[i] = keyLabel(key, platform)
	}
	return strings.Join(labels, " + ")
}

func modifierKeys(mods ui.Modifiers) []string {
	var keys []string
	for _, item := range []struct {
		mask ui.Modifiers
		code string
	}{{ui.Ctrl, "Control"}, {ui.Alt, "Alt"}, {ui.Shift, "Shift"}, {ui.Super, "Meta"}} {
		if mods&item.mask != 0 {
			keys = append(keys, item.code)
		}
	}
	return keys
}

type shortcutRecorder struct {
	mods     ui.Modifiers
	held     map[ui.Key]bool
	ordinary bool
}

func (r *shortcutRecorder) reset() {
	*r = shortcutRecorder{}
}

func (r *shortcutRecorder) modifiers(mods ui.Modifiers) []string {
	added := mods &^ r.mods
	r.mods = mods
	r.endGesture()
	// Releases preserve the chord. New presses use the current simultaneous
	// set, never a union of modifiers that may not have overlapped.
	if added == 0 || r.ordinary {
		return nil
	}
	return modifierKeys(mods)
}

func recordingKey(event ui.InputEvent) ui.Key {
	if event.PhysicalKey != ui.KeyUnknown {
		return event.PhysicalKey
	}
	return event.Key
}

func (r *shortcutRecorder) press(event ui.InputEvent) {
	if r.held == nil {
		r.held = make(map[ui.Key]bool)
	}
	r.held[recordingKey(event)] = true
	r.mods, r.ordinary = event.Mods, true
}

func (r *shortcutRecorder) release(event ui.InputEvent) {
	delete(r.held, recordingKey(event))
	r.mods = event.Mods
	r.endGesture()
}

func (r *shortcutRecorder) endGesture() {
	// An ordinary chord survives modifier changes until every key is up.
	if r.mods == 0 && len(r.held) == 0 {
		r.ordinary = false
	}
}

func physicalCode(key ui.Key) string {
	switch {
	case key >= ui.KeyA && key <= ui.KeyZ:
		return "Key" + string(rune('A'+key-ui.KeyA))
	case key >= ui.Key0 && key <= ui.Key9:
		return "Digit" + string(rune('0'+key-ui.Key0))
	case key >= ui.KeyF1 && key <= ui.KeyF12:
		return fmt.Sprintf("F%d", key-ui.KeyF1+1)
	}
	return map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyEscape: "Escape", ui.KeyBackspace: "Backspace", ui.KeyTab: "Tab",
		ui.KeySpace: "Space", ui.KeyDelete: "Delete", ui.KeyInsert: "Insert", ui.KeyHome: "Home", ui.KeyEnd: "End",
		ui.KeyPageUp: "PageUp", ui.KeyPageDown: "PageDown", ui.KeyLeft: "ArrowLeft", ui.KeyRight: "ArrowRight",
		ui.KeyUp: "ArrowUp", ui.KeyDown: "ArrowDown", ui.KeyMinus: "Minus", ui.KeyEqual: "Equal",
		ui.KeyComma: "Comma", ui.KeyPeriod: "Period", ui.KeySlash: "Slash", ui.KeySemicolon: "Semicolon",
		ui.KeyQuote: "Quote", ui.KeyBracketLeft: "BracketLeft", ui.KeyBracketRight: "BracketRight",
		ui.KeyBackslash: "Backslash", ui.KeyBackquote: "Backquote",
	}[key]
}

func recordedKeys(event ui.InputEvent) ([]string, error) {
	code := physicalCode(recordingKey(event))
	if code == "" {
		return nil, fmt.Errorf("这个按键暂不能直接录制，请使用手动选择")
	}
	return input.NormalizeKeys(append(modifierKeys(event.Mods), code))
}

type shortcutPreset struct {
	name string
	keys []string
}

func shortcutPresets(platform string) []shortcutPreset {
	primary := "Control"
	if platform == "darwin" {
		primary = "Meta"
	}
	return []shortcutPreset{
		{"复制", []string{primary, "KeyC"}}, {"粘贴", []string{primary, "KeyV"}},
		{"撤销", []string{primary, "KeyZ"}}, {"全选", []string{primary, "KeyA"}},
		{"查找", []string{primary, "KeyF"}}, {"切换标签", []string{"Control", "Tab"}}, {"空格", []string{"Space"}},
	}
}

func shortcutName(keys []string, platform string) string {
	for _, preset := range shortcutPresets(platform) {
		if slices.Equal(keys, preset.keys) {
			return preset.name
		}
	}
	if len(keys) == 1 {
		return "单键映射"
	}
	return "自定义组合"
}
