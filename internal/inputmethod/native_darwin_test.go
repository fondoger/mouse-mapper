//go:build darwin && (amd64 || arm64)

package inputmethod

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

type fakeMacInput struct {
	api         *macInputAPI
	front       macProcessSerial
	frontError  int16
	texts       map[uintptr]string
	properties  map[uintptr]map[uintptr]uintptr
	sources     []uintptr
	current     uintptr
	listCalls   int
	selectCalls []uintptr
	released    []uintptr
	getCalls    int
}

func newFakeMacInput() *fakeMacInput {
	fake := &fakeMacInput{
		front:      macProcessSerial{Low: 42},
		texts:      map[uintptr]string{},
		properties: map[uintptr]map[uintptr]uintptr{},
	}
	fake.api = &macInputAPI{
		runOnMain:    func(fn func()) { fn() },
		isMainThread: func() int32 { return 1 },
		frontProcess: func(process *macProcessSerial) int16 {
			*process = fake.front
			return fake.frontError
		},
		createSourceList: func(_ uintptr, includeAll bool) uintptr {
			if includeAll {
				panic("must not include or enable unconfigured input sources")
			}
			fake.listCalls++
			return 99
		},
		getProperty: func(source, key uintptr) uintptr { return fake.properties[source][key] },
		copyCurrent: func() uintptr {
			fake.getCalls++
			return fake.current
		},
		selectSource: func(source uintptr) int32 {
			fake.selectCalls = append(fake.selectCalls, source)
			fake.current = source
			return 0
		},
		arrayCount: func(uintptr) int64 { return int64(len(fake.sources)) },
		arrayAt:    func(_ uintptr, index int64) uintptr { return fake.sources[index] },
		stringLength: func(value uintptr) int64 {
			return int64(len(fake.texts[value]))
		},
		stringMaxSize: func(length int64, encoding uint32) int64 { return length * 3 },
		stringCString: func(value uintptr, data *byte, capacity int64, encoding uint32) bool {
			text := fake.texts[value]
			if encoding != macUTF8 || capacity <= int64(len(text)) {
				return false
			}
			copy(unsafe.Slice(data, int(capacity)), append([]byte(text), 0))
			return true
		},
		booleanValue:     func(value uintptr) bool { return value == 10 },
		equal:            func(a, b uintptr) bool { return a == b },
		release:          func(value uintptr) { fake.released = append(fake.released, value) },
		propertyID:       1,
		propertyName:     2,
		propertyEnabled:  3,
		propertySelect:   4,
		propertyCategory: 5,
		categoryKeyboard: 20,
	}
	return fake
}

func (fake *fakeMacInput) add(id, name string) uintptr {
	source := uintptr(100 + len(fake.sources))
	idText := uintptr(1000 + len(fake.texts))
	fake.texts[idText] = id
	nameText := uintptr(1000 + len(fake.texts))
	fake.texts[nameText] = name
	fake.properties[source] = map[uintptr]uintptr{
		fake.api.propertyID:       idText,
		fake.api.propertyName:     nameText,
		fake.api.propertyEnabled:  10,
		fake.api.propertySelect:   10,
		fake.api.propertyCategory: 20,
	}
	fake.sources = append(fake.sources, source)
	return source
}

func (fake *fakeMacInput) focus(t *testing.T) *macFocus {
	t.Helper()
	focus, err := captureMacFocus(fake.api)
	if err != nil {
		t.Fatal(err)
	}
	return focus
}

func TestMacInputAPIBindings(t *testing.T) {
	// Symbol resolution is read-only; this does not query or switch an IME.
	if defaultMacInputError != nil {
		t.Fatal(defaultMacInputError)
	}
}

func TestMacListIncludesLocalizedSelectableKeyboardSources(t *testing.T) {
	fake := newFakeMacInput()
	wechat := "\u5fae\u4fe1\u8f93\u5165\u6cd5"
	doubao := "\u8c46\u5305\u8f93\u5165\u6cd5"
	fake.add("com.example.wechat", wechat)
	fake.add("com.example.doubao", doubao)
	fake.add("com.apple.keylayout.ABC", "ABC")
	fake.add("com.example.wechat", "duplicate")
	disabled := fake.add("com.example.disabled", "disabled")
	fake.properties[disabled][fake.api.propertyEnabled] = 11
	parent := fake.add("com.example.parent", "non-selectable parent")
	fake.properties[parent][fake.api.propertySelect] = 11
	palette := fake.add("com.example.palette", "palette")
	fake.properties[palette][fake.api.propertyCategory] = 21
	noID := fake.add("", "missing ID")
	delete(fake.properties[noID], fake.api.propertyID)
	fake.sources = append(fake.sources, 0)

	methods, err := listMacMethods(context.Background(), fake.api)
	if err != nil {
		t.Fatal(err)
	}
	want := []Method{
		{ID: "macos:com.apple.keylayout.ABC", Name: "ABC"},
		{ID: "macos:com.example.wechat", Name: wechat},
		{ID: "macos:com.example.doubao", Name: doubao},
	}
	if !slices.Equal(methods, want) {
		t.Fatalf("methods = %#v, want %#v", methods, want)
	}
	if !slices.Equal(fake.released, []uintptr{99}) {
		t.Fatalf("must release owning source array once, got %v", fake.released)
	}
	if len(fake.selectCalls) != 0 {
		t.Fatal("enumeration selected an input method")
	}
}

func TestMacListNameFallbackAndStableOrdering(t *testing.T) {
	fake := newFakeMacInput()
	unnamed := fake.add("fallback.name", "")
	delete(fake.properties[unnamed], fake.api.propertyName)
	fake.add("z-source", "Same")
	fake.add("a-source", "same")
	methods, err := listMacMethods(context.Background(), fake.api)
	if err != nil {
		t.Fatal(err)
	}
	want := []Method{{"macos:fallback.name", "fallback.name"}, {"macos:a-source", "same"}, {"macos:z-source", "Same"}}
	if !slices.Equal(methods, want) {
		t.Fatalf("methods = %#v, want %#v", methods, want)
	}
}

func TestMacListFailureAndCancellation(t *testing.T) {
	fake := newFakeMacInput()
	fake.api.createSourceList = func(uintptr, bool) uintptr { return 0 }
	if _, err := listMacMethods(context.Background(), fake.api); err == nil {
		t.Fatal("accepted a missing native array")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake.api.createSourceList = func(uintptr, bool) uintptr {
		t.Fatal("enumerated after context cancellation")
		return 0
	}
	if _, err := listMacMethods(ctx, fake.api); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled list = %v", err)
	}
}

func TestMacCaptureFocusNeverWaitsForMainOrReadsInputSources(t *testing.T) {
	fake := newFakeMacInput()
	fake.api.runOnMain = func(func()) { t.Fatal("focus capture dispatched to main") }
	focus := fake.focus(t)
	if !focus.Valid() || fake.listCalls != 0 || fake.getCalls != 0 {
		t.Fatal("focus capture touched TIS or failed to capture foreground")
	}
	fake.front.Low++
	if focus.Valid() {
		t.Fatal("changed application remained valid")
	}
	fake.front.Low--
	if focus.Valid() {
		t.Fatal("a captured focus became valid after a known application change")
	}
}

func TestMacCaptureFocusErrorsFailClosed(t *testing.T) {
	fake := newFakeMacInput()
	fake.frontError = -50
	if _, err := captureMacFocus(fake.api); err == nil {
		t.Fatal("accepted GetFrontProcess failure")
	}
	fake.frontError = 0
	fake.front = macProcessSerial{}
	if _, err := captureMacFocus(fake.api); err == nil {
		t.Fatal("accepted missing foreground process")
	}
	if (*macFocus)(nil).Valid() {
		t.Fatal("nil focus is valid")
	}
}

func TestMacSwitchSkipsAlreadyActiveSource(t *testing.T) {
	fake := newFakeMacInput()
	fake.current = fake.add("target", "Target")
	if err := fake.focus(t).Switch(context.Background(), "macos:target"); err != nil {
		t.Fatal(err)
	}
	if len(fake.selectCalls) != 0 || fake.listCalls != 0 || fake.getCalls != 1 {
		t.Fatalf("reselected active IME: selects=%v lists=%d reads=%d", fake.selectCalls, fake.listCalls, fake.getCalls)
	}
	if !slices.Equal(fake.released, []uintptr{fake.current}) {
		t.Fatalf("current-source ownership leaked: releases=%v", fake.released)
	}
}

func TestMacSwitchSelectsOnceAndConfirmsActivation(t *testing.T) {
	fake := newFakeMacInput()
	fake.current = fake.add("original", "Original")
	target := fake.add("target", "Target")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := fake.focus(t).Switch(ctx, "macos:target"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.selectCalls, []uintptr{target}) || fake.current != target {
		t.Fatalf("selects=%v current=%d", fake.selectCalls, fake.current)
	}
	if fake.getCalls < 3 || time.Since(started) < macSettleTime {
		t.Fatal("shortcut may run before the new IME receives its activation events")
	}
}

func TestMacSwitchDoesNotTreatSelectionRequestAsActivation(t *testing.T) {
	fake := newFakeMacInput()
	fake.current = fake.add("original", "Original")
	target := fake.add("target", "Target")
	fake.api.selectSource = func(source uintptr) int32 {
		fake.selectCalls = append(fake.selectCalls, source)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	if err := fake.focus(t).Switch(ctx, "macos:target"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unconfirmed activation = %v", err)
	}
	if !slices.Equal(fake.selectCalls, []uintptr{target}) {
		t.Fatalf("retried selection instead of only checking activation: %v", fake.selectCalls)
	}
}

func TestMacSwitchRejectsUnavailableOrInvalidTargets(t *testing.T) {
	for _, id := range []string{"macos:missing", "windows:target", "macos:", ""} {
		t.Run(id, func(t *testing.T) {
			fake := newFakeMacInput()
			fake.current = fake.add("original", "Original")
			if err := fake.focus(t).Switch(context.Background(), id); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("invalid target = %v", err)
			}
			if len(fake.selectCalls) != 0 {
				t.Fatal("selected an unrelated input source")
			}
		})
	}
}

func TestMacSwitchHandlesNativeFailures(t *testing.T) {
	fake := newFakeMacInput()
	fake.add("target", "Target")
	if err := fake.focus(t).Switch(context.Background(), "macos:target"); err == nil || !strings.Contains(err.Error(), "active input method") {
		t.Fatalf("unreadable active source = %v", err)
	}
	fake.current = fake.add("original", "Original")
	fake.api.selectSource = func(uintptr) int32 { return -50 }
	if err := fake.focus(t).Switch(context.Background(), "macos:target"); err == nil || !strings.Contains(err.Error(), "-50") {
		t.Fatalf("native selection failure = %v", err)
	}
}

func TestMacSwitchAbortsAfterFocusChange(t *testing.T) {
	for _, duringSelection := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[duringSelection], func(t *testing.T) {
			fake := newFakeMacInput()
			fake.current = fake.add("original", "Original")
			fake.add("target", "Target")
			focus := fake.focus(t)
			if duringSelection {
				fake.api.selectSource = func(source uintptr) int32 {
					fake.current = source
					fake.front.Low++
					return 0
				}
			} else {
				fake.front.Low++
			}
			if err := focus.Switch(context.Background(), "macos:target"); !errors.Is(err, ErrFocusChanged) {
				t.Fatalf("focus-change result = %v", err)
			}
			if focus.Valid() {
				t.Fatal("the lost focus was reusable")
			}
		})
	}
}

func TestMacSwitchRechecksCancellationAfterNativeFocusRead(t *testing.T) {
	fake := newFakeMacInput()
	fake.current = fake.add("original", "Original")
	fake.add("target", "Target")
	focus := fake.focus(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	fake.api.frontProcess = func(process *macProcessSerial) int16 {
		*process = fake.front
		reads++
		if reads == 2 {
			cancel()
		}
		return 0
	}
	if err := focus.Switch(ctx, "macos:target"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled focus read = %v", err)
	}
	if len(fake.selectCalls) != 0 {
		t.Fatal("selected an input method after the native focus query outlived cancellation")
	}
}

func TestMacMainDispatchCancellationDoesNotExecuteStaleSwitch(t *testing.T) {
	fake := newFakeMacInput()
	fake.current = fake.add("original", "Original")
	fake.add("target", "Target")
	fake.api.isMainThread = func() int32 { return 0 }
	queued := make(chan func(), 1)
	resumed := make(chan struct{})
	dispatchExited := make(chan struct{})
	fake.api.runOnMain = func(fn func()) {
		queued <- fn
		<-resumed
		close(dispatchExited)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	focus := fake.focus(t)
	go func() { result <- focus.Switch(ctx, "macos:target") }()
	var callback func()
	select {
	case callback = <-queued:
	case <-time.After(time.Second):
		t.Fatal("main dispatch was not queued")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled switch = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited on the stalled UI thread")
	}
	callback()
	close(resumed)
	<-dispatchExited
	if len(fake.selectCalls) != 0 || fake.getCalls != 0 || fake.listCalls != 0 {
		t.Fatal("a cancelled, queued operation touched the input method")
	}
}

func TestMacMainDispatchDirectAndStoppedLoop(t *testing.T) {
	fake := newFakeMacInput()
	fake.api.runOnMain = func(func()) { t.Fatal("main-thread caller would deadlock dispatching to itself") }
	result, err := macOnMain(context.Background(), fake.api, func() (int, error) { return 42, nil })
	if err != nil || result != 42 {
		t.Fatalf("main-thread result = %d, %v", result, err)
	}
	fake.api.isMainThread = func() int32 { return 0 }
	fake.api.runOnMain = func(func()) {}
	if _, err := macOnMain(context.Background(), fake.api, func() (int, error) {
		t.Fatal("stopped loop executed work")
		return 0, nil
	}); err == nil {
		t.Fatal("reported success after MyGo dropped the callback")
	}
}

func TestMacStringRejectsMalformedNativeValues(t *testing.T) {
	fake := newFakeMacInput()
	fake.texts[300] = "example"
	fake.api.stringCString = func(uintptr, *byte, int64, uint32) bool { return false }
	if _, err := fake.api.string(300); err == nil {
		t.Fatal("accepted a failed UTF-8 conversion")
	}
	fake.api.stringLength = func(uintptr) int64 { return -1 }
	if _, err := fake.api.string(300); err == nil {
		t.Fatal("accepted a negative CFString length")
	}
	fake.api.stringLength = func(uintptr) int64 { return 1 }
	fake.api.stringMaxSize = func(int64, uint32) int64 { return -1 }
	if _, err := fake.api.string(300); err == nil {
		t.Fatal("accepted an invalid UTF-8 buffer size")
	}
}
