package input

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type manualPressTimer struct {
	work    func()
	stopped bool
}

func (timer *manualPressTimer) Stop() bool {
	was := !timer.stopped
	timer.stopped = true
	return was
}

type manualPressClock struct {
	now    time.Time
	timers []*manualPressTimer
}

func (clock *manualPressClock) install(e *Engine) {
	e.now = func() time.Time { return clock.now }
	e.afterFunc = func(_ time.Duration, work func()) pressTimer {
		timer := &manualPressTimer{work: work}
		clock.timers = append(clock.timers, timer)
		return timer
	}
}

func longPressEngine(t *testing.T) (*Engine, *fakeDriver, *preparedFocus, *manualPressClock) {
	t.Helper()
	e, d := testEngine(t, []string{"Control", "KeyC"}, []string{"Enter"})
	focus := newPreparedFocus()
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	clock := &manualPressClock{now: time.Unix(1, 0)}
	clock.install(e)
	config := e.config
	config.Forward.LongPress = &LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 500}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(); waitPreparation(t, e) })
	return e, d, focus, clock
}

func TestConfiguredShortPressFiresOnlyOnRelease(t *testing.T) {
	e, d, _, clock := longPressEngine(t)
	for _, down := range []bool{true, true} {
		if !e.mouseButton(Forward, down) {
			t.Fatal("mapped down escaped")
		}
	}
	assertEvents(t, d)
	if len(clock.timers) != 1 {
		t.Fatal("duplicate down rescheduled the threshold")
	}
	clock.now = clock.now.Add(499 * time.Millisecond)
	if !e.mouseButton(Forward, false) {
		t.Fatal("mapped up escaped")
	}
	assertEvents(t, d, "Enter down", "Enter up")
	clock.now = clock.now.Add(time.Second)
	clock.timers[0].work() // A stopped timer can already be queued.
	e.mouseButton(Forward, false)
	assertEvents(t, d, "Enter down", "Enter up")
	if len(e.references) != 0 || len(e.timedPresses) != 0 || len(e.active) != 0 {
		t.Fatal("short tap retained button, timer or output-key ownership")
	}
}

func TestLongPressFiresAtThresholdOnceWithoutWaitingForRelease(t *testing.T) {
	e, d, _, clock := longPressEngine(t)
	triggered := make(chan Trigger, 2)
	e.onTrigger = func(trigger Trigger) { triggered <- trigger }
	e.mouseButton(Forward, true)
	clock.now = clock.now.Add(500 * time.Millisecond)
	clock.timers[0].work()
	assertEvents(t, d, "Escape down", "Escape up")
	if !e.mouseHeld(Forward) || len(e.references) != 0 {
		t.Fatal("long action is not a complete tap while the physical button remains owned")
	}
	clock.now = clock.now.Add(10 * time.Second)
	clock.timers[0].work()
	e.mouseButton(Forward, true)
	e.mouseButton(Forward, false)
	e.mouseButton(Forward, false)
	assertEvents(t, d, "Escape down", "Escape up")
	select {
	case event := <-triggered:
		if event.Button != Forward || !slices.Equal(event.Keys, []string{"Escape"}) {
			t.Fatalf("wrong long notification: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("long action had no notification")
	}
	select {
	case event := <-triggered:
		t.Fatalf("duplicate action notification: %+v", event)
	default:
	}
}

func TestReleaseUsesElapsedTimeEvenWhenTimerIsLate(t *testing.T) {
	for _, elapsed := range []time.Duration{499 * time.Millisecond, 500 * time.Millisecond, 20 * time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			e, d, _, clock := longPressEngine(t)
			e.mouseButton(Forward, true)
			clock.now = clock.now.Add(elapsed)
			e.mouseButton(Forward, false)
			clock.timers[0].work()
			key := "Escape"
			if elapsed < 500*time.Millisecond {
				key = "Enter"
			}
			assertEvents(t, d, key+" down", key+" up")
		})
	}
}

func TestEarlyTimerCannotTurnAShortPressIntoLongPress(t *testing.T) {
	e, d, _, clock := longPressEngine(t)
	e.mouseButton(Forward, true)
	clock.now = clock.now.Add(100 * time.Millisecond)
	clock.timers[0].work()
	assertEvents(t, d)
	if len(clock.timers) != 2 {
		t.Fatal("early wake did not reschedule remaining time")
	}
	e.mouseButton(Forward, false)
	clock.now = clock.now.Add(time.Second)
	clock.timers[1].work()
	assertEvents(t, d, "Enter down", "Enter up")
}

func TestLongPressButtonsAndThresholdsAreIndependent(t *testing.T) {
	e, d, _, clock := longPressEngine(t)
	config := e.config
	config.Back.LongPress = &LongPressBinding{Keys: []string{"Tab"}, ThresholdMS: 1000}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	e.mouseButton(Forward, true)
	clock.now = clock.now.Add(100 * time.Millisecond)
	e.mouseButton(Back, true)
	clock.now = clock.now.Add(500 * time.Millisecond)
	clock.timers[0].work()
	e.mouseButton(Back, false)
	e.mouseButton(Forward, false)
	clock.now = clock.now.Add(time.Second)
	clock.timers[1].work()
	assertEvents(t, d, "Escape down", "Escape up", "Control down", "KeyC down", "KeyC up", "Control up")
}

func TestLongPressCancelledByLifecycleNeverFallsBackToShort(t *testing.T) {
	for _, operation := range []string{"pause", "edit", "disable", "stop", "native failure"} {
		t.Run(operation, func(t *testing.T) {
			e, d, _, clock := longPressEngine(t)
			e.mouseButton(Forward, true)
			config := e.config
			switch operation {
			case "pause":
				config.Enabled = false
			case "edit":
				config.Forward.LongPress = &LongPressBinding{Keys: []string{"Tab"}, ThresholdMS: 700}
			case "disable":
				config.Forward.Enabled = false
			case "stop":
				if err := e.Stop(); err != nil {
					t.Fatal(err)
				}
			case "native failure":
				e.nativeFailed(errors.New("hook stopped"))
			}
			if operation == "pause" || operation == "edit" || operation == "disable" {
				if err := e.Apply(config); err != nil {
					t.Fatal(err)
				}
			}
			clock.now = clock.now.Add(time.Second)
			clock.timers[0].work()
			e.mouseButton(Forward, false)
			assertEvents(t, d)
			if len(e.timedPresses) != 0 {
				t.Fatal("lifecycle retained a pending timer")
			}
		})
	}
}

func TestOldLongTimerCannotFireANewerPressAfterRestart(t *testing.T) {
	e, d, _, clock := longPressEngine(t)
	e.mouseButton(Forward, true)
	old := clock.timers[0]
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Second)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	e.mouseButton(Forward, true)
	old.work()
	assertEvents(t, d)
	clock.now = clock.now.Add(500 * time.Millisecond)
	clock.timers[1].work()
	e.mouseButton(Forward, false)
	assertEvents(t, d, "Escape down", "Escape up")
}

func TestLongPressFocusLossAndInjectionFailuresNeverFallBack(t *testing.T) {
	for _, failure := range []string{"focus-short", "focus-long", "injection", "capture"} {
		t.Run(failure, func(t *testing.T) {
			e, d, focus, clock := longPressEngine(t)
			if failure == "capture" {
				e.captureFocus = func() (inputmethod.Focus, error) { return nil, inputmethod.ErrFocusChanged }
			}
			if !e.mouseButton(Forward, true) {
				t.Fatal("failed action leaked its down")
			}
			if strings.HasPrefix(failure, "focus-") {
				focus.valid.Store(false)
			}
			if failure == "injection" {
				d.fail = "Escape down"
			}
			if failure != "focus-short" && failure != "capture" {
				clock.now = clock.now.Add(500 * time.Millisecond)
				clock.timers[0].work()
			}
			if !e.mouseButton(Forward, false) {
				t.Fatal("failed action leaked its up")
			}
			assertEvents(t, d)
			if e.Status().Error == "" {
				t.Fatal("failed action was not reported")
			}
		})
	}
}

func TestLongPressInputMethodPreparationStartsOnlyForTheChosenAction(t *testing.T) {
	for _, long := range []bool{false, true} {
		t.Run(map[bool]string{false: "short", true: "long"}[long], func(t *testing.T) {
			e, d, focus, clock := longPressEngine(t)
			config := e.config
			config.Forward.InputMethod, config.Forward.InputMethodName = "test:short", "Short IME"
			config.Forward.LongPress = &LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 500, InputMethod: "test:long", InputMethodName: "Long IME"}
			if err := e.Apply(config); err != nil {
				t.Fatal(err)
			}
			e.mouseButton(Forward, true)
			select {
			case id := <-focus.started:
				t.Fatalf("IME %q started on undecided down", id)
			default:
			}
			id, key := "test:short", "Enter"
			if long {
				clock.now = clock.now.Add(500 * time.Millisecond)
				clock.timers[0].work()
				id, key = "test:long", "Escape"
			}
			e.mouseButton(Forward, false)
			expectPreparation(t, focus, id)
			expectPreparedEvents(t, e, d)
			close(focus.ready)
			waitPreparation(t, e)
			expectPreparedEvents(t, e, d, key+" down", key+" up")
			clock.timers[0].work()
			expectPreparedEvents(t, e, d, key+" down", key+" up")
		})
	}
}

func TestLongPressFailureAfterReadinessNeverSendsShortAction(t *testing.T) {
	e, d, focus, clock := longPressEngine(t)
	config := e.config
	config.Forward.LongPress = &LongPressBinding{Keys: []string{"Escape"}, InputMethod: "test:long", ThresholdMS: 500}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	focus.err = context.DeadlineExceeded
	close(focus.ready)
	e.mouseButton(Forward, true)
	clock.now = clock.now.Add(500 * time.Millisecond)
	clock.timers[0].work()
	waitPreparation(t, e)
	e.mouseButton(Forward, false)
	expectPreparedEvents(t, e, d)
	if !strings.Contains(e.Status().Error, "deadline") {
		t.Fatal("preparation error lost")
	}
}

func TestLongPressValidationAndOwnership(t *testing.T) {
	base := Binding{Enabled: true, Keys: []string{"Enter"}, LongPress: &LongPressBinding{Keys: []string{"Escape"}}}
	normalized, err := NormalizeBinding(base)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.LongPress.ThresholdMS != DefaultLongPressThresholdMS {
		t.Fatal("missing threshold did not default")
	}
	normalized.LongPress.Keys[0] = "Tab"
	normalized.LongPress.ThresholdMS = 1000
	if base.LongPress.Keys[0] != "Escape" || base.LongPress.ThresholdMS != 0 {
		t.Fatal("normalization shares caller-owned long action")
	}
	for _, threshold := range []int{-1, 99, 5001} {
		base.LongPress.ThresholdMS = threshold
		if _, err := NormalizeBinding(base); err == nil {
			t.Fatalf("accepted threshold %d", threshold)
		}
	}
	for _, action := range []*LongPressBinding{{}, {Keys: []string{"Invalid"}}, {Keys: []string{"Enter"}, InputMethodName: "name without ID"}} {
		base.LongPress = action
		if _, err := NormalizeBinding(base); err == nil {
			t.Fatalf("accepted invalid action %+v", action)
		}
	}
}

func TestLongTimerDispatchDoesNotHoldMutexOrReplayAfterRelease(t *testing.T) {
	e, d := dispatchedEngine(t)
	focus := newPreparedFocus()
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	clock := &manualPressClock{now: time.Unix(1, 0)}
	clock.install(e)
	config := e.config
	config.Forward.Keys = []string{"Enter"}
	config.Forward.LongPress = &LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 500}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	d.checkUnlocked = true
	d.queue = make(chan *queuedTestInput, 1)
	dispatchedMouse(t, e, d, Forward, true)
	clock.now = clock.now.Add(500 * time.Millisecond)
	done := make(chan struct{})
	go func() { clock.timers[0].work(); close(done) }()
	queued := nextQueuedInput(t, e, d)
	// The release wins the native queue race, but its elapsed duration is long.
	dispatchedMouse(t, e, d, Forward, false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled timer dispatcher did not return")
	}
	if err := d.execute(queued.work); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, d.fakeDriver, "Escape down", "Escape up")
}

func TestQueuedLongTimerCannotReplayAfterSettingsChange(t *testing.T) {
	e, d := dispatchedEngine(t)
	focus := newPreparedFocus()
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	clock := &manualPressClock{now: time.Unix(1, 0)}
	clock.install(e)
	config := e.config
	config.Forward.LongPress = &LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 500}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	d.queue = make(chan *queuedTestInput, 1)
	dispatchedMouse(t, e, d, Forward, true)
	clock.now = clock.now.Add(time.Second)
	done := make(chan struct{})
	go func() { clock.timers[0].work(); close(done) }()
	queued := nextQueuedInput(t, e, d)
	config.Enabled = false
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("settings edit did not cancel queued timer")
	}
	if err := d.execute(queued.work); err != nil {
		t.Fatal(err)
	}
	dispatchedMouse(t, e, d, Forward, false)
	assertEvents(t, d.fakeDriver)
}

func TestLongActionDoesNotReleaseAnotherButtonsHeldModifier(t *testing.T) {
	e, d, _, clock := longPressEngine(t)
	config := e.config
	config.Back.Keys = []string{"Control"}
	config.Forward.LongPress = &LongPressBinding{Keys: []string{"Control", "Escape"}, ThresholdMS: 500}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	e.mouseButton(Back, true)
	e.mouseButton(Forward, true)
	clock.now = clock.now.Add(500 * time.Millisecond)
	clock.timers[0].work()
	e.mouseButton(Forward, false)
	assertEvents(t, d, "Control down", "Escape down", "Escape up")
	e.mouseButton(Back, false)
	assertEvents(t, d, "Control down", "Escape down", "Escape up", "Control up")
}

func TestDisabledLongMappingDoesNotCaptureOrConsumeNewInput(t *testing.T) {
	e, d, _, _ := longPressEngine(t)
	config := e.config
	config.Forward.Enabled = false
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	e.captureFocus = func() (inputmethod.Focus, error) { t.Fatal("disabled long mapping captured focus"); return nil, nil }
	if e.mouseButton(Forward, true) || e.mouseButton(Forward, false) {
		t.Fatal("disabled mapping swallowed new input")
	}
	assertEvents(t, d)
}
