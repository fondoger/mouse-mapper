//go:build !windows && !darwin

package inputmethod

import "context"

func List(context.Context) ([]Method, error) { return nil, ErrUnavailable }
func CaptureFocus() (Focus, error)           { return nil, ErrUnavailable }
