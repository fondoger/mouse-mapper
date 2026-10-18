package desktop

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updaterUI "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/mouse-mapper/internal/config"
	"github.com/fondoger/mouse-mapper/internal/input"
)

type Assets struct {
	Icon         []byte
	Tray         []byte
	TrayTemplate []byte
}

type application struct {
	assets       Assets
	mapper       *Mapper
	view         *nativeView
	mainWindow   *mygo.Window
	overlay      *mygo.Window
	tray         *mygo.Tray
	trayToggle   *mygo.MenuItem
	mainReady    bool
	showOnReady  bool
	quitting     atomic.Bool
	triggers     chan input.Trigger
	done         chan struct{}
	doneOnce     sync.Once
	overlayTimer *time.Timer
	sequence     uint64
	overlayError error
}

// Run starts MyGo on the process's main thread and keeps the input service
// alive when the settings window and even the tray icon are hidden.
func Run(assets Assets) error {
	mygo.App.SetName("Mouse Mapper")
	if mygo.App.Version() == "" {
		mygo.App.SetVersion("0.1.0")
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return nil
	}
	directory := os.Getenv("MOUSE_MAPPER_CONFIG_DIR")
	if directory == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("find configuration directory: %w", err)
		}
		directory = filepath.Join(base, "Mouse Mapper")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if os.Getenv("MYGO_GENERATE") == "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create configuration directory: %w", err)
		}
	}
	path := filepath.Join(directory, "settings.json")
	// Keep MyGo's window and updater preferences beside our settings, including
	// when a test or a portable deployment overrides the configuration directory.
	mygo.App.SetPath(mygo.PathUserData, directory)
	app := &application{assets: assets, triggers: make(chan input.Trigger, 32), done: make(chan struct{})}
	engine := input.New(func(trigger input.Trigger) {
		// Never hold up a native input hook while the interface is busy painting.
		select {
		case app.triggers <- trigger:
		default:
		}
	})
	app.mapper = newMapper(config.Store{Path: path}, engine, app, path, mygo.App.Version())
	app.mapper.onChanged = func(_ State) {
		if app.quitting.Load() {
			return
		}
		mygo.RunOnMain(func() {
			if app.quitting.Load() {
				return
			}
			state := app.mapper.GetState()
			app.refreshTray(state)
			if app.view != nil && app.mainWindow != nil {
				app.view.acceptState(state)
				app.mainWindow.Invalidate()
			}
		})
	}
	mygo.Use(updaterUI.New(updater.Options{Interval: updateIntervalDays * 24 * time.Hour, Icon: assets.Icon, Language: "zh-Hans"}))
	app.mapper.updates = nativeUpdater{}
	updater.OnChange(app.mapper.publish)
	mygo.Theme.SetSource(mygo.ThemeLight)
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnSecondInstance(func(_ []string, _ string) { app.showMain() })
	mygo.App.OnActivate(func(_ bool) { app.showMain() })
	mygo.App.OnBeforeQuit(func(_ *mygo.QuitEvent) {
		app.quitting.Store(true)
		if app.overlayTimer != nil {
			app.overlayTimer.Stop()
		}
		app.doneOnce.Do(func() { close(app.done) })
	})
	mygo.App.OnQuit(func() {
		if err := app.mapper.stop(); err != nil {
			log.Printf("stop input: %v", err)
		}
	})
	mygo.App.WhenReady(app.ready)
	return mygo.App.Run()
}

func (app *application) ready() {
	app.showOnReady = !mygo.App.WasOpenedAtLogin()
	app.view = newNativeView(app.mapper)
	app.view.dispatch = func(fn func()) {
		if app.mainWindow != nil && !app.quitting.Load() {
			app.mainWindow.Update(fn)
		}
	}
	app.mainWindow = mygo.NewWindow(mygo.WindowOptions{
		Title: "Mouse Mapper", Content: ui.View(app.view.main), Width: 1080, Height: 760,
		MinWidth: 880, MinHeight: 640, Hidden: true,
		TitleBarStyle: mygo.TitleBarHiddenInset, TitleBarHeight: 52,
		BackgroundColor: "#f5f5f7", StateKey: "preferences",
	})
	app.mainReady = true
	app.mainWindow.SetIcon(app.assets.Icon)
	app.mainWindow.SetMenu(mygo.NewMenu(nil))
	app.mainWindow.OnClose(func(event *mygo.CloseEvent) {
		if app.quitting.Load() {
			return
		}
		event.PreventDefault()
		app.showOnReady = false
		app.mainWindow.Hide()
		go func() { _, _ = app.mapper.SetRecording(false) }()
	})
	app.mainWindow.OnHide(func() {
		go func() { _, _ = app.mapper.SetRecording(false) }()
	})
	app.overlay = mygo.NewWindow(mygo.WindowOptions{
		Title: "Mouse Mapper Shortcut", Content: ui.View(app.view.overlay), Width: 350, Height: 104,
		Hidden: true, Frameless: true, Transparent: true, BackgroundColor: "#0000",
		AlwaysOnTop: true, SkipTaskbar: true, DisableResize: true, DisableMove: true,
		DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true, DisableShadow: true,
		Panel: true,
	})
	app.overlay.SetMenu(mygo.NewMenu(nil))
	app.overlay.SetIgnoreMouseEvents(true)
	app.overlay.SetVisibleOnAllWorkspaces(true)
	app.overlayError = configureOverlay(app.overlay)
	// A hidden native surface does not paint until shown. Waiting for the
	// web-page-style ready event here would keep the first window hidden forever.
	if app.showOnReady {
		app.showMain()
	}
	go app.receiveTriggers()
	go func() {
		app.mapper.initialize()
		if app.mapper.GetState().Warning != "" {
			mygo.RunOnMain(app.showMain)
		}
	}()
}

func (app *application) showMain() {
	if app.quitting.Load() {
		return
	}
	app.showOnReady = true
	if app.mainWindow != nil && app.mainReady {
		app.mainWindow.Show()
		app.mainWindow.Restore()
		app.mainWindow.Focus()
		go func() { _, _ = app.mapper.SetRecording(false) }()
	}
}

func (app *application) openAtLogin() bool { return mygo.App.OpenAtLogin() }

func (app *application) setOpenAtLogin(enabled bool) error {
	return mygo.App.SetOpenAtLogin(enabled)
}

func (app *application) applyPreferences(settings Settings) error {
	var trayErr error
	mygo.RunOnMain(func() {
		if app.quitting.Load() {
			return
		}
		if settings.HideTray {
			if app.tray != nil {
				app.tray.Destroy()
				app.tray = nil
				app.trayToggle = nil
			}
		} else if app.tray == nil {
			app.trayToggle = &mygo.MenuItem{
				Label: "Pause mappings",
				Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
					go func() {
						if _, err := app.mapper.ToggleEnabled(); err != nil {
							app.mapper.setDesktopWarning(err)
							app.mapper.publish()
							mygo.RunOnMain(app.showMain)
						}
					}()
				},
			}
			icon, template := app.assets.Tray, false
			if runtime.GOOS == "darwin" {
				icon, template = app.assets.TrayTemplate, true
			}
			app.tray, trayErr = mygo.NewTray(mygo.TrayOptions{
				Icon: icon, IconIsTemplate: template, ToolTip: "Mouse Mapper",
				Menu: mygo.NewMenu([]*mygo.MenuItem{
					{Label: "Open Mouse Mapper", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { app.showMain() }},
					mygo.Separator(), app.trayToggle, updater.MenuItem(), mygo.Separator(),
					{Label: "Quit Mouse Mapper", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { app.quit() }},
				}),
			})
			if trayErr != nil {
				app.showMain()
			}
		}
		if !settings.ShowOverlay {
			app.sequence++
			if app.overlayTimer != nil {
				app.overlayTimer.Stop()
			}
			app.overlay.Hide()
		}
		app.refreshTray(app.mapper.GetState())
	})
	if trayErr != nil {
		trayErr = fmt.Errorf("The tray icon is unavailable: %w. Reopen the app to return to settings", trayErr)
	}
	return errors.Join(trayErr, app.overlayError)
}

func (app *application) refreshTray(state State) {
	if app.tray == nil || app.trayToggle == nil {
		return
	}
	label, tip := "Resume mappings", "Mouse Mapper - Paused"
	if state.Settings.Enabled {
		label, tip = "Pause mappings", "Mouse Mapper - Active"
		if !state.Status.Running {
			tip = "Mouse Mapper - Needs attention"
		} else if state.Recording {
			tip = "Mouse Mapper - Recording a shortcut"
		}
	}
	app.trayToggle.SetLabel(label)
	app.tray.SetToolTip(tip)
}

func (app *application) receiveTriggers() {
	health := time.NewTicker(2 * time.Second)
	defer health.Stop()
	lastStatus := app.mapper.engine.Status()
	for {
		select {
		case <-app.done:
			return
		case <-health.C:
			status := app.mapper.engine.Status()
			if status != lastStatus {
				lastStatus = status
				app.mapper.publish()
			}
		case trigger := <-app.triggers:
			event, show := app.mapper.shortcut(trigger)
			app.notify(event, show)
		}
	}
}

func (app *application) preview(event ShortcutEvent) error {
	if app.quitting.Load() {
		return errors.New("the app is quitting")
	}
	app.notify(event, true)
	return app.overlayError
}

func (app *application) notify(event ShortcutEvent, show bool) {
	mygo.RunOnMain(func() {
		if app.quitting.Load() || app.overlay == nil {
			return
		}
		app.sequence++
		event.Sequence = app.sequence
		app.view.acceptShortcut(event)
		app.mainWindow.Invalidate()
		app.overlay.Invalidate()
		if !show || app.overlayError != nil {
			app.overlay.Hide()
			return
		}
		display := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
		area := display.WorkArea
		app.overlay.SetPosition(area.X+area.Width-350-24, area.Y+area.Height-104-24)
		app.overlay.ShowInactive()
		if app.overlayTimer != nil {
			app.overlayTimer.Stop()
		}
		sequence := event.Sequence
		app.overlayTimer = time.AfterFunc(time.Duration(event.Duration)*time.Millisecond, func() {
			mygo.RunOnMain(func() {
				if !app.quitting.Load() && app.sequence == sequence {
					app.overlay.Hide()
				}
			})
		})
	})
}

func (app *application) quit() { mygo.App.Quit() }
