//go:build windows

package inputmethod

import (
	"context"

	"github.com/fondoger/mouse-mapper/internal/inputmethod/winprobe"
	"golang.org/x/sys/windows"
)

func winReadForegroundProfile(ctx context.Context, focus *winFocus) (winProfile, error) {
	if err := ctx.Err(); err != nil {
		return winProfile{}, err
	}
	if focus == nil || !focus.Valid() {
		return winProfile{}, ErrFocusChanged
	}
	window := focus.control
	if window == 0 {
		window = focus.window
	}
	profile, err := winprobe.Current(ctx, winprobe.Target{
		Process: focus.process, Thread: focus.thread, Window: window,
	})
	if err != nil {
		return winProfile{}, err
	}
	if !focus.Valid() {
		return winProfile{}, ErrFocusChanged
	}
	return winProfile{Kind: profile.Kind, Language: profile.Language,
		Class: windows.GUID(profile.Class), Profile: windows.GUID(profile.Profile),
		Category: winKeyboardCategoryGUID, Layout: uintptr(profile.Layout)}, nil
}
