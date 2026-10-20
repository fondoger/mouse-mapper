package input

import (
	"context"
	"fmt"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

type pressTimer interface{ Stop() bool }

type timedPress struct {
	button   string
	binding  Binding
	focus    inputmethod.Focus
	deadline time.Time
	timer    pressTimer
	ctx      context.Context
	cancel   context.CancelFunc
	decided  bool
}

func (e *Engine) beginTimedPressLocked(button string, binding Binding) {
	focus, err := e.captureFocus()
	if err != nil || focus == nil {
		if err == nil {
			err = inputmethod.ErrFocusChanged
		}
		e.deferredErrorLocked(err)
		return
	}
	delay := time.Duration(binding.LongPress.ThresholdMS) * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	press := &timedPress{button: button, binding: binding, focus: focus,
		deadline: e.now().Add(delay), ctx: ctx, cancel: cancel}
	e.timedPresses[button] = press
	press.timer = e.afterFunc(delay, func() { e.longPressElapsed(press) })
}

func (e *Engine) longPressElapsed(press *timedPress) {
	// Dispatch without the mutex: Windows owns synthetic output on its hook
	// thread. Identity and cancellation checks also reject already queued timers.
	err := e.onInputThread(press.ctx, func() error {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.timedPresses[press.button] != press || press.decided || press.ctx.Err() != nil {
			return nil
		}
		if remaining := press.deadline.Sub(e.now()); remaining > 0 {
			press.timer = e.afterFunc(remaining, func() { e.longPressElapsed(press) })
			return nil
		}
		e.decideTimedPressLocked(press, true)
		return nil
	})
	if err != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.timedPresses[press.button] == press && !press.decided && press.ctx.Err() == nil {
			press.decided = true
			e.deferredErrorLocked(err)
		}
	}
}

func (e *Engine) releaseTimedPressLocked(press *timedPress) {
	if press.timer != nil {
		press.timer.Stop()
	}
	if !press.decided {
		// A busy event loop may process the release before the timer callback.
		// Elapsed monotonic time, not callback order, decides which action wins.
		e.decideTimedPressLocked(press, !e.now().Before(press.deadline))
	}
	delete(e.timedPresses, press.button)
	press.cancel()
}

func (e *Engine) decideTimedPressLocked(press *timedPress, long bool) {
	press.decided = true
	if !press.focus.Valid() {
		e.deferredErrorLocked(inputmethod.ErrFocusChanged)
		return
	}
	binding := press.binding
	if long {
		action := binding.LongPress
		binding = Binding{Enabled: true, Keys: action.Keys, InputMethod: action.InputMethod, InputMethodName: action.InputMethodName}
	}
	if binding.InputMethod != "" {
		// Retain the focus captured on mouse-down, but a fresh preparation
		// context lets a decided tap finish after the physical button is up.
		e.prepareActionLocked(press.button, binding, press.focus, false)
		return
	}
	keys, err := e.pressKeysLocked(binding.Keys)
	if err == nil {
		err = e.releaseKeysLocked(keys)
	}
	if err != nil {
		e.deferredErrorLocked(err)
		return
	}
	e.triggerLocked(press.button, binding.Keys)
}

func (e *Engine) cancelPendingLocked() {
	e.cancelPreparedLocked()
	for button, press := range e.timedPresses {
		if press.timer != nil {
			press.timer.Stop()
		}
		press.cancel()
		delete(e.timedPresses, button)
	}
}

func (e *Engine) deferredErrorLocked(err error) {
	e.status.Error = fmt.Sprintf("Deferred mapping failed; shortcut skipped: %v", err)
	e.status.Message = e.status.Error
}
