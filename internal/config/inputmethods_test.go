package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInputMethodSelectionPersistsPerButton(t *testing.T) {
	settings := Default()
	settings.Back.InputMethod, settings.Back.InputMethodName = "test:wechat", "WeChat"
	settings.Forward.InputMethod, settings.Forward.InputMethodName = "test:doubao", "Doubao"
	store := Store{Path: filepath.Join(t.TempDir(), "settings.json")}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Back.InputMethod != "test:wechat" || loaded.Forward.InputMethod != "test:doubao" || loaded.Back.InputMethodName != "WeChat" {
		t.Fatalf("input-method selection did not round-trip: %+v", loaded)
	}
	paused := loaded.Input(true)
	if paused.Enabled || paused.Back.InputMethod != "test:wechat" {
		t.Fatal("recording must pause input without losing its saved preparation")
	}
}

func TestExistingSettingsDefaultToNoInputMethodSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	data := `{"schemaVersion":1,"enabled":true,"back":{"enabled":true,"keys":["Control","KeyC"]},"forward":{"enabled":true,"keys":["Control","KeyV"]},"startAtLogin":false,"hideTray":true,"showOverlay":true,"overlayDuration":1200}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Back.InputMethod != "" || loaded.Forward.InputMethod != "" || !loaded.HideTray {
		t.Fatalf("legacy preferences changed on load: %+v", loaded)
	}
}

func TestSettingsRejectMalformedInputMethodMetadata(t *testing.T) {
	for _, method := range []string{"bad\x00id", strings.Repeat("x", 513)} {
		settings := Default()
		settings.Back.InputMethod = method
		if _, err := Normalize(settings); err == nil {
			t.Fatal("malformed native input-method ID accepted")
		}
	}
	settings := Default()
	settings.Forward.InputMethodName = "name without selection"
	if _, err := Normalize(settings); err == nil {
		t.Fatal("orphaned input-method name accepted")
	}
}
