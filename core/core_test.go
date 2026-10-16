package core

import (
	"image"
	"math"
	"sort"
	"testing"

	"golang.org/x/image/draw"
)

// Positions of the numbers in the original photo 1 (before they were erased
// to make testdata/photo1_clean.jpg). The program must reproduce the same
// numbering when started from 69.
var photo1Truth = []struct {
	n    int
	x, y float64
}{
	{69, 123, 138},
	{70, 193, 52},
	{71, 258, 176},
	{72, 415, 346},
	{73, 550, 228},
	{74, 618, 336},
	{75, 686, 245},
	{76, 848, 118},
	{77, 954, 166},
	{78, 1006, 32},
	{79, 1068, 188},
	{80, 1114, 134},
	{81, 1178, 34},
	{82, 1226, 152},
	{83, 1320, 46},
	{84, 1328, 188},
	{85, 1364, 320},
	{86, 1582, 395},
	{87, 1638, 286},
	{88, 1744, 354},
	{89, 135, 894},
	{90, 232, 766},
	{91, 348, 832},
	{92, 471, 785},
	{93, 597, 931},
	{94, 706, 842},
	{95, 909, 804},
	{96, 1000, 896},
	{97, 1096, 820},
	{98, 1177, 760},
	{99, 1282, 846},
	{100, 1374, 790},
	{101, 1571, 846},
	{102, 1713, 783},
	{103, 1902, 894},
	{104, 1735, 991},
	{105, 288, 1429},
	{106, 402, 1382},
	{107, 476, 1462},
	{108, 700, 1385},
	{109, 996, 1376},
	{110, 1648, 1432},
}

func analyzeFile(t *testing.T, path string) (*Analysis, *image.RGBA) {
	t.Helper()
	img, err := LoadImage(path)
	if err != nil {
		t.Fatal(err)
	}
	return Analyze(img), img
}

// checkGeometry verifies the invariants every layout must satisfy.
func checkGeometry(t *testing.T, a *Analysis, lay *Layout) {
	t.Helper()
	for _, l := range lay.Labels {
		// the line axis passes through the centre of the number
		d := l.Line.Dir()
		off := l.Center.Sub(l.Line.Free)
		if perp := math.Abs(off.Dot(d.Perp())); perp > 0.5 {
			t.Errorf("label %d: centre is %.2f px off the line axis", l.Number, perp)
		}
		if off.Dot(d) <= 0 {
			t.Errorf("label %d is not beyond the free end of its line", l.Number)
		}
		// inside the image
		b := l.Box
		if b.X0 < 0 || b.Y0 < 0 || b.X1 > float64(a.W) || b.Y1 > float64(a.H) {
			t.Errorf("label %d is outside the image: %+v", l.Number, b)
		}
		// does not touch other numbers or lines
		for _, o := range lay.Labels {
			if o != l && b.Overlaps(o.Box) {
				t.Errorf("labels %d and %d overlap", l.Number, o.Number)
			}
			if segRectDist(o.Line.Inner, o.Line.Free, b) < a.Stroke/2 {
				t.Errorf("label %d overlaps the line of label %d", l.Number, o.Number)
			}
		}
	}
	if lay.Conflicts != 0 {
		t.Errorf("%d conflicts", lay.Conflicts)
	}
}

func TestPhoto1Numbering(t *testing.T) {
	a, _ := analyzeFile(t, "../testdata/photo1_clean.jpg")
	if len(a.Lines) != len(photo1Truth) {
		t.Fatalf("found %d lines, want %d", len(a.Lines), len(photo1Truth))
	}
	opt := Options{Start: 69, Font: LoadEmbeddedFont()}
	lay := MakeLayout(a, NewLabels(a), opt)
	checkGeometry(t, a, lay)
	for _, g := range photo1Truth {
		var best *Label
		bd := math.Inf(1)
		for _, l := range lay.Labels {
			if d := l.Center.Dist(Pt{g.x, g.y}); d < bd {
				best, bd = l, d
			}
		}
		if best.Number != g.n {
			t.Errorf("number at (%.0f,%.0f): got %d, want %d", g.x, g.y, best.Number, g.n)
		}
		if bd > 45 {
			t.Errorf("number %d placed %.0f px away from the original position", g.n, bd)
		}
	}
	if math.Abs(lay.DigitHeight-50) > 5 {
		t.Errorf("digit height %.0f, the original uses about 50", lay.DigitHeight)
	}
}

func TestPhoto2Columns(t *testing.T) {
	a, img := analyzeFile(t, "../testdata/photo2.jpg")
	if len(a.Lines) != 26 {
		t.Fatalf("found %d lines, want 26", len(a.Lines))
	}
	lay := MakeLayout(a, NewLabels(a), Options{Start: 1, Font: LoadEmbeddedFont()})
	checkGeometry(t, a, lay)
	ls := append([]*Label(nil), lay.Labels...)
	sort.Slice(ls, func(i, j int) bool { return ls[i].Number < ls[j].Number })
	for i, l := range ls {
		left := l.Center.X < float64(img.Rect.Dx())/2
		if (i < 12) != left {
			t.Errorf("label %d at x=%.0f is in the wrong column", l.Number, l.Center.X)
		}
		// free ends point away from the objects: left column to the left
		if left != (l.Line.Dir().X < 0) {
			t.Errorf("label %d is on the wrong end of its line", l.Number)
		}
		if i > 0 && i != 12 && l.Center.Y <= ls[i-1].Center.Y {
			t.Errorf("label %d is not below label %d", l.Number, ls[i-1].Number)
		}
	}
	// other orders keep the same set of numbers
	for _, mode := range []OrderMode{OrderRows, OrderColumns} {
		lay := MakeLayout(a, NewLabels(a), Options{Start: 5, Order: mode, Font: LoadEmbeddedFont()})
		seen := map[int]bool{}
		for _, l := range lay.Labels {
			seen[l.Number] = true
		}
		for n := 5; n < 5+26; n++ {
			if !seen[n] {
				t.Errorf("mode %d: number %d missing", mode, n)
			}
		}
	}
}

// A black perforated tray with straight slots as dark as the drawn lines
// (pasted into photo 2) must not get numbers: its slots run into other dark
// slots, while a leader line ends in free space.
func TestTraySlotsAreNotLines(t *testing.T) {
	a, _ := analyzeFile(t, "../testdata/tray_synthetic.jpg")
	tray := Rect{700, 180, 1260, 740}
	real := 0
	for _, l := range a.Lines {
		if tray.Contains(l.Inner) {
			real++
			if tray.Contains(l.Free) {
				t.Errorf("line inside the tray detected: %+v", l)
			}
		}
	}
	if real != 2 {
		t.Errorf("found %d lines starting on the tray, want the 2 drawn ones", real)
	}
	if len(a.Lines) != 28 {
		t.Errorf("found %d lines, want 28", len(a.Lines))
	}
}

func TestScaledPhotos(t *testing.T) {
	for _, tc := range []struct {
		path  string
		scale float64
		lines int
	}{
		{"../testdata/photo2.jpg", 0.5, 26},
		{"../testdata/photo2.jpg", 1.8, 26},
		{"../testdata/photo1_clean.jpg", 0.6, 42},
	} {
		img, err := LoadImage(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		w := int(float64(img.Rect.Dx()) * tc.scale)
		h := int(float64(img.Rect.Dy()) * tc.scale)
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Rect, img, img.Rect, draw.Src, nil)
		a := Analyze(dst)
		if len(a.Lines) != tc.lines {
			t.Errorf("%s x%.1f: found %d lines, want %d", tc.path, tc.scale, len(a.Lines), tc.lines)
		}
	}
}

func TestLabelCenter(t *testing.T) {
	// horizontal line pointing left: the number is left of the end, centred
	c := LabelCenter(Pt{100, 50}, Pt{-1, 0}, 40, 20, 5)
	if c != (Pt{75, 50}) {
		t.Errorf("got %+v", c)
	}
	// vertical line pointing up
	c = LabelCenter(Pt{100, 50}, Pt{0, -1}, 40, 20, 5)
	if c != (Pt{100, 35}) {
		t.Errorf("got %+v", c)
	}
}

func TestExifOrientation(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Pix[0] = 255              // top-left red
	r := applyOrientation(src, 6) // rotate 90° clockwise
	if r.Rect.Dx() != 2 || r.Rect.Dy() != 3 {
		t.Fatalf("size %v", r.Rect)
	}
	if r.Pix[(0*r.Stride)+(1*4)] != 255 { // top-right
		t.Errorf("rotation is wrong")
	}
}
