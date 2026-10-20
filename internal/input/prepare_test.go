package input

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type preparedFocus struct {
	valid   atomic.Bool
	started chan string
	ready   chan struct{}
	err     error
}

func newPreparedFocus() *preparedFocus {
	f := &preparedFocus{started: make(chan string, maxPrepared), ready: make(chan struct{})}
	f.valid.Store(true)
	return f
}

func (f *preparedFocus) Valid() bool { return f.valid.Load() }
func (f *preparedFocus) Switch(ctx context.Context, id string) error {
	f.started <- id
	select {
	case <-f.ready:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func preparedEngine(t *testing.T) (*Engine, *fakeDriver, *preparedFocus) {
	t.Helper()
	e, d := testEngine(t, []string{"Control", "KeyC"}, []string{"KeyV"})
	focus := newPreparedFocus()
	e.captureFocus = func() (inputmethod.Focus, error) { return focus, nil }
	settings := e.config
	settings.Back.InputMethod, settings.Back.InputMethodName = "test:wechat", "WeChat"
	settings.Forward.InputMethod, settings.Forward.InputMethodName = "test:doubao", "Doubao"
	if err := e.Apply(settings); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = e.Stop()
		waitPreparation(t, e)
	})
	return e, d, focus
}

func waitPreparation(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		idle := !e.prepareWorker
		e.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("input-method worker did not finish")
}

func expectPreparation(t *testing.T, f *preparedFocus, id string) {
	t.Helper()
	select {
	case got := <-f.started:
		if got != id {
			t.Fatalf("switch target = %q, want %q", got, id)
		}
	case <-time.After(time.Second):
		t.Fatal("input-method switch did not start")
	}
}

func expectPreparedEvents(t *testing.T, e *Engine, d *fakeDriver, want ...string) {
	t.Helper()
	e.mu.Lock()
	got := slices.Clone(d.events)
	e.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestInputMethodHoldWaitsForReadiness(t *testing.T) {
	e, d, focus := preparedEngine(t)
	if !e.mouseButton(Back, true) {
		t.Fatal("prepared press should be swallowed")
	}
	expectPreparation(t, focus, "test:wechat")
	expectPreparedEvents(t, e, d)
	if !e.mouseButton(Back, true) {
		t.Fatal("duplicate hardware down should be swallowed")
	}
	close(focus.ready)
	waitPreparation(t, e)
	expectPreparedEvents(t, e, d, "Control down", "KeyC down")
	if !e.mouseButton(Back, false) {
		t.Fatal("release should be swallowed")
	}
	expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyC up", "Control up")
}

func TestInputMethodQuickTapIsNotLost(t *testing.T) {
	e, d, focus := preparedEngine(t)
	triggered := make(chan Trigger, 1)
	e.onTrigger = func(event Trigger) { triggered <- event }
	e.mouseButton(Back, true)
	expectPreparation(t, focus, "test:wechat")
	if !e.mouseButton(Back, false) {
		t.Fatal("pending short-click release escaped")
	}
	expectPreparedEvents(t, e, d)
	close(focus.ready)
	waitPreparation(t, e)
	expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyC up", "Control up")
	select {
	case event := <-triggered:
		if event.Button != Back || !slices.Equal(event.Keys, []string{"Control", "KeyC"}) {
			t.Fatalf("wrong trigger: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("completed shortcut did not notify the overlay")
	}
}

func TestInputMethodFailureNeverSendsShortcut(t *testing.T) {
	for _, failure := range []error{inputmethod.ErrUnavailable, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			e, d, focus := preparedEngine(t)
			focus.err = failure
			close(focus.ready)
			e.mouseButton(Back, true)
			waitPreparation(t, e)
			expectPreparedEvents(t, e, d)
			if !e.mouseButton(Back, false) || !strings.Contains(e.Status().Error, failure.Error()) {
				t.Fatal("failure did not preserve swallowed release or report its cause")
			}
		})
	}
}

func TestInputMethodFocusChangeCancelsShortcut(t *testing.T) {
	e, d, focus := preparedEngine(t)
	e.mouseButton(Back, true)
	expectPreparation(t, focus, "test:wechat")
	focus.valid.Store(false)
	close(focus.ready)
	waitPreparation(t, e)
	e.mouseButton(Back, false)
	expectPreparedEvents(t, e, d)
	if !strings.Contains(e.Status().Error, "foreground") {
		t.Fatal("focus loss was not reported")
	}
}

func TestInputMethodPendingWorkCancelledByLifecycle(t *testing.T) {
	for _, operation := range []string{"edit", "pause", "stop", "native failure"} {
		t.Run(operation, func(t *testing.T) {
			e, d, focus := preparedEngine(t)
			e.mouseButton(Back, true)
			expectPreparation(t, focus, "test:wechat")
			switch operation {
			case "edit":
				settings := e.config
				settings.Back.InputMethod = "test:different-method"
				if err := e.Apply(settings); err != nil {
					t.Fatal(err)
				}
			case "pause":
				if err := e.Apply(Config{}); err != nil {
					t.Fatal(err)
				}
			case "stop":
				if err := e.Stop(); err != nil {
					t.Fatal(err)
				}
			case "native failure":
				e.nativeFailed(errors.New("test native disconnect"))
			}
			waitPreparation(t, e)
			close(focus.ready)
			e.mouseButton(Back, false)
			expectPreparedEvents(t, e, d)
			if strings.Contains(e.Status().Error, "context canceled") {
				t.Fatal("intentional cancellation surfaced as a stale error")
			}
		})
	}
}

func TestInputMethodRapidTapsRemainFIFO(t *testing.T) {
	e, d, focus := preparedEngine(t)
	for _, button := range []string{Back, Forward, Back} {
		e.mouseButton(button, true)
		e.mouseButton(button, false)
	}
	expectPreparation(t, focus, "test:wechat")
	expectPreparedEvents(t, e, d)
	close(focus.ready)
	waitPreparation(t, e)
	expectPreparation(t, focus, "test:doubao")
	expectPreparation(t, focus, "test:wechat")
	expectPreparedEvents(t, e, d,
		"Control down", "KeyC down", "KeyC up", "Control up",
		"KeyV down", "KeyV up",
		"Control down", "KeyC down", "KeyC up", "Control up")
}

func TestInputMethodOldTapDoesNotOverwriteNewHold(t *testing.T) {
	e, d, focus := preparedEngine(t)
	e.mouseButton(Back, true)
	e.mouseButton(Back, false)
	e.mouseButton(Back, true)
	expectPreparation(t, focus, "test:wechat")
	close(focus.ready)
	waitPreparation(t, e)
	expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyC up", "Control up", "Control down", "KeyC down")
	e.mouseButton(Back, false)
	expectPreparedEvents(t, e, d, "Control down", "KeyC down", "KeyC up", "Control up", "Control down", "KeyC down", "KeyC up", "Control up")
}

func TestInputMethodQueueIsBounded(t *testing.T) {
	e, d, focus := preparedEngine(t)
	for i := 0; i < maxPrepared; i++ {
		if !e.mouseButton(Back, true) || !e.mouseButton(Back, false) {
			t.Fatalf("queued tap %d unexpectedly passed through", i)
		}
	}
	expectPreparation(t, focus, "test:wechat")
	if !e.mouseButton(Back, true) || !e.mouseButton(Back, true) || !e.mouseButton(Back, false) {
		t.Fatal("an overflowing mapped request must not fall back to navigation")
	}
	if err := e.Apply(Config{}); err != nil {
		t.Fatal(err)
	}
	waitPreparation(t, e)
	expectPreparedEvents(t, e, d)
}

func TestInputMethodCaptureFailureStillConsumesMappedClick(t *testing.T) {
	for _, button := range []string{Back, Forward} {
		t.Run(button, func(t *testing.T) {
			e, d, _ := preparedEngine(t)
			e.captureFocus = func() (inputmethod.Focus, error) { return nil, inputmethod.ErrFocusChanged }
			if !e.mouseButton(button, true) || !e.mouseButton(button, true) || !e.mouseButton(button, false) {
				t.Fatal("capture failure allowed original navigation")
			}
			expectPreparedEvents(t, e, d)
			if len(e.active) != 0 || !strings.Contains(e.Status().Error, "foreground") {
				t.Fatal("failed capture retained a press or lost its error")
			}
		})
	}
}
