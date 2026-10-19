package input

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

const (
	prepareTimeout = 2 * time.Second
	maxPrepared    = 32
)

type preparedPress struct {
	button  string
	binding Binding
	focus   inputmethod.Focus
	ctx     context.Context
	cancel  context.CancelFunc
	down    bool
}

// Only capture the foreground target inside the OS hook. Switching input
// methods may involve IPC, so a bounded FIFO performs it outside the hook.
func (e *Engine) preparePressLocked(button string, binding Binding) bool {
	if len(e.preparing) >= maxPrepared {
		e.preparationErrorLocked(errors.New("too many pending input-method changes; shortcut skipped"))
		return false
	}
	focus, err := e.captureFocus()
	if err != nil || focus == nil {
		if err == nil {
			err = inputmethod.ErrFocusChanged
		}
		e.preparationErrorLocked(err)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), prepareTimeout)
	press := &preparedPress{button: button, binding: binding, focus: focus, ctx: ctx, cancel: cancel, down: true}
	if e.preparing == nil {
		e.preparing = make(map[*preparedPress]struct{})
		e.pendingButton = make(map[string]*preparedPress)
	}
	e.preparing[press] = struct{}{}
	e.pendingButton[button] = press
	e.active[button] = nil
	e.prepareQueue = append(e.prepareQueue, press)
	if !e.prepareWorker {
		e.prepareWorker = true
		go e.runPrepared()
	}
	return true
}

func (e *Engine) runPrepared() {
	for {
		e.mu.Lock()
		if len(e.prepareQueue) == 0 {
			e.prepareWorker = false
			e.mu.Unlock()
			return
		}
		press := e.prepareQueue[0]
		e.prepareQueue[0] = nil
		e.prepareQueue = e.prepareQueue[1:]
		e.mu.Unlock()

		err := press.ctx.Err()
		if err == nil && !press.focus.Valid() {
			err = inputmethod.ErrFocusChanged
		}
		if err == nil {
			err = press.focus.Switch(press.ctx, press.binding.InputMethod)
		}
		e.finishPrepared(press, err)
		press.cancel()
	}
}

func (e *Engine) finishPrepared(press *preparedPress, err error) {
	dispatchErr := e.onInputThread(press.ctx, func() error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.finishPreparedLocked(press, err)
		return nil
	})
	if dispatchErr == nil {
		return
	}
	// A rejected or expired task must only discard its request, never retry
	// injection on this worker. Lifecycle cancellation has already removed it.
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.removePreparedLocked(press) {
		e.preparationErrorLocked(errors.Join(err, dispatchErr))
	}
}

func (e *Engine) finishPreparedLocked(press *preparedPress, err error) {
	if !e.removePreparedLocked(press) {
		return
	}
	if err == nil {
		err = press.ctx.Err()
	}
	if err == nil && !press.focus.Valid() {
		err = inputmethod.ErrFocusChanged
	}
	if err != nil {
		e.preparationErrorLocked(err)
		return
	}
	keys, err := e.pressKeysLocked(press.binding.Keys)
	if err != nil {
		e.preparationErrorLocked(err)
		return
	}
	if press.down {
		e.active[press.button] = keys
	} else {
		// A short click may finish while the IME is changing. Deliver one complete
		// tap after readiness, rather than dropping it or leaving the keys held.
		if err := e.releaseKeysLocked(keys); err != nil {
			e.preparationErrorLocked(err)
			return
		}
	}
	e.triggerLocked(press.button, press.binding.Keys)
}

func (e *Engine) removePreparedLocked(press *preparedPress) bool {
	if _, exists := e.preparing[press]; !exists {
		return false // A settings edit, pause, native failure or quit cancelled this work.
	}
	delete(e.preparing, press)
	if e.pendingButton[press.button] == press {
		delete(e.pendingButton, press.button)
	}
	return true
}

func (e *Engine) cancelPreparedLocked() {
	for press := range e.preparing {
		press.cancel()
	}
	clear(e.preparing)
	clear(e.pendingButton)
	e.prepareQueue = nil
}

func (e *Engine) preparationErrorLocked(err error) {
	e.status.Error = fmt.Sprintf("Input method preparation failed; shortcut skipped: %v", err)
	e.status.Message = e.status.Error
}
