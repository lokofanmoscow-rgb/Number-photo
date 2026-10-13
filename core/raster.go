// Package core contains the image analysis (leader line detection), label
// placement, ordering and rendering logic. It has no GUI dependencies so it
// can be built and tested on any platform.
package core

import (
	"image"
	"image/draw"
	"math"
)

// Pt is a point in image coordinates (pixels).
type Pt struct{ X, Y float64 }

func (p Pt) Add(q Pt) Pt       { return Pt{p.X + q.X, p.Y + q.Y} }
func (p Pt) Sub(q Pt) Pt       { return Pt{p.X - q.X, p.Y - q.Y} }
func (p Pt) Mul(k float64) Pt  { return Pt{p.X * k, p.Y * k} }
func (p Pt) Dot(q Pt) float64  { return p.X*q.X + p.Y*q.Y }
func (p Pt) Len() float64      { return math.Hypot(p.X, p.Y) }
func (p Pt) Dist(q Pt) float64 { return p.Sub(q).Len() }
func (p Pt) Perp() Pt          { return Pt{-p.Y, p.X} }
func (p Pt) Norm() Pt {
	l := p.Len()
	if l == 0 {
		return Pt{}
	}
	return p.Mul(1 / l)
}

// Rect is an axis-aligned rectangle [X0,X1) x [Y0,Y1) with float coordinates.
type Rect struct{ X0, Y0, X1, Y1 float64 }

func (r Rect) W() float64 { return r.X1 - r.X0 }
func (r Rect) H() float64 { return r.Y1 - r.Y0 }
func (r Rect) Center() Pt { return Pt{(r.X0 + r.X1) / 2, (r.Y0 + r.Y1) / 2} }
func (r Rect) Inset(m float64) Rect {
	return Rect{r.X0 + m, r.Y0 + m, r.X1 - m, r.Y1 - m}
}
func (r Rect) Overlaps(o Rect) bool {
	return r.X0 < o.X1 && o.X0 < r.X1 && r.Y0 < o.Y1 && o.Y0 < r.Y1
}
func (r Rect) Contains(p Pt) bool {
	return p.X >= r.X0 && p.X < r.X1 && p.Y >= r.Y0 && p.Y < r.Y1
}

// RectAt returns a w x h rectangle centred on c.
func RectAt(c Pt, w, h float64) Rect {
	return Rect{c.X - w/2, c.Y - h/2, c.X + w/2, c.Y + h/2}
}

// ToRGBA converts any image to a tightly packed *image.RGBA with origin 0,0.
func ToRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// Mask is a binary image.
type Mask struct {
	W, H int
	Pix  []bool
}

func NewMask(w, h int) *Mask { return &Mask{W: w, H: h, Pix: make([]bool, w*h)} }

func (m *Mask) At(x, y int) bool {
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return false
	}
	return m.Pix[y*m.W+x]
}

// Integral is a summed-area table of a mask for O(1) box counts.
type Integral struct {
	W, H int
	S    []int32 // (W+1)*(H+1)
}

func NewIntegral(m *Mask) *Integral {
	w, h := m.W, m.H
	s := make([]int32, (w+1)*(h+1))
	for y := 0; y < h; y++ {
		var row int32
		for x := 0; x < w; x++ {
			if m.Pix[y*w+x] {
				row++
			}
			s[(y+1)*(w+1)+x+1] = s[y*(w+1)+x+1] + row
		}
	}
	return &Integral{W: w, H: h, S: s}
}

// Count returns the number of set pixels in the integer box [x0,x1) x [y0,y1)
// clipped to the image.
func (in *Integral) Count(x0, y0, x1, y1 int) int {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > in.W {
		x1 = in.W
	}
	if y1 > in.H {
		y1 = in.H
	}
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	w := in.W + 1
	return int(in.S[y1*w+x1] - in.S[y0*w+x1] - in.S[y1*w+x0] + in.S[y0*w+x0])
}

// Fraction returns the share of set pixels inside r (only the part of r that
// lies inside the image is considered) and the share of r that lies inside the
// image.
func (in *Integral) Fraction(r Rect) (frac, inside float64) {
	x0, y0 := int(math.Floor(r.X0)), int(math.Floor(r.Y0))
	x1, y1 := int(math.Ceil(r.X1)), int(math.Ceil(r.Y1))
	total := (x1 - x0) * (y1 - y0)
	if total <= 0 {
		return 0, 0
	}
	cx0, cy0, cx1, cy1 := max(x0, 0), max(y0, 0), min(x1, in.W), min(y1, in.H)
	area := (cx1 - cx0) * (cy1 - cy0)
	if cx1 <= cx0 || cy1 <= cy0 || area <= 0 {
		return 0, 0
	}
	return float64(in.Count(cx0, cy0, cx1, cy1)) / float64(area), float64(area) / float64(total)
}
