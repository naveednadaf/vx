// Generates the extension icons (flat rounded-square + node glyph).
// Run: go run ./internal/genicons
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

func lerp(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		255,
	}
}

func drawIcon(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg1 := color.RGBA{13, 17, 23, 255}   // #0d1117
	bg2 := color.RGBA{22, 27, 34, 255}   // #161b22
	accent := color.RGBA{45, 212, 191, 255}  // #2dd4bf
	accent2 := color.RGBA{59, 130, 246, 255} // #3b82f6
	radius := float64(size) * 0.22

	cx, cy := float64(size)/2, float64(size)/2

	// Node positions on a circle (network motif).
	nodes := make([][2]float64, 5)
	for i := range nodes {
		angle := float64(i)*2*math.Pi/5 - math.Pi/2
		nodes[i] = [2]float64{cx + math.Cos(angle)*float64(size)*0.27, cy + math.Sin(angle)*float64(size)*0.27}
	}
	center := [2]float64{cx, cy}
	lineW := float64(size) * 0.035
	nodeR := float64(size) * 0.065

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5

			// Rounded-rect mask.
			dx := math.Max(math.Abs(px-cx)-(float64(size)/2-radius), 0)
			dy := math.Max(math.Abs(py-cy)-(float64(size)/2-radius), 0)
			if math.Hypot(dx, dy) > 1 {
				continue
			}

			c := lerp(bg1, bg2, (px+py)/float64(size*2))

			// Lines from center to nodes.
			for _, n := range nodes {
				if distToSegment(px, py, center[0], center[1], n[0], n[1]) < lineW {
					c = lerp(c, accent2, 0.55)
				}
			}
			// Node dots.
			for _, n := range nodes {
				if math.Hypot(px-n[0], py-n[1]) < nodeR {
					c = accent
				}
			}
			// Center dot (the proxy).
			if math.Hypot(px-cx, py-cy) < nodeR*1.4 {
				c = accent
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func distToSegment(px, py, x1, y1, x2, y2 float64) float64 {
	dx, dy := x2-x1, y2-y1
	if dx == 0 && dy == 0 {
		return math.Hypot(px-x1, py-y1)
	}
	t := ((px-x1)*dx + (py-y1)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(x1+t*dx), py-(y1+t*dy))
}

func main() {
	out := os.Args[1]
	if out == "" {
		out = "extension/icons"
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	for _, size := range []int{16, 32, 48, 128} {
		f, err := os.Create(filepath.Join(out, "icon"+strconv.Itoa(size)+".png"))
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, drawIcon(size)); err != nil {
			panic(err)
		}
		f.Close()
	}
}