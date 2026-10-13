package core

import (
	"image"
	"image/color"
	"math"
)

// DebugBackground returns an image visualising the background mask
// (background white, objects grey, black pixels black).
func (a *Analysis) DebugBackground() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, a.aw, a.ah))
	for y := 0; y < a.ah; y++ {
		for x := 0; x < a.aw; x++ {
			i := y*a.aw + x
			c := color.RGBA{255, 255, 255, 255}
			switch {
			case a.black.Pix[i] && a.thinBlk.Pix[i]:
				c = color.RGBA{0, 0, 0, 255}
			case a.black.Pix[i]:
				c = color.RGBA{90, 0, 0, 255}
			case !a.bg.Pix[i]:
				c = color.RGBA{150, 150, 150, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// DebugLines draws detected lines on a copy of img: inner part blue, free end
// red.
func DebugLines(img image.Image, lines []Line) *image.RGBA {
	dst := ToRGBA(img)
	for _, l := range lines {
		drawSeg(dst, l.Inner, l.Free, color.RGBA{0, 90, 255, 255}, 2)
		drawDot(dst, l.Free, 6, color.RGBA{255, 0, 0, 255})
	}
	return dst
}

// DebugBoxes outlines label boxes.
func DebugBoxes(dst *image.RGBA, lay *Layout) {
	for _, l := range lay.Labels {
		r := l.Box
		c := color.RGBA{255, 0, 255, 255}
		drawSeg(dst, Pt{r.X0, r.Y0}, Pt{r.X1, r.Y0}, c, 0)
		drawSeg(dst, Pt{r.X1, r.Y0}, Pt{r.X1, r.Y1}, c, 0)
		drawSeg(dst, Pt{r.X1, r.Y1}, Pt{r.X0, r.Y1}, c, 0)
		drawSeg(dst, Pt{r.X0, r.Y1}, Pt{r.X0, r.Y0}, c, 0)
	}
}

func drawSeg(dst *image.RGBA, p, q Pt, c color.RGBA, r int) {
	n := int(math.Ceil(p.Dist(q))) + 1
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		x := int(math.Round(p.X + (q.X-p.X)*t))
		y := int(math.Round(p.Y + (q.Y-p.Y)*t))
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				if image.Pt(x+dx, y+dy).In(dst.Rect) {
					dst.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}
}

func drawDot(dst *image.RGBA, p Pt, r int, c color.RGBA) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				x, y := int(p.X)+dx, int(p.Y)+dy
				if image.Pt(x, y).In(dst.Rect) {
					dst.SetRGBA(x, y, c)
				}
			}
		}
	}
}
