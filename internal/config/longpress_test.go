package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fondoger/mouse-mapper/internal/input"
)

func TestOldSettingsDoNotEnableLongPress(t *testing.T) {
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	got, err := Normalize(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got.Back.LongPress != nil || got.Forward.LongPress != nil || !reflect.DeepEqual(got, Default()) {
		t.Fatal("loading existing settings changed immediate press behavior")
	}
}

func TestLongPressSettingsRoundTripAndDeepClone(t *testing.T) {
	settings := Default()
	settings.Forward.Keys = []string{"Enter"}
	settings.Forward.LongPress = &input.LongPressBinding{Keys: []string{"Escape"}, ThresholdMS: 650,
		InputMethod: "test:long", InputMethodName: "Long action input method"}
	settings.Back.LongPress = &input.LongPressBinding{Keys: []string{"Alt", "KeyC"}, ThresholdMS: 1000}
	store := Store{Path: filepath.Join(t.TempDir(), "settings.json")}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatalf("long action round trip: got=%+v err=%v", got, err)
	}
	clone := Clone(settings)
	clone.Forward.LongPress.Keys[0] = "Tab"
	clone.Forward.LongPress.ThresholdMS = 1000
	clone.Forward.LongPress.InputMethod = "test:edited"
	clone.Back.LongPress.Keys[0] = "Shift"
	if settings.Forward.LongPress.Keys[0] != "Escape" || settings.Forward.LongPress.ThresholdMS != 650 ||
		settings.Forward.LongPress.InputMethod != "test:long" || settings.Back.LongPress.Keys[0] != "Alt" {
		t.Fatal("settings clone shares mutable long-action data")
	}
	config := settings.Input(false)
	config.Forward.LongPress.Keys[0] = "Tab"
	if settings.Forward.LongPress.Keys[0] != "Escape" {
		t.Fatal("input config shares settings' long keys")
	}
}

func TestNormalizeLongPressDefaultThresholdAndValidation(t *testing.T) {
	settings := Default()
	settings.Forward.LongPress = &input.LongPressBinding{Keys: []string{"Escape"}}
	got, err := Normalize(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got.Forward.LongPress.ThresholdMS != input.DefaultLongPressThresholdMS || settings.Forward.LongPress.ThresholdMS != 0 {
		t.Fatal("default threshold is missing or changed caller settings")
	}
	settings.Forward.Enabled = false
	settings.Forward.Keys = nil
	settings.Forward.LongPress.ThresholdMS = 99
	if _, err := Normalize(settings); err == nil {
		t.Fatal("disabled button accepted invalid long action")
	}
	settings.Forward.LongPress.ThresholdMS = 500
	settings.Forward.LongPress.Keys = nil
	if _, err := Normalize(settings); err == nil {
		t.Fatal("accepted configured empty long action")
	}
}
