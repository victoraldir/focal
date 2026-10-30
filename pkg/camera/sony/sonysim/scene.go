package sonysim

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
)

// sceneParams drives the synthetic picture: a sunset that loops every
// sunsetPeriod, so an interval session against the simulator produces a
// recognisable timelapse.
type sceneParams struct {
	// phase runs 0→1 over one sunset.
	phase float64
	// exposure is a linear brightness multiplier (2^EV).
	exposure float64
	// zoom is 0–100; it magnifies the sun.
	zoom int
	// focused is false while the simulated lens hunts, which softens the sun.
	focused bool
}

// renderScene draws the scene at w×h and returns it as a JPEG.
func renderScene(w, h int, p sceneParams, quality int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fw, fh := float64(w), float64(h)

	// Sun descends from high in the sky to below the horizon.
	horizon := 0.68 * fh
	sunX := fw * (0.35 + 0.3*p.phase)
	sunY := fh*0.15 + (horizon+fh*0.12-fh*0.15)*p.phase
	sunR := fh * (0.06 + 0.06*float64(p.zoom)/100)
	glowR := sunR * 3.2
	edge := 1.5
	if !p.focused {
		edge = sunR * 0.5
	}

	// Sky colours blend from day to dusk as phase advances.
	day := [2][3]float64{{70, 140, 230}, {170, 210, 245}}
	dusk := [2][3]float64{{30, 25, 80}, {250, 120, 60}}
	lerp := func(a, b, t float64) float64 { return a + (b-a)*t }
	t := smooth(p.phase)

	// Ridge line: two octaves of sine give a believable silhouette.
	ridge := make([]float64, w)
	for x := range ridge {
		fx := float64(x) / fw
		ridge[x] = horizon - fh*(0.05*math.Sin(fx*7.1+1.3)+0.03*math.Sin(fx*19.7+0.4)+0.04)
	}

	for y := 0; y < h; y++ {
		fy := float64(y) / horizon
		var sky [3]float64
		for c := 0; c < 3; c++ {
			top := lerp(day[0][c], dusk[0][c], t)
			bottom := lerp(day[1][c], dusk[1][c], t)
			sky[c] = lerp(top, bottom, math.Min(fy, 1))
		}
		for x := 0; x < w; x++ {
			var r, g, b float64
			if float64(y) > ridge[x] {
				// Foreground hills, faintly lit by the sky.
				shade := 0.25 * (1 - t*0.8)
				r, g, b = sky[0]*shade, sky[1]*shade*1.1, sky[2]*shade*0.9
			} else {
				r, g, b = sky[0], sky[1], sky[2]
				dx, dy := float64(x)-sunX, float64(y)-sunY
				d := math.Hypot(dx, dy)
				if d < glowR {
					glow := math.Pow(1-d/glowR, 2) * 0.55
					r, g, b = r+(255-r)*glow, g+(200-g)*glow, b+(120-b)*glow
				}
				if d < sunR+edge {
					a := math.Min(1, math.Max(0, (sunR+edge-d)/(2*edge)))
					sun := [3]float64{255, lerp(250, 170, t), lerp(220, 90, t)}
					r, g, b = lerp(r, sun[0], a), lerp(g, sun[1], a), lerp(b, sun[2], a)
				}
			}
			img.SetRGBA(x, y, color.RGBA{clamp(r * p.exposure), clamp(g * p.exposure), clamp(b * p.exposure), 255})
		}
	}

	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	return buf.Bytes()
}

func smooth(x float64) float64 { return x * x * (3 - 2*x) }

func clamp(v float64) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	default:
		return uint8(v)
	}
}
