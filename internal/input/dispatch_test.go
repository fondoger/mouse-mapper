package input

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type queuedTestInput struct {
	work   func() error
	result chan error
}

type dispatchTestDriver struct {
	*fakeDriver
	engine         *Engine
	checkUnlocked  bool
	queue          chan *queuedTestInput
	rejectPrepared error
	rejectNext     error
	dispatches     atomic.Int32
	executing      atomic.Int32
}

func (d *dispatchTestDriver) runOnInputThread(ctx context.Context, work func() error) error {
	d.dispatches.Add(1)
	if err := d.rejectNext; err != nil {
		d.rejectNext = nil
		return err
	}
	if d.checkUnlocked {
		if !d.engine.mu.TryLock() {
			return errors.New("input work was dispatched with the engine mutex held")
		}
		d.engine.mu.Unlock()
	}
	if ctx.Done() != nil {
		if d.rejectPrepared != nil {
			return d.rejectPrepared
		}
		if d.queue != nil {
			request := &queuedTestInput{work: work, result: make(chan error, 1)}
			d.queue <- request
			select {
			case err := <-request.result:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return d.execute(work)
}

func (d *dispatchTestDriver) execute(work func() error) error {
	d.executing.Add(1)
	defer d.executing.Add(-1)
	return work()
}

func (d *dispatchTestDriver) sendKey(code string, down bool, modifiers []string) error {
	if d.executing.Load() == 0 {
		return errors.New("key injection ran outside the input dispatcher")
	}
	return d.fakeDriver.sendKey(code, down, modifiers)
}

func dispatchedEngine(t *testing.T) (*Engine, *dispatchTestDriver) {
	t.Helper()
	e := New(nil)
	d := &dispatchTestDriver{fakeDriver: &fakeDriver{physical: map[string]bool{}}, engine: e}
	e.driver = d
	if err := e.Apply(Config{Enabled: true, Back: Binding{Enabled: true, Keys: []string{"Control"}}, Forward: Binding{Enabled: true, Keys: []string{"KeyV"}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	return e, d
}

func dispatchedMouse(t *testing.T, e *Engine, d *dispatchTestDriver, button string, down bool) {
	t.Helper()
	if err := d.execute(func() error {
		if !e.mouseButton(button, down) {
			return errors.New("mapped mouse input was not swallowed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func queuedPreparation(t *testing.T) (*Engine, *dispatchTestDriver, *preparedFocus) {
	t.Helper()
	e, d := dispatchedEngine(t)
	focus := newPreparedFocus()
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	config := e.config
	config.Back.InputMethod, config.Back.InputMethodName = "test:wechat", "WeChat"
	config.Forward.InputMethod, config.Forward.InputMethodName = "test:doubao", "Doubao"
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	d.queue = make(chan *queuedTestInput, maxPrepared)
	t.Cleanup(func() {
		_ = e.Stop()
		waitPreparation(t, e)
	})
	return e, d, focus
}

func nextQueuedInput(t *testing.T, e *Engine, d *dispatchTestDriver) *queuedTestInput {
	t.Helper()
	select {
	case request := <-d.queue:
		if !e.mu.TryLock() {
			t.Fatal("prepared worker waits on the input thread while holding the engine mutex")
		}
		e.mu.Unlock()
		return request
	case <-time.After(time.Second):
		t.Fatal("prepared input was not dispatched")
		return nil
	}
}

func TestLifecycleKeyReleasesRunOnInputThread(t *testing.T) {
	e, d := dispatchedEngine(t)
	d.checkUnlocked = true
	dispatchedMouse(t, e, d, Back, true)
	config := e.config
	config.Back.Keys = []string{"Alt"}
	if err := e.Apply(config); err != nil {
		t.Fatal(err)
	}
	dispatchedMouse(t, e, d, Back, false)
	dispatchedMouse(t, e, d, Back, true)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	// Failed releases retained from an old native session are also cleaned up
	// through the dispatcher before a replacement hook can be started.
	e.references["Shift"] = &keyReference{owned: true}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, d.fakeDriver, "Control down", "Control up", "Alt down", "Alt up", "Shift up")
	if got := d.dispatches.Load(); got != 5 {
		t.Fatalf("lifecycle operations dispatched %d times, want 5", got)
	}
}

func TestPreparedCompletionWaitsForInputThreadBeforeNextSwitch(t *testing.T) {
	e, d, focus := queuedPreparation(t)
	for _, button := range []string{Back, Forward} {
		dispatchedMouse(t, e, d, button, true)
		dispatchedMouse(t, e, d, button, false)
	}
	expectPreparation(t, focus, "test:wechat")
	close(focus.ready)
	first := nextQueuedInput(t, e, d)
	expectPreparedEvents(t, e, d.fakeDriver)
	select {
	case target := <-focus.started:
		t.Fatalf("switched to %s before the previous shortcut was delivered", target)
	default:
	}
	result := d.execute(first.work)
	expectPreparedEvents(t, e, d.fakeDriver, "Control down", "Control up")
	select {
	case target := <-focus.started:
		t.Fatalf("switched to %s before dispatch completion returned", target)
	default:
	}
	first.result <- result
	expectPreparation(t, focus, "test:doubao")
	second := nextQueuedInput(t, e, d)
	second.result <- d.execute(second.work)
	waitPreparation(t, e)
	expectPreparedEvents(t, e, d.fakeDriver, "Control down", "Control up", "KeyV down", "KeyV up")
	if status := e.Status(); status.Error != "" {
		t.Fatal(status.Error)
	}
}

func TestStopRetriesRejectedCleanupOnlyAfterNativeShutdown(t *testing.T) {
	for _, nativeStopFails := range []bool{false, true} {
		name := "successful stop"
		if nativeStopFails {
			name = "failed stop"
		}
		t.Run(name, func(t *testing.T) {
			e, d := dispatchedEngine(t)
			dispatchedMouse(t, e, d, Back, true)
			d.rejectNext = context.DeadlineExceeded
			if nativeStopFails {
				d.stopErr = errors.New("test hook still active")
			}
			err := e.Stop()
			if nativeStopFails {
				if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, d.stopErr) {
					t.Fatalf("Stop = %v, want dispatch and shutdown failures", err)
				}
				assertEvents(t, d.fakeDriver, "Control down")
				d.stopErr = nil
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertEvents(t, d.fakeDriver, "Control down", "Control up")
			}
		})
	}
}

func TestQueuedPreparedCompletionCannotReplayAfterLifecycleChange(t *testing.T) {
	for _, operation := range []string{"edit", "stop", "restart"} {
		t.Run(operation, func(t *testing.T) {
			e, d, focus := queuedPreparation(t)
			close(focus.ready)
			dispatchedMouse(t, e, d, Back, true)
			dispatchedMouse(t, e, d, Back, false)
			request := nextQueuedInput(t, e, d)
			if operation == "edit" {
				config := e.config
				config.Back.InputMethod = "test:replacement"
				if err := e.Apply(config); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := e.Stop(); err != nil {
					t.Fatal(err)
				}
				if operation == "restart" {
					if err := e.Start(); err != nil {
						t.Fatal(err)
					}
				}
			}
			waitPreparation(t, e)
			before := e.Status()
			// Even a backend that invokes an old closure after rejecting its wait
			// cannot revive a request removed by a settings edit or shutdown.
			if err := d.execute(request.work); err != nil {
				t.Fatal(err)
			}
			expectPreparedEvents(t, e, d.fakeDriver)
			if after := e.Status(); after != before || after.Error != "" {
				t.Fatalf("cancelled completion changed status: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestRejectedPreparedDispatchNeverFallsBackToWorkerInjection(t *testing.T) {
	e, d, focus := queuedPreparation(t)
	d.rejectPrepared = errors.New("test input thread closed")
	close(focus.ready)
	dispatchedMouse(t, e, d, Back, true)
	waitPreparation(t, e)
	dispatchedMouse(t, e, d, Back, false)
	expectPreparedEvents(t, e, d.fakeDriver)
	if status := e.Status(); !strings.Contains(status.Error, d.rejectPrepared.Error()) {
		t.Fatalf("dispatch rejection was not reported: %+v", status)
	}
}

func TestDispatchedStopDoesNotJoinUnresponsiveInputMethodWorker(t *testing.T) {
	e, d, _ := queuedPreparation(t)
	focus := &cancellationIgnoringFocus{started: make(chan context.Context, 1), resume: make(chan struct{})}
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	defer close(focus.resume)
	dispatchedMouse(t, e, d, Back, true)
	var workerContext context.Context
	select {
	case workerContext = <-focus.started:
	case <-time.After(time.Second):
		t.Fatal("input-method switch did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the input-method worker")
	}
	if !errors.Is(workerContext.Err(), context.Canceled) || e.Status().Running {
		t.Fatal("Stop did not cancel preparation and stop the listener")
	}
	expectPreparedEvents(t, e, d.fakeDriver)
}
