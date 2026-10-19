//go:build windows

package input

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The dispatcher tests use a private thread message queue only. They install no
// hooks, inject no keys, and do not change the foreground window or input method.
func windowsDispatchQueue(t *testing.T) (*windowsDriver, *windowsInputSession) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	var message windowsMessage
	peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 0)
	d := newNativeDriver(nil).(*windowsDriver)
	session := &windowsInputSession{
		id: 1, thread: windows.GetCurrentThreadId(), ready: make(chan struct{}),
		done: make(chan struct{}), accepting: true,
	}
	close(session.ready)
	d.session = session
	t.Cleanup(func() {
		d.mu.Lock()
		current := d.session
		d.rejectInputTasksLocked(current)
		current.thread = 0
		select {
		case <-current.done:
		default:
			close(current.done)
		}
		d.mu.Unlock()
		for {
			found, _, _ := peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, windowsInputWorkMessage, windowsInputStopMessage, 1)
			if found == 0 {
				break
			}
		}
	})
	return d, session
}

func queuedWindowsWork(t *testing.T, d *windowsDriver, ctx context.Context, work func() error) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- d.runOnInputThread(ctx, work) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		queued := len(d.session.tasks) > 0
		d.mu.Unlock()
		if queued {
			return result
		}
		select {
		case err := <-result:
			t.Fatalf("input work returned before being queued: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("input work was not queued")
	return result
}

func windowsWorkResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("input dispatcher did not return")
		return nil
	}
}

func nextWindowsWorkMessage(t *testing.T) windowsMessage {
	t.Helper()
	var message windowsMessage
	found, _, _ := peekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, windowsInputWorkMessage, windowsInputStopMessage, 1)
	if found == 0 {
		t.Fatal("queued input work did not post its wake message")
	}
	return message
}

func TestWindowsDispatcherRunsOnMessageThreadAndAllowsInlineWork(t *testing.T) {
	d, session := windowsDispatchQueue(t)
	wantThread := windows.GetCurrentThreadId()
	var actualThread, nestedThread uint32
	result := queuedWindowsWork(t, d, context.Background(), func() error {
		actualThread = windows.GetCurrentThreadId()
		return d.runOnInputThread(context.Background(), func() error {
			nestedThread = windows.GetCurrentThreadId()
			return nil
		})
	})
	if stop := d.handleInputMessage(session, nextWindowsWorkMessage(t)); stop {
		t.Fatal("work message requested shutdown")
	}
	if err := windowsWorkResult(t, result); err != nil {
		t.Fatal(err)
	}
	if actualThread != wantThread || nestedThread != wantThread {
		t.Fatalf("dispatched threads = %d/%d, want %d", actualThread, nestedThread, wantThread)
	}
	if err := d.stop(); err == nil {
		t.Fatal("synchronous stop from the input thread was not rejected")
	}
}

func TestWindowsDispatcherCancelsQueuedWorkWithoutLateExecution(t *testing.T) {
	d, session := windowsDispatchQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var called atomic.Bool
	result := queuedWindowsWork(t, d, ctx, func() error { called.Store(true); return nil })
	cancel()
	if err := windowsWorkResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dispatch = %v", err)
	}
	d.handleInputMessage(session, nextWindowsWorkMessage(t))
	if called.Load() || len(session.tasks) != 0 {
		t.Fatal("cancelled task survived its wait or ran on a stale wake message")
	}
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	if err := d.runOnInputThread(expired, func() error { called.Store(true); return nil }); !errors.Is(err, context.DeadlineExceeded) || called.Load() {
		t.Fatalf("expired inline dispatch ran work or returned %v", err)
	}
}

func TestWindowsDispatcherDoesNotDetachAlreadyRunningWorkOnCancellation(t *testing.T) {
	d, session := windowsDispatchQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	workErr := errors.New("completed input work")
	result := queuedWindowsWork(t, d, ctx, func() error {
		close(entered)
		<-release
		return workErr
	})
	// No native APIs run in this callback, so a helper can model a busy hook
	// while the test cancels its waiter and controls the callback's completion.
	go d.runInputTask(session)
	<-entered
	cancel()
	select {
	case err := <-result:
		close(release)
		t.Fatalf("running input work was detached on cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := windowsWorkResult(t, result); !errors.Is(err, workErr) {
		t.Fatalf("running dispatch = %v, want its completion result", err)
	}
}

func TestWindowsDispatcherRejectsOldSessionTasksAndWakeMessages(t *testing.T) {
	d, previous := windowsDispatchQueue(t)
	var oldCalled, newCalled atomic.Bool
	oldResult := queuedWindowsWork(t, d, context.Background(), func() error { oldCalled.Store(true); return nil })
	d.rejectInputTasks(previous)
	if err := windowsWorkResult(t, oldResult); !errors.Is(err, errWindowsInputStopped) {
		t.Fatalf("retired-session dispatch = %v", err)
	}
	d.mu.Lock()
	previous.thread = 0
	close(previous.done)
	current := &windowsInputSession{
		id: previous.id + 1, thread: windows.GetCurrentThreadId(), ready: make(chan struct{}),
		done: make(chan struct{}), accepting: true,
	}
	close(current.ready)
	d.session = current
	d.mu.Unlock()
	newResult := queuedWindowsWork(t, d, context.Background(), func() error { newCalled.Store(true); return nil })
	if stop := d.handleInputMessage(current, nextWindowsWorkMessage(t)); stop || newCalled.Load() {
		t.Fatal("an old session's wake consumed new-session work")
	}
	if stop := d.handleInputMessage(current, windowsMessage{Message: windowsInputStopMessage, WParam: previous.id}); stop {
		t.Fatal("an old stop message stopped the replacement session")
	}
	d.handleInputMessage(current, nextWindowsWorkMessage(t))
	if err := windowsWorkResult(t, newResult); err != nil {
		t.Fatal(err)
	}
	if oldCalled.Load() || !newCalled.Load() {
		t.Fatal("input work crossed native session boundaries")
	}
}

func TestWindowsDispatcherPostingFailureRemovesTask(t *testing.T) {
	d, session := windowsDispatchQueue(t)
	session.thread = ^uint32(0) // No Windows thread owns this ID or its message queue.
	var called bool
	if err := d.runOnInputThread(context.Background(), func() error { called = true; return nil }); err == nil {
		t.Fatal("posting to a missing input thread succeeded")
	}
	if len(session.tasks) != 0 || called {
		t.Fatal("failed wake posting left a task that could execute later")
	}
}

func TestWindowsOfflineInputHoldsStartupGateButNotQueueMutex(t *testing.T) {
	d := newNativeDriver(nil).(*windowsDriver)
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- d.runOnInputThread(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	gateHeld := !d.transition.TryLock()
	if !gateHeld {
		d.transition.Unlock()
	}
	queueFree := d.mu.TryLock()
	if queueFree {
		d.mu.Unlock()
	}
	close(release)
	if err := windowsWorkResult(t, result); err != nil {
		t.Fatal(err)
	}
	if !gateHeld || !queueFree {
		t.Fatalf("offline work has startup gate=%v, queue mutex free=%v", gateHeld, queueFree)
	}
}
