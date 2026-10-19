//go:build !windows

package desktop

import "github.com/egoist/mygo"

func configureOverlay(_ *mygo.Window) error { return nil }

func placeOverlay(window *mygo.Window, width, height int) error {
	display := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	window.SetMovable(true)
	window.SetBounds(overlayFrame(display.WorkArea, width, height, overlayMargin))
	window.SetMovable(false)
	return nil
}
