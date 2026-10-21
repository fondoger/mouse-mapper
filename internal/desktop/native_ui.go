package desktop

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/input"
)

const repositoryURL = "https://github.com/fondoger/mouse-mapper"

var (
	ink      = ui.Hex("#242932")
	muted    = ui.Hex("#818996")
	line     = ui.Hex("#e5e9ef")
	canvas   = ui.Hex("#f6f7f9")
	blue     = ui.Hex("#357be6")
	blueWash = ui.Hex("#edf4ff")
	green    = ui.Hex("#259267")
	white    = ui.Hex("#ffffff")
	clear    = ui.Transparent
)

type nativeMapper interface {
	GetState() State
	SaveSettings(Settings) (State, error)
	ChangeSettings(func(*Settings)) (State, error)
	SetRecording(bool) (State, error)
	RetryInput() State
	OpenPermissionSettings() error
	Preview(string) error
	Quit()
}

// All UI state stays on MyGo's main thread. Disk, startup registrations and
// native hooks run outside a frame; dispatch brings only their results back.
type nativeView struct {
	mapper   nativeMapper
	state    State
	dispatch func(func())
	work     func(func())
	page     int
	busy     bool
	error    string
	help     bool

	editing             bool
	editButton          string
	editKeys            []string
	editMode            int
	capture             bool
	captured            bool
	captureFocus        ui.Handle
	recorder            shortcutRecorder
	editError           string
	manualQuery         string
	editInputMethod     string
	editInputMethodName string
	editAction          int
	editDraft           nativeBindingDraft

	lastEvent     ShortcutEvent
	mainSequence  uint64
	mainTriggerAt time.Time
}

func newNativeView(mapper nativeMapper) *nativeView {
	prepareInterfaceFont()
	return &nativeView{
		mapper: mapper, state: mapper.GetState(),
		dispatch: func(fn func()) { fn() },
		work:     func(fn func()) { go fn() },
	}
}

func (v *nativeView) acceptState(state State) {
	v.state = state
	if !state.Recording {
		v.editing = false
		v.stopCapture()
	}
}

func (v *nativeView) acceptShortcut(event ShortcutEvent) {
	event.Keys = slices.Clone(event.Keys)
	v.lastEvent = event
}

func (v *nativeView) perform(operation func() (State, error), after func()) {
	if v.busy {
		return
	}
	v.busy, v.error, v.editError = true, "", ""
	v.work(func() {
		_, err := operation()
		v.dispatch(func() {
			v.busy = false
			// Tray actions, a hidden window or a recording timeout may have
			// changed the service while this completion waited for the UI.
			v.acceptState(v.mapper.GetState())
			if err != nil {
				if v.editing {
					v.editError = err.Error()
				} else {
					v.error = err.Error()
				}
				return
			}
			if after != nil {
				after()
			}
		})
	})
}

func (v *nativeView) save(change func(*Settings)) {
	v.perform(func() (State, error) { return v.mapper.ChangeSettings(change) }, nil)
}

func (v *nativeView) action(fn func() error) {
	v.perform(func() (State, error) {
		err := fn()
		return v.mapper.GetState(), err
	}, nil)
}

func (v *nativeView) startEditor(button string) {
	binding := v.state.Settings.Back
	if button == input.Forward {
		binding = v.state.Settings.Forward
	}
	draft := newNativeBindingDraft(binding)
	v.perform(func() (State, error) {
		state, err := v.mapper.SetRecording(true)
		if err == nil {
			if provider, ok := v.mapper.(inputMethodProvider); ok {
				state = provider.RefreshInputMethods()
			}
		}
		return state, err
	}, func() {
		if !v.state.Recording {
			return
		}
		v.editing, v.capture, v.captured = true, false, false
		v.editButton, v.editMode, v.editError = button, 0, ""
		v.editDraft, v.editAction = draft, 0
		v.loadEditorAction()
		v.stopCapture()
	})
}

func (v *nativeView) cancelEditor() {
	v.perform(func() (State, error) { return v.mapper.SetRecording(false) }, nil)
}

func (v *nativeView) saveEditor() {
	binding, err := v.editorBinding()
	if err != nil {
		v.editError = err.Error()
		return
	}
	button := v.editButton
	v.perform(func() (State, error) {
		state, err := v.mapper.ChangeSettings(func(next *Settings) {
			if button == input.Back {
				next.Back = binding
			} else {
				next.Forward = binding
			}
		})
		if err != nil {
			return state, err
		}
		return v.mapper.SetRecording(false)
	}, nil)
}

func nativeTheme(c *ui.Context) {
	t := *c.Theme()
	t.Font = "Manrope, PingFang SC, Microsoft YaHei"
	t.Radius, t.Spacing = 9, 4
	if !c.Preferences().HighContrast {
		t.Background, t.Surface, t.Border = canvas, white, line
		t.Text, t.TextMuted = ink, muted
		t.Accent, t.AccentHover, t.AccentPressed = blue, ui.Hex("#286bd2"), ui.Hex("#205ab7")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#f0f3f8"), ui.Hex("#e8edf5")
		t.Focus, t.Success = blue.Alpha(.42), green
	}
	c.SetTheme(&t)
}

func (v *nativeView) main(c *ui.Context) {
	nativeTheme(c)
	c.Root().Background(canvas)
	if v.mainSequence != v.lastEvent.Sequence {
		v.mainSequence, v.mainTriggerAt = v.lastEvent.Sequence, c.Now()
	}
	ui.Column(c.Key("application")).Fill().Children(func() {
		v.titlebar(c)
		ui.Scroll(c.Key("content")).Grow(1).Padding(24, 42, 20).Gap(22).Children(func() {
			v.pageHeading(c)
			v.messages(c)
			if v.page == 0 {
				v.mappings(c)
			} else {
				v.preferences(c)
			}
			v.footer(c)
		})
	})
	if v.editing {
		v.editor(c)
	}
	if v.help {
		v.helpDialog(c)
	}
}

func (v *nativeView) titlebar(c *ui.Context) {
	bar := c.TitleBar()
	ui.Row(c.Key("titlebar")).Height(max(58, bar.Height)).Padding(0, max(20, bar.Right+12), 0, max(25, bar.Left+14)).
		DragWindow().BorderWidth(0, 0, 1, 0).BorderColor(line.Alpha(.7)).Children(func() {
		ui.Row(c).Gap(9).Children(func() {
			ui.Icon(c, iconMouse).FontSize(21).TextColor(blue)
			ui.Text(c, "Mouse Mapper").FontSize(14).FontWeight(700).LetterSpacing(-.3)
		})
		ui.Spacer(c)
		seg := ui.SegmentedBase(c.Key("navigation"), &v.page, 2)
		seg.Track.Gap(3).Padding(3).Radius(9).Background(ui.Hex("#eaeef3")).Children(func() {
			for i, title := range []string{"按键映射", "偏好设置"} {
				segment := seg.Segment(i).Gap(7).Padding(7, 16).Radius(7).Label(title)
				if v.page == i {
					segment.Background(white).TextColor(ink).Shadow(0, 1, 3, 0, ui.RGBA(26, 40, 68, .06))
				} else {
					segment.TextColor(muted)
				}
				segment.Children(func() {
					icon := iconMouse
					if i == 1 {
						icon = iconSettings
					}
					ui.Icon(c, icon).FontSize(15)
					ui.Text(c, title).FontSize(12).FontWeight(600)
				})
			}
		})
		ui.Spacer(c)
		if iconButton(c.Key("help"), iconHelp, "使用帮助").Clicked() {
			v.help = true
		}
	})
}

func (v *nativeView) pageHeading(c *ui.Context) {
	ui.Row(c.Key("heading")).Gap(18).Children(func() {
		ui.Column(c).Grow(1).Gap(7).Children(func() {
			title, subtitle := "让侧键，更顺手。", "给两个小按键，一点新的可能。"
			if v.page == 1 {
				title, subtitle = "安静地，为你工作。", "让 Mouse Mapper 按你的方式融入日常。"
			}
			ui.Text(c, title).FontSize(28).FontWeight(650).LetterSpacing(-.6)
			ui.Text(c, subtitle).FontSize(13).TextColor(muted)
		})
		v.status(c)
		on := v.state.Settings.Enabled
		if ui.Switch(c.Key("enabled"), &on).Label("启用鼠标映射").Disabled(v.busy).Changed() {
			v.save(func(s *Settings) { s.Enabled = on })
		}
	})
}

func (v *nativeView) status(c *ui.Context) {
	color, label := muted, "映射已暂停"
	if v.state.Settings.Enabled {
		if v.state.Recording {
			label = "录制时暂停"
		} else if v.state.Status.Error != "" {
			color, label = ui.Hex("#ae7925"), "输入受到限制"
		} else if v.state.Status.Running {
			color, label = green, "映射已启用"
		} else {
			color, label = ui.Hex("#ae7925"), "需要留意"
		}
	}
	ui.Row(c).Gap(6).Padding(7, 10).Radius(20).Background(color.Alpha(.07)).Children(func() {
		ui.Box(c).Size(5, 5).Radius(3).Background(color)
		ui.Text(c, label).FontSize(11).FontWeight(600).TextColor(color)
	})
}

func (v *nativeView) messages(c *ui.Context) {
	message := strings.TrimSpace(strings.Join([]string{v.error, v.state.Warning}, " "))
	needsInput := v.state.Settings.Enabled && !v.state.Status.Running && !v.state.Recording
	if v.state.Status.Error != "" {
		message = strings.TrimSpace(message + " " + v.state.Status.Error)
	} else if needsInput && v.state.Status.Message != "" {
		message = strings.TrimSpace(message + " " + v.state.Status.Message)
	}
	if message == "" {
		return
	}
	ui.Column(c.Key("notice")).Padding(15, 18).Gap(10).Radius(12).Background(ui.Hex("#fff8e9")).Border(1, ui.Hex("#f0e2c2")).Children(func() {
		ui.Row(c).Gap(9).Children(func() {
			ui.Icon(c, iconShield).FontSize(17).TextColor(ui.Hex("#af8135"))
			ui.Text(c, "有一件事需要你确认").FontSize(12).FontWeight(600).Grow(1)
			if v.error != "" && iconButton(c, iconClose, "关闭错误提示").Clicked() {
				v.error = ""
			}
		})
		ui.Text(c, message).FontSize(11).TextColor(ui.Hex("#836738")).Selectable()
		if needsInput {
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "重新连接").FontSize(11).Disabled(v.busy).Clicked() {
					v.perform(func() (State, error) { return v.mapper.RetryInput(), nil }, nil)
				}
				if v.state.Status.Platform == "darwin" && ui.Button(c, "打开系统权限设置").FontSize(11).Disabled(v.busy).Clicked() {
					v.action(v.mapper.OpenPermissionSettings)
				}
			})
		}
	})
}

func (v *nativeView) mappings(c *ui.Context) {
	ui.Row(c.Key("mapping-workspace")).Gap(25).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("mouse")).WidthPercent(35).MinWidth(230).Radius(20).Gradient(white, ui.Hex("#eff3f8"), 155).Border(1, line).Padding(20, 15, 17).Children(func() {
			ui.Text(c, "YOUR MOUSE, YOUR WAY.").FontSize(9).FontWeight(700).LetterSpacing(1.3).TextColor(ui.Hex("#a0a9b7")).TextAlign(ui.Center)
			ui.Box(c).Height(280).FillWidth().Children(func() {
				ui.Image(c, mousePicture).Fill().Fit(ui.Contain)
				ui.Text(c, "05").Absolute().Top(55).Left(15).FontSize(10).FontWeight(600).TextColor(ui.Hex("#8fa0b6"))
				ui.Text(c, "04").Absolute().Top(136).Left(15).FontSize(10).FontWeight(600).TextColor(ui.Hex("#8fa0b6"))
			})
			ui.Text(c, "鼠标侧键 · 全局映射").FontSize(11).TextAlign(ui.Center).TextColor(muted)
		})
		ui.Column(c.Key("mappings")).Grow(1).Gap(15).Children(func() {
			v.mappingCard(c.Key("back-card"), input.Back, v.state.Settings.Back)
			v.mappingCard(c.Key("forward-card"), input.Forward, v.state.Settings.Forward)
		})
	})
	ui.Row(c.Key("overlay-preference")).Gap(14).Padding(18, 21).Radius(15).Background(blueWash.Alpha(.75)).Border(1, ui.Hex("#e0eafb")).Children(func() {
		ui.Icon(c, iconSparkle).FontSize(22).TextColor(blue)
		ui.Column(c).Grow(1).Gap(5).Children(func() {
			ui.Text(c, "按下，看见。").FontSize(13).FontWeight(600)
			ui.Text(c, "在桌面右下角轻轻提示，不打断当下。").FontSize(11).TextColor(muted)
		})
		if ui.Button(c, "预览提示").FontSize(11).Padding(8, 12).Disabled(v.busy).Clicked() {
			v.action(func() error { return v.mapper.Preview(input.Back) })
		}
		on := v.state.Settings.ShowOverlay
		if ui.Switch(c.Key("show-overlay"), &on).Label("显示快捷键提示").Disabled(v.busy).Changed() {
			v.save(func(s *Settings) { s.ShowOverlay = on })
		}
	})
	ui.Row(c.Key("mapping-note")).Gap(7).Children(func() {
		ui.Icon(c, iconShield).FontSize(13).TextColor(ui.Hex("#98a2b1"))
		ui.Text(c, "默认按住侧键时保持按键；启用长按后，单击或长按只触发一次。暂停映射恢复原始前进 / 后退。").FontSize(10).TextColor(muted)
	})
}

func (v *nativeView) mappingCard(c *ui.Context, button string, binding input.Binding) {
	name, number, icon := "后退键", "04", iconBack
	if button == input.Forward {
		name, number, icon = "前进键", "05", iconForward
	}
	active := v.lastEvent.Button == button && !v.mainTriggerAt.IsZero() && c.Now().Sub(v.mainTriggerAt) < time.Second
	border := line
	if active {
		border = blue.Alpha(.55)
		c.After(time.Second - c.Now().Sub(v.mainTriggerAt))
	}
	ui.Column(c).Grow(1).MinHeight(164).Padding(20, 23).Gap(18).Radius(18).Background(white).Border(1, border).
		Shadow(0, 2, 5, 0, ui.RGBA(23, 38, 67, .025)).Children(func() {
		ui.Row(c).Gap(11).Children(func() {
			iconTile(c, icon, blue, 33)
			ui.Text(c, name).FontSize(14).FontWeight(650)
			ui.Text(c, "侧键 "+number).FontSize(10).TextColor(ui.Hex("#a1a9b6")).Grow(1)
			on := binding.Enabled
			if ui.Switch(c.Key("button-enabled"), &on).Label("启用" + name + "映射").Disabled(v.busy).Changed() {
				v.save(func(s *Settings) {
					if button == input.Back {
						s.Back.Enabled = on
					} else {
						s.Forward.Enabled = on
					}
				})
			}
		})
		ui.Row(c).Gap(15).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
				if binding.LongPress == nil {
					keycaps(c, binding.Keys, v.state.Status.Platform, 21)
				} else {
					v.mappingActionSummary(c.Key("short-action"), "单击", binding.Keys, binding.InputMethod, binding.InputMethodName)
					long := binding.LongPress
					v.mappingActionSummary(c.Key("long-action"), fmt.Sprintf("长按 · %d ms", long.ThresholdMS), long.Keys, long.InputMethod, long.InputMethodName)
				}
				caption := shortcutName(binding.Keys, v.state.Status.Platform)
				if !binding.Enabled {
					caption = "未启用 · 保留原始功能"
				} else if active {
					caption = "刚刚触发"
					if v.lastEvent.Preview {
						caption = "提示预览 · 未发送按键"
					}
				}
				if binding.LongPress == nil || !binding.Enabled || active {
					ui.Text(c, caption).FontSize(10).TextColor(muted)
				}
				if binding.LongPress == nil && binding.InputMethod != "" {
					method := "先切换到 " + v.inputMethodName(binding.InputMethod, binding.InputMethodName)
					ui.Text(c, method).FontSize(10).TextColor(blue).SingleLine().MinWidth(0).FillWidth().Tooltip(method)
				}
			})
			if ui.ButtonBase(c.Key("edit")).Label("编辑"+name).Gap(5).Padding(9, 11).Radius(8).
				Background(ui.Hex("#f5f7fa")).TextColor(muted).Disabled(v.busy).Children(func() {
				ui.Text(c, "更改").FontSize(11)
				ui.Icon(c, iconChevron).FontSize(12)
			}).Clicked() {
				v.startEditor(button)
			}
		})
	})
}

func (v *nativeView) preferences(c *ui.Context) {
	sectionLabel(c, "后台运行")
	ui.Column(c.Key("background-settings")).Background(white).Radius(16).Border(1, line).Padding(3, 20).Dividers(1, line).Children(func() {
		v.settingRow(c.Key("login"), iconPower, "开机自动启动", "登录后静默运行，不自动打开主界面。", v.state.Settings.StartAtLogin, func(on bool) {
			v.save(func(s *Settings) { s.StartAtLogin = on })
		})
		v.settingRow(c.Key("tray"), iconTray, "在后台隐藏托盘图标", "再次打开 App，随时回到主界面。关闭窗口不会退出映射。", v.state.Settings.HideTray, func(on bool) {
			v.save(func(s *Settings) { s.HideTray = on })
		})
	})
	sectionLabel(c, "轻提示")
	ui.Column(c.Key("visual-settings")).Background(white).Radius(16).Border(1, line).Padding(3, 20).Dividers(1, line).Children(func() {
		v.settingRow(c.Key("hint"), iconEye, "显示快捷键提示", "在鼠标所在屏幕的右下角闪现，小巧、不抢焦点，也不拦截点击。", v.state.Settings.ShowOverlay, func(on bool) {
			v.save(func(s *Settings) { s.ShowOverlay = on })
		})
		ui.Row(c.Key("duration")).Padding(15, 0).Gap(12).Children(func() {
			ui.Text(c, "停留时长").FontSize(12).Grow(1)
			label := durationLabel(v.state.Settings.OverlayDuration)
			options := []string{"0.5 秒", "1.2 秒", "2.0 秒", "3.0 秒"}
			if !slices.Contains(options, label) {
				options = append(options, label)
			}
			if ui.Select(c, &label, options).Label("提示停留时长").Width(100).FontSize(11).Disabled(v.busy).Changed() {
				var seconds float64
				if _, err := fmt.Sscanf(label, "%f 秒", &seconds); err == nil {
					v.save(func(s *Settings) { s.OverlayDuration = int(seconds*1000 + .5) })
				}
			}
			if ui.Button(c, "预览提示").FontSize(11).Disabled(v.busy).Clicked() {
				v.action(func() error { return v.mapper.Preview(input.Back) })
			}
		})
	})
	v.updatePreferences(c)
}

func durationLabel(milliseconds int) string {
	return fmt.Sprintf("%.1f 秒", float64(milliseconds)/1000)
}

func (v *nativeView) settingRow(c *ui.Context, icon *ui.SVG, title, detail string, on bool, changed func(bool)) {
	row := ui.Row(c).Gap(14).Padding(17, 0)
	toggleChanged := false
	row.Children(func() {
		iconTile(c, icon, blue, 34)
		ui.Column(c).Grow(1).Gap(5).Children(func() {
			ui.Text(c, title).FontSize(12).FontWeight(600)
			ui.Text(c, detail).FontSize(10).TextColor(muted)
		})
		if ui.Switch(c.Key("toggle"), &on).Label(title).Disabled(v.busy).Changed() {
			toggleChanged = true
			changed(on)
		}
	})
	if row.Clicked() && !toggleChanged && !v.busy {
		changed(!on)
	}
}

func (v *nativeView) footer(c *ui.Context) {
	ui.Row(c.Key("footer")).Gap(6).Padding(8, 0, 0).Children(func() {
		if v.busy {
			ui.Spinner(c).Size(12, 12)
			ui.Text(c, "正在应用…").FontSize(10).TextColor(muted)
		} else {
			ui.Icon(c, iconCheck).FontSize(12).TextColor(green.Alpha(.8))
			ui.Text(c, "所有更改自动保存").FontSize(10).TextColor(muted)
		}
		ui.Spacer(c)
		ui.Text(c, "MyGo Native UI  ·  v"+v.state.Version).FontSize(10).TextColor(ui.Hex("#a1a8b3"))
		if ui.ButtonBase(c.Key("github")).Label("在 GitHub 查看源码").Padding(5, 8).Gap(4).TextColor(muted).Children(func() {
			ui.Text(c, "GitHub").FontSize(10)
			ui.Icon(c, iconExternal).FontSize(10)
		}).Clicked() {
			c.OpenURL(repositoryURL)
		}
	})
}

func (v *nativeView) editor(c *ui.Context) {
	name := "后退键"
	if v.editButton == input.Forward {
		name = "前进键"
	}
	wasEditing := v.editing
	dialog := ui.DialogBase(c.Key("shortcut-editor"), &v.editing, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(24, 35, 53, .22))
		_, availableHeight := c.Size()
		panel.Width(530).MaxWidthPercent(94).Height(min(668, availableHeight*.94)).Padding(24).Gap(13).Radius(23).Background(white).Border(1, line).
			Shadow(0, 18, 60, 0, ui.RGBA(15, 27, 47, .17))
		ui.Scroll(c.Key("editor-body")).Grow(1).MinHeight(0).Gap(13).Padding(0, 12, 2, 0).Children(func() {
			ui.Row(c).Children(func() {
				iconTile(c, iconKeyboard, blue, 43)
				ui.Spacer(c)
				ui.Text(c, "长按").FontSize(11).TextColor(muted).Margin(0, 8, 0, 0)
				on := v.editDraft.longEnabled
				if ui.Switch(c.Key("enable-long-press"), &on).Label("启用长按映射").Disabled(v.busy).Margin(0, 12, 0, 0).Changed() {
					v.setLongPressEnabled(on)
				}
				if iconButton(c.Key("cancel-x"), iconClose, "取消编辑").Disabled(v.busy).Clicked() {
					v.cancelEditor()
				}
			})
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "为"+name+"设置快捷键").FontSize(21).FontWeight(650)
				detail := "一个按键，或一个组合。按你的习惯来。"
				if v.editDraft.longEnabled {
					detail = "单击松开时触发，长按到时触发；每次只执行一个动作。"
				}
				ui.Text(c, detail).FontSize(12).TextColor(muted)
			})
			if v.editDraft.longEnabled {
				v.longPressEditor(c.Key("long-press-editor"))
			}
			if ui.Segmented(c.Key("record-mode"), &v.editMode, "键盘录制", "手动选择").Label("设置方式").Disabled(v.busy).Changed() {
				v.stopCapture()
				if v.editMode == 1 {
					v.syncManualQuery()
				}
			}
			if v.editMode == 0 {
				v.captureArea(c)
			} else {
				ui.Box(c).FillWidth().Padding(16).Center().Radius(13).Background(canvas).Children(func() {
					keycaps(c, v.editKeys, v.state.Status.Platform, 24).FillWidth().Justify(ui.Center)
				})
				v.manualKeys(c)
			}
			v.inputMethodChoice(c.Key("input-method-choice"))
			sectionLabel(c, "或从常用操作开始")
			ui.Row(c).Wrap().Gap(7).Children(func() {
				for _, preset := range shortcutPresets(v.state.Status.Platform) {
					if ui.Button(c.Key(preset.name), preset.name).FontSize(11).Padding(7, 11).Disabled(v.busy).Clicked() {
						v.editKeys = slices.Clone(preset.keys)
						v.stopCapture()
						v.captured, v.editError = true, ""
						v.syncManualQuery()
					}
				}
			})
			message, color := "编辑期间暂停映射。系统保留的快捷键可通过手动选择设置。", muted
			if v.editError != "" {
				message, color = v.editError, ui.Hex("#bf4b46")
			} else if v.editDraft.longEnabled {
				if _, err := parseLongPressThreshold(v.editDraft.threshold); err != nil {
					message, color = err.Error(), ui.Hex("#bf4b46")
				}
			}
			ui.Text(c, message).FontSize(10).TextColor(color).Selectable()
		})
		ui.Row(c).Gap(9).Justify(ui.End).Shrink(0).Children(func() {
			if ui.Button(c, "取消").Padding(10, 19).Disabled(v.busy).Clicked() {
				v.cancelEditor()
			}
			_, validationError := v.editorBinding()
			if ui.PrimaryButton(c, "保存快捷键").Padding(10, 20).Disabled(v.busy || validationError != nil || (v.editMode == 1 && v.editError != "")).Clicked() {
				v.saveEditor()
			}
		})
	})
	if wasEditing && dialog.Dismissed() {
		// An armed field consumes Escape before the dialog. Other dismissals
		// must still resume the native input service.
		v.editing = true
		if !v.busy {
			v.cancelEditor()
		}
	}
}

func (v *nativeView) stopCapture() {
	v.capture = false
	v.recorder.reset()
	v.captureFocus.CancelFocus()
}

func (v *nativeView) captureArea(c *ui.Context) {
	ui.Column(c.Key("shortcut-field")).Gap(8).FillWidth().Children(func() {
		ui.Row(c).Gap(14).FillWidth().Children(func() {
			ui.Text(c, "快捷键").FontSize(12).TextColor(ink)
			ui.Spacer(c)
			area := ui.Row(c.Key("capture")).Width(300).MinWidth(0).Height(42).Padding(0, 12).Radius(8).
				Background(canvas).Border(1, line).Focusable().InputMethod(false).Bind(&v.captureFocus).
				Label("快捷键录制区域").Disabled(v.busy)
			if area.Clicked() {
				v.capture, v.captured, v.editError = true, false, ""
				v.recorder.reset()
				// Modifiers held before activation must not replace the prompt.
				v.recorder.mods = c.Modifiers()
				v.captureFocus.Focus()
			} else if !area.Focused() || area.PressedOutside() || v.busy {
				v.stopCapture()
			}
			if v.capture {
				area.BorderColor(blue).Background(white)
				if keys := v.recorder.modifiers(c.Modifiers()); len(keys) != 0 {
					v.editKeys, v.captured, v.editError = keys, true, ""
				}
			}
			area.HandleInput(v.recordInput).Children(func() {
				text, color := describeKeys(v.editKeys, v.state.Status.Platform), ink
				if v.capture && !v.captured {
					text, color = "按下快捷键", muted
				} else if text == "" {
					text, color = "点击设置快捷键", muted
				}
				ui.Text(c, text).FontSize(13).TextColor(color).SingleLine().Grow(1).MinWidth(0).Tooltip(text)
			})
		})
		hint := "点击输入框后录制，点击其他位置结束。"
		if v.capture {
			hint = "Esc、Enter、Tab 均可录入；取消编辑请点击“取消”。"
		}
		ui.Text(c, hint).FontSize(10).TextColor(muted)
	})
}

func (v *nativeView) recordInput(event ui.InputEvent) bool {
	if !v.capture || v.busy {
		v.recorder.reset()
		return false
	}
	if event.Kind == ui.InputKeyUp {
		v.recorder.release(event)
		return true
	}
	if event.Kind == ui.InputText || event.Kind == ui.InputCompose {
		return true
	}
	if event.Kind != ui.InputKeyDown {
		return false
	}
	if event.Repeat {
		return true
	}
	v.recorder.press(event)
	keys, err := recordedKeys(event)
	if err != nil {
		v.editError = err.Error()
		return true
	}
	v.editKeys, v.captured, v.editError = keys, true, ""
	return true
}

func (v *nativeView) manualKeys(c *ui.Context) {
	ui.Row(c).Gap(7).Children(func() {
		for _, code := range modifierNames {
			on := slices.Contains(v.editKeys, code)
			button := ui.ButtonBase(c.Key(code)).Label("修饰键 "+keyLabel(code, v.state.Status.Platform)).
				Padding(9, 14).Radius(8).Border(1, line).Background(white).Disabled(v.busy)
			if on {
				button.Background(blueWash).BorderColor(blue.Alpha(.35)).TextColor(blue)
			}
			if button.Children(func() { ui.Text(c, keyLabel(code, v.state.Status.Platform)).FontSize(12).FontWeight(600) }).Clicked() {
				if on {
					v.editKeys = slices.DeleteFunc(v.editKeys, func(key string) bool { return key == code })
				} else {
					v.editKeys = append(v.editKeys, code)
				}
				v.editKeys, _ = input.NormalizeKeys(v.editKeys)
			}
		}
	})
	options, codeByLabel := []string{"仅修饰键"}, map[string]string{}
	for _, code := range v.state.SupportedCodes {
		if slices.Contains(modifierNames, code) {
			continue
		}
		label := keyLabel(code, v.state.Status.Platform)
		options = append(options, label)
		codeByLabel[label] = code
	}
	ui.Row(c).Gap(13).Children(func() {
		ui.Text(c, "按键").FontSize(12)
		if ui.Combobox(c.Key("manual-key"), &v.manualQuery, options).Label("选择按键").Grow(1).Disabled(v.busy).Changed() {
			code, valid := codeByLabel[v.manualQuery]
			if valid || v.manualQuery == "仅修饰键" {
				v.editKeys = slices.DeleteFunc(v.editKeys, func(key string) bool { return !slices.Contains(modifierNames, key) })
				if code != "" {
					v.editKeys = append(v.editKeys, code)
				}
				v.editError = ""
			} else {
				v.editError = "请从列表选择一个有效按键。"
			}
		}
		if ui.Button(c, "仅修饰键").Label("使用仅修饰键").FontSize(11).Disabled(v.busy).Clicked() {
			v.editKeys = slices.DeleteFunc(v.editKeys, func(key string) bool { return !slices.Contains(modifierNames, key) })
			v.manualQuery = "仅修饰键"
			v.editError = ""
		}
	})
}

func (v *nativeView) syncManualQuery() {
	v.manualQuery = "仅修饰键"
	for _, key := range v.editKeys {
		if !slices.Contains(modifierNames, key) {
			v.manualQuery = keyLabel(key, v.state.Status.Platform)
		}
	}
}

func (v *nativeView) helpDialog(c *ui.Context) {
	ui.DialogBase(c.Key("help-dialog"), &v.help, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(24, 35, 53, .22))
		panel.Width(530).MaxWidthPercent(94).Padding(28).Gap(18).Radius(22).Background(white).Border(1, line)
		ui.Row(c).Children(func() {
			ui.Text(c, "简单设置，顺手使用。").FontSize(21).FontWeight(650).Grow(1)
			if iconButton(c, iconClose, "关闭帮助").Clicked() {
				v.help = false
			}
		})
		for _, item := range []struct{ title, detail string }{
			{"01  选择一个侧键", "点击“更改”录制或手动选择快捷键。默认映射按住与松开；启用长按后，可分别设置单击、长按动作和各自的输入法，每次只触发一个动作。"},
			{"02  让它留在后台", "关闭窗口后映射仍然工作。可在偏好设置中启用开机自启、隐藏托盘；再次打开 App 即可回到这里。"},
			{"03  让系统允许输入", "macOS 需要辅助功能 / 输入监控权限；授权后点“重新连接”。Windows 无法向权限高于本程序的应用发送按键。"},
			{"04  保持轻量与安全", "不记录输入内容，不上传使用记录。自动更新每 7 天检查一次 GitHub 发布，并验证 MyGo 更新签名。系统保留的组合键可能无法模拟。"},
		} {
			ui.Column(c.Key(item.title)).Gap(6).Children(func() {
				ui.Text(c, item.title).FontSize(12).FontWeight(650)
				ui.Text(c, item.detail).FontSize(11).TextColor(muted).LineHeight(1.65)
			})
		}
		if v.state.ConfigPath != "" {
			ui.Text(c, "配置保存在："+v.state.ConfigPath).FontSize(9).TextColor(muted).Selectable()
		}
		ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
			if ui.Button(c, "完全退出").TextColor(ui.Hex("#b16c67")).Clicked() {
				v.mapper.Quit()
			}
			if ui.PrimaryButton(c, "知道了").Clicked() {
				v.help = false
			}
		})
	})
}

func (v *nativeView) overlay(c *ui.Context) {
	nativeTheme(c)
	c.Root().Background(overlayBackground())
	if v.lastEvent.Sequence == 0 {
		return
	}
	ui.Row(c.Key("shortcut-toast")).Fill().Gap(9).Padding(8, 12).Radius(overlayRadius).
		Background(white).Border(1, line).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Icon(c, iconMouse).FontSize(14).TextColor(blue)
		ui.Text(c, describeKeys(v.lastEvent.Keys, v.state.Status.Platform)).
			Font(overlayFont).FontSize(overlayFontSize).FontWeight(600).TextColor(ink).NoWrap()
	})
}

func keycaps(c *ui.Context, keys []string, platform string, size float32) ui.Element {
	return ui.Row(c).Gap(5).Wrap().Children(func() {
		if len(keys) == 0 {
			ui.Text(c, "尚未设置").FontSize(size).TextColor(muted)
		}
		for i, code := range keys {
			if i > 0 {
				ui.Text(c, "+").FontSize(max(9, size*.55)).TextColor(ui.Hex("#acb3bf"))
			}
			ui.Box(c.Key(fmt.Sprintf("key-%d", i))).MinWidth(size+15).Padding(5, 9).Radius(7).
				Gradient(white, ui.Hex("#f4f6f9"), 180).Border(1, ui.Hex("#e1e6ed")).
				Shadow(0, 2, 0, 0, ui.Hex("#e6eaf0")).Center().Children(func() {
				ui.Text(c, keyLabel(code, platform)).FontSize(size).FontWeight(550).NoWrap()
			})
		}
	})
}

func iconTile(c *ui.Context, icon *ui.SVG, color ui.Color, size float32) {
	ui.Box(c).Size(size, size).Radius(size * .26).Background(color.Alpha(.075)).Center().Children(func() {
		ui.Icon(c, icon).FontSize(size * .49).TextColor(color)
	})
}

func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return ui.ButtonBase(c).Label(label).Tooltip(label).Size(28, 28).Radius(7).Center().TextColor(muted).
		Children(func() { ui.Icon(c, icon).FontSize(17) })
}

func sectionLabel(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(10).FontWeight(600).LetterSpacing(.5).TextColor(muted)
}

func (v *nativeView) updatePreferences(c *ui.Context) {
	sectionLabel(c, "关于与更新")
	ui.Column(c.Key("update-settings")).Background(white).Radius(16).Border(1, line).Padding(17, 20).Gap(12).Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			iconTile(c, iconRefresh, blue, 34)
			ui.Column(c).Grow(1).Gap(5).Children(func() {
				ui.Text(c, "保持最新，也保持安静。").FontSize(12).FontWeight(600)
				ui.Text(c, "每 7 天检查一次 · 更新经过签名验证").FontSize(10).TextColor(muted)
			})
			if ui.Button(c, "检查更新").FontSize(11).Disabled(v.busy).Clicked() {
				if controller, ok := v.mapper.(interface{ CheckForUpdates() }); ok {
					controller.CheckForUpdates()
				} else {
					v.error = "当前构建未启用自动更新，请从 GitHub 下载正式版"
				}
			}
		})
		ui.Divider(c).Background(line)
		ui.Row(c.Key("update-options")).Gap(25).Wrap().Children(func() {
			checks := v.state.Update.AutomaticChecks
			if ui.Checkbox(c.Key("automatic-checks"), &checks, "自动检查更新").FontSize(11).Disabled(v.busy || !v.state.Update.Enabled).Changed() {
				if controller, ok := v.mapper.(interface{ SetAutomaticChecks(bool) State }); ok {
					v.perform(func() (State, error) { return controller.SetAutomaticChecks(checks), nil }, nil)
				}
			}
			downloads := v.state.Update.AutomaticDownloads
			if ui.Checkbox(c.Key("automatic-downloads"), &downloads, "自动下载并安装更新").FontSize(11).
				Disabled(v.busy || !v.state.Update.Enabled || !checks).Changed() {
				if controller, ok := v.mapper.(interface{ SetAutomaticDownloads(bool) State }); ok {
					v.perform(func() (State, error) { return controller.SetAutomaticDownloads(downloads), nil }, nil)
				}
			}
		})
		checkNote := "尚未检查。自动安装默认关闭，启用后下载的新版本在下次启动时生效。"
		if !v.state.Update.LastCheck.IsZero() {
			checkNote = "最近检查：" + v.state.Update.LastCheck.Local().Format("2006-01-02 15:04") + "。新版本在下次启动时生效。"
		}
		if !v.state.Update.Enabled {
			checkNote = "当前为开发构建或不可自更新的安装位置，自动更新未启用。正式发布版内置更新签名。"
		}
		ui.Text(c, checkNote).FontSize(10).TextColor(muted)
	})
}
