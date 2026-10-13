package core

import (
	_ "embed"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Carlito is metrically compatible with Calibri (the font used in the sample
// photos). It is embedded so the program works even without system fonts.
//
//go:embed fonts/Carlito-Regular.ttf
var carlitoTTF []byte

// EmbeddedFontName is the display name of the built-in font.
const EmbeddedFontName = "Carlito (встроенный)"

// Font is a loaded TrueType/OpenType font.
type Font struct {
	Name string
	f    *opentype.Font

	mu        sync.Mutex
	digitRate float64 // digit ink height per 1px of font size
	faces     map[float64]font.Face
}

// FontInfo describes a font file found on the system.
type FontInfo struct {
	Name  string
	Path  string
	Index int // index inside a font collection
}

// LoadEmbeddedFont returns the built-in font.
func LoadEmbeddedFont() *Font {
	f, err := opentype.Parse(carlitoTTF)
	if err != nil {
		panic(err)
	}
	return newFont(EmbeddedFontName, f)
}

// LoadFont loads a font described by fi.
func LoadFont(fi FontInfo) (*Font, error) {
	data, err := os.ReadFile(fi.Path)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(fi.Path), ".ttc") || strings.EqualFold(filepath.Ext(fi.Path), ".otc") {
		c, err := opentype.ParseCollection(data)
		if err != nil {
			return nil, err
		}
		f, err := c.Font(fi.Index)
		if err != nil {
			return nil, err
		}
		return newFont(fi.Name, f), nil
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return nil, err
	}
	return newFont(fi.Name, f), nil
}

func newFont(name string, f *opentype.Font) *Font {
	ft := &Font{Name: name, f: f, faces: map[float64]font.Face{}}
	face := ft.face(1000)
	b, _ := font.BoundString(face, "0123456789")
	h := float64(b.Max.Y-b.Min.Y) / 64
	if h <= 0 {
		h = 700
	}
	ft.digitRate = h / 1000
	return ft
}

func (ft *Font) face(size float64) font.Face {
	size = math.Round(size*64) / 64
	if fc, ok := ft.faces[size]; ok {
		return fc
	}
	fc, err := opentype.NewFace(ft.f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	if len(ft.faces) > 64 {
		ft.faces = map[float64]font.Face{}
	}
	ft.faces[size] = fc
	return fc
}

// sizeFor returns the font size giving the requested digit height.
func (ft *Font) sizeFor(digitHeight float64) float64 { return digitHeight / ft.digitRate }

// InkBox returns the ink bounds of text rendered with the given digit height,
// relative to the text origin (baseline start).
func (ft *Font) InkBox(text string, digitHeight float64) Rect {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	b, _ := font.BoundString(ft.face(ft.sizeFor(digitHeight)), text)
	return Rect{float64(b.Min.X) / 64, float64(b.Min.Y) / 64, float64(b.Max.X) / 64, float64(b.Max.Y) / 64}
}

// TextSize returns the ink width and height of text.
func (ft *Font) TextSize(text string, digitHeight float64) (float64, float64) {
	r := ft.InkBox(text, digitHeight)
	return r.W(), r.H()
}

// Draw renders text so that its ink box is centred on c.
func (ft *Font) Draw(dst *image.RGBA, text string, digitHeight float64, c Pt, col color.Color) {
	ink := ft.InkBox(text, digitHeight)
	ox := c.X - (ink.X0+ink.X1)/2
	oy := c.Y - (ink.Y0+ink.Y1)/2
	ft.mu.Lock()
	defer ft.mu.Unlock()
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: ft.face(ft.sizeFor(digitHeight)),
		Dot:  fixed.Point26_6{X: fixed.Int26_6(math.Round(ox * 64)), Y: fixed.Int26_6(math.Round(oy * 64))},
	}
	d.DrawString(text)
}

// SystemFontDirs returns directories that usually contain fonts.
func SystemFontDirs() []string {
	var dirs []string
	if runtime.GOOS == "windows" {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		dirs = append(dirs, filepath.Join(win, "Fonts"))
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			dirs = append(dirs, filepath.Join(la, "Microsoft", "Windows", "Fonts"))
		}
	} else {
		dirs = append(dirs, "/usr/share/fonts", "/usr/local/share/fonts")
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, ".fonts"), filepath.Join(home, ".local/share/fonts"))
		}
	}
	return dirs
}

// ListFonts scans the system font directories. Only the name table of every
// file is read, so this is fast.
func ListFonts() []FontInfo {
	seen := map[string]bool{}
	var out []FontInfo
	var buf sfnt.Buffer
	add := func(f *sfnt.Font, path string, idx int) {
		name, err := f.Name(&buf, sfnt.NameIDFull)
		if err != nil || name == "" {
			fam, err2 := f.Name(&buf, sfnt.NameIDFamily)
			if err2 != nil || fam == "" {
				return
			}
			name = fam
		}
		// The font must have digits.
		if gi, err := f.GlyphIndex(&buf, '0'); err != nil || gi == 0 {
			return
		}
		key := strings.ToLower(name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, FontInfo{Name: name, Path: path, Index: idx})
	}
	for _, dir := range SystemFontDirs() {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".ttf" && ext != ".otf" && ext != ".ttc" && ext != ".otc" {
				return nil
			}
			fh, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer fh.Close()
			if ext == ".ttc" || ext == ".otc" {
				c, err := sfnt.ParseCollectionReaderAt(fh)
				if err != nil {
					return nil
				}
				for i := 0; i < c.NumFonts() && i < 8; i++ {
					if f, err := c.Font(i); err == nil {
						add(f, path, i)
					}
				}
				return nil
			}
			if f, err := sfnt.ParseReaderAt(fh); err == nil {
				add(f, path, 0)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// PreferredFont picks the default font from the list: Calibri, then Carlito,
// then Arial. It returns -1 when none is found (use the embedded font).
func PreferredFont(list []FontInfo) int {
	for _, want := range []string{"calibri", "carlito", "arial"} {
		for i, fi := range list {
			if strings.TrimSuffix(strings.ToLower(fi.Name), " regular") == want {
				return i
			}
		}
	}
	return -1
}
