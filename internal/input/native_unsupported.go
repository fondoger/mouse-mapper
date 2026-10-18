//go:build !windows && !(darwin && (amd64 || arm64))

package input

import (
	"fmt"
	"runtime"
)

type unsupportedDriver struct{}

func newNativeDriver(*Engine) nativeDriver { return &unsupportedDriver{} }
func (*unsupportedDriver) start() error {
	return fmt.Errorf("global mouse remapping is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
}
func (*unsupportedDriver) stop() error { return nil }
func (*unsupportedDriver) sendKey(string, bool, []string) error {
	return fmt.Errorf("native keyboard injection is unavailable")
}
func (*unsupportedDriver) physicallyDown(string) bool { return false }
func (d *unsupportedDriver) openPermissions() error   { return d.start() }
func (*unsupportedDriver) supports(code string) bool  { return keyCodes[code] }
