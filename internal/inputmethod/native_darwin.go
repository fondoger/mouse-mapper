//go:build darwin && (amd64 || arm64)

package inputmethod

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
)

const (
	macMethodPrefix = "macos:"
	macUTF8         = 0x08000100
	macPollInterval = 20 * time.Millisecond
	macSettleTime   = 60 * time.Millisecond
)

type macProcessSerial struct {
	High uint32
	Low  uint32
}

type macInputAPI struct {
	runOnMain        func(func())
	isMainThread     func() int32
	frontProcess     func(*macProcessSerial) int16
	createSourceList func(uintptr, bool) uintptr
	getProperty      func(uintptr, uintptr) uintptr
	copyCurrent      func() uintptr
	selectSource     func(uintptr) int32
	arrayCount       func(uintptr) int64
	arrayAt          func(uintptr, int64) uintptr
	stringLength     func(uintptr) int64
	stringMaxSize    func(int64, uint32) int64
	stringCString    func(uintptr, *byte, int64, uint32) bool
	booleanValue     func(uintptr) bool
	equal            func(uintptr, uintptr) bool
	release          func(uintptr)
	propertyID       uintptr
	propertyName     uintptr
	propertyEnabled  uintptr
	propertySelect   uintptr
	propertyCategory uintptr
	categoryKeyboard uintptr
}

// Resolve symbols at startup, not inside the time-sensitive mouse event tap.
// Loading the frameworks does not enumerate, enable, or select input sources.
var defaultMacInputAPI, defaultMacInputError = loadMacInputAPI()

func loadMacInputAPI() (*macInputAPI, error) {
	carbon, err := purego.Dlopen("/System/Library/Frameworks/Carbon.framework/Carbon", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load macOS input sources: %w", err)
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load CoreFoundation: %w", err)
	}
	system, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load macOS system library: %w", err)
	}
	api := &macInputAPI{runOnMain: mygo.RunOnMain}
	var copyPointer func(*uintptr, uintptr, uintptr) uintptr
	for _, symbol := range []struct {
		target  any
		library uintptr
		name    string
	}{
		{&api.isMainThread, system, "pthread_main_np"},
		{&copyPointer, system, "memcpy"},
		{&api.frontProcess, carbon, "GetFrontProcess"},
		{&api.createSourceList, carbon, "TISCreateInputSourceList"},
		{&api.getProperty, carbon, "TISGetInputSourceProperty"},
		{&api.copyCurrent, carbon, "TISCopyCurrentKeyboardInputSource"},
		{&api.selectSource, carbon, "TISSelectInputSource"},
		{&api.arrayCount, cf, "CFArrayGetCount"},
		{&api.arrayAt, cf, "CFArrayGetValueAtIndex"},
		{&api.stringLength, cf, "CFStringGetLength"},
		{&api.stringMaxSize, cf, "CFStringGetMaximumSizeForEncoding"},
		{&api.stringCString, cf, "CFStringGetCString"},
		{&api.booleanValue, cf, "CFBooleanGetValue"},
		{&api.equal, cf, "CFEqual"},
		{&api.release, cf, "CFRelease"},
	} {
		address, lookupErr := purego.Dlsym(symbol.library, symbol.name)
		if lookupErr != nil {
			return nil, fmt.Errorf("load %s: %w", symbol.name, lookupErr)
		}
		purego.RegisterFunc(symbol.target, address)
	}
	for _, property := range []struct {
		target *uintptr
		name   string
	}{
		{&api.propertyID, "kTISPropertyInputSourceID"},
		{&api.propertyName, "kTISPropertyLocalizedName"},
		{&api.propertyEnabled, "kTISPropertyInputSourceIsEnabled"},
		{&api.propertySelect, "kTISPropertyInputSourceIsSelectCapable"},
		{&api.propertyCategory, "kTISPropertyInputSourceCategory"},
		{&api.categoryKeyboard, "kTISCategoryKeyboardInputSource"},
	} {
		address, lookupErr := purego.Dlsym(carbon, property.name)
		if lookupErr != nil {
			return nil, fmt.Errorf("load %s: %w", property.name, lookupErr)
		}
		// CFStringRef constants are exported pointer variables. Use a typed
		// destination rather than converting an arbitrary uintptr to a Go pointer.
		copyPointer(property.target, address, 8)
		if *property.target == 0 {
			return nil, fmt.Errorf("macOS input-source property %s is unavailable", property.name)
		}
	}
	return api, nil
}

func List(ctx context.Context) ([]Method, error) {
	if defaultMacInputError != nil {
		return nil, defaultMacInputError
	}
	return listMacMethods(ctx, defaultMacInputAPI)
}

func listMacMethods(ctx context.Context, api *macInputAPI) ([]Method, error) {
	return macOnMain(ctx, api, func() ([]Method, error) {
		methods := []Method{}
		seen := map[string]bool{}
		err := api.eachSource(ctx, func(source uintptr, id string) error {
			if seen[id] {
				return nil
			}
			name, err := api.string(api.getProperty(source, api.propertyName))
			if err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
				name = id
			}
			methods = append(methods, Method{ID: macMethodPrefix + id, Name: name})
			seen[id] = true
			return nil
		})
		if err != nil {
			return nil, err
		}
		slices.SortFunc(methods, func(a, b Method) int {
			if order := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
				return order
			}
			return strings.Compare(a.ID, b.ID)
		})
		return methods, nil
	})
}

type macFocus struct {
	api     *macInputAPI
	process macProcessSerial
	lost    atomic.Bool
}

func CaptureFocus() (Focus, error) {
	if defaultMacInputError != nil {
		return nil, defaultMacInputError
	}
	return captureMacFocus(defaultMacInputAPI)
}

func captureMacFocus(api *macInputAPI) (*macFocus, error) {
	var process macProcessSerial
	if status := api.frontProcess(&process); status != 0 {
		return nil, fmt.Errorf("read foreground application: macOS error %d", status)
	}
	if process == (macProcessSerial{}) {
		return nil, errors.New("there is no foreground application")
	}
	return &macFocus{api: api, process: process}, nil
}

func (focus *macFocus) Valid() bool {
	if focus == nil || focus.api == nil || focus.lost.Load() {
		return false
	}
	// GetFrontProcess is safe in the event tap and does not wait for AppKit or
	// another app's accessibility server. This is app-level validation: changing
	// focused windows within the same app cannot be detected without blocking AX IPC.
	var current macProcessSerial
	valid := focus.api.frontProcess(&current) == 0 && current == focus.process
	if !valid {
		focus.lost.Store(true)
	}
	return valid
}

func (focus *macFocus) Switch(ctx context.Context, methodID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if focus == nil || focus.api == nil {
		return ErrFocusChanged
	}
	if !strings.HasPrefix(methodID, macMethodPrefix) || len(methodID) == len(macMethodPrefix) {
		return fmt.Errorf("%w: the selection is not a macOS input source", ErrUnavailable)
	}
	id := strings.TrimPrefix(methodID, macMethodPrefix)
	api := focus.api
	alreadyActive, err := macOnMain(ctx, api, func() (bool, error) {
		if !focus.Valid() {
			return false, ErrFocusChanged
		}
		current, err := api.currentID()
		if err != nil {
			return false, err
		}
		if current == id {
			if !focus.Valid() {
				return false, ErrFocusChanged
			}
			return true, nil
		}
		found := false
		err = api.eachSource(ctx, func(source uintptr, sourceID string) error {
			if sourceID != id || found {
				return nil
			}
			found = true
			if err := ctx.Err(); err != nil {
				return err
			}
			if !focus.Valid() {
				return ErrFocusChanged
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if status := api.selectSource(source); status != 0 {
				return fmt.Errorf("select input method: macOS error %d", status)
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if !found {
			return false, fmt.Errorf("%w: add it in System Settings > Keyboard > Text Input", ErrUnavailable)
		}
		return false, nil
	})
	if err != nil || alreadyActive {
		return err
	}

	// TISSelectInputSource starts activation; do not send the shortcut until
	// the source is observed selected and the input method has had a brief turn
	// through the run loop. Existing selections intentionally skip this delay.
	ticker := time.NewTicker(macPollInterval)
	defer ticker.Stop()
	var activeSince time.Time
	for {
		active, err := macOnMain(ctx, api, func() (bool, error) {
			if !focus.Valid() {
				return false, ErrFocusChanged
			}
			current, err := api.currentID()
			if !focus.Valid() {
				return false, ErrFocusChanged
			}
			return current == id, err
		})
		if err != nil {
			return err
		}
		if active {
			if activeSince.IsZero() {
				activeSince = time.Now()
			}
			if time.Since(activeSince) >= macSettleTime {
				return nil
			}
		} else {
			activeSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type macMainResult[T any] struct {
	value T
	err   error
}

func macOnMain[T any](ctx context.Context, api *macInputAPI, fn func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if api.isMainThread() != 0 {
		value, err := fn()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}
		return value, err
	}
	result := make(chan macMainResult[T], 1)
	go func() {
		ran := false
		api.runOnMain(func() {
			ran = true
			if err := ctx.Err(); err != nil {
				result <- macMainResult[T]{err: err}
				return
			}
			value, err := fn()
			result <- macMainResult[T]{value: value, err: err}
		})
		if !ran {
			result <- macMainResult[T]{err: errors.New("the application's main event loop is not running")}
		}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case completed := <-result:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return completed.value, completed.err
	}
}

func (api *macInputAPI) eachSource(ctx context.Context, fn func(uintptr, string) error) error {
	sources := api.createSourceList(0, false)
	if sources == 0 {
		return errors.New("macOS could not enumerate enabled input sources")
	}
	defer api.release(sources)
	count := api.arrayCount(sources)
	if count < 0 || count > 4096 {
		return errors.New("macOS returned an invalid input-source list")
	}
	for index := int64(0); index < count; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		source := api.arrayAt(sources, index)
		if source == 0 {
			continue
		}
		category := api.getProperty(source, api.propertyCategory)
		enabled := api.getProperty(source, api.propertyEnabled)
		selectable := api.getProperty(source, api.propertySelect)
		if category == 0 || !api.equal(category, api.categoryKeyboard) || enabled == 0 || !api.booleanValue(enabled) || selectable == 0 || !api.booleanValue(selectable) {
			continue
		}
		id, err := api.string(api.getProperty(source, api.propertyID))
		if err != nil {
			return err
		}
		if id != "" {
			if err := fn(source, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (api *macInputAPI) currentID() (string, error) {
	source := api.copyCurrent()
	if source == 0 {
		return "", errors.New("macOS could not read the active input method")
	}
	defer api.release(source)
	id, err := api.string(api.getProperty(source, api.propertyID))
	if err == nil && id == "" {
		err = errors.New("macOS returned an active input method without an identifier")
	}
	return id, err
}

func (api *macInputAPI) string(value uintptr) (string, error) {
	if value == 0 {
		return "", nil
	}
	length := api.stringLength(value)
	if length == 0 {
		return "", nil
	}
	if length < 0 || length > 16384 {
		return "", errors.New("macOS returned an invalid input-source label")
	}
	capacity := api.stringMaxSize(length, macUTF8)
	if capacity < 0 || capacity > 65536 {
		return "", errors.New("macOS returned an invalid input-source label size")
	}
	buffer := make([]byte, capacity+1)
	if !api.stringCString(value, &buffer[0], int64(len(buffer)), macUTF8) {
		return "", errors.New("could not decode a macOS input-source label")
	}
	if end := slices.Index(buffer, byte(0)); end >= 0 {
		return string(buffer[:end]), nil
	}
	return "", errors.New("macOS returned an unterminated input-source label")
}
