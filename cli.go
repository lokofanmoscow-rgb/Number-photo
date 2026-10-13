package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"numberphoto/core"
)

func runCLI(args []string) int {
	attachConsole()
	fs := flag.NewFlagSet("NumberPhoto", flag.ContinueOnError)
	fs.Bool("cli", true, "режим командной строки")
	start := fs.Int("start", 1, "с какого числа начинать")
	order := fs.String("order", "auto", "порядок: auto, rows (строками), cols (столбцами)")
	size := fs.Float64("size", 0, "высота цифр в пикселях (0 = автоматически)")
	fontName := fs.String("font", "", "шрифт: имя (например Calibri) или путь к .ttf")
	debug := fs.Bool("debug", false, "сохранить отладочные изображения")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s %s\n\nИспользование:\n  NumberPhoto.exe -cli [параметры] вход.jpg [выход.jpg]\n\nПараметры:\n", appTitle, appVersion)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	in := fs.Arg(0)
	out := fs.Arg(1)
	if out == "" {
		out = DefaultOutputName(in)
	}
	img, err := core.LoadImage(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	font, err := resolveFont(*fontName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	core.SetDebug(*debug)
	a := core.Analyze(img)
	opt := core.Options{Start: *start, Order: parseOrder(*order), DigitHeight: *size, Font: font}
	lay := core.MakeLayout(a, core.NewLabels(a), opt)
	res := core.Render(img, lay, opt)
	if err := core.SaveImage(out, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *debug {
		base := strings.TrimSuffix(out, filepath.Ext(out))
		_ = core.SaveImage(base+"_debug_lines.png", core.DebugLines(img, a.Lines))
		_ = core.SaveImage(base+"_debug_mask.png", a.DebugBackground())
	}
	last := *start + len(lay.Labels) - 1
	fmt.Printf("%s: линий %d, номера %d-%d, высота цифр %.0f px, шрифт %s -> %s\n",
		filepath.Base(in), len(lay.Labels), *start, last, lay.DigitHeight, font.Name, out)
	if lay.Conflicts > 0 {
		fmt.Printf("внимание: %d чисел не удалось разместить без наложений\n", lay.Conflicts)
	}
	return 0
}

func parseOrder(s string) core.OrderMode {
	switch strings.ToLower(s) {
	case "rows", "row", "строками":
		return core.OrderRows
	case "cols", "columns", "col", "столбцами":
		return core.OrderColumns
	}
	return core.OrderAuto
}

// resolveFont finds a font by name or path; empty name means the default.
func resolveFont(name string) (*core.Font, error) {
	if name != "" {
		if _, err := os.Stat(name); err == nil {
			return core.LoadFont(core.FontInfo{Name: filepath.Base(name), Path: name})
		}
	}
	list := core.ListFonts()
	if name != "" {
		want := strings.ToLower(name)
		for _, fi := range list {
			n := strings.ToLower(fi.Name)
			if n == want || strings.TrimSuffix(n, " regular") == want {
				return core.LoadFont(fi)
			}
		}
		return nil, fmt.Errorf("шрифт %q не найден", name)
	}
	if i := core.PreferredFont(list); i >= 0 {
		if f, err := core.LoadFont(list[i]); err == nil {
			return f, nil
		}
	}
	return core.LoadEmbeddedFont(), nil
}

// DefaultOutputName returns "<name>_num.jpg" next to the input.
func DefaultOutputName(in string) string {
	ext := strings.ToLower(filepath.Ext(in))
	if ext != ".png" {
		ext = ".jpg"
	}
	return strings.TrimSuffix(in, filepath.Ext(in)) + "_num" + ext
}
