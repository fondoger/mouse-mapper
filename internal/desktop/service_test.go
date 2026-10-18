package desktop

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fondoger/mouse-mapper/internal/config"
	"github.com/fondoger/mouse-mapper/internal/input"
)

type serviceStore struct {
	mu       sync.Mutex
	settings Settings
	loadErr  error
	saveErr  error
	saves    int
}

func (s *serviceStore) Load() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return config.Clone(s.settings), s.loadErr
}

func (s *serviceStore) Save(settings Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.settings = config.Clone(settings)
	return nil
}

type serviceEngine struct {
	mu           sync.Mutex
	config       input.Config
	status       input.Status
	applyError   func(input.Config) error
	startErr     error
	starts       int
	applications int
}

func (e *serviceEngine) Apply(settings input.Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applications++
	if e.applyError != nil {
		if err := e.applyError(settings); err != nil {
			return err
		}
	}
	e.config = settings
	return nil
}

func (e *serviceEngine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts++
	e.status = input.Status{Platform: "windows", Available: e.startErr == nil, Running: e.startErr == nil}
	return e.startErr
}

func (e *serviceEngine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.Running = false
	return nil
}

func (e *serviceEngine) Status() input.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (*serviceEngine) OpenPermissions() error { return nil }

func (e *serviceEngine) snapshot() input.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config
}

type serviceHost struct {
	mu                 sync.Mutex
	login              bool
	loginFailure       bool
	loginCalls         []bool
	preferencesErr     error
	previewed          []ShortcutEvent
	preferenceEntered  chan struct{}
	preferenceReleased chan struct{}
	quitCalled         bool
}

func (h *serviceHost) openAtLogin() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.login
}

func (h *serviceHost) setOpenAtLogin(on bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loginCalls = append(h.loginCalls, on)
	h.login = on
	if on && h.loginFailure {
		return errors.New("OS denied startup registration")
	}
	return nil
}

func (h *serviceHost) applyPreferences(Settings) error {
	if h.preferenceEntered != nil {
		close(h.preferenceEntered)
		<-h.preferenceReleased
	}
	return h.preferencesErr
}

func (h *serviceHost) preview(event ShortcutEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.previewed = append(h.previewed, event)
	return nil
}

func (h *serviceHost) quit() { h.quitCalled = true }

func mapperFixture(t *testing.T) (*Mapper, *serviceStore, *serviceEngine, *serviceHost) {
	t.Helper()
	store := &serviceStore{settings: config.Default()}
	engine := &serviceEngine{}
	host := &serviceHost{}
	mapper := newMapper(store, engine, host, "test/settings.json", "0.1.0")
	mapper.initialize()
	t.Cleanup(func() { _ = mapper.stop() })
	return mapper, store, engine, host
}

func TestSaveRollsBackPartialAutostartFailure(t *testing.T) {
	mapper, store, engine, host := mapperFixture(t)
	before := mapper.GetState()
	host.loginFailure = true
	next := config.Clone(before.Settings)
	next.StartAtLogin = true
	next.Back.Keys = []string{"Alt", "ArrowLeft"}
	state, err := mapper.SaveSettings(next)
	if err == nil || !strings.Contains(err.Error(), "OS denied") {
		t.Fatalf("want startup registration error, got %v", err)
	}
	if host.openAtLogin() || store.saves != 0 {
		t.Fatalf("failed registration changed saved state: login=%v, saves=%d", host.login, store.saves)
	}
	if !reflect.DeepEqual(host.loginCalls, []bool{true, false}) {
		t.Fatalf("partial registration not undone: %v", host.loginCalls)
	}
	if !reflect.DeepEqual(state.Settings, before.Settings) || !reflect.DeepEqual(engine.snapshot(), before.Settings.Input(false)) {
		t.Fatal("failed startup preference changed the active mapping")
	}
}

func TestSaveRollsBackDiskFailure(t *testing.T) {
	mapper, store, engine, host := mapperFixture(t)
	before := mapper.GetState().Settings
	store.saveErr = errors.New("disk is full")
	next := config.Clone(before)
	next.StartAtLogin = true
	next.Back.Keys = []string{"Shift", "KeyD"}
	state, err := mapper.SaveSettings(next)
	if err == nil || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("want persistence error, got %v", err)
	}
	if host.openAtLogin() || !reflect.DeepEqual(engine.snapshot(), before.Input(false)) {
		t.Fatal("disk failure left a changed startup registration or native mapping")
	}
	if !reflect.DeepEqual(state.Settings, before) || !reflect.DeepEqual(store.settings, before) {
		t.Fatal("disk failure lost the original settings")
	}
}

func TestSaveRejectsNativeUnsupportedKeysDuringRecording(t *testing.T) {
	mapper, store, engine, _ := mapperFixture(t)
	if _, err := mapper.SetRecording(true); err != nil {
		t.Fatal(err)
	}
	engine.applyError = func(settings input.Config) error {
		if reflect.DeepEqual(settings.Back.Keys, []string{"F24"}) {
			return errors.New("F24 not available on this platform")
		}
		return nil
	}
	next := mapper.GetState().Settings
	next.Back.Keys = []string{"F24"}
	state, err := mapper.SaveSettings(next)
	if err == nil || store.saves != 0 || !state.Recording {
		t.Fatalf("unsupported capture saved or ended recording: %v, %d, %#v", err, store.saves, state)
	}
	if engine.snapshot().Enabled {
		t.Fatal("rejecting an edit resumed remapping during capture")
	}
}

func TestRecordingDoesNotPersistTemporaryDisable(t *testing.T) {
	mapper, store, engine, _ := mapperFixture(t)
	state, err := mapper.SetRecording(true)
	if err != nil || !state.Recording || !state.Settings.Enabled || engine.snapshot().Enabled {
		t.Fatalf("invalid recording state: %#v, %v", state, err)
	}
	state.Settings.Forward.Keys = []string{"Shift"}
	state, err = mapper.SaveSettings(state.Settings)
	if err != nil || !store.settings.Enabled || engine.snapshot().Enabled {
		t.Fatalf("capture pause was persisted or edit resumed input: %#v, %v", state, err)
	}
	state, err = mapper.SetRecording(false)
	if err != nil || state.Recording || !engine.snapshot().Enabled {
		t.Fatalf("capture did not resume input: %#v, %v", state, err)
	}
	if !reflect.DeepEqual(engine.snapshot().Forward.Keys, []string{"Shift"}) {
		t.Fatal("resuming lost the newly saved modifier-only shortcut")
	}
}

func TestMalformedConfigurationIsNeverOverwritten(t *testing.T) {
	store := &serviceStore{loadErr: errors.New("unsupported settings schema 99")}
	engine := &serviceEngine{}
	host := &serviceHost{}
	mapper := newMapper(store, engine, host, "test/future.json", "0.1.0")
	mapper.initialize()
	t.Cleanup(func() { _ = mapper.stop() })
	state := mapper.GetState()
	if state.Settings.Enabled || engine.snapshot().Enabled || !strings.Contains(state.Warning, "original file was kept") {
		t.Fatalf("unsafe malformed configuration fallback: %#v", state)
	}
	if _, err := mapper.SaveSettings(config.Default()); err == nil || store.saves != 0 {
		t.Fatal("a newer or damaged settings file was overwritten")
	}
}

func TestInitializeHonorsExternallyDisabledStartup(t *testing.T) {
	store := &serviceStore{settings: config.Default()}
	store.settings.StartAtLogin = true
	engine, host := &serviceEngine{}, &serviceHost{}
	mapper := newMapper(store, engine, host, "test/settings.json", "0.1.0")
	mapper.initialize()
	t.Cleanup(func() { _ = mapper.stop() })
	if mapper.GetState().Settings.StartAtLogin || len(host.loginCalls) != 0 {
		t.Fatal("startup re-enabled a login item disabled through system settings")
	}
}

func TestPreviewNeverInjectsKeysAndStateIsCopied(t *testing.T) {
	mapper, _, engine, host := mapperFixture(t)
	applications := engine.applications
	if err := mapper.Preview(input.Back); err != nil {
		t.Fatal(err)
	}
	if len(host.previewed) != 1 || !host.previewed[0].Preview || engine.applications != applications {
		t.Fatal("preview should only show a visual notification")
	}
	if err := mapper.Preview("left"); err == nil {
		t.Fatal("an invalid mouse button was accepted")
	}
	state := mapper.GetState()
	state.Settings.Back.Keys[0] = "Alt"
	if reflect.DeepEqual(state.Settings.Back.Keys, mapper.GetState().Settings.Back.Keys) {
		t.Fatal("the UI can mutate settings without validation or persistence")
	}
}

func TestInputFailureCanBeRetried(t *testing.T) {
	mapper, _, engine, _ := mapperFixture(t)
	engine.startErr = errors.New("Accessibility permission is missing")
	failed := mapper.RetryInput()
	if failed.Status.Running || !strings.Contains(failed.Status.Message, "permission") {
		t.Fatalf("failed native service reported success: %#v", failed.Status)
	}
	engine.startErr = nil
	if !mapper.RetryInput().Status.Running {
		t.Fatal("successful permission retry still reports an error")
	}
}

func TestShutdownDoesNotWaitForPreferenceUICallback(t *testing.T) {
	mapper, _, engine, host := mapperFixture(t)
	host.preferenceEntered = make(chan struct{})
	host.preferenceReleased = make(chan struct{})
	saved := make(chan error, 1)
	next := mapper.GetState().Settings
	next.HideTray = true
	go func() { _, err := mapper.SaveSettings(next); saved <- err }()
	select {
	case <-host.preferenceEntered:
	case <-time.After(time.Second):
		t.Fatal("save never reached its simulated UI callback")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- mapper.stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(host.preferenceReleased)
		<-saved
		t.Fatal("shutdown waited for a save that was waiting for the UI thread")
	}
	if engine.Status().Running {
		t.Fatal("shutdown left the native input hook active")
	}
	close(host.preferenceReleased)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if mapper.RetryInput().Status.Running {
		t.Fatal("a queued retry restarted the engine after shutdown")
	}
	if _, err := mapper.SetRecording(false); !errors.Is(err, errStopping) {
		t.Fatalf("a queued recording callback changed a stopped engine: %v", err)
	}
}

type serviceUpdater struct {
	prefs  UpdateState
	checks int
}

func (u *serviceUpdater) state() UpdateState   { return u.prefs }
func (u *serviceUpdater) check()               { u.checks++ }
func (u *serviceUpdater) setChecks(on bool)    { u.prefs.AutomaticChecks = on }
func (u *serviceUpdater) setDownloads(on bool) { u.prefs.AutomaticDownloads = on }

func TestUpdaterDefaultsAndNativeDelegation(t *testing.T) {
	mapper, _, _, _ := mapperFixture(t)
	initial := mapper.GetState().Update
	if initial.IntervalDays != 7 || !initial.AutomaticChecks || initial.AutomaticDownloads {
		t.Fatalf("updates must check weekly but not silently install by default: %#v", initial)
	}
	lastCheck := time.Date(2026, time.October, 19, 10, 0, 0, 0, time.UTC)
	updates := &serviceUpdater{prefs: UpdateState{
		Enabled: true, AutomaticChecks: true, IntervalDays: 7, LastCheck: lastCheck,
	}}
	mapper.updates = updates
	mapper.CheckForUpdates()
	if updates.checks != 1 {
		t.Fatal("manual checks do not delegate to the native updater")
	}
	if mapper.SetAutomaticChecks(false).Update.AutomaticChecks {
		t.Fatal("automatic check preference was not updated")
	}
	if !mapper.SetAutomaticDownloads(true).Update.AutomaticDownloads {
		t.Fatal("automatic download preference was not updated")
	}
	if !mapper.GetState().Update.LastCheck.Equal(lastCheck) {
		t.Fatal("the last successful check was not surfaced")
	}
}

type firstSaveBarrier struct {
	*serviceStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *firstSaveBarrier) Save(settings Settings) error {
	err := s.serviceStore.Save(settings)
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return err
}

func TestChangeSettingsReadsAfterConcurrentSaveCommits(t *testing.T) {
	store := &firstSaveBarrier{
		serviceStore: &serviceStore{settings: config.Default()},
		entered:      make(chan struct{}), release: make(chan struct{}),
	}
	engine, host := &serviceEngine{}, &serviceHost{}
	mapper := newMapper(store, engine, host, "test/settings.json", "0.1.0")
	mapper.initialize()
	t.Cleanup(func() { _ = mapper.stop() })
	whole := mapper.GetState().Settings
	whole.Back.Keys = []string{"Alt", "ArrowLeft"}
	whole.ShowOverlay = false
	first := make(chan error, 1)
	go func() { _, err := mapper.SaveSettings(whole); first <- err }()
	<-store.entered
	second := make(chan error, 1)
	patchEntered := make(chan struct{})
	go func() {
		_, err := mapper.ChangeSettings(func(settings *Settings) {
			close(patchEntered)
			settings.HideTray = true
		})
		second <- err
	}()
	select {
	case <-patchEntered:
		close(store.release)
		<-first
		<-second
		t.Fatal("the patch read settings outside the transaction lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	final := mapper.GetState().Settings
	if !final.HideTray || final.ShowOverlay || !reflect.DeepEqual(final.Back.Keys, whole.Back.Keys) {
		t.Fatalf("independent settings edits overwrote one another: %#v", final)
	}
	if !reflect.DeepEqual(store.settings, final) || !reflect.DeepEqual(engine.snapshot(), final.Input(false)) {
		t.Fatal("settings, persisted data and native configuration diverged")
	}
}

func TestConcurrentToggleAndPreferenceChangesAreAtomic(t *testing.T) {
	mapper, store, engine, _ := mapperFixture(t)
	var work sync.WaitGroup
	for range 40 {
		work.Go(func() {
			if _, err := mapper.ToggleEnabled(); err != nil {
				t.Error(err)
			}
		})
		work.Go(func() {
			if _, err := mapper.ChangeSettings(func(settings *Settings) { settings.OverlayDuration++ }); err != nil {
				t.Error(err)
			}
		})
	}
	work.Wait()
	final := mapper.GetState().Settings
	if !final.Enabled || final.OverlayDuration != 1240 || store.saves != 80 {
		t.Fatalf("concurrent changes were lost: enabled=%v, duration=%d, writes=%d", final.Enabled, final.OverlayDuration, store.saves)
	}
	if !reflect.DeepEqual(engine.snapshot(), final.Input(false)) || !reflect.DeepEqual(store.settings, final) {
		t.Fatal("concurrent changes left the native engine or saved preferences stale")
	}
}

func TestChangeSettingsRejectsNilPatch(t *testing.T) {
	mapper, store, _, _ := mapperFixture(t)
	if _, err := mapper.ChangeSettings(nil); err == nil || store.saves != 0 {
		t.Fatal("an absent patch must not rewrite settings")
	}
}
