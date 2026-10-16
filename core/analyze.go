package core

import (
	"fmt"
	"image"
	"math"
	"sort"
)

// Line is a detected leader line. Free is the end where the number goes,
// Inner is the end that touches the object.
type Line struct {
	Free, Inner Pt
	Width       float64
	// Confidence of the free-end decision (difference of end scores).
	Confidence float64
}

// Dir returns the unit vector pointing from the inner end towards the free end.
func (l Line) Dir() Pt { return l.Free.Sub(l.Inner).Norm() }

// Length of the line in pixels.
func (l Line) Length() float64 { return l.Free.Dist(l.Inner) }

// Swapped returns the line with the ends exchanged.
func (l Line) Swapped() Line {
	l.Free, l.Inner = l.Inner, l.Free
	return l
}

// Analysis holds everything learnt about a photo.
type Analysis struct {
	W, H int // size of the original image

	// The analysis may run on a reduced copy of the photo. k converts original
	// coordinates into analysis coordinates (k <= 1).
	k      float64
	aw, ah int
	rgb    []uint8 // analysis image, 3 bytes per pixel

	black   *Mask // dark neutral pixels
	core    *Mask // very dark pixels (the core of drawn lines)
	bg      *Mask // background (mat + grid lines)
	objInt  *Integral
	blkInt  *Integral
	thinBlk *Mask

	Stroke    float64 // stroke width of drawn lines, original pixels
	MatColor  [3]float64
	GridColor [3]float64
	HasGrid   bool

	Lines []Line
	// DigitHeight is the suggested digit height (original pixels).
	DigitHeight float64
}

const (
	blackThreshold = 70
	blackMaxChroma = 28
	// Drawn lines are almost pure black in their core.
	coreThreshold = 38
	coreMaxChroma = 25
	// Median brightness along the axis of a drawn line must not exceed the
	// brightness of clearly drawn lines by more than this.
	lineDarknessMargin = 12
)

// maxAnalysisDim limits the resolution used for analysis.
const maxAnalysisDim = 2600

// Analyze finds the leader lines in img.
func Analyze(img image.Image) *Analysis {
	src := ToRGBA(img)
	a := &Analysis{W: src.Rect.Dx(), H: src.Rect.Dy(), k: 1}
	md := max(a.W, a.H)
	if md > maxAnalysisDim {
		a.k = float64(maxAnalysisDim) / float64(md)
	}
	a.prepare(src)
	a.DigitHeight = DefaultDigitHeight(a.W, a.H)
	a.estimateBackground()
	a.estimateStroke()
	a.detectLines()
	return a
}

// DefaultDigitHeight is the preferred digit height for an image of the given
// size (about 2.8% of the geometric mean of the sides).
func DefaultDigitHeight(w, h int) float64 {
	return math.Round(0.028 * math.Sqrt(float64(w)*float64(h)))
}

// prepare builds the (possibly reduced) RGB buffer used for analysis.
func (a *Analysis) prepare(src *image.RGBA) {
	if a.k == 1 {
		a.aw, a.ah = a.W, a.H
		a.rgb = make([]uint8, a.aw*a.ah*3)
		for y := 0; y < a.ah; y++ {
			row := src.Pix[y*src.Stride:]
			for x := 0; x < a.aw; x++ {
				o := (y*a.aw + x) * 3
				a.rgb[o], a.rgb[o+1], a.rgb[o+2] = row[x*4], row[x*4+1], row[x*4+2]
			}
		}
		return
	}
	a.aw = max(1, int(math.Round(float64(a.W)*a.k)))
	a.ah = max(1, int(math.Round(float64(a.H)*a.k)))
	a.rgb = make([]uint8, a.aw*a.ah*3)
	// Box filter downscale.
	sx := float64(a.W) / float64(a.aw)
	sy := float64(a.H) / float64(a.ah)
	for y := 0; y < a.ah; y++ {
		y0, y1 := int(float64(y)*sy), int(float64(y+1)*sy)
		y1 = min(max(y1, y0+1), a.H)
		for x := 0; x < a.aw; x++ {
			x0, x1 := int(float64(x)*sx), int(float64(x+1)*sx)
			x1 = min(max(x1, x0+1), a.W)
			var r, g, b, n int
			for yy := y0; yy < y1; yy++ {
				row := src.Pix[yy*src.Stride:]
				for xx := x0; xx < x1; xx++ {
					r += int(row[xx*4])
					g += int(row[xx*4+1])
					b += int(row[xx*4+2])
					n++
				}
			}
			o := (y*a.aw + x) * 3
			a.rgb[o], a.rgb[o+1], a.rgb[o+2] = uint8(r/n), uint8(g/n), uint8(b/n)
		}
	}
}

func (a *Analysis) px(i int) (float64, float64, float64) {
	return float64(a.rgb[i*3]), float64(a.rgb[i*3+1]), float64(a.rgb[i*3+2])
}

func colorDist(r, g, b float64, c [3]float64) float64 {
	return math.Sqrt((r-c[0])*(r-c[0]) + (g-c[1])*(g-c[1]) + (b-c[2])*(b-c[2]))
}

// segDist returns the distance from colour (r,g,b) to the segment p-q in RGB
// space.
func segDist(r, g, b float64, p, q [3]float64) float64 {
	d := [3]float64{q[0] - p[0], q[1] - p[1], q[2] - p[2]}
	v := [3]float64{r - p[0], g - p[1], b - p[2]}
	dd := d[0]*d[0] + d[1]*d[1] + d[2]*d[2]
	t := 0.0
	if dd > 0 {
		t = (v[0]*d[0] + v[1]*d[1] + v[2]*d[2]) / dd
	}
	t = math.Max(0, math.Min(1, t))
	return colorDist(r, g, b, [3]float64{p[0] + t*d[0], p[1] + t*d[1], p[2] + t*d[2]})
}

// dominantColor returns the mean colour of the most populated colour cell
// among the pixels selected by sel, refined by averaging pixels within radius.
func (a *Analysis) dominantColor(sel func(i int) bool, radius float64) ([3]float64, int) {
	var hist [4096]int
	n := a.aw * a.ah
	for i := 0; i < n; i++ {
		if !sel(i) {
			continue
		}
		r, g, b := a.rgb[i*3]>>4, a.rgb[i*3+1]>>4, a.rgb[i*3+2]>>4
		hist[int(r)<<8|int(g)<<4|int(b)]++
	}
	best, bc := 0, -1
	for k, c := range hist {
		// Smooth a little by adding direct neighbours.
		s := c
		r, g, b := k>>8, (k>>4)&15, k&15
		for _, d := range [][3]int{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}} {
			rr, gg, bb := r+d[0], g+d[1], b+d[2]
			if rr >= 0 && rr < 16 && gg >= 0 && gg < 16 && bb >= 0 && bb < 16 {
				s += hist[rr<<8|gg<<4|bb] / 2
			}
		}
		if s > bc {
			bc, best = s, k
		}
	}
	c := [3]float64{float64(best>>8)*16 + 8, float64((best>>4)&15)*16 + 8, float64(best&15)*16 + 8}
	count := 0
	for it := 0; it < 3; it++ {
		var sr, sg, sb float64
		count = 0
		for i := 0; i < n; i++ {
			if !sel(i) {
				continue
			}
			r, g, b := a.px(i)
			if colorDist(r, g, b, c) < radius {
				sr += r
				sg += g
				sb += b
				count++
			}
		}
		if count == 0 {
			break
		}
		c = [3]float64{sr / float64(count), sg / float64(count), sb / float64(count)}
	}
	return c, count
}

// runLengths computes, for every pixel where m is set, the length of the run
// of set pixels through it in the four principal directions.
func runLengths(m []bool, w, h int) (hr, vr, d1, d2 []uint16) {
	n := w * h
	hr, vr, d1, d2 = make([]uint16, n), make([]uint16, n), make([]uint16, n), make([]uint16, n)
	clamp := func(v int) uint16 {
		if v > 65535 {
			return 65535
		}
		return uint16(v)
	}
	// generic walker over a line of pixels given start and step
	walk := func(out []uint16, x, y, dx, dy int) {
		start := -1
		cnt := 0
		var idx []int
		for x >= 0 && y >= 0 && x < w && y < h {
			i := y*w + x
			if m[i] {
				if start < 0 {
					start = 0
					idx = idx[:0]
				}
				idx = append(idx, i)
				cnt++
			} else if start >= 0 {
				v := clamp(cnt)
				for _, j := range idx {
					out[j] = v
				}
				start, cnt = -1, 0
			}
			x += dx
			y += dy
		}
		if start >= 0 {
			v := clamp(cnt)
			for _, j := range idx {
				out[j] = v
			}
		}
	}
	for y := 0; y < h; y++ {
		walk(hr, 0, y, 1, 0)
	}
	for x := 0; x < w; x++ {
		walk(vr, x, 0, 0, 1)
	}
	// diagonals (down-right) start from top row and left column
	for x := 0; x < w; x++ {
		walk(d1, x, 0, 1, 1)
	}
	for y := 1; y < h; y++ {
		walk(d1, 0, y, 1, 1)
	}
	// anti-diagonals (down-left) start from top row and right column
	for x := 0; x < w; x++ {
		walk(d2, x, 0, -1, 1)
	}
	for y := 1; y < h; y++ {
		walk(d2, w-1, y, -1, 1)
	}
	return
}

// estimateBackground finds the mat colour, the grid-line colour and builds the
// background mask.
func (a *Analysis) estimateBackground() {
	n := a.aw * a.ah
	a.black = NewMask(a.aw, a.ah)
	a.core = NewMask(a.aw, a.ah)
	for i := 0; i < n; i++ {
		r, g, b := a.rgb[i*3], a.rgb[i*3+1], a.rgb[i*3+2]
		// Drawn lines are neutral black; coloured shadows are not.
		mx, chroma := max(r, g, b), max(r, g, b)-min(r, g, b)
		if mx < blackThreshold && chroma < blackMaxChroma {
			a.black.Pix[i] = true
		}
		if mx < coreThreshold && chroma < coreMaxChroma {
			a.core.Pix[i] = true
		}
	}
	const matRadius = 50.0
	a.MatColor, _ = a.dominantColor(func(i int) bool { return !a.black.Pix[i] }, 40)

	mat := make([]bool, n)
	nonMat := make([]bool, n)
	for i := 0; i < n; i++ {
		r, g, b := a.px(i)
		mat[i] = colorDist(r, g, b, a.MatColor) < matRadius
		nonMat[i] = !mat[i] && !a.black.Pix[i]
	}
	hr, vr, d1, d2 := runLengths(nonMat, a.aw, a.ah)
	gmax := uint16(math.Max(9, math.Round(0.0075*float64(max(a.aw, a.ah)))))
	thin := make([]bool, n)
	matLum := (a.MatColor[0] + a.MatColor[1] + a.MatColor[2]) / 3
	thinCount := 0
	for i := 0; i < n; i++ {
		if !nonMat[i] {
			continue
		}
		mn := min(hr[i], vr[i], d1[i], d2[i])
		if mn <= gmax {
			thin[i] = true
			thinCount++
		}
	}
	// Grid colour: dominant colour of thin structures that are brighter than
	// the mat.
	grid, cnt := a.dominantColor(func(i int) bool {
		if !thin[i] {
			return false
		}
		r, g, b := a.px(i)
		return (r+g+b)/3 > matLum+25
	}, 45)
	if cnt > n/400 {
		a.HasGrid = true
		a.GridColor = grid
	}

	a.bg = NewMask(a.aw, a.ah)
	obj := NewMask(a.aw, a.ah)
	for i := 0; i < n; i++ {
		if a.black.Pix[i] {
			obj.Pix[i] = true
			continue
		}
		if mat[i] {
			a.bg.Pix[i] = true
			continue
		}
		if a.HasGrid && thin[i] {
			r, g, b := a.px(i)
			if segDist(r, g, b, a.MatColor, a.GridColor) < 45 {
				a.bg.Pix[i] = true
				continue
			}
		}
		obj.Pix[i] = true
	}
	a.objInt = NewIntegral(obj)
	a.blkInt = NewIntegral(a.black)
}

// estimateStroke estimates the stroke width of drawn lines and builds the mask
// of thin black structures.
func (a *Analysis) estimateStroke() {
	w, h := a.aw, a.ah
	hr, vr, _, _ := runLengths(a.core.Pix, w, h)
	labels, sizes := components(a.core, true)
	var hist [161]float64 // 0.25 px bins up to 40 px
	sw := make([]float32, w*h)
	for i := range a.core.Pix {
		if !a.core.Pix[i] {
			continue
		}
		hh, vv := float64(hr[i]), float64(vr[i])
		s := hh * vv / math.Sqrt(hh*hh+vv*vv)
		sw[i] = float32(s)
		if sizes[labels[i]] < 60 {
			continue
		}
		b := int(math.Round(s * 4))
		if b >= 6 && b < len(hist) {
			hist[b]++
		}
	}
	// smooth and take the mode
	best, bv := 0, -1.0
	for i := 6; i < len(hist); i++ {
		s := 0.0
		for d := -2; d <= 2; d++ {
			if i+d >= 0 && i+d < len(hist) {
				s += hist[i+d] * (3 - math.Abs(float64(d))) / 3
			}
		}
		if s > bv {
			bv, best = s, i
		}
	}
	t := float64(best) / 4
	if t < 2 {
		t = 2
	}
	a.Stroke = t / a.k
	limit := float32(1.5*t + 1)
	a.thinBlk = NewMask(w, h)
	for i := range a.core.Pix {
		if a.core.Pix[i] && sw[i] <= limit {
			a.thinBlk.Pix[i] = true
		}
	}
}

// components labels connected components of m (8-connectivity when eight is
// true). Label 0 is the background; sizes[label] is the pixel count.
func components(m *Mask, eight bool) ([]int32, []int) {
	w, h := m.W, m.H
	labels := make([]int32, w*h)
	sizes := []int{0}
	stack := make([]int, 0, 1024)
	for i := range m.Pix {
		if !m.Pix[i] || labels[i] != 0 {
			continue
		}
		id := int32(len(sizes))
		size := 0
		labels[i] = id
		stack = append(stack[:0], i)
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			size++
			x, y := j%w, j/w
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 || (!eight && dx != 0 && dy != 0) {
						continue
					}
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					k := yy*w + xx
					if m.Pix[k] && labels[k] == 0 {
						labels[k] = id
						stack = append(stack, k)
					}
				}
			}
		}
		sizes = append(sizes, size)
	}
	return labels, sizes
}

// ---------------------------------------------------------------------------
// Line extraction

type ipt struct{ x, y int32 }

// segment in analysis coordinates
type segment struct {
	p0, p1 Pt
	width  float64
}

func (s segment) dir() Pt         { return s.p1.Sub(s.p0).Norm() }
func (s segment) length() float64 { return s.p1.Dist(s.p0) }

// detectLines extracts straight thick black strokes.
func (a *Analysis) detectLines() {
	t := a.Stroke * a.k
	lmin := math.Max(4*t, 0.012*float64(max(a.aw, a.ah)))
	labels, sizes := components(a.thinBlk, true)
	groups := make([][]ipt, len(sizes))
	minArea := int(lmin * t * 0.5)
	for i, l := range labels {
		if l == 0 || sizes[l] < minArea {
			continue
		}
		groups[l] = append(groups[l], ipt{int32(i % a.aw), int32(i / a.aw)})
	}
	var segs []segment
	for _, g := range groups {
		if len(g) < minArea {
			continue
		}
		segs = append(segs, a.houghSegments(g, t, lmin)...)
	}
	segs = a.mergeCollinear(segs, t)
	// Drawn lines are the darkest strokes in the photo. Measure how dark the
	// obvious ones are (long, with clean sides) to reject dark object edges.
	// The reference is the darkest such stroke, not a typical one: a dark
	// object (a perforated tray, a grille) can give dozens of slightly
	// lighter candidates against a handful of drawn lines.
	dark := make([]float64, len(segs))
	ref := math.Inf(1)
	for i, s := range segs {
		dark[i] = a.axisDarkness(s)
		if s.length() >= 2*lmin && a.sideClean(s, t) >= 0.8 && s.width >= 0.55*t && s.width <= 1.7*t {
			ref = math.Min(ref, dark[i])
		}
	}
	maxDark := 20.0
	if !math.IsInf(ref, 1) {
		// Thin lines in a reduced photo are lighter and vary more, hence
		// the relative part.
		maxDark = math.Max(ref+lineDarknessMargin, 2*ref)
	}
	maxDark = math.Max(12, math.Min(32, maxDark))
	// keep segments whose width is consistent with the stroke width
	var good []segment
	for i, s := range segs {
		if debugLines {
			fmt.Printf("seg (%.0f,%.0f)-(%.0f,%.0f) len=%.0f w=%.1f dark=%.0f/%.0f sides=%.2f\n", s.p0.X, s.p0.Y, s.p1.X, s.p1.Y, s.length(), s.width, dark[i], maxDark, a.sideClean(s, t))
		}
		if s.length() >= lmin && s.width >= 0.55*t && s.width <= 1.7*t && dark[i] <= maxDark {
			good = append(good, s)
		}
	}
	// Re-estimate the stroke from accepted segments (median) and filter again.
	if len(good) >= 3 {
		ws := make([]float64, len(good))
		for i, s := range good {
			ws[i] = s.width
		}
		sort.Float64s(ws)
		med := ws[len(ws)/2]
		var g2 []segment
		for _, s := range good {
			if s.width >= 0.6*med && s.width <= 1.5*med {
				g2 = append(g2, s)
			}
		}
		good = g2
		a.Stroke = med / a.k
	}
	a.Lines = a.Lines[:0]
	for _, s := range good {
		if l, ok := a.orient(s); ok {
			a.Lines = append(a.Lines, l)
		}
	}
}

// houghSegments repeatedly finds the strongest straight stroke among pts.
func (a *Analysis) houghSegments(pts []ipt, t, lmin float64) []segment {
	const nTheta = 360
	var cosT, sinT [nTheta]float64
	for i := 0; i < nTheta; i++ {
		th := math.Pi * float64(i) / nTheta
		cosT[i], sinT[i] = math.Cos(th), math.Sin(th)
	}
	var out []segment
	alive := make([]bool, len(pts))
	for i := range alive {
		alive[i] = true
	}
	remaining := len(pts)
	failures := 0
	minArea := int(lmin * t * 0.5)
	for remaining >= minArea && failures < 25 && len(out) < 60 {
		// bounding box of alive points
		x0, y0, x1, y1 := int32(math.MaxInt32), int32(math.MaxInt32), int32(math.MinInt32), int32(math.MinInt32)
		for i, p := range pts {
			if !alive[i] {
				continue
			}
			x0, y0 = min(x0, p.x), min(y0, p.y)
			x1, y1 = max(x1, p.x), max(y1, p.y)
		}
		bw, bh := int(x1-x0)+1, int(y1-y0)+1
		R := 2*bw + bh + 4
		off := bw + 1
		acc := make([]int32, nTheta*R)
		for i, p := range pts {
			if !alive[i] {
				continue
			}
			fx, fy := float64(p.x-x0), float64(p.y-y0)
			for th := 0; th < nTheta; th++ {
				r := int(math.Round(fx*cosT[th]+fy*sinT[th])) + off
				acc[th*R+r]++
			}
		}
		win := int(math.Round(t)) + 1
		bestV, bestTh, bestR := int32(-1), 0, 0
		for th := 0; th < nTheta; th++ {
			row := acc[th*R : (th+1)*R]
			var s int32
			for r := 0; r < R; r++ {
				s += row[r]
				if r >= win {
					s -= row[r-win]
				}
				if s > bestV {
					bestV, bestTh, bestR = s, th, r-win/2
				}
			}
		}
		if float64(bestV) < lmin*t*0.5 {
			break
		}
		n := Pt{cosT[bestTh], sinT[bestTh]}
		rho := float64(bestR - off)
		origin := Pt{float64(x0), float64(y0)}
		tol := t/2 + 1.5
		// collect inliers and refine with PCA
		sel := func(c Pt, nrm Pt, rhoC float64) []int {
			var idx []int
			for i, p := range pts {
				if !alive[i] {
					continue
				}
				q := Pt{float64(p.x), float64(p.y)}
				if math.Abs(q.Sub(c).Dot(nrm)-rhoC) <= tol {
					idx = append(idx, i)
				}
			}
			return idx
		}
		idx := sel(origin, n, rho)
		c, u := pca(pts, idx)
		nrm := u.Perp()
		idx = sel(c, nrm, 0)
		if len(idx) == 0 {
			failures++
			continue
		}
		c, u = pca(pts, idx)
		nrm = u.Perp()
		idx = sel(c, nrm, 0)
		seg, runIdx, ok := a.longestRun(pts, idx, c, u, t)
		if ok && seg.length() >= lmin {
			out = append(out, seg)
			failures = 0
			// remove everything near the accepted segment
			d := seg.dir()
			nn := d.Perp()
			for i, p := range pts {
				if !alive[i] {
					continue
				}
				q := Pt{float64(p.x), float64(p.y)}
				v := q.Sub(seg.p0)
				s := v.Dot(d)
				if math.Abs(v.Dot(nn)) <= t/2+2.5 && s >= -t && s <= seg.length()+t {
					alive[i] = false
					remaining--
				}
			}
		} else {
			failures++
			if len(runIdx) == 0 {
				runIdx = idx
			}
			for _, i := range runIdx {
				if alive[i] {
					alive[i] = false
					remaining--
				}
			}
		}
	}
	return out
}

var debugLines = false

// SetDebug enables diagnostic output.
func SetDebug(on bool) { debugLines = on }

// sideClean returns the share of positions along s where both sides of the
// stroke (just outside it) are not dark.
func (a *Analysis) sideClean(s segment, t float64) float64 {
	d := s.dir()
	n := d.Perp()
	off := s.width/2 + 2.5
	clean, tot := 0, 0
	for u := 0.0; u <= s.length(); u += 1 {
		p := s.p0.Add(d.Mul(u))
		ok := true
		for _, sg := range []float64{-1, 1} {
			dark := true
			for _, extra := range []float64{0, 1.5} {
				q := p.Add(n.Mul(sg * (off + extra)))
				if !a.black.At(int(math.Round(q.X)), int(math.Round(q.Y))) {
					dark = false
				}
			}
			if dark {
				ok = false
			}
		}
		tot++
		if ok {
			clean++
		}
	}
	if tot == 0 {
		return 0
	}
	return float64(clean) / float64(tot)
}

// axisDarkness returns the median brightness along the axis of s (taking the
// darkest pixel within one pixel of the axis).
func (a *Analysis) axisDarkness(s segment) float64 {
	d := s.dir()
	n := d.Perp()
	var vals []float64
	for t := 0.0; t <= s.length(); t++ {
		p := s.p0.Add(d.Mul(t))
		best := 255.0
		for _, o := range []float64{-1, 0, 1} {
			q := p.Add(n.Mul(o))
			x, y := int(math.Round(q.X)), int(math.Round(q.Y))
			if x < 0 || y < 0 || x >= a.aw || y >= a.ah {
				continue
			}
			r, g, b := a.px(y*a.aw + x)
			best = math.Min(best, math.Max(r, math.Max(g, b)))
		}
		vals = append(vals, best)
	}
	if len(vals) == 0 {
		return 255
	}
	sort.Float64s(vals)
	return vals[len(vals)/2]
}

// pca returns the centroid and the principal direction of pts[idx].
func pca(pts []ipt, idx []int) (Pt, Pt) {
	var sx, sy float64
	for _, i := range idx {
		sx += float64(pts[i].x)
		sy += float64(pts[i].y)
	}
	nf := float64(len(idx))
	if nf == 0 {
		return Pt{}, Pt{1, 0}
	}
	c := Pt{sx / nf, sy / nf}
	var cxx, cxy, cyy float64
	for _, i := range idx {
		dx, dy := float64(pts[i].x)-c.X, float64(pts[i].y)-c.Y
		cxx += dx * dx
		cxy += dx * dy
		cyy += dy * dy
	}
	th := 0.5 * math.Atan2(2*cxy, cxx-cyy)
	return c, Pt{math.Cos(th), math.Sin(th)}
}

// longestRun finds the longest well-filled run of the stroke along direction u.
func (a *Analysis) longestRun(pts []ipt, idx []int, c, u Pt, t float64) (segment, []int, bool) {
	if len(idx) == 0 {
		return segment{}, nil, false
	}
	proj := make([]float64, len(idx))
	lo, hi := math.Inf(1), math.Inf(-1)
	for k, i := range idx {
		q := Pt{float64(pts[i].x), float64(pts[i].y)}
		s := q.Sub(c).Dot(u)
		proj[k] = s
		lo = math.Min(lo, s)
		hi = math.Max(hi, s)
	}
	nb := int(hi-lo) + 1
	cnt := make([]int, nb)
	for _, s := range proj {
		cnt[int(s-lo)]++
	}
	need := int(math.Max(1, math.Round(0.4*t)))
	maxGap := int(math.Max(2, t/2))
	bestA, bestB := -1, -1
	runA, lastFilled := -1, -10
	for b := 0; b < nb; b++ {
		if cnt[b] >= need {
			if runA < 0 || b-lastFilled > maxGap+1 {
				runA = b
			}
			lastFilled = b
			if b-runA > bestB-bestA {
				bestA, bestB = runA, b
			}
		}
	}
	if bestA < 0 {
		return segment{}, nil, false
	}
	var runIdx []int
	area := 0
	for k, i := range idx {
		b := int(proj[k] - lo)
		if b >= bestA && b <= bestB {
			runIdx = append(runIdx, i)
			area++
		}
	}
	length := float64(bestB - bestA + 1)
	s0, s1 := lo+float64(bestA), lo+float64(bestB)+1
	seg := segment{p0: c.Add(u.Mul(s0)), p1: c.Add(u.Mul(s1)), width: float64(area) / length}
	return seg, runIdx, true
}

// mergeCollinear joins segments that lie on the same straight line and are
// connected through dark pixels (a line drawn across a dark object).
func (a *Analysis) mergeCollinear(segs []segment, t float64) []segment {
	changed := true
	for changed {
		changed = false
	outer:
		for i := 0; i < len(segs); i++ {
			for j := i + 1; j < len(segs); j++ {
				if m, ok := a.tryMerge(segs[i], segs[j], t); ok {
					segs[i] = m
					segs = append(segs[:j], segs[j+1:]...)
					changed = true
					break outer
				}
			}
		}
	}
	return segs
}

func (a *Analysis) tryMerge(s1, s2 segment, t float64) (segment, bool) {
	d1, d2 := s1.dir(), s2.dir()
	// Short pieces have unreliable angles; the distance check below decides.
	if math.Abs(d1.Dot(d2)) < math.Cos(12*math.Pi/180) {
		return segment{}, false
	}
	// direction of the longer segment
	d := d1
	base := s1.p0
	if s2.length() > s1.length() {
		d = d2
		base = s2.p0
	}
	nrm := d.Perp()
	for _, p := range []Pt{s1.p0, s1.p1, s2.p0, s2.p1} {
		if math.Abs(p.Sub(base).Dot(nrm)) > 0.9*t {
			return segment{}, false
		}
	}
	// order along d
	a0, a1 := s1.p0.Sub(base).Dot(d), s1.p1.Sub(base).Dot(d)
	b0, b1 := s2.p0.Sub(base).Dot(d), s2.p1.Sub(base).Dot(d)
	if a0 > a1 {
		a0, a1 = a1, a0
	}
	if b0 > b1 {
		b0, b1 = b1, b0
	}
	var gs, ge float64
	if a1 <= b0 {
		gs, ge = a1, b0
	} else if b1 <= a0 {
		gs, ge = b1, a0
	} else {
		gs, ge = 0, 0 // overlapping
	}
	if ge-gs > 0 {
		// The gap must be dark all the way.
		dark, total := 0, 0
		for s := gs; s <= ge; s++ {
			p := base.Add(d.Mul(s))
			total++
			if a.black.At(int(math.Round(p.X)), int(math.Round(p.Y))) {
				dark++
			}
		}
		if total > 0 && float64(dark)/float64(total) < 0.9 {
			return segment{}, false
		}
	}
	lo, hi := math.Min(a0, b0), math.Max(a1, b1)
	w := (s1.width*s1.length() + s2.width*s2.length()) / (s1.length() + s2.length())
	return segment{p0: base.Add(d.Mul(lo)), p1: base.Add(d.Mul(hi)), width: w}, true
}

// ---------------------------------------------------------------------------
// Free end decision

// toA converts original coordinates to analysis coordinates.
func (a *Analysis) toA(p Pt) Pt { return p.Mul(a.k) }

// fromA converts analysis coordinates to original coordinates.
func (a *Analysis) fromA(p Pt) Pt { return p.Mul(1 / a.k) }

// ObjectFraction returns the share of non-background pixels in r (original
// coordinates) and the share of r inside the image.
func (a *Analysis) ObjectFraction(r Rect) (float64, float64) {
	return a.objInt.Fraction(Rect{r.X0 * a.k, r.Y0 * a.k, r.X1 * a.k, r.Y1 * a.k})
}

// BlackFraction returns the share of dark pixels in r (original coordinates).
func (a *Analysis) BlackFraction(r Rect) float64 {
	f, _ := a.blkInt.Fraction(Rect{r.X0 * a.k, r.Y0 * a.k, r.X1 * a.k, r.Y1 * a.k})
	return f
}

// halfDiskFraction samples a mask (through its integral image) in the half
// disk of radius rad beyond end e (analysis coordinates) in direction d.
func (a *Analysis) halfDiskFraction(in *Integral, e, d Pt, rad, skip float64) float64 {
	obj, tot := 0, 0
	nrm := d.Perp()
	step := math.Max(1, rad/12)
	for s := skip; s <= rad; s += step {
		for q := -rad; q <= rad; q += step {
			if s*s+q*q > rad*rad {
				continue
			}
			p := e.Add(d.Mul(s)).Add(nrm.Mul(q))
			x, y := int(math.Round(p.X)), int(math.Round(p.Y))
			tot++
			if x < 0 || y < 0 || x >= a.aw || y >= a.ah {
				obj++ // outside the photo counts as blocked
				continue
			}
			if in.Count(x, y, x+1, y+1) > 0 {
				obj++
			}
		}
	}
	if tot == 0 {
		return 1
	}
	return float64(obj) / float64(tot)
}

// endInfo describes the surroundings of one end of a stroke.
type endInfo struct {
	score     float64 // how unsuitable the end is for a number; lower is better
	nearBlack float64 // share of dark pixels just beyond the end
	boxBlack  float64 // share of dark pixels where the number would go
}

// clean reports whether nothing dark continues beyond the end: a drawn
// leader line stops in free space, while slots, ribs and edges of dark
// objects run into other dark structures.
func (e endInfo) clean() bool {
	return e.nearBlack <= maxNearBlack && e.boxBlack <= maxBoxBlack
}

const (
	maxNearBlack = 0.06
	maxBoxBlack  = 0.04
	// maxEndScore is the largest acceptable clutter score of the free end.
	maxEndScore = 0.45
)

// endScore examines end e (direction d pointing outwards). Coordinates are in
// analysis space.
func (a *Analysis) endScore(e, d Pt, t float64) endInfo {
	h := a.DigitHeight * a.k
	rad := math.Max(3*t, 0.6*h)
	near := a.halfDiskFraction(a.objInt, e, d, rad, 1.5)
	w := 1.1 * h
	gap := 0.25 * h
	c := LabelCenter(e, d, w, h, gap)
	box := RectAt(c, w, h)
	f, inside := a.objInt.Fraction(box)
	boxScore := f*inside + (1 - inside)
	var info endInfo
	info.nearBlack = a.halfDiskFraction(a.blkInt, e, d, rad, math.Max(3, 0.5*t))
	info.boxBlack, _ = a.blkInt.Fraction(box)
	info.score = 0.55*near + 0.45*boxScore
	// Dark material at an end makes it a poor place for the number.
	info.score += 2*math.Max(0, info.nearBlack-0.02) + 2*math.Max(0, info.boxBlack-0.01)
	return info
}

// extendThroughDark follows the stroke from end e in direction d while the
// pixels on its axis stay dark. The new end is accepted only if the walk
// finishes as a thin stroke of the same width with clean sides, i.e. the
// drawn line re-emerged from the dark object and stopped in free space.
func (a *Analysis) extendThroughDark(e, d Pt, width, t float64) Pt {
	maxWalk := 0.25 * float64(max(a.aw, a.ah))
	isBlack := func(p Pt) bool { return a.black.At(int(math.Round(p.X)), int(math.Round(p.Y))) }
	if !isBlack(e.Add(d.Mul(2))) {
		return e
	}
	n := d.Perp()
	gap := 0
	last := 0.0
	for s := 1.0; s <= maxWalk; s++ {
		if isBlack(e.Add(d.Mul(s))) {
			last, gap = s, 0
			continue
		}
		gap++
		if gap > 2 {
			break
		}
	}
	if last < 3*t || last >= maxWalk {
		return e
	}
	// The last 3t pixels must look like the drawn line: dark core, light sides.
	off := width/2 + 2.5
	for s := last - 3*t; s <= last-1; s++ {
		p := e.Add(d.Mul(s))
		if !isBlack(p) || isBlack(p.Add(n.Mul(off))) || isBlack(p.Sub(n.Mul(off))) ||
			isBlack(p.Add(n.Mul(off+2))) || isBlack(p.Sub(n.Mul(off+2))) {
			return e
		}
	}
	return e.Add(d.Mul(last))
}

// orient decides which end of s is free and converts to original coordinates.
func (a *Analysis) orient(s segment) (Line, bool) {
	t := a.Stroke * a.k
	d := s.dir()
	// A line drawn across a dark object may have been found only up to that
	// object; follow it to where it really ends.
	s.p0 = a.extendThroughDark(s.p0, d.Mul(-1), s.width, t)
	s.p1 = a.extendThroughDark(s.p1, d, s.width, t)
	e0 := a.endScore(s.p0, d.Mul(-1), t)
	e1 := a.endScore(s.p1, d, t)
	sc0, sc1 := e0.score, e1.score
	// local outwardness: the label end should point away from nearby objects
	mid := s.p0.Add(s.p1).Mul(0.5)
	L := s.length()
	rad := L + 3*a.DigitHeight*a.k
	cx, cy, cn := 0.0, 0.0, 0.0
	step := math.Max(2, rad/40)
	for y := mid.Y - rad; y <= mid.Y+rad; y += step {
		for x := mid.X - rad; x <= mid.X+rad; x += step {
			xi, yi := int(x), int(y)
			if xi < 0 || yi < 0 || xi >= a.aw || yi >= a.ah {
				continue
			}
			if a.objInt.Count(xi, yi, xi+1, yi+1) > 0 && !a.black.At(xi, yi) {
				cx += x
				cy += y
				cn++
			}
		}
	}
	if cn > 0 {
		cen := Pt{cx / cn, cy / cn}
		v := mid.Sub(cen)
		if v.Len() > 1 {
			o := v.Norm().Dot(d) // >0: p1 points away from objects
			sc1 -= 0.08 * o
			sc0 += 0.08 * o
		}
	}
	var l Line
	free := e1
	if sc1 <= sc0 {
		l = Line{Free: a.fromA(s.p1), Inner: a.fromA(s.p0)}
	} else {
		l = Line{Free: a.fromA(s.p0), Inner: a.fromA(s.p1)}
		free = e0
	}
	l.Width = s.width / a.k
	l.Confidence = math.Abs(sc1 - sc0)
	if debugLines {
		fmt.Printf("  orient (%.0f,%.0f)-(%.0f,%.0f): free score=%.2f nearBlack=%.2f boxBlack=%.2f other=%.2f\n",
			s.p0.X, s.p0.Y, s.p1.X, s.p1.Y, free.score, free.nearBlack, free.boxBlack, math.Max(sc0, sc1))
	}
	// A leader line ends in free space, where its number goes.
	if math.Min(sc0, sc1) > maxEndScore || !free.clean() {
		return l, false
	}
	return l, true
}

// LineAt tries to find a leader line close to point p (original coordinates);
// used when the user clicks on a line that was not detected automatically.
func (a *Analysis) LineAt(p Pt) (Line, bool) {
	q := a.toA(p)
	t := a.Stroke * a.k
	rad := int(math.Max(4*t, 10))
	// nearest black pixel
	bx, by, bd := -1, -1, math.Inf(1)
	for y := int(q.Y) - rad; y <= int(q.Y)+rad; y++ {
		for x := int(q.X) - rad; x <= int(q.X)+rad; x++ {
			if a.black.At(x, y) {
				d := math.Hypot(float64(x)-q.X, float64(y)-q.Y)
				if d < bd {
					bx, by, bd = x, y, d
				}
			}
		}
	}
	if bx < 0 {
		return Line{}, false
	}
	// flood the black component restricted to a window
	win := int(0.25 * float64(max(a.aw, a.ah)))
	var pts []ipt
	seen := map[int]bool{}
	stack := []int{by*a.aw + bx}
	seen[stack[0]] = true
	for len(stack) > 0 && len(pts) < 400000 {
		j := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := j%a.aw, j/a.aw
		if a.thinBlk.Pix[j] {
			pts = append(pts, ipt{int32(x), int32(y)})
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				xx, yy := x+dx, y+dy
				if xx < 0 || yy < 0 || xx >= a.aw || yy >= a.ah || abs(xx-bx) > win || abs(yy-by) > win {
					continue
				}
				k := yy*a.aw + xx
				if !seen[k] && a.black.Pix[k] {
					seen[k] = true
					stack = append(stack, k)
				}
			}
		}
	}
	segs := a.houghSegments(pts, t, math.Max(3*t, 12))
	// choose the segment nearest to the click
	best, bestD := -1, math.Inf(1)
	for i, s := range segs {
		d := distToSegment(q, s.p0, s.p1)
		if d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 || bestD > 3*t+4 {
		return Line{}, false
	}
	l, _ := a.orient(segs[best])
	return l, true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// distToSegment returns the distance from p to segment a-b.
func distToSegment(p, a, b Pt) float64 {
	ab := b.Sub(a)
	l2 := ab.Dot(ab)
	if l2 == 0 {
		return p.Dist(a)
	}
	t := math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/l2))
	return p.Dist(a.Add(ab.Mul(t)))
}
