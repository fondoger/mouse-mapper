package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultsAndClone(t *testing.T) {
	defaults := Default()
	normalized, err := Normalize(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, normalized) {
		t.Fatalf("defaults are not normalized: %#v", normalized)
	}
	copy := Clone(defaults)
	copy.Back.Keys[0] = "Alt"
	if reflect.DeepEqual(copy.Back.Keys, defaults.Back.Keys) {
		t.Fatal("clone shares shortcut storage")
	}
	if defaults.Input(true).Enabled {
		t.Fatal("recording must suspend remapping without changing settings")
	}
}

func TestNormalizeRejectsInvalidSettings(t *testing.T) {
	cases := map[string]func(*Settings){
		"newer schema":   func(s *Settings) { s.SchemaVersion++ },
		"missing schema": func(s *Settings) { s.SchemaVersion = 0 },
		"short duration": func(s *Settings) { s.OverlayDuration = 499 },
		"long duration":  func(s *Settings) { s.OverlayDuration = 3001 },
		"unknown key":    func(s *Settings) { s.Back.Keys = []string{"NotARealKey"} },
		"multiple keys":  func(s *Settings) { s.Back.Keys = []string{"KeyC", "KeyV"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := Default()
			change(&s)
			if _, err := Normalize(s); err == nil {
				t.Fatal("invalid settings accepted")
			}
		})
	}
}

func TestStoreRoundTripAndReplace(t *testing.T) {
	dir := t.TempDir()
	store := Store{Path: filepath.Join(dir, "nested", "settings.json")}
	initial, err := store.Load()
	if err != nil || !reflect.DeepEqual(initial, Default()) {
		t.Fatalf("missing file should yield defaults: %#v, %v", initial, err)
	}
	settings := Default()
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	settings.HideTray = true
	settings.Back.Keys = []string{"Alt", "ArrowLeft"}
	if err := store.Save(settings); err != nil {
		t.Fatalf("replacing existing file: %v", err)
	}
	got, err := store.Load()
	if err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatalf("roundtrip: got %#v, want %#v, error %v", got, settings, err)
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("temporary file left behind: %v, %v", entries, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.Path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("settings permissions: %v, %v", info, err)
		}
	}
}

func TestStoreRejectsInvalidFilesWithoutOverwriting(t *testing.T) {
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"broken":         "{",
		"unknown schema": strings.Replace(string(data), `"schemaVersion":1`, `"schemaVersion":98`, 1),
		"unknown fields": strings.TrimSuffix(string(data), "}") + `,"futurePreference":true}`,
		"trailing JSON":  string(data) + " {}",
		"oversized":      strings.Repeat(" ", maxSettingsBytes+1),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := (Store{Path: path}).Load(); err == nil {
				t.Fatal("invalid settings file accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != bad {
				t.Fatal("loading changed the settings file")
			}
		})
	}
}
