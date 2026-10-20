package config

import (
	"fmt"
	"runtime"
	"slices"

	"github.com/fondoger/mouse-mapper/internal/input"
)

const SchemaVersion = 1

// Settings is the on-disk configuration and the desktop API's preferences.
type Settings struct {
	SchemaVersion   int           `json:"schemaVersion"`
	Enabled         bool          `json:"enabled"`
	Back            input.Binding `json:"back"`
	Forward         input.Binding `json:"forward"`
	StartAtLogin    bool          `json:"startAtLogin"`
	HideTray        bool          `json:"hideTray"`
	ShowOverlay     bool          `json:"showOverlay"`
	OverlayDuration int           `json:"overlayDuration"`
}

func Default() Settings {
	modifier := "Control"
	if runtime.GOOS == "darwin" {
		modifier = "Meta"
	}
	return Settings{
		SchemaVersion:   SchemaVersion,
		Enabled:         true,
		Back:            input.Binding{Enabled: true, Keys: []string{modifier, "KeyC"}},
		Forward:         input.Binding{Enabled: true, Keys: []string{modifier, "KeyV"}},
		ShowOverlay:     true,
		OverlayDuration: 1200,
	}
}

func Clone(s Settings) Settings {
	s.Back.Keys = slices.Clone(s.Back.Keys)
	s.Forward.Keys = slices.Clone(s.Forward.Keys)
	for _, binding := range []*input.Binding{&s.Back, &s.Forward} {
		if binding.LongPress != nil {
			long := *binding.LongPress
			long.Keys = slices.Clone(long.Keys)
			binding.LongPress = &long
		}
	}
	return s
}

func (s Settings) Input(recording bool) input.Config {
	s = Clone(s)
	return input.Config{Enabled: s.Enabled && !recording, Back: s.Back, Forward: s.Forward}
}

func Normalize(s Settings) (Settings, error) {
	if s.SchemaVersion != SchemaVersion {
		return Settings{}, fmt.Errorf("unsupported settings schema %d (this app supports %d)", s.SchemaVersion, SchemaVersion)
	}
	if s.OverlayDuration < 500 || s.OverlayDuration > 3000 {
		return Settings{}, fmt.Errorf("overlay duration must be between 500 and 3000 milliseconds")
	}
	s = Clone(s)
	for name, binding := range map[string]*input.Binding{"back": &s.Back, "forward": &s.Forward} {
		normalized, err := input.NormalizeBinding(*binding)
		if err != nil {
			return Settings{}, fmt.Errorf("%s button: %w", name, err)
		}
		*binding = normalized
	}
	if err := input.ValidateConfig(s.Input(false)); err != nil {
		return Settings{}, err
	}
	return s, nil
}
