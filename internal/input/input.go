// Package input translates global mouse side-button presses into keyboard input.
package input

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

const (
	Back    = "back"
	Forward = "forward"
)

type Binding struct {
	Enabled         bool              `json:"enabled"`
	Keys            []string          `json:"keys"`
	InputMethod     string            `json:"inputMethod,omitempty"`
	InputMethodName string            `json:"inputMethodName,omitempty"`
	LongPress       *LongPressBinding `json:"longPress,omitempty"`
}

const (
	DefaultLongPressThresholdMS = 500
	MinLongPressThresholdMS     = 100
	MaxLongPressThresholdMS     = 5000
)

// A configured long press selects one action at the threshold or on release.
// Hold keeps the long action's keys down until the physical side button is up.
type LongPressBinding struct {
	Keys            []string `json:"keys"`
	InputMethod     string   `json:"inputMethod,omitempty"`
	InputMethodName string   `json:"inputMethodName,omitempty"`
	ThresholdMS     int      `json:"thresholdMs"`
	Hold            bool     `json:"hold,omitempty"`
}

type Config struct {
	Enabled bool    `json:"enabled"`
	Back    Binding `json:"back"`
	Forward Binding `json:"forward"`
}

type Trigger struct {
	Button string   `json:"button"`
	Keys   []string `json:"keys"`
}

type Status struct {
	Platform  string `json:"platform"`
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
	Message   string `json:"message"`
	Error     string `json:"error,omitempty"`
}

// The generic modifier names intentionally map to the left-hand native keys.
// This lets a user's physically held right-hand modifier remain independent.
var modifiers = []string{"Control", "Alt", "Shift", "Meta"}

var keyCodes = func() map[string]bool {
	codes := map[string]bool{}
	for _, code := range modifiers {
		codes[code] = true
	}
	for c := 'A'; c <= 'Z'; c++ {
		codes["Key"+string(c)] = true
	}
	for c := '0'; c <= '9'; c++ {
		codes["Digit"+string(c)] = true
		codes["Numpad"+string(c)] = true
	}
	for n := 1; n <= 24; n++ {
		codes[fmt.Sprintf("F%d", n)] = true
	}
	for _, code := range []string{
		"Escape", "Tab", "Backspace", "Enter", "Space", "Delete", "Insert",
		"CapsLock", "PrintScreen", "ScrollLock", "Pause",
		"Home", "End", "PageUp", "PageDown", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown",
		"Minus", "Equal", "BracketLeft", "BracketRight", "Backslash", "Semicolon", "Quote",
		"Backquote", "Comma", "Period", "Slash", "IntlBackslash",
		"NumpadAdd", "NumpadSubtract", "NumpadMultiply", "NumpadDivide", "NumpadDecimal", "NumpadEnter",
	} {
		codes[code] = true
	}
	return codes
}()

// NormalizeKeys validates a chord and returns modifiers in a stable order,
// followed by at most one ordinary KeyboardEvent.code. Modifier-only mappings
// are useful for holding Shift or Control while dragging with the mouse.
func NormalizeKeys(keys []string) ([]string, error) {
	if len(keys) > 5 {
		return nil, errors.New("a shortcut can contain at most four modifiers and one key")
	}
	seen := map[string]bool{}
	var ordinary string
	for _, code := range keys {
		if !keyCodes[code] {
			return nil, fmt.Errorf("unsupported key code %q", code)
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		if !slices.Contains(modifiers, code) {
			if ordinary != "" {
				return nil, errors.New("a shortcut can contain only one non-modifier key")
			}
			ordinary = code
		}
	}
	result := make([]string, 0, len(seen))
	for _, modifier := range modifiers {
		if seen[modifier] {
			result = append(result, modifier)
		}
	}
	if ordinary != "" {
		result = append(result, ordinary)
	}
	return result, nil
}

func ValidateConfig(config Config) error {
	_, err := normalizeConfig(config)
	return err
}

func normalizeConfig(config Config) (Config, error) {
	for _, item := range []struct {
		name    string
		binding *Binding
	}{{Back, &config.Back}, {Forward, &config.Forward}} {
		binding, err := NormalizeBinding(*item.binding)
		if err != nil {
			return Config{}, fmt.Errorf("%s mapping: %w", item.name, err)
		}
		*item.binding = binding
	}
	return config, nil
}

// NormalizeBinding also copies all mutable action data before the engine or
// settings store takes ownership of it. Old bindings have no long-press action.
func NormalizeBinding(binding Binding) (Binding, error) {
	if err := inputmethod.ValidateSelection(binding.InputMethod, binding.InputMethodName); err != nil {
		return Binding{}, err
	}
	keys, err := NormalizeKeys(binding.Keys)
	if err != nil {
		return Binding{}, err
	}
	if binding.Enabled && len(keys) == 0 {
		return Binding{}, errors.New("choose a key or shortcut first")
	}
	binding.Keys = keys
	if binding.LongPress != nil {
		long := *binding.LongPress
		if err := inputmethod.ValidateSelection(long.InputMethod, long.InputMethodName); err != nil {
			return Binding{}, fmt.Errorf("long press: %w", err)
		}
		long.Keys, err = NormalizeKeys(long.Keys)
		if err != nil {
			return Binding{}, fmt.Errorf("long press: %w", err)
		}
		if len(long.Keys) == 0 {
			return Binding{}, errors.New("long press: choose a key or shortcut first")
		}
		if long.ThresholdMS == 0 {
			long.ThresholdMS = DefaultLongPressThresholdMS
		}
		if long.ThresholdMS < MinLongPressThresholdMS || long.ThresholdMS > MaxLongPressThresholdMS {
			return Binding{}, fmt.Errorf("long press threshold must be between %d and %d milliseconds", MinLongPressThresholdMS, MaxLongPressThresholdMS)
		}
		binding.LongPress = &long
	}
	return binding, nil
}

type nativeDriver interface {
	start() error
	stop() error
	sendKey(code string, down bool, heldModifiers []string) error
	physicallyDown(code string) bool
	openPermissions() error
	supports(code string) bool
}

// Windows input injection must run on its hook thread. Waiting for that thread
// while holding the engine mutex can deadlock a concurrent hardware callback.
// A dispatcher returns only after work finishes or is irrevocably discarded.
type inputThreadDispatcher interface {
	runOnInputThread(context.Context, func() error) error
}

type keyReference struct {
	count int
	owned bool
}

// Engine owns the output keys, not merely the current settings. In particular,
// a settings edit cannot strand a modifier or leak the release of a swallowed
// mouse press to the application underneath.
type Engine struct {
	lifecycle     sync.Mutex
	mu            sync.Mutex
	config        Config
	status        Status
	driver        nativeDriver
	active        map[string][]string
	references    map[string]*keyReference
	onTrigger     func(Trigger)
	nativeFailure uint64
	nativeError   error
	captureFocus  func() (inputmethod.Focus, error)
	prepareQueue  []*preparedPress
	preparing     map[*preparedPress]struct{}
	pendingButton map[string]*preparedPress
	prepareWorker bool
	timedPresses  map[string]*timedPress
	now           func() time.Time
	afterFunc     func(time.Duration, func()) pressTimer
}

func New(onTrigger func(Trigger)) *Engine {
	e := &Engine{
		status: Status{Platform: runtime.GOOS, Message: "Input service has not started"},
		active: map[string][]string{}, references: map[string]*keyReference{},
		onTrigger: onTrigger, captureFocus: inputmethod.CaptureFocus,
		timedPresses: map[string]*timedPress{}, now: time.Now,
		afterFunc: func(delay time.Duration, work func()) pressTimer { return time.AfterFunc(delay, work) },
	}
	e.driver = newNativeDriver(e)
	return e
}

func (e *Engine) Apply(config Config) error {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return err
	}
	for _, binding := range []Binding{normalized.Back, normalized.Forward} {
		actions := [][]string{binding.Keys}
		if binding.LongPress != nil {
			actions = append(actions, binding.LongPress.Keys)
		}
		for _, keys := range actions {
			for _, code := range keys {
				if !e.driver.supports(code) {
					return fmt.Errorf("%s is not supported on %s", code, runtime.GOOS)
				}
			}
			if runtime.GOOS == "windows" && slices.Contains(keys, "Control") && slices.Contains(keys, "Alt") && slices.Contains(keys, "Delete") {
				return errors.New("Windows does not allow apps to simulate the secure Control+Alt+Delete shortcut")
			}
		}
	}
	e.lifecycle.Lock()
	defer e.lifecycle.Unlock()
	e.mu.Lock()
	if equalConfig(e.config, normalized) {
		e.mu.Unlock()
		return nil
	}
	e.cancelPendingLocked()
	e.mu.Unlock()
	return e.onInputThread(context.Background(), func() error {
		e.mu.Lock()
		defer e.mu.Unlock()
		// Keep the active button entries until their physical releases arrive.
		if err := e.releaseAllLocked(); err != nil {
			return err
		}
		e.config = normalized
		return nil
	})
}

func (e *Engine) Start() error {
	e.lifecycle.Lock()
	defer e.lifecycle.Unlock()
	e.mu.Lock()
	if e.status.Running {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	var failureGeneration uint64
	if err := e.onInputThread(context.Background(), func() error {
		e.mu.Lock()
		defer e.mu.Unlock()
		if err := e.releaseAllLocked(); err != nil {
			return err
		}
		// A release may have happened while the native hook was not installed.
		e.active = map[string][]string{}
		failureGeneration = e.nativeFailure
		return nil
	}); err != nil {
		return err
	}
	err := e.driver.start()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.nativeFailure != failureGeneration {
		// A driver can publish readiness and then fail before Start gets the
		// mutex back. Do not resurrect the failed service with stale readiness.
		failure := errors.Join(e.nativeError, err)
		e.status.Message = failure.Error()
		e.status.Error = failure.Error()
		return failure
	}
	e.status.Available = err == nil
	e.status.Running = err == nil
	if err != nil {
		e.status.Message = err.Error()
		e.status.Error = err.Error()
		return err
	}
	e.status.Message = "Listening for mouse side buttons"
	e.status.Error = ""
	return nil
}

func (e *Engine) Stop() error {
	e.lifecycle.Lock()
	defer e.lifecycle.Unlock()
	e.mu.Lock()
	// Cancel immediately, without joining an IME worker that may be waiting on
	// the UI thread which called Stop during application shutdown.
	e.cancelPendingLocked()
	e.status.Running = false
	e.status.Message = "Input service stopped"
	e.mu.Unlock()
	cleanupRan := false
	cleanup := func() error {
		cleanupRan = true
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.releaseAllLocked()
	}
	err := e.onInputThread(context.Background(), cleanup)
	stopErr := e.driver.stop()
	if err != nil && !cleanupRan && stopErr == nil {
		// A stalled queue may have rejected cleanup, then stopped successfully.
		// Retry only after native shutdown makes offline injection safe.
		err = e.onInputThread(context.Background(), cleanup)
	}
	e.mu.Lock()
	if stopErr == nil {
		e.active = map[string][]string{}
	}
	if combined := errors.Join(err, stopErr); combined != nil {
		e.status.Message = combined.Error()
		e.status.Error = combined.Error()
	} else {
		e.status.Error = ""
	}
	e.mu.Unlock()
	return errors.Join(err, stopErr)
}

func (e *Engine) onInputThread(ctx context.Context, work func() error) error {
	if dispatcher, ok := e.driver.(inputThreadDispatcher); ok {
		return dispatcher.runOnInputThread(ctx, work)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return work()
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	status := e.status
	if status.Running && !e.config.Enabled && status.Message == "Listening for mouse side buttons" {
		status.Message = "Mapping paused"
	}
	return status
}

func (e *Engine) OpenPermissions() error {
	e.lifecycle.Lock()
	defer e.lifecycle.Unlock()
	return e.driver.openPermissions()
}

// SupportedCodes returns the native backend's supported KeyboardEvent codes.
func SupportedCodes() []string {
	driver := newNativeDriver(nil)
	result := make([]string, 0, len(keyCodes))
	for code := range keyCodes {
		if driver.supports(code) {
			result = append(result, code)
		}
	}
	slices.Sort(result)
	return result
}

func (e *Engine) mouseButton(button string, down bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	var binding Binding
	switch button {
	case Back:
		binding = e.config.Back
	case Forward:
		binding = e.config.Forward
	default:
		return false
	}
	mapped := e.status.Running && e.config.Enabled && binding.Enabled
	keys, active := e.active[button]
	if !down {
		if !active {
			// Some mouse software duplicates hardware releases as injected input.
			// A mapped side button must not regain its navigation action on an extra up.
			return mapped
		}
		if press := e.timedPresses[button]; press != nil {
			e.releaseTimedPressLocked(press)
			// A delayed timer may decide and press a held action during release.
			keys = e.active[button]
		}
		if pending := e.pendingButton[button]; pending != nil {
			pending.down = false
			delete(e.pendingButton, button)
		}
		delete(e.active, button)
		if err := e.releaseKeysLocked(keys); err != nil {
			e.status.Message = err.Error()
			e.status.Error = err.Error()
		}
		return true
	}
	if active {
		return true
	}
	if !mapped {
		return false
	}
	// Claim the entire click before any fallible work. Failed preparation or
	// partial key injection must never fall back to browser navigation.
	e.active[button] = nil
	if binding.LongPress != nil {
		e.beginTimedPressLocked(button, binding)
		return true
	}
	if binding.InputMethod != "" {
		e.preparePressLocked(button, binding)
		return true
	}
	pressed, err := e.pressKeysLocked(binding.Keys)
	if err != nil {
		e.status.Message = err.Error()
		e.status.Error = err.Error()
		return true
	}
	e.active[button] = pressed
	e.triggerLocked(button, binding.Keys)
	return true
}

func (e *Engine) pressKeysLocked(keys []string) ([]string, error) {
	if err := e.retryPendingReleasesLocked(); err != nil {
		return nil, err
	}
	pressed := make([]string, 0, len(keys))
	for _, code := range keys {
		if ref := e.references[code]; ref != nil {
			ref.count++
			pressed = append(pressed, code)
			continue
		}
		ref := &keyReference{count: 1, owned: !e.driver.physicallyDown(code)}
		e.references[code] = ref
		if ref.owned {
			if err := e.driver.sendKey(code, true, e.heldModifiersLocked()); err != nil {
				delete(e.references, code)
				releaseErr := e.releaseKeysLocked(pressed)
				return nil, errors.Join(err, releaseErr)
			}
		}
		pressed = append(pressed, code)
	}
	return pressed, nil
}

func (e *Engine) triggerLocked(button string, keys []string) {
	e.status.Message = "Listening for mouse side buttons"
	e.status.Error = ""
	if e.onTrigger != nil {
		trigger := Trigger{Button: button, Keys: slices.Clone(keys)}
		// Native hook callbacks must never wait for the interface or an overlay.
		go e.onTrigger(trigger)
	}
}

// physicalKey is called only for hardware keyboard events. Keeping a physical
// key-up from releasing a mapped key also handles the user releasing a modifier
// that was already held before the side button went down.
func (e *Engine) physicalKey(code string, down bool) bool {
	if down {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ref := e.references[code]; ref != nil {
		if ref.count == 0 {
			// A real key-up can finish a previously denied synthetic release.
			delete(e.references, code)
			return false
		}
		ref.owned = true
		return true
	}
	return false
}

func (e *Engine) mouseHeld(button string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, held := e.active[button]
	return held
}

func (e *Engine) nativeFailed(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cleanupErr := e.releaseAllLocked()
	e.nativeFailure++
	e.nativeError = errors.Join(err, cleanupErr)
	e.status.Available = false
	e.status.Running = false
	e.status.Message = e.nativeError.Error()
	e.status.Error = e.status.Message
}

func (e *Engine) releaseKeysLocked(keys []string) error {
	var failures []error
	for i := len(keys) - 1; i >= 0; i-- {
		code := keys[i]
		ref := e.references[code]
		if ref == nil {
			continue
		}
		ref.count--
		if ref.count > 0 {
			continue
		}
		delete(e.references, code)
		if ref.owned && !e.driver.physicallyDown(code) {
			if err := e.driver.sendKey(code, false, e.heldModifiersLocked()); err != nil {
				// Retain ownership so an explicit pause/stop retries cleanup.
				ref.count = 0
				e.references[code] = ref
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (e *Engine) retryPendingReleasesLocked() error {
	var pending []string
	for code, ref := range e.references {
		if ref.count == 0 {
			ref.count = 1
			pending = append(pending, code)
		}
	}
	return e.releaseKeysLocked(pending)
}

func (e *Engine) releaseAllLocked() error {
	e.cancelPendingLocked()
	// Release ordinary keys before modifiers even if both buttons share a chord.
	keys := make([]string, 0, len(e.references))
	for _, modifier := range modifiers {
		if ref := e.references[modifier]; ref != nil {
			ref.count = 1
			keys = append(keys, modifier)
		}
	}
	for code, ref := range e.references {
		if !slices.Contains(modifiers, code) {
			ref.count = 1
			keys = append(keys, code)
		}
	}
	for button := range e.active {
		e.active[button] = nil
	}
	err := e.releaseKeysLocked(keys)
	if err != nil {
		e.status.Message = err.Error()
		e.status.Error = err.Error()
	}
	return err
}

func (e *Engine) heldModifiersLocked() []string {
	result := make([]string, 0, len(modifiers))
	for _, code := range modifiers {
		if ref := e.references[code]; ref != nil && ref.count > 0 {
			result = append(result, code)
		}
	}
	return result
}

func equalConfig(a, b Config) bool {
	return a.Enabled == b.Enabled && equalBinding(a.Back, b.Back) && equalBinding(a.Forward, b.Forward)
}

func equalBinding(a, b Binding) bool {
	return a.Enabled == b.Enabled && slices.Equal(a.Keys, b.Keys) &&
		a.InputMethod == b.InputMethod && a.InputMethodName == b.InputMethodName && equalLongPress(a.LongPress, b.LongPress)
}

func equalLongPress(a, b *LongPressBinding) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ThresholdMS == b.ThresholdMS && a.Hold == b.Hold && slices.Equal(a.Keys, b.Keys) &&
		a.InputMethod == b.InputMethod && a.InputMethodName == b.InputMethodName
}
