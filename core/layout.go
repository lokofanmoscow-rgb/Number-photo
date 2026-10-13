package core

import (
	"image"
	"image/color"
	"math"
	"sort"
	"strconv"
)

// OrderMode selects how labels are numbered.
type OrderMode int

const (
	// OrderAuto groups labels into rows (numbers above/below the object) and
	// columns (numbers left/right of the object). Rows are read left to right,
	// columns top to bottom, groups from the top of the photo downwards and
	// from left to right.
	OrderAuto OrderMode = iota
	// OrderRows reads labels in rows: left to right, then top to bottom.
	OrderRows
	// OrderColumns reads labels in columns: top to bottom, then left to right.
	OrderColumns
)

// Label is one number attached to a line.
type Label struct {
	Line Line
	// Extra is an additional distance between the line end and the number
	// (along the line), set by the user or by collision avoidance.
	Extra float64
	// UserExtra is true when Extra was set manually and must be kept.
	UserExtra bool
	// Pin forces this label to a given position in the numbering (1-based,
	// 0 = automatic).
	Pin int

	Number int
	Text   string
	Center Pt
	Box    Rect // ink box of the text
}

// Options control the layout.
type Options struct {
	Start       int
	Order       OrderMode
	DigitHeight float64 // 0 = automatic
	Font        *Font
	Color       color.Color
}

// GapRatio is the distance between the line end and the number, relative to
// the digit height.
const GapRatio = 0.25

// Layout is the result of placing labels.
type Layout struct {
	Labels      []*Label
	DigitHeight float64
	Conflicts   int // labels that could not be placed without overlap
}

// LabelCenter returns the centre of a w x h box placed beyond end e in the
// direction d so that the line axis passes exactly through the centre of the
// box and the box is `gap` away from e along the axis.
func LabelCenter(e, d Pt, w, h, gap float64) Pt {
	return e.Add(d.Mul(gap + boxRayDist(d, w/2, h/2)))
}

func boxRayDist(d Pt, hw, hh float64) float64 {
	ax, ay := math.Abs(d.X), math.Abs(d.Y)
	tx, ty := math.Inf(1), math.Inf(1)
	if ax > 1e-9 {
		tx = hw / ax
	}
	if ay > 1e-9 {
		ty = hh / ay
	}
	return math.Min(tx, ty)
}

// side of the line end: 0 left, 1 right, 2 top, 3 bottom
func sideOf(d Pt) int {
	if math.Abs(d.X) > math.Abs(d.Y) {
		if d.X < 0 {
			return 0
		}
		return 1
	}
	if d.Y < 0 {
		return 2
	}
	return 3
}

// place computes the position of label l for digit height h.
func (o *Options) place(l *Label, h float64) {
	w, hh := o.Font.TextSize(l.Text, h)
	gap := GapRatio * h
	l.Center = LabelCenter(l.Line.Free, l.Line.Dir(), w, hh, gap+l.Extra)
	l.Box = RectAt(l.Center, w, hh)
}

// MakeLayout numbers and places labels for the given lines.
func MakeLayout(a *Analysis, labels []*Label, opt Options) *Layout {
	if opt.Font == nil {
		opt.Font = LoadEmbeddedFont()
	}
	lay := &Layout{Labels: labels}
	if len(labels) == 0 {
		lay.DigitHeight = opt.DigitHeight
		if lay.DigitHeight <= 0 {
			lay.DigitHeight = a.DigitHeight
		}
		return lay
	}
	if opt.DigitHeight > 0 {
		lay.DigitHeight = opt.DigitHeight
		lay.Conflicts = opt.arrange(a, labels, opt.DigitHeight)
		return lay
	}
	// Automatic size: the largest size (not above the preferred one) that
	// lets every number be placed without overlapping anything.
	h0 := a.DigitHeight
	bestH, bestC := h0, math.MaxInt
	for h := h0; h >= 0.5*h0; h *= 0.95 {
		c := opt.arrange(a, labels, h)
		if c < bestC {
			bestH, bestC = h, c
		}
		if c == 0 {
			break
		}
	}
	bestH = math.Round(bestH)
	lay.DigitHeight = bestH
	lay.Conflicts = opt.arrange(a, labels, bestH)
	return lay
}

// arrange numbers the labels and positions them for digit height h. It
// returns the number of labels left in conflict.
func (o *Options) arrange(a *Analysis, labels []*Label, h float64) int {
	o.number(labels, h)
	// Reset automatic offsets.
	for _, l := range labels {
		if !l.UserExtra {
			l.Extra = 0
		}
		o.place(l, h)
	}
	conflicts := 0
	// Place in numbering order; each label avoids the ones placed before.
	order := make([]*Label, len(labels))
	copy(order, labels)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Number < order[j].Number })
	var placed []*Label
	for _, l := range order {
		if l.UserExtra {
			o.place(l, h)
			placed = append(placed, l)
			continue
		}
		bestE, bestS := 0.0, math.Inf(1)
		for k := 0; k <= 10; k++ {
			l.Extra = float64(k) * 0.15 * h
			o.place(l, h)
			s := o.badness(a, l, placed, labels, h)
			// prefer small offsets
			s += 0.01 * float64(k)
			if s < bestS {
				bestS, bestE = s, l.Extra
			}
			if s < 0.05 {
				break
			}
		}
		l.Extra = bestE
		o.place(l, h)
		if bestS >= 1 {
			conflicts++
		}
		placed = append(placed, l)
	}
	return conflicts
}

// badness scores a placement: >=1 means a hard conflict (overlap with another
// number, a line or the image border); object overlap adds a soft penalty.
func (o *Options) badness(a *Analysis, l *Label, placed, all []*Label, h float64) float64 {
	m := 0.12 * h
	box := l.Box
	s := 0.0
	// inside the image
	if box.X0 < 1 || box.Y0 < 1 || box.X1 > float64(a.W-1) || box.Y1 > float64(a.H-1) {
		s += 2
	}
	big := Rect{box.X0 - m, box.Y0 - m, box.X1 + m, box.Y1 + m}
	for _, p := range placed {
		if p != l && big.Overlaps(p.Box) {
			s += 2
		}
	}
	// lines (all labels' lines; the own line ends GapRatio*h away)
	t := a.Stroke
	for _, p := range all {
		lm := t/2 + 0.08*h
		if p == l {
			lm = t / 2
		}
		if segRectDist(p.Line.Inner, p.Line.Free, box) < lm {
			s += 1.5
		}
	}
	// objects
	f, _ := a.ObjectFraction(box.Inset(-0.05 * h))
	if f > 0.03 {
		s += math.Min(0.9, f*3)
	}
	return s
}

// segRectDist returns the distance between segment p-q and rectangle r.
func segRectDist(p, q Pt, r Rect) float64 {
	if r.Contains(p) || r.Contains(q) {
		return 0
	}
	corners := []Pt{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X1, r.Y1}, {r.X0, r.Y1}}
	// intersection with edges
	for i := 0; i < 4; i++ {
		if segsIntersect(p, q, corners[i], corners[(i+1)%4]) {
			return 0
		}
	}
	d := math.Inf(1)
	for _, c := range corners {
		d = math.Min(d, distToSegment(c, p, q))
	}
	for _, e := range []Pt{p, q} {
		dx := math.Max(math.Max(r.X0-e.X, 0), e.X-r.X1)
		dy := math.Max(math.Max(r.Y0-e.Y, 0), e.Y-r.Y1)
		d = math.Min(d, math.Hypot(dx, dy))
	}
	return d
}

func segsIntersect(p1, p2, p3, p4 Pt) bool {
	cross := func(a, b, c Pt) float64 { return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X) }
	d1, d2 := cross(p3, p4, p1), cross(p3, p4, p2)
	d3, d4 := cross(p1, p2, p3), cross(p1, p2, p4)
	return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))
}

// number assigns numbers to labels according to the order mode.
func (o *Options) number(labels []*Label, h float64) {
	// positions with a nominal text so the order does not depend on numbers
	nominal := strconv.Itoa(o.Start + len(labels) - 1)
	w, hh := o.Font.TextSize(nominal, h)
	pos := make(map[*Label]Pt, len(labels))
	for _, l := range labels {
		pos[l] = LabelCenter(l.Line.Free, l.Line.Dir(), w, hh, GapRatio*h)
	}
	var seq []*Label
	switch o.Order {
	case OrderRows:
		seq = orderLines(labels, pos, h, false)
	case OrderColumns:
		seq = orderLines(labels, pos, w, true)
	default:
		seq = orderAuto(labels, pos, w, h)
	}
	// apply pins: pinned labels go to their requested positions
	var free, pinned []*Label
	for _, l := range seq {
		if l.Pin > 0 {
			pinned = append(pinned, l)
		} else {
			free = append(free, l)
		}
	}
	if len(pinned) > 0 {
		sort.SliceStable(pinned, func(i, j int) bool { return pinned[i].Pin < pinned[j].Pin })
		res := make([]*Label, 0, len(seq))
		fi := 0
		for _, p := range pinned {
			for len(res) < p.Pin-1 && fi < len(free) {
				res = append(res, free[fi])
				fi++
			}
			res = append(res, p)
		}
		res = append(res, free[fi:]...)
		seq = res
	}
	for i, l := range seq {
		l.Number = o.Start + i
		l.Text = strconv.Itoa(l.Number)
	}
}

// orderLines orders labels in rows (or columns when cols is true). tol is the
// size of a label across the reading direction.
func orderLines(labels []*Label, pos map[*Label]Pt, tol float64, cols bool) []*Label {
	key := func(l *Label) (float64, float64) {
		p := pos[l]
		if cols {
			return p.X, p.Y
		}
		return p.Y, p.X
	}
	s := make([]*Label, len(labels))
	copy(s, labels)
	sort.SliceStable(s, func(i, j int) bool {
		a, _ := key(s[i])
		b, _ := key(s[j])
		return a < b
	})
	var out []*Label
	var cur []*Label
	last := math.Inf(-1)
	flush := func() {
		sort.SliceStable(cur, func(i, j int) bool {
			_, a := key(cur[i])
			_, b := key(cur[j])
			return a < b
		})
		out = append(out, cur...)
		cur = cur[:0]
	}
	for _, l := range s {
		k, _ := key(l)
		if len(cur) > 0 && k-last > 0.7*tol {
			flush()
		}
		cur = append(cur, l)
		last = k
	}
	flush()
	return out
}

type labelGroup struct {
	labels []*Label
	row    bool // numbers above/below objects: read left to right
	box    Rect
}

// update sorts the members along the reading direction and recomputes the
// bounding box.
func (g *labelGroup) update(pos map[*Label]Pt, w, h float64) {
	sort.SliceStable(g.labels, func(i, j int) bool {
		a, b := pos[g.labels[i]], pos[g.labels[j]]
		if g.row {
			return a.X < b.X
		}
		return a.Y < b.Y
	})
	g.box = Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, l := range g.labels {
		p := pos[l]
		g.box.X0 = math.Min(g.box.X0, p.X-w/2)
		g.box.Y0 = math.Min(g.box.Y0, p.Y-h/2)
		g.box.X1 = math.Max(g.box.X1, p.X+w/2)
		g.box.Y1 = math.Max(g.box.Y1, p.Y+h/2)
	}
}

// regroup moves a label into a row (or column) of the other orientation when
// it lies inside that row's band. Example: a nearly horizontal leader line
// whose number ends up in the bottom row of numbers is read with that row.
func regroup(groups []*labelGroup, pos map[*Label]Pt, w, h float64) []*labelGroup {
	type move struct {
		l        *Label
		from, to *labelGroup
	}
	var moves []move
	for _, g := range groups {
		for _, l := range g.labels {
			p := pos[l]
			var best *labelGroup
			bestD := math.Inf(1)
			for _, g2 := range groups {
				if g2 == g || g2.row == g.row || len(g2.labels) < 2 {
					continue
				}
				band := g2.box.Inset(-0.3 * h)
				if !band.Contains(p) {
					continue
				}
				if d := p.Dist(band.Center()); d < bestD {
					best, bestD = g2, d
				}
			}
			if best != nil {
				moves = append(moves, move{l, g, best})
			}
		}
	}
	if len(moves) == 0 {
		return groups
	}
	for _, m := range moves {
		for i, x := range m.from.labels {
			if x == m.l {
				m.from.labels = append(m.from.labels[:i], m.from.labels[i+1:]...)
				break
			}
		}
		m.to.labels = append(m.to.labels, m.l)
	}
	var out []*labelGroup
	for _, g := range groups {
		if len(g.labels) > 0 {
			g.update(pos, w, h)
			out = append(out, g)
		}
	}
	return out
}

// orderAuto implements OrderAuto.
func orderAuto(labels []*Label, pos map[*Label]Pt, w, h float64) []*Label {
	bySide := map[int][]*Label{}
	for _, l := range labels {
		s := sideOf(l.Line.Dir())
		bySide[s] = append(bySide[s], l)
	}
	var groups []*labelGroup
	for side := 0; side < 4; side++ {
		ls := bySide[side]
		if len(ls) == 0 {
			continue
		}
		row := side >= 2
		// cluster across the reading direction
		across := func(l *Label) float64 {
			if row {
				return pos[l].Y
			}
			return pos[l].X
		}
		sort.SliceStable(ls, func(i, j int) bool { return across(ls[i]) < across(ls[j]) })
		gapLimit := 3 * h
		if !row {
			gapLimit = 2.5 * w
		}
		var cur []*Label
		emit := func() {
			if len(cur) == 0 {
				return
			}
			g := &labelGroup{row: row, labels: append([]*Label(nil), cur...)}
			g.update(pos, w, h)
			groups = append(groups, g)
			cur = cur[:0]
		}
		for i, l := range ls {
			if i > 0 && across(l)-across(ls[i-1]) > gapLimit {
				emit()
			}
			cur = append(cur, l)
		}
		emit()
	}
	groups = regroup(groups, pos, w, h)
	// Order groups: take the top-most remaining group; among the groups that
	// overlap it vertically, the left-most one goes first.
	var out []*Label
	for len(groups) > 0 {
		top := 0
		for i, g := range groups {
			if g.box.Y0 < groups[top].box.Y0 {
				top = i
			}
		}
		pick := top
		for i, g := range groups {
			if g.box.Y0 < groups[top].box.Y1 && g.box.X0 < groups[pick].box.X0 {
				pick = i
			}
		}
		out = append(out, groups[pick].labels...)
		groups = append(groups[:pick], groups[pick+1:]...)
	}
	return out
}

// Render draws the labels on a copy of img.
func Render(img image.Image, lay *Layout, opt Options) *image.RGBA {
	dst := ToRGBA(img)
	col := opt.Color
	if col == nil {
		col = color.Black
	}
	for _, l := range lay.Labels {
		opt.Font.Draw(dst, l.Text, lay.DigitHeight, l.Center, col)
	}
	return dst
}

// NewLabels creates labels for all detected lines.
func NewLabels(a *Analysis) []*Label {
	out := make([]*Label, 0, len(a.Lines))
	for _, ln := range a.Lines {
		out = append(out, &Label{Line: ln})
	}
	return out
}

// Reposition recomputes the position of one label using the current digit
// height (used while the user drags a number).
func (lay *Layout) Reposition(l *Label, opt Options) {
	opt.place(l, lay.DigitHeight)
}

// MinExtra is the smallest allowed Extra (keeps a small gap to the line end).
func MinExtra(h float64) float64 { return -(GapRatio - 0.08) * h }

// HitTest returns the label whose box (inflated by pad) contains p.
func (lay *Layout) HitTest(p Pt, pad float64) *Label {
	var best *Label
	bestD := math.Inf(1)
	for _, l := range lay.Labels {
		r := l.Box.Inset(-pad)
		if r.Contains(p) {
			if d := p.Dist(l.Center); d < bestD {
				best, bestD = l, d
			}
		}
	}
	return best
}

// Clone returns a deep copy of labels (for undo).
func CloneLabels(ls []*Label) []*Label {
	out := make([]*Label, len(ls))
	for i, l := range ls {
		c := *l
		out[i] = &c
	}
	return out
}

// HasLine reports whether one of the labels already uses line ln (both ends
// within tol).
func HasLine(ls []*Label, ln Line, tol float64) bool {
	for _, l := range ls {
		a, b := l.Line.Free, l.Line.Inner
		if (a.Dist(ln.Free) < tol && b.Dist(ln.Inner) < tol) || (a.Dist(ln.Inner) < tol && b.Dist(ln.Free) < tol) {
			return true
		}
		// the click may have found only a part of an existing line
		if distToSegment(ln.Free, l.Line.Inner, l.Line.Free) < tol && distToSegment(ln.Inner, l.Line.Inner, l.Line.Free) < tol {
			return true
		}
	}
	return false
}

// KeepInside pulls a label back towards its line end until it lies inside a
// w x h image (or reaches the smallest allowed distance).
func (lay *Layout) KeepInside(l *Label, opt Options, w, h int) {
	inside := func() bool {
		b := l.Box
		return b.X0 >= 1 && b.Y0 >= 1 && b.X1 <= float64(w-1) && b.Y1 <= float64(h-1)
	}
	minE := MinExtra(lay.DigitHeight)
	for i := 0; i < 2000 && !inside() && l.Extra > minE; i++ {
		l.Extra = math.Max(minE, l.Extra-1)
		opt.place(l, lay.DigitHeight)
	}
}
