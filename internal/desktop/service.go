package desktop

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fondoger/mouse-mapper/internal/config"
	"github.com/fondoger/mouse-mapper/internal/input"
	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type Settings = config.Settings

type State struct {
	Settings          Settings             `json:"settings"`
	Status            input.Status         `json:"status"`
	Version           string               `json:"version"`
	Recording         bool                 `json:"recording"`
	ConfigPath        string               `json:"configPath"`
	Warning           string               `json:"warning"`
	SupportedCodes    []string             `json:"supportedCodes"`
	Update            UpdateState          `json:"update"`
	InputMethods      []inputmethod.Method `json:"inputMethods"`
	InputMethodsError string               `json:"inputMethodsError,omitempty"`
}

type ShortcutEvent struct {
	Button   string   `json:"button"`
	Keys     []string `json:"keys"`
	Preview  bool     `json:"preview"`
	Duration int      `json:"duration"`
	Sequence uint64   `json:"sequence"`
}

type settingsStore interface {
	Load() (Settings, error)
	Save(Settings) error
}

type inputEngine interface {
	Apply(input.Config) error
	Start() error
	Stop() error
	Status() input.Status
	OpenPermissions() error
}

type desktopHost interface {
	openAtLogin() bool
	setOpenAtLogin(bool) error
	applyPreferences(Settings) error
	preview(ShortcutEvent) error
	quit()
}

// Mapper is the small application service shared by the native views and tray.
type Mapper struct {
	opMu     sync.Mutex
	nativeMu sync.Mutex
	timerMu  sync.Mutex
	methodMu sync.Mutex
	mu       sync.RWMutex
	stopping atomic.Bool

	settings       Settings
	store          settingsStore
	engine         inputEngine
	host           desktopHost
	updates        updateService
	version        string
	configPath     string
	recording      bool
	recordTimer    *time.Timer
	loadErr        error
	inputErr       error
	configWarning  string
	desktopWarning string
	onChanged      func(State)
	inputMethods   []inputmethod.Method
	methodError    string
	listMethods    func(context.Context) ([]inputmethod.Method, error)
}

func newMapper(store settingsStore, engine inputEngine, host desktopHost, path, version string) *Mapper {
	return &Mapper{
		settings:    config.Default(),
		store:       store,
		engine:      engine,
		host:        host,
		version:     version,
		configPath:  path,
		listMethods: inputmethod.List,
	}
}

func (m *Mapper) initialize() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	settings, loadErr := m.store.Load()
	if loadErr != nil {
		settings = config.Default()
		settings.Enabled = false
	}
	// Respect a login item the user disabled outside the app; startup never
	// silently re-enables it or writes to the OS's startup registrations.
	settings.StartAtLogin = m.host.openAtLogin()
	m.mu.Lock()
	m.settings = settings
	m.loadErr = loadErr
	if loadErr != nil {
		m.configWarning = fmt.Sprintf("Settings could not be loaded: %v. Remapping is paused. The original file was kept at %s; repair or move it and restart the app.", loadErr, m.configPath)
	}
	m.mu.Unlock()
	err := m.changeEngine(func(engine inputEngine) error {
		if err := engine.Apply(settings.Input(false)); err != nil {
			return err
		}
		return engine.Start()
	})
	m.mu.Lock()
	m.inputErr = err
	m.mu.Unlock()
	m.setDesktopWarning(m.host.applyPreferences(settings))
	m.publish()
}

// GetState returns saved preferences and the actual native input service state.
func (m *Mapper) GetState() State {
	m.mu.RLock()
	state := State{
		Settings:          config.Clone(m.settings),
		Version:           m.version,
		Recording:         m.recording,
		ConfigPath:        m.configPath,
		Warning:           strings.TrimSpace(strings.Join([]string{m.configWarning, m.desktopWarning}, " ")),
		SupportedCodes:    input.SupportedCodes(),
		InputMethods:      slices.Clone(m.inputMethods),
		InputMethodsError: m.methodError,
	}
	inputErr := m.inputErr
	m.mu.RUnlock()
	state.Status = m.engine.Status()
	if m.updates != nil {
		state.Update = m.updates.state()
	} else {
		state.Update = UpdateState{AutomaticChecks: true, IntervalDays: updateIntervalDays}
	}
	if state.Status.Platform == "" {
		state.Status.Platform = runtime.GOOS
	}
	if inputErr != nil {
		state.Status.Running = false
		state.Status.Message = inputErr.Error()
		state.Status.Error = inputErr.Error()
	}
	return state
}

// SaveSettings validates, applies and atomically persists a complete update.
// Failed native or startup changes leave the old configuration in place.
func (m *Mapper) SaveSettings(settings Settings) (State, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.saveLocked(settings)
}

// ChangeSettings reads and patches the current preferences inside the same
// transaction. Independent tray and window edits cannot overwrite each other.
// The callback must not block or call another Mapper method.
func (m *Mapper) ChangeSettings(change func(*Settings)) (State, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if change == nil {
		return m.GetState(), errors.New("a settings change is required")
	}
	m.mu.RLock()
	next := config.Clone(m.settings)
	m.mu.RUnlock()
	change(&next)
	return m.saveLocked(next)
}

// ToggleEnabled is shared by tray actions that can race with settings edits.
func (m *Mapper) ToggleEnabled() (State, error) {
	return m.ChangeSettings(func(settings *Settings) { settings.Enabled = !settings.Enabled })
}

func (m *Mapper) saveLocked(settings Settings) (State, error) {
	if m.stopping.Load() {
		return m.GetState(), errStopping
	}
	next, err := config.Normalize(settings)
	if err != nil {
		return m.GetState(), err
	}
	m.mu.RLock()
	old, recording, loadErr := config.Clone(m.settings), m.recording, m.loadErr
	m.mu.RUnlock()
	if loadErr != nil {
		return m.GetState(), fmt.Errorf("the existing settings file is unreadable and will not be overwritten: %w", loadErr)
	}
	wasLogin := m.host.openAtLogin()
	loginChanged := wasLogin != next.StartAtLogin
	if loginChanged {
		if err := m.host.setOpenAtLogin(next.StartAtLogin); err != nil {
			// An OS API can fail after partially changing its registration.
			rollbackErr := m.host.setOpenAtLogin(wasLogin)
			return m.GetState(), errors.Join(fmt.Errorf("change launch at login: %w", err), wrapError("restore launch at login", rollbackErr))
		}
	}
	rollback := func(cause error) error {
		applyErr := m.changeEngine(func(engine inputEngine) error { return engine.Apply(old.Input(recording)) })
		var loginErr error
		if loginChanged {
			loginErr = m.host.setOpenAtLogin(wasLogin)
		}
		if applyErr != nil {
			m.nativeMu.Lock()
			_ = m.engine.Stop()
			m.nativeMu.Unlock()
			m.mu.Lock()
			m.inputErr = fmt.Errorf("could not restore the previous mapping: %w", applyErr)
			m.mu.Unlock()
		}
		return errors.Join(cause, wrapError("restore previous mapping", applyErr), wrapError("restore launch at login", loginErr))
	}
	if err := m.changeEngine(func(engine inputEngine) error { return engine.Apply(next.Input(recording)) }); err != nil {
		rollbackErr := rollback(fmt.Errorf("apply mapping: %w", err))
		return m.GetState(), rollbackErr
	}
	if err := m.store.Save(next); err != nil {
		rollbackErr := rollback(err)
		return m.GetState(), rollbackErr
	}
	m.mu.Lock()
	m.settings = config.Clone(next)
	m.mu.Unlock()
	m.setDesktopWarning(m.host.applyPreferences(next))
	m.publish()
	return m.GetState(), nil
}

// RetryInput retries OS permissions and native input initialization.
func (m *Mapper) RetryInput() State {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.RLock()
	settings, recording := config.Clone(m.settings), m.recording
	m.mu.RUnlock()
	err := m.changeEngine(func(engine inputEngine) error {
		if err := engine.Stop(); err != nil {
			return err
		}
		if err := engine.Apply(settings.Input(recording)); err != nil {
			return err
		}
		return engine.Start()
	})
	m.mu.Lock()
	m.inputErr = err
	m.mu.Unlock()
	m.publish()
	return m.GetState()
}

// OpenPermissionSettings opens the OS's input/accessibility permission settings.
func (m *Mapper) OpenPermissionSettings() error { return m.engine.OpenPermissions() }

// Preview displays a shortcut without sending any keyboard events.
func (m *Mapper) Preview(button string) error {
	m.mu.RLock()
	settings := config.Clone(m.settings)
	m.mu.RUnlock()
	var keys []string
	switch button {
	case "back":
		keys = settings.Back.Keys
	case "forward":
		keys = settings.Forward.Keys
	default:
		return errors.New("button must be back or forward")
	}
	if len(keys) == 0 {
		return errors.New("assign a shortcut before previewing it")
	}
	return m.host.preview(ShortcutEvent{Button: button, Keys: keys, Preview: true, Duration: settings.OverlayDuration})
}

// SetRecording suspends remapping while the native view records a shortcut.
func (m *Mapper) SetRecording(recording bool) (State, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.RLock()
	settings := config.Clone(m.settings)
	m.mu.RUnlock()
	if err := m.changeEngine(func(engine inputEngine) error { return engine.Apply(settings.Input(recording)) }); err != nil {
		return m.GetState(), err
	}
	m.timerMu.Lock()
	if m.recordTimer != nil {
		m.recordTimer.Stop()
		m.recordTimer = nil
	}
	if m.stopping.Load() {
		recording = false
	}
	m.mu.Lock()
	m.recording = recording
	m.mu.Unlock()
	if recording {
		// A dismissed or stalled editor must not leave the background service paused.
		m.recordTimer = time.AfterFunc(90*time.Second, func() { _, _ = m.SetRecording(false) })
	}
	m.timerMu.Unlock()
	m.publish()
	return m.GetState(), nil
}

// Quit exits the background service as well as closing the settings window.
func (m *Mapper) Quit() { m.host.quit() }

func (m *Mapper) stop() error {
	// OnBeforeQuit runs on the UI thread before MyGo drains its remaining tasks.
	// Do not take opMu: a pending preference save may hold it while awaiting
	// that thread. nativeMu encloses only OS input calls, never a UI callback.
	m.stopping.Store(true)
	m.timerMu.Lock()
	if m.recordTimer != nil {
		m.recordTimer.Stop()
		m.recordTimer = nil
	}
	m.timerMu.Unlock()
	m.nativeMu.Lock()
	defer m.nativeMu.Unlock()
	return m.engine.Stop()
}

var errStopping = errors.New("Mouse Mapper is shutting down")

func (m *Mapper) changeEngine(change func(inputEngine) error) error {
	m.nativeMu.Lock()
	defer m.nativeMu.Unlock()
	if m.stopping.Load() {
		return errStopping
	}
	return change(m.engine)
}

func (m *Mapper) setDesktopWarning(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.desktopWarning = ""
	if err != nil {
		m.desktopWarning = err.Error()
	}
}

func (m *Mapper) publish() {
	if m.onChanged != nil {
		m.onChanged(m.GetState())
	}
}

func (m *Mapper) shortcut(trigger input.Trigger) (ShortcutEvent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return ShortcutEvent{
		Button: trigger.Button, Keys: slices.Clone(trigger.Keys), Duration: m.settings.OverlayDuration,
	}, m.settings.ShowOverlay && m.settings.Enabled && !m.recording
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
