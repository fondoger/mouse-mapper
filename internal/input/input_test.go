package input

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeDriver struct {
	events   []string
	physical map[string]bool
	fail     string
	started  bool
	stopErr  error
	stops    int
	startErr error
	onStart  func()
}

func (d *fakeDriver) start() error {
	d.started = true
	if d.onStart != nil {
		d.onStart()
	}
	return d.startErr
}
func (d *fakeDriver) stop() error { d.started = false; d.stops++; return d.stopErr }
func (d *fakeDriver) sendKey(code string, down bool, modifiers []string) error {
	suffix := " up"
	if down {
		suffix = " down"
	}
	if code+suffix == d.fail {
		return errors.New("injection denied")
	}
	d.events = append(d.events, code+suffix)
	return nil
}
func (d *fakeDriver) physicallyDown(code string) bool { return d.physical[code] }
func (d *fakeDriver) openPermissions() error          { return nil }
func (d *fakeDriver) supports(code string) bool       { return keyCodes[code] }

func testEngine(t *testing.T, back, forward []string) (*Engine, *fakeDriver) {
	t.Helper()
	e := New(nil)
	d := &fakeDriver{physical: map[string]bool{}}
	e.driver = d
	config := Config{Enabled: true, Back: Binding{Enabled: len(back) > 0, Keys: back}, Forward: Binding{Enabled: len(forward) > 0, Keys: forward}}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	return e, d
}

func assertEvents(t *testing.T, d *fakeDriver, want ...string) {
	t.Helper()
	if !slices.Equal(d.events, want) {
		t.Fatalf("events = %v, want %v", d.events, want)
	}
}

func TestNormalizeKeys(t *testing.T) {
	got, err := NormalizeKeys([]string{"KeyC", "Shift", "Control", "Control"})
	if err != nil || !slices.Equal(got, []string{"Control", "Shift", "KeyC"}) {
		t.Fatalf("NormalizeKeys = %v, %v", got, err)
	}
	for _, keys := range [][]string{{"KeyA", "KeyB"}, {"NotAKey"}, {"a"}, {"Control", "Alt", "Shift", "Meta", "KeyA", "KeyB"}} {
		if _, err := NormalizeKeys(keys); err == nil {
			t.Errorf("accepted invalid shortcut %v", keys)
		}
	}
	if got, err := NormalizeKeys([]string{"Shift"}); err != nil || !slices.Equal(got, []string{"Shift"}) {
		t.Fatalf("modifier-only = %v, %v", got, err)
	}
	if err := ValidateConfig(Config{Back: Binding{Enabled: true}}); err == nil {
		t.Fatal("accepted enabled empty mapping")
	}
}

func TestPressReleaseAndSwallow(t *testing.T) {
	e, d := testEngine(t, []string{"Control", "KeyC"}, nil)
	if !e.mouseButton(Back, true) || !e.mouseButton(Back, true) || !e.mouseButton(Back, false) {
		t.Fatal("mapped click was not swallowed")
	}
	if e.mouseButton(Back, false) || e.mouseButton(Forward, true) {
		t.Fatal("unmapped event was swallowed")
	}
	assertEvents(t, d, "Control down", "KeyC down", "KeyC up", "Control up")
}

func TestSharedModifierReferenceCounts(t *testing.T) {
	e, d := testEngine(t, []string{"Control", "KeyC"}, []string{"Control", "KeyV"})
	e.mouseButton(Back, true)
	e.mouseButton(Forward, true)
	e.mouseButton(Back, false)
	assertEvents(t, d, "Control down", "KeyC down", "KeyV down", "KeyC up")
	e.mouseButton(Forward, false)
	assertEvents(t, d, "Control down", "KeyC down", "KeyV down", "KeyC up", "KeyV up", "Control up")
}

func TestIdenticalChordsOnlyReleaseLastOwner(t *testing.T) {
	e, d := testEngine(t, []string{"Shift"}, []string{"Shift"})
	e.mouseButton(Back, true)
	e.mouseButton(Forward, true)
	e.mouseButton(Back, false)
	assertEvents(t, d, "Shift down")
	e.mouseButton(Forward, false)
	assertEvents(t, d, "Shift down", "Shift up")
}

func TestPreheldKeysAreNotReleased(t *testing.T) {
	e, d := testEngine(t, []string{"Control", "KeyC"}, nil)
	d.physical["Control"] = true
	e.mouseButton(Back, true)
	e.mouseButton(Back, false)
	assertEvents(t, d, "KeyC down", "KeyC up")
}

func TestPhysicalReleaseTransfersOwnership(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	d.physical["Control"] = true
	e.mouseButton(Back, true)
	d.physical["Control"] = false
	if !e.physicalKey("Control", false) {
		t.Fatal("physical release should be withheld until the mapped hold ends")
	}
	e.mouseButton(Back, false)
	assertEvents(t, d, "Control up")
}

func TestPhysicalPressWhileMappedIsPreserved(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	e.mouseButton(Back, true)
	d.physical["Control"] = true
	if e.physicalKey("Control", true) {
		t.Fatal("physical press must pass through")
	}
	e.mouseButton(Back, false)
	assertEvents(t, d, "Control down")
	d.physical["Control"] = false
	if e.physicalKey("Control", false) {
		t.Fatal("physical release must pass through after mapped hold ends")
	}
}

func TestSettingsEditReleasesButSwallowsOriginalUp(t *testing.T) {
	e, d := testEngine(t, []string{"Control", "KeyC"}, nil)
	e.mouseButton(Back, true)
	if err := e.Apply(Config{Enabled: true, Back: Binding{Enabled: true, Keys: []string{"Shift", "KeyV"}}}); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, d, "Control down", "KeyC down", "KeyC up", "Control up")
	if !e.mouseButton(Back, true) {
		t.Fatal("held button must not be remapped mid-press")
	}
	if !e.mouseButton(Back, false) {
		t.Fatal("original up must remain swallowed")
	}
	e.mouseButton(Back, true)
	assertEvents(t, d, "Control down", "KeyC down", "KeyC up", "Control up", "Shift down", "KeyV down")
}

func TestPauseAndStopReleaseKeys(t *testing.T) {
	e, d := testEngine(t, []string{"Alt", "KeyA"}, nil)
	e.mouseButton(Back, true)
	if err := e.Apply(Config{}); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, d, "Alt down", "KeyA down", "KeyA up", "Alt up")
	if !e.mouseButton(Back, false) {
		t.Fatal("pause leaked a swallowed mouse release")
	}
	if e.mouseButton(Back, true) {
		t.Fatal("pause swallowed a new press")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Status().Running || d.started {
		t.Fatal("service did not stop")
	}
}

func TestFailedInjectionRollsBackAndPassesMouse(t *testing.T) {
	e, d := testEngine(t, []string{"Control", "KeyC"}, nil)
	d.fail = "KeyC down"
	if e.mouseButton(Back, true) || e.mouseButton(Back, false) {
		t.Fatal("failed injection must pass through")
	}
	assertEvents(t, d, "Control down", "Control up")
	if !strings.Contains(e.Status().Message, "injection denied") {
		t.Fatal("missing failure status")
	}
	if len(e.references) != 0 {
		t.Fatalf("references leaked: %v", e.references)
	}
}

func TestFailedReleaseCanBeRetried(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	e.mouseButton(Back, true)
	d.fail = "Control up"
	e.mouseButton(Back, false)
	if len(e.references) != 1 {
		t.Fatal("lost failed key-up ownership")
	}
	d.fail = ""
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, d, "Control down", "Control up")
}

func TestFailedReleaseRetriesBeforeNextPress(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	e.mouseButton(Back, true)
	d.fail = "Control up"
	e.mouseButton(Back, false)
	d.fail = ""
	if !e.mouseButton(Back, true) {
		t.Fatal("new press was not remapped after cleanup")
	}
	e.mouseButton(Back, false)
	assertEvents(t, d, "Control down", "Control up", "Control down", "Control up")
	if len(e.references) != 0 {
		t.Fatal("failed release retained a phantom key owner")
	}
}

func TestFailedReleaseDoesNotSwallowUserRecovery(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	e.mouseButton(Back, true)
	d.fail = "Control up"
	e.mouseButton(Back, false)
	if e.physicalKey("Control", false) {
		t.Fatal("user's physical key-up must be allowed to recover a failed release")
	}
	if len(e.references) != 0 {
		t.Fatal("physical key-up did not clear the failed release")
	}
}

func TestStopRetriesNativeShutdownAfterFailure(t *testing.T) {
	e, d := testEngine(t, []string{"KeyA"}, nil)
	d.stopErr = errors.New("temporary shutdown failure")
	if err := e.Stop(); err == nil {
		t.Fatal("missing shutdown failure")
	}
	d.stopErr = nil
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if d.stops != 2 {
		t.Fatalf("native stop called %d times, want retry", d.stops)
	}
}

func TestRestartDropsPressesFromOldNativeSession(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	e.mouseButton(Back, true)
	e.nativeFailed(errors.New("native hook closed"))
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	e.mouseButton(Back, true)
	e.mouseButton(Back, false)
	assertEvents(t, d, "Control down", "Control up", "Control down", "Control up")
}

func TestConcurrentConfigurationAndInput(t *testing.T) {
	e, _ := testEngine(t, []string{"Control", "KeyC"}, []string{"Shift"})
	config := e.config
	var work sync.WaitGroup
	for _, button := range []string{Back, Forward} {
		work.Go(func() {
			for range 200 {
				e.mouseButton(button, true)
				e.mouseButton(button, false)
			}
		})
	}
	work.Go(func() {
		for range 200 {
			if err := e.Apply(config); err != nil {
				t.Error(err)
			}
			_ = e.Status()
		}
	})
	work.Wait()
	if len(e.references) != 0 {
		t.Fatal("concurrent operations left output keys held")
	}
}

func TestApplyCopiesCallerMemory(t *testing.T) {
	e, _ := testEngine(t, []string{"KeyA"}, nil)
	keys := []string{"Control", "KeyC"}
	config := Config{Enabled: true, Back: Binding{Enabled: true, Keys: keys}}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	keys[1] = "KeyV"
	if !reflect.DeepEqual(e.config.Back.Keys, []string{"Control", "KeyC"}) {
		t.Fatal("settings retain caller-owned slice")
	}
}

func TestUnchangedApplyDoesNotInterruptHold(t *testing.T) {
	e, d := testEngine(t, []string{"Control"}, nil)
	e.mouseButton(Back, true)
	if err := e.Apply(e.config); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, d, "Control down")
}

func TestStartDoesNotOverwriteImmediateNativeFailure(t *testing.T) {
	e := New(nil)
	nativeErr := errors.New("native loop exited immediately after readiness")
	d := &fakeDriver{physical: map[string]bool{}}
	e.driver = d
	d.onStart = func() { e.nativeFailed(nativeErr) }
	if err := e.Start(); !errors.Is(err, nativeErr) {
		t.Fatalf("Start = %v, want native failure", err)
	}
	status := e.Status()
	if status.Running || status.Available || !strings.Contains(status.Error, nativeErr.Error()) {
		t.Fatalf("Start resurrected the failed native service: %#v", status)
	}
	d.onStart = nil
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if status := e.Status(); !status.Running || status.Error != "" {
		t.Fatalf("retry did not recover: %#v", status)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusSeparatesRuntimeErrorsFromListeningState(t *testing.T) {
	e, d := testEngine(t, []string{"Control", "KeyC"}, nil)
	if status := e.Status(); status.Error != "" {
		t.Fatalf("healthy listener reports error: %#v", status)
	}
	d.fail = "KeyC down"
	e.mouseButton(Back, true)
	if status := e.Status(); !status.Running || !strings.Contains(status.Error, "injection denied") {
		t.Fatalf("missing nonfatal injection error: %#v", status)
	}
	d.fail = ""
	e.mouseButton(Back, true)
	if status := e.Status(); status.Error != "" {
		t.Fatalf("successful press did not clear error: %#v", status)
	}
	d.fail = "KeyC up"
	e.mouseButton(Back, false)
	if status := e.Status(); status.Error == "" {
		t.Fatal("failed key release did not set status error")
	}
	d.fail = ""
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if status := e.Status(); status.Error != "" {
		t.Fatalf("clean Stop did not clear error: %#v", status)
	}
}

func TestFailedStartAndRepeatedStopKeepErrorState(t *testing.T) {
	e := New(nil)
	d := &fakeDriver{physical: map[string]bool{}, startErr: errors.New("native permission denied")}
	e.driver = d
	if err := e.Start(); err == nil || e.Status().Error == "" {
		t.Fatal("failed Start did not expose an error")
	}
	d.startErr = nil
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	d.stopErr = errors.New("native shutdown failed")
	if err := e.Stop(); err == nil || e.Status().Error == "" {
		t.Fatal("failed Stop did not expose an error")
	}
	d.stopErr = nil
	if err := e.Stop(); err != nil || e.Status().Error != "" {
		t.Fatalf("successful repeated Stop = %v, %#v", err, e.Status())
	}
}
