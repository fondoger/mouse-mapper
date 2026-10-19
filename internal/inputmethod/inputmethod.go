// Package inputmethod discovers installed input methods and selects one before
// a shortcut, without changing the user's default input-method preferences.
package inputmethod

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Method struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	ErrFocusChanged = errors.New("the foreground application changed; the shortcut was cancelled")
	ErrUnavailable  = errors.New("the selected input method is not available")
)

// Focus is captured without waiting inside an input hook. Switch runs on a
// background worker and returns only after the selected input method is active.
// Valid prevents a delayed shortcut from being delivered to a different target.
type Focus interface {
	Switch(context.Context, string) error
	Valid() bool
}

// ValidateSelection accepts an empty ID as the opt-out and bounds opaque native
// identifiers. Missing or uninstalled methods stay saved for the user to repair.
func ValidateSelection(id, name string) error {
	for _, field := range []struct {
		value string
		limit int
		name  string
	}{{id, 512, "input method ID"}, {name, 256, "input method name"}} {
		if len(field.value) > field.limit || !utf8.ValidString(field.value) || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("invalid %s", field.name)
		}
	}
	if id == "" && name != "" {
		return errors.New("an input method name requires an ID")
	}
	return nil
}
