package main

import (
	_ "embed"
	"log"

	"github.com/fondoger/mouse-mapper/internal/desktop"
)

//go:embed resources/icon.png
var appIcon []byte

//go:embed resources/tray.png
var trayIcon []byte

//go:embed resources/tray-template.png
var trayTemplate []byte

func main() {
	if err := desktop.Run(desktop.Assets{Icon: appIcon, Tray: trayIcon, TrayTemplate: trayTemplate}); err != nil {
		log.Fatal(err)
	}
}
