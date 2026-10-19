package desktop

import (
	"errors"
	"testing"
	"time"
)

func TestBeforeQuitStopsInputBeforeQueuedUIWork(t *testing.T) {
	mapper, _, engine, _ := mapperFixture(t)
	app := &application{mapper: mapper, done: make(chan struct{})}
	app.overlayTimer = time.NewTimer(time.Hour)
	t.Cleanup(func() { app.overlayTimer.Stop() })
	app.beginQuit()
	if !app.quitting.Load() || !mapper.stopping.Load() || engine.Status().Running {
		t.Fatal("quit preparation returned before input shutdown")
	}
	select {
	case <-app.done:
	default:
		t.Fatal("quit preparation did not stop overlay delivery")
	}
	if app.overlayTimer.Stop() {
		t.Fatal("quit preparation left the overlay timer active")
	}
	if mapper.RetryInput().Status.Running {
		t.Fatal("queued UI work restarted input after quit preparation")
	}
	if _, err := mapper.SetRecording(false); !errors.Is(err, errStopping) {
		t.Fatalf("queued recording completion should fail after quit preparation: %v", err)
	}
	app.beginQuit()
	if err := mapper.stop(); err != nil {
		t.Fatalf("OnQuit must be safe after early shutdown: %v", err)
	}
}
