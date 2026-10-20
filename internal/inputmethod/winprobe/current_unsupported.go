//go:build !windows

package winprobe

import "context"

func rejectReparsePoint(string) error { return nil }

func Current(ctx context.Context, target Target) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	return Profile{}, ErrUnsupported
}
