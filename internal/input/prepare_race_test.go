package input

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type cancellationIgnoringFocus struct {
	started chan context.Context
	resume  chan struct{}
	result  error
}

func (*cancellationIgnoringFocus) Valid() bool { return true }

func (focus *cancellationIgnoringFocus) Switch(ctx context.Context, _ string) error {
	focus.started <- ctx
	<-focus.resume
	return focus.result
}

func TestInputMethodCancelledWorkerDrainsFreshRequestWithoutStaleOutput(t *testing.T) {
	for _, oldResult := range []error{nil, errors.New("stale switch failure")} {
		name := "late success"
		if oldResult != nil {
			name = "late failure"
		}
		t.Run(name, func(t *testing.T) {
			e, d, _ := preparedEngine(t)
			old := &cancellationIgnoringFocus{
				started: make(chan context.Context, 1),
				resume:  make(chan struct{}),
				result:  oldResult,
			}
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(old.resume) }) }
			t.Cleanup(resume)
			e.mu.Lock()
			e.captureFocus = func() (inputmethod.Focus, error) { return old, nil }
			e.mu.Unlock()
			if !e.mouseButton(Back, true) {
				t.Fatal("old press was not queued")
			}
			var oldContext context.Context
			select {
			case oldContext = <-old.started:
			case <-time.After(time.Second):
				t.Fatal("old switch did not begin")
			}

			e.mu.Lock()
			settings := e.config
			e.mu.Unlock()
			settings.Back.InputMethod = "test:replacement"
			settings.Back.Keys = []string{"Alt", "KeyA"}
			if err := e.Apply(settings); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(oldContext.Err(), context.Canceled) {
				t.Fatal("settings edit did not cancel the old switch")
			}
			if !e.mouseButton(Back, false) {
				t.Fatal("old physical release escaped after cancellation")
			}

			fresh := newPreparedFocus()
			e.mu.Lock()
			e.captureFocus = func() (inputmethod.Focus, error) { return fresh, nil }
			e.mu.Unlock()
			if !e.mouseButton(Back, true) || !e.mouseButton(Back, false) {
				t.Fatal("fresh short click was not queued")
			}
			e.mu.Lock()
			workerRunning := e.prepareWorker
			queued, tracked := len(e.prepareQueue), len(e.preparing)
			e.mu.Unlock()
			if !workerRunning || queued != 1 || tracked != 1 {
				t.Fatalf("fresh request was lost behind cancelled worker: running=%v queued=%d tracked=%d", workerRunning, queued, tracked)
			}
			select {
			case <-fresh.started:
				t.Fatal("started a second worker while the old switch still ran")
			default:
			}
			expectPreparedEvents(t, e, d)

			resume()
			expectPreparation(t, fresh, "test:replacement")
			expectPreparedEvents(t, e, d)
			if status := e.Status(); status.Error != "" {
				t.Fatalf("stale completion contaminated pending status: %s", status.Error)
			}
			close(fresh.ready)
			waitPreparation(t, e)
			expectPreparedEvents(t, e, d, "Alt down", "KeyA down", "KeyA up", "Alt up")
			if status := e.Status(); status.Error != "" {
				t.Fatalf("stale completion contaminated fresh status: %s", status.Error)
			}
		})
	}
}

func TestInputMethodPreparedHoldsShareModifierOwnership(t *testing.T) {
	for _, firstRelease := range []string{Back, Forward} {
		t.Run(firstRelease+" released first", func(t *testing.T) {
			e, d, focus := preparedEngine(t)
			e.mu.Lock()
			settings := e.config
			e.mu.Unlock()
			settings.Forward.Keys = []string{"Control", "KeyV"}
			if err := e.Apply(settings); err != nil {
				t.Fatal(err)
			}
			close(focus.ready)
			if !e.mouseButton(Back, true) {
				t.Fatal("back press was not queued")
			}
			waitPreparation(t, e)
			expectPreparedEvents(t, e, d, "Control down", "KeyC down")
			if !e.mouseButton(Forward, true) {
				t.Fatal("forward press was not queued")
			}
			waitPreparation(t, e)
			expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyV down")
			assertPreparedControlReference(t, e, 2)

			if !e.mouseButton(firstRelease, false) {
				t.Fatal("first release escaped")
			}
			assertPreparedControlReference(t, e, 1)
			lastRelease := Forward
			firstKey, lastKey := "KeyC", "KeyV"
			if firstRelease == Forward {
				lastRelease = Back
				firstKey, lastKey = "KeyV", "KeyC"
			}
			expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyV down", firstKey+" up")
			if !e.mouseButton(lastRelease, false) {
				t.Fatal("last release escaped")
			}
			expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyV down", firstKey+" up", lastKey+" up", "Control up")
			e.mu.Lock()
			active, references := len(e.active), len(e.references)
			e.mu.Unlock()
			if active != 0 || references != 0 {
				t.Fatalf("prepared holds leaked ownership: active=%d references=%d", active, references)
			}
		})
	}
}

func assertPreparedControlReference(t *testing.T, e *Engine, want int) {
	t.Helper()
	e.mu.Lock()
	count, owned := 0, false
	if ref := e.references["Control"]; ref != nil {
		count, owned = ref.count, ref.owned
	}
	e.mu.Unlock()
	if count != want || !owned {
		t.Fatalf("Control ownership = count:%d owned:%v, want count:%d owned:true", count, owned, want)
	}
}

func TestInputMethodExpiredQueuedRequestNeverSwitches(t *testing.T) {
	e, d, focus := preparedEngine(t)
	if !e.mouseButton(Back, true) || !e.mouseButton(Back, false) {
		t.Fatal("first short click was not queued")
	}
	expectPreparation(t, focus, "test:wechat")
	if !e.mouseButton(Forward, true) || !e.mouseButton(Forward, false) {
		t.Fatal("second short click was not queued")
	}

	// The first switch holds the FIFO, so changing the second request's context
	// under the engine mutex cannot race a worker. Avoid a real two-second wait.
	e.mu.Lock()
	if len(e.prepareQueue) != 1 {
		e.mu.Unlock()
		t.Fatal("expected one queued request behind the blocked switch")
	}
	queued := e.prepareQueue[0]
	queued.cancel()
	queued.ctx, queued.cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	e.mu.Unlock()
	close(focus.ready)
	waitPreparation(t, e)

	expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyC up", "Control up")
	select {
	case target := <-focus.started:
		t.Fatalf("expired request switched input method to %q", target)
	default:
	}
	if status := e.Status(); !strings.Contains(status.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("expired request did not report its deadline: %s", status.Error)
	}
}
