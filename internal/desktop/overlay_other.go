//go:build !windows

package desktop

import "github.com/egoist/mygo"

func configureOverlay(_ *mygo.Window) error { return nil }
