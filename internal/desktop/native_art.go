package desktop

import (
	_ "embed"
	"log"
	"sync"

	"github.com/egoist/mygo/ui"
)

//go:embed assets/Manrope.ttf
var interfaceFont []byte

var registerInterfaceFont sync.Once

func prepareInterfaceFont() {
	registerInterfaceFont.Do(func() {
		if err := ui.RegisterFont(interfaceFont, "Manrope"); err != nil {
			log.Printf("use platform font: %v", err)
		}
	})
}

func lineIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`))
}

var (
	iconMouse    = lineIcon(`<rect x="6.5" y="2.5" width="11" height="19" rx="5.5"/><path d="M12 3v6M6.5 10h11"/>`)
	iconSettings = lineIcon(`<path d="M4 7h7m4 0h5M4 17h3m4 0h9"/><circle cx="13" cy="7" r="2"/><circle cx="9" cy="17" r="2"/>`)
	iconKeyboard = lineIcon(`<rect x="2.5" y="5" width="19" height="14" rx="3"/><path d="M6 9h.01M10 9h.01M14 9h.01M18 9h.01M6 12.5h.01M10 12.5h.01M14 12.5h.01M18 12.5h.01M8 16h8"/>`)
	iconBack     = lineIcon(`<path d="m10 5-7 7 7 7M3 12h18"/>`)
	iconForward  = lineIcon(`<path d="m14 5 7 7-7 7M21 12H3"/>`)
	iconChevron  = lineIcon(`<path d="m9 6 6 6-6 6"/>`)
	iconCheck    = lineIcon(`<path d="m5 12 4 4L19 6"/>`)
	iconPower    = lineIcon(`<path d="M12 2v9M6 5a9 9 0 1 0 12 0"/>`)
	iconEye      = lineIcon(`<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>`)
	iconSparkle  = lineIcon(`<path d="m12 3 2.4 6.6L21 12l-6.6 2.4L12 21l-2.4-6.6L3 12l6.6-2.4L12 3Z"/>`)
	iconHelp     = lineIcon(`<circle cx="12" cy="12" r="9"/><path d="M9.5 9a2.5 2.5 0 0 1 5 0c0 2-2.5 2-2.5 4M12 16.5h.01"/>`)
	iconClose    = lineIcon(`<path d="m6 6 12 12M6 18 18 6"/>`)
	iconExternal = lineIcon(`<path d="M14 3h7v7m0-7L10 14M10 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-5"/>`)
	iconRefresh  = lineIcon(`<path d="M20 7v5h-5M4 17v-5h5M5.5 7a7.5 7.5 0 0 1 12.8-2L20 7M4 17l1.7 2a7.5 7.5 0 0 0 12.8-2"/>`)
	iconShield   = lineIcon(`<path d="m12 2 8 3v6c0 5-8 10-8 10S4 16 4 11V5l8-3Z"/><path d="m8.5 11.5 2.5 2.5 4.5-5"/>`)
	iconTray     = lineIcon(`<path d="M4 8h16l2 11H2L4 8ZM8 8V4h8v4M3 14h5l2 3h4l2-3h5"/>`)
)

var mousePicture = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 350 370">
  <defs>
    <linearGradient id="body" x1="0" y1="0" x2="1" y2="0.8"><stop stop-color="#ffffff"/><stop offset="0.38" stop-color="#fafbfc"/><stop offset="1" stop-color="#dce1e8"/></linearGradient>
    <linearGradient id="side" x1="0" y1="0" x2="1" y2="0"><stop stop-color="#aeb7c5"/><stop offset="0.3" stop-color="#d1d8e2"/><stop offset="1" stop-color="#edf0f4"/></linearGradient>
    <linearGradient id="wheel" x1="0" y1="0" x2="1" y2="0"><stop stop-color="#aeb7c4"/><stop offset="0.5" stop-color="#e0e4eb"/><stop offset="1" stop-color="#abb5c4"/></linearGradient>
    <radialGradient id="shadow"><stop stop-color="#8394ab" stop-opacity=".18"/><stop offset="1" stop-color="#8394ab" stop-opacity="0"/></radialGradient>
  </defs>
  <ellipse cx="175" cy="311" rx="110" ry="45" fill="url(#shadow)"/>
  <g transform="rotate(17 188 180)">
    <path d="M113 145C113 58 140 30 187 30c53 0 81 34 81 116l-2 100c-2 49-26 83-76 85-47 1-76-29-78-78Z" fill="url(#side)" stroke="#c5ccd7"/>
    <path d="M123 139c0-81 22-110 66-110 49 0 74 33 74 110v105c0 48-25 76-70 76-44 0-70-28-70-76Z" fill="url(#body)" stroke="#cbd2dc" stroke-width=".8"/>
    <path d="M193 30v111m-69 12c39 12 94 11 138-2" fill="none" stroke="#cdd4de" stroke-width="1.1"/>
    <path d="M129 126c0-61 18-85 45-91" fill="none" stroke="white" stroke-width="2.5" stroke-linecap="round" opacity=".8"/>
    <rect x="183" y="70" width="18" height="47" rx="9" fill="#b4bdcb" stroke="white" stroke-width="2"/>
    <rect x="186" y="73" width="12" height="41" rx="6" fill="url(#wheel)"/>
    <path d="M188 80h8m-8 5h8m-8 5h8m-8 5h8m-8 5h8m-8 5h8" stroke="#9aa6b7" stroke-width=".7"/>
    <path d="M113 127c1-5 8-7 9-1l-1 28c0 5-7 8-8 2Z" fill="#538bcb"/>
    <path d="M113 166c1-5 8-6 8 0v28c0 6-7 7-8 1Z" fill="#538bcb"/>
    <path d="M178 270h28" stroke="#d3d9e2" stroke-width="2" stroke-linecap="round"/>
  </g>
  <g fill="none" stroke="#a4b3c5" stroke-width="1" stroke-dasharray="2 3">
    <path d="M105 116H62l-12-12H22"/><path d="M93 155H64l-14 18H22"/>
  </g>
  <g fill="#7b96b8"><circle cx="106" cy="116" r="3"/><circle cx="94" cy="155" r="3"/></g>
</svg>`))
