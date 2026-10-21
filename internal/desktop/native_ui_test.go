package desktop

import (
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/config"
	"github.com/fondoger/mouse-mapper/internal/input"
)

type uiMapper struct {
	state    State
	saveErr  error
	previews int
	checks   int
	quit     bool
}

func newUIMapper() *uiMapper {
	return &uiMapper{state: State{
		Settings: config.Default(), Version: "0.4.0", SupportedCodes: input.SupportedCodes(),
		Status:     input.Status{Platform: runtime.GOOS, Available: true, Running: true},
		ConfigPath: "settings.json",
		Update:     UpdateState{Enabled: true, AutomaticChecks: true, IntervalDays: 7},
	}}
}

func (m *uiMapper) GetState() State {
	s := m.state
	s.Settings = config.Clone(s.Settings)
	return s
}
func (m *uiMapper) SaveSettings(settings Settings) (State, error) {
	if m.saveErr != nil {
		return m.GetState(), m.saveErr
	}
	next, err := config.Normalize(settings)
	if err == nil {
		m.state.Settings = next
	}
	return m.GetState(), err
}
func (m *uiMapper) ChangeSettings(change func(*Settings)) (State, error) {
	settings := config.Clone(m.state.Settings)
	change(&settings)
	return m.SaveSettings(settings)
}
func (m *uiMapper) SetRecording(on bool) (State, error) {
	m.state.Recording = on
	return m.GetState(), nil
}
func (m *uiMapper) RetryInput() State             { return m.GetState() }
func (m *uiMapper) OpenPermissionSettings() error { return nil }
func (m *uiMapper) Preview(string) error          { m.previews++; return nil }
func (m *uiMapper) Quit()                         { m.quit = true }
func (m *uiMapper) CheckForUpdates()              { m.checks++ }
func (m *uiMapper) SetAutomaticChecks(on bool) State {
	m.state.Update.AutomaticChecks = on
	return m.GetState()
}
func (m *uiMapper) SetAutomaticDownloads(on bool) State {
	m.state.Update.AutomaticDownloads = on
	return m.GetState()
}

func testView(m *uiMapper) *nativeView {
	v := newNativeView(m)
	v.work = func(fn func()) { fn() }
	return v
}

func click(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	if err := tt.Click(name); err != nil {
		t.Fatalf("click %q: %v\nvisible text: %q", name, err, tt.Texts())
	}
}

func TestNativeViewRecordsAndSavesShortcut(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	if !tt.HasText("让侧键，更顺手。") {
		t.Fatal("missing native main view")
	}
	click(t, tt, "编辑后退键")
	if !m.state.Recording || !v.editing {
		t.Fatal("recording must suspend mapping before the editor opens")
	}
	click(t, tt, "快捷键录制区域")
	tt.Key(ui.Ctrl|ui.Shift, ui.KeyK)
	click(t, tt, "保存快捷键")
	want := []string{"Control", "Shift", "KeyK"}
	if !slices.Equal(m.state.Settings.Back.Keys, want) {
		t.Fatalf("saved %v; want %v", m.state.Settings.Back.Keys, want)
	}
	if m.state.Recording || v.editing {
		t.Fatal("saving must close the editor and resume mappings")
	}
}

func TestNativeViewEscapeResumesMapping(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑前进键")
	tt.Key(0, ui.KeyEscape)
	if m.state.Recording || v.editing {
		t.Fatal("dismissing the modal left the input service paused")
	}
}

func TestNativeViewFailedSaveKeepsOldMapping(t *testing.T) {
	m := newUIMapper()
	old := slices.Clone(m.state.Settings.Back.Keys)
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	click(t, tt, "快捷键录制区域")
	tt.Key(0, ui.KeySpace)
	m.saveErr = errors.New("disk is read-only")
	click(t, tt, "保存快捷键")
	if !slices.Equal(old, m.state.Settings.Back.Keys) || !tt.HasText("disk is read-only") {
		t.Fatal("failed save did not preserve the mapping and explain the error")
	}
	click(t, tt, "取消")
	if m.state.Recording {
		t.Fatal("cancel after failed save did not resume mapping")
	}
}

func TestNativeViewTogglesAndPreviews(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "启用鼠标映射")
	if m.state.Settings.Enabled {
		t.Fatal("global toggle did not pause mappings")
	}
	click(t, tt, "显示快捷键提示")
	if m.state.Settings.ShowOverlay {
		t.Fatal("overlay setting was not persisted")
	}
	click(t, tt, "预览提示")
	if m.previews != 1 {
		t.Fatal("preview did not call the non-injecting desktop API")
	}
	click(t, tt, "偏好设置")
	click(t, tt, "在后台隐藏托盘图标")
	if !m.state.Settings.HideTray {
		t.Fatal("hide-tray preference was not persisted")
	}
	click(t, tt, "开机自动启动")
	if !m.state.Settings.StartAtLogin {
		t.Fatal("launch-at-login preference was not persisted")
	}
}

func TestRecordedKeysUsePhysicalPosition(t *testing.T) {
	keys, err := recordedKeys(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyZ, PhysicalKey: ui.KeyY, Mods: ui.Ctrl})
	if err != nil || !slices.Equal(keys, []string{"Control", "KeyY"}) {
		t.Fatalf("physical recording: %v, %v", keys, err)
	}
	if _, err := recordedKeys(ui.InputEvent{Key: ui.KeyBack}); err == nil {
		t.Fatal("a mouse side button is not a keyboard binding")
	}
}

func TestNativeViewRecordsModifierOnly(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	click(t, tt, "编辑后退键")
	click(t, tt, "快捷键录制区域")
	modifier := ui.Ctrl
	if runtime.GOOS == "darwin" {
		modifier = ui.Super
	}
	tt.HoldModifiers(modifier)
	tt.HoldModifiers(0)
	click(t, tt, "保存快捷键")
	primary := "Control"
	if runtime.GOOS == "darwin" {
		primary = "Meta"
	}
	if !slices.Equal(m.state.Settings.Back.Keys, []string{primary}) {
		t.Fatalf("modifier-only binding = %v", m.state.Settings.Back.Keys)
	}
}

func TestNativeUpdatePreferences(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	tt := ui.NewTester(func(c *ui.Context) {
		nativeTheme(c)
		ui.Column(c).Fill().Padding(15).Gap(10).Children(func() { v.updatePreferences(c) })
	}, 750, 300)
	click(t, tt, "自动下载并安装更新")
	if !m.state.Update.AutomaticDownloads {
		t.Fatal("automatic downloads did not persist via the native updater")
	}
	click(t, tt, "自动检查更新")
	if m.state.Update.AutomaticChecks {
		t.Fatal("automatic update checks did not turn off")
	}
	click(t, tt, "检查更新")
	if m.checks != 1 {
		t.Fatal("manual check did not open the official updater")
	}
}

func TestNativeEditorCompletionCannotUndoExternalCancel(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	var pending func()
	v.dispatch = func(fn func()) { pending = fn }
	v.startEditor(input.Back)
	if !m.state.Recording || pending == nil {
		t.Fatal("expected a recording response queued for the UI")
	}
	_, _ = m.SetRecording(false)
	v.acceptState(m.GetState())
	pending()
	if v.editing || v.capture || v.state.Recording {
		t.Fatal("stale completion reopened an editor after the backend resumed")
	}
}

func TestNativePreferencePatchPreservesConcurrentTrayChanges(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	var operation func()
	v.work = func(fn func()) { operation = fn }
	v.save(func(settings *Settings) { settings.StartAtLogin = true })
	m.state.Settings.Enabled = false
	operation()
	if m.state.Settings.Enabled || !m.state.Settings.StartAtLogin {
		t.Fatal("a queued preference change reverted the tray's mapping pause")
	}
}

func TestNativeCompletionRereadsAuthoritativeState(t *testing.T) {
	m := newUIMapper()
	v := testView(m)
	var completion func()
	v.dispatch = func(fn func()) { completion = fn }
	v.save(func(settings *Settings) { settings.ShowOverlay = false })
	m.state.Settings.HideTray = true
	completion()
	if !v.state.Settings.HideTray || v.state.Settings.ShowOverlay {
		t.Fatal("UI completion replaced a newer service state with an old snapshot")
	}
}

func TestNativeShowsSoftInputFailures(t *testing.T) {
	m := newUIMapper()
	m.state.Status.Error = "Input blocked by a higher-integrity window"
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	if !tt.HasText(m.state.Status.Error) || !tt.HasText("输入受到限制") {
		t.Fatal("an input error was hidden because the native hook was still running")
	}
}

// Opt-in native renderer snapshots are useful for visual QA and documentation;
// they exercise the same view code as the app without a browser or webview.
func TestNativeSnapshots(t *testing.T) {
	directory := os.Getenv("MOUSE_MAPPER_UI_SNAPSHOTS")
	if directory == "" {
		t.Skip("set MOUSE_MAPPER_UI_SNAPSHOTS to write native UI snapshots")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newUIMapper()
	m.state.InputMethods = uiInputMethods()
	v := testView(m)
	tt := ui.NewTester(v.main, 1080, 760)
	writeSnapshot(t, tt, filepath.Join(directory, "mappings.png"))
	click(t, tt, "编辑后退键")
	writeSnapshot(t, tt, filepath.Join(directory, "shortcut-editor.png"))
	click(t, tt, "选择触发前的输入法")
	writeSnapshot(t, tt, filepath.Join(directory, "input-method-picker.png"))
	click(t, tt, "微信输入法")
	writeSnapshot(t, tt, filepath.Join(directory, "input-method-selected.png"))
	click(t, tt, "取消")
	click(t, tt, "偏好设置")
	writeSnapshot(t, tt, filepath.Join(directory, "preferences.png"))
	v.acceptShortcut(ShortcutEvent{Button: input.Back, Keys: []string{"Control", "KeyC"}, Sequence: 1, Duration: 1200})
	width, height := overlaySize(v.lastEvent.Keys, v.state.Status.Platform)
	to := ui.NewTester(v.overlay, width, height)
	to.Advance(180 * time.Millisecond)
	writeSnapshot(t, to, filepath.Join(directory, "overlay.png"))
}

func writeSnapshot(t *testing.T, tt *ui.Tester, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, tt.Image()); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
