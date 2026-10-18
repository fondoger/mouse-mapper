// Command genicons renders the app's code-native vector icon without external tools.
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "resources", "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0755); err != nil {
		log.Fatal(err)
	}
	for _, asset := range []struct {
		name     string
		size     int
		template bool
	}{
		{"icon.png", 1024, false},
		{"tray.png", 64, false},
		{"tray-template.png", 64, true},
	} {
		img := render(asset.size, asset.template)
		file, err := os.Create(filepath.Join(*out, asset.name))
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(file, img); err != nil {
			file.Close()
			log.Fatal(err)
		}
		if err := file.Close(); err != nil {
			log.Fatal(err)
		}
	}
}

func render(size int, template bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const samples = 4
	for py := range size {
		for px := range size {
			var red, green, blue, alpha float64
			for sy := range samples {
				for sx := range samples {
					x := (float64(px) + (float64(sx)+0.5)/samples) * 1024 / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/samples) * 1024 / float64(size)
					r, g, b, a := sample(x, y, template)
					red += r * a
					green += g * a
					blue += b * a
					alpha += a
				}
			}
			if alpha > 0 {
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(math.Round(red / alpha)), G: uint8(math.Round(green / alpha)),
					B: uint8(math.Round(blue / alpha)), A: uint8(math.Round(alpha * 255 / (samples * samples))),
				})
			}
		}
	}
	return img
}

func sample(x, y float64, template bool) (r, g, b, a float64) {
	stroke := 34.0
	if template {
		stroke = 48
	}
	outline := roundedRect(x, y, 557, 515, 358, 580, 177)
	mouse := math.Abs(outline) <= stroke/2 ||
		roundedRect(x, y, 557, 366, 31, 94, 15) <= 0 ||
		roundedRect(x, y, 557, 474, 336, stroke, stroke/2) <= 0 ||
		roundedRect(x, y, 321, 446, 35, 87, 17) <= 0 ||
		roundedRect(x, y, 321, 557, 35, 73, 17) <= 0
	if mouse {
		if template {
			return 0, 0, 0, 1
		}
		return 255, 255, 255, 1
	}
	if template || roundedRect(x, y, 512, 512, 944, 944, 217) > 0 {
		return 0, 0, 0, 0
	}
	shade := (x*0.24 + y*0.76) / 1024
	return 58 - shade*30, 129 - shade*38, 248 - shade*19, 1
}

func roundedRect(x, y, cx, cy, width, height, radius float64) float64 {
	dx := math.Abs(x-cx) - width/2 + radius
	dy := math.Abs(y-cy) - height/2 + radius
	return math.Hypot(math.Max(dx, 0), math.Max(dy, 0)) + math.Min(math.Max(dx, dy), 0) - radius
}
