// Command smoke verifies the real native window lifecycle without remapping
// the user's input or changing their startup registration or preferences.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/mouse-mapper/internal/config"
	"github.com/fondoger/mouse-mapper/internal/desktop"
)

type report struct {
	Platform             string `json:"platform"`
	Architecture         string `json:"architecture"`
	NativeWindows        int    `json:"nativeWindows"`
	UsesWebView          bool   `json:"usesWebView"`
	OverlayDoesNotFocus  bool   `json:"overlayDoesNotFocus"`
	StartedVisible       bool   `json:"startedVisible"`
	CloseHidesWindow     bool   `json:"closeHidesWindow"`
	SecondInstanceReopen bool   `json:"secondInstanceReopen"`
	Screenshot           string `json:"screenshot"`
	Error                string `json:"error,omitempty"`
}

func main() {
	output := flag.String("out", ".mygo/smoke", "report and native screenshot directory")
	second := flag.Bool("second-instance", false, "internal second-launch probe")
	flag.Parse()
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "native smoke test requires Windows or macOS")
		os.Exit(1)
	}
	assets := desktop.Assets{
		Icon: read("resources/icon.png"), Tray: read("resources/tray.png"),
		TrayTemplate: read("resources/tray-template.png"),
	}
	if *second {
		if err := desktop.Run(assets); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	directory, err := filepath.Abs(*output)
	check(err)
	check(os.MkdirAll(directory, 0o755))
	configuration := filepath.Join(directory, "config")
	check(os.Setenv("MOUSE_MAPPER_CONFIG_DIR", configuration))
	settings := config.Default()
	settings.Enabled, settings.HideTray = false, true
	check((config.Store{Path: filepath.Join(configuration, "settings.json")}).Save(settings))
	result := report{Platform: runtime.GOOS, Architecture: runtime.GOARCH}
	done := make(chan struct{})
	mygo.App.WhenReady(func() {
		go func() {
			defer close(done)
			if err := inspect(directory, &result); err != nil {
				result.Error = err.Error()
			}
			mygo.App.Quit()
		}()
	})
	check(desktop.Run(assets))
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		result.Error = "no smoke result (another Mouse Mapper instance may already be running)"
	}
	data, err := json.MarshalIndent(result, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(directory, "report.json"), append(data, '\n'), 0o644))
	fmt.Println(string(data))
	if result.Error != "" {
		os.Exit(1)
	}
}

func inspect(directory string, result *report) error {
	var mainWindow *mygo.Window
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, window := range mygo.Windows() {
			if window.Title() == "Mouse Mapper" {
				mainWindow = window
			}
		}
		if mainWindow != nil && mainWindow.IsVisible() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if mainWindow == nil || !mainWindow.IsVisible() {
		var windows []string
		for _, window := range mygo.Windows() {
			windows = append(windows, fmt.Sprintf("%q visible=%t native=%t", window.Title(), window.IsVisible(), window.Page() == nil))
		}
		return fmt.Errorf("the native main window did not become visible: %v", windows)
	}
	time.Sleep(600 * time.Millisecond)
	for _, window := range mygo.Windows() {
		if window.Page() != nil {
			result.UsesWebView = true
		} else {
			result.NativeWindows++
		}
	}
	result.StartedVisible = mainWindow.IsVisible()
	if result.UsesWebView || result.NativeWindows < 2 {
		return fmt.Errorf("expected native main and overlay windows, got %+v", result)
	}
	for _, window := range mygo.Windows() {
		if window.Title() == "Mouse Mapper Shortcut" {
			window.ShowInactive()
			time.Sleep(150 * time.Millisecond)
			result.OverlayDoesNotFocus = !window.IsFocused()
			window.Hide()
		}
	}
	if !result.OverlayDoesNotFocus {
		return errors.New("the shortcut overlay unexpectedly acquired keyboard focus")
	}
	image, err := mainWindow.CapturePage()
	if err != nil {
		return fmt.Errorf("capture native surface: %w", err)
	}
	result.Screenshot = filepath.Join(directory, "native-window.png")
	if err := os.WriteFile(result.Screenshot, image, 0o644); err != nil {
		return err
	}
	mainWindow.Close()
	time.Sleep(250 * time.Millisecond)
	result.CloseHidesWindow = !mainWindow.IsVisible() && !mainWindow.IsDestroyed()
	if !result.CloseHidesWindow {
		return errors.New("close did not leave the existing main window hidden")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-second-instance")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("second launch: %w: %s", err, output)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !mainWindow.IsVisible() {
		time.Sleep(50 * time.Millisecond)
	}
	result.SecondInstanceReopen = mainWindow.IsVisible()
	if !result.SecondInstanceReopen {
		return errors.New("the second launch did not reopen the hidden window")
	}
	return nil
}

func read(path string) []byte {
	data, err := os.ReadFile(path)
	check(err)
	return data
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
