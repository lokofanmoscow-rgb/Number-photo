//go:build windows

package main

import (
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"numberphoto/core"
)

const helpText = `• Откройте фото кнопкой, через Ctrl+V или перетащите файл в окно.
• Числа автоматически встают у свободного конца каждой чёрной линии.
• Перетащите число мышью, чтобы сдвинуть его вдоль линии.
• Правый щелчок по числу: удалить, перенести на другой конец линии, задать номер. Delete — удалить число под курсором.
• Щелчок по линии без числа добавляет число.
• Колесо мыши — масштаб, перетаскивание фона — прокрутка.
• Ctrl+Z — отменить.`

var orderNames = []string{
	"Авто",
	"Строками: слева направо",
	"Столбцами: сверху вниз",
}

// App is the main window state.
type App struct {
	mw        *walk.MainWindow
	canvas    *walk.CustomWidget
	startEdit *walk.NumberEdit
	orderCB   *walk.ComboBox
	fontCB    *walk.ComboBox
	sizeEdit  *walk.NumberEdit
	autoCB    *walk.CheckBox
	contCB    *walk.CheckBox
	status    *walk.StatusBarItem
	info      *walk.StatusBarItem

	settings Settings

	// document
	img         *image.RGBA
	srcPath     string
	analysis    *core.Analysis
	labels      []*core.Label
	layout      *core.Layout
	result      *image.RGBA
	bgra        []byte
	gen         int
	busy        bool
	undoStack   [][]*core.Label
	pendingNext int

	fonts    []core.FontInfo
	fontList []string
	font     *core.Font

	// view
	zoom   float64
	cx, cy float64

	// mouse state
	downX, downY int
	dragLabel    *core.Label
	dragExtra    float64
	dragStart    core.Pt
	panning      bool
	panCX, panCY float64
	moved        bool
	hover        *core.Label
	ctxLabel     *core.Label
	ctxPoint     core.Pt

	actDelete, actFlip, actNumber, actResetPos, actAdd, actRedetect *walk.Action

	bgBrush  *walk.SolidColorBrush
	hoverPen *walk.CosmeticPen
	hintFont *walk.Font

	updating bool
}

func runGUI(args []string) {
	runtime.LockOSThread()
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("Внутренняя ошибка: %v\n\n%s", r, debug.Stack())
			if dir, err := os.UserConfigDir(); err == nil {
				_ = os.MkdirAll(filepath.Join(dir, "NumberPhoto"), 0o755)
				_ = os.WriteFile(filepath.Join(dir, "NumberPhoto", "crash.log"), []byte(msg), 0o644)
			}
			walk.MsgBox(nil, appTitle, msg, walk.MsgBoxIconError)
		}
	}()
	app := &App{settings: loadSettings(), zoom: 1}
	app.font = core.LoadEmbeddedFont()
	app.fontList = []string{core.EmbeddedFontName}
	if err := app.create(); err != nil {
		walk.MsgBox(nil, appTitle, "Не удалось создать окно: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	go app.loadFontsAsync()
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			app.openFile(a)
			break
		}
	}
	app.mw.Run()
}

func (app *App) create() error {
	s := app.settings
	var err error
	app.bgBrush, err = walk.NewSolidColorBrush(walk.RGB(64, 64, 64))
	if err != nil {
		return err
	}
	app.hoverPen, _ = walk.NewCosmeticPen(walk.PenSolid, walk.RGB(255, 140, 0))
	app.hintFont, _ = walk.NewFont("Segoe UI", 14, 0)

	err = MainWindow{
		AssignTo: &app.mw,
		Title:    appTitle,
		Icon:     2,
		Size:     Size{Width: s.Width, Height: s.Height},
		MinSize:  Size{Width: 820, Height: 520},
		Layout:   HBox{MarginsZero: true, Spacing: 6},
		MenuItems: []MenuItem{
			Menu{Text: "&Файл", Items: []MenuItem{
				Action{Text: "&Открыть фото…", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyO}, OnTriggered: app.openDialog},
				Action{Text: "&Вставить фото из буфера", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyV}, OnTriggered: app.paste},
				Separator{},
				Action{Text: "&Сохранить как…", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyS}, OnTriggered: app.saveDialog},
				Action{Text: "&Копировать результат", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyC}, OnTriggered: app.copyResult},
				Separator{},
				Action{Text: "В&ыход", OnTriggered: func() { app.mw.Close() }},
			}},
			Menu{Text: "&Правка", Items: []MenuItem{
				Action{Text: "&Отменить", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyZ}, OnTriggered: app.undo},
				Action{Text: "Найти линии заново (сбросить правки)", OnTriggered: app.resetEdits},
			}},
			Menu{Text: "&Вид", Items: []MenuItem{
				Action{Text: "По размеру окна", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.Key0}, OnTriggered: func() { app.zoom = 1; app.canvas.Invalidate() }},
				Action{Text: "Увеличить", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyOEMPlus}, OnTriggered: func() { app.zoomCenter(1.25) }},
				Action{Text: "Уменьшить", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyOEMMinus}, OnTriggered: func() { app.zoomCenter(0.8) }},
			}},
			Menu{Text: "&Справка", Items: []MenuItem{
				Action{Text: "Как пользоваться", Shortcut: Shortcut{Key: walk.KeyF1}, OnTriggered: func() {
					walk.MsgBox(app.mw, appTitle+" "+appVersion, helpText, walk.MsgBoxIconInformation)
				}},
			}},
		},
		Children: []Widget{
			CustomWidget{
				AssignTo:            &app.canvas,
				PaintPixels:         app.paint,
				PaintMode:           PaintBuffered,
				InvalidatesOnResize: true,
				StretchFactor:       4,
				OnMouseDown:         app.mouseDown,
				OnMouseMove:         app.mouseMove,
				OnMouseUp:           app.mouseUp,
				OnKeyDown:           app.keyDown,
			},
			Composite{
				Alignment: AlignHNearVNear,
				MinSize:   Size{Width: 300},
				MaxSize:   Size{Width: 300},
				Layout:    VBox{Alignment: AlignHNearVNear, Margins: Margins{Left: 2, Top: 6, Right: 8, Bottom: 6}},
				Children: []Widget{
					GroupBox{Title: "Фото", Layout: VBox{}, Children: []Widget{
						PushButton{Text: "Открыть фото…  (Ctrl+O)", OnClicked: app.openDialog},
						PushButton{Text: "Вставить из буфера  (Ctrl+V)", OnClicked: app.paste},
					}},
					GroupBox{Title: "Нумерация", Layout: Grid{Columns: 2}, Children: []Widget{
						Label{Text: "Начать с числа:"},
						NumberEdit{AssignTo: &app.startEdit, Decimals: 0, MinValue: 0, MaxValue: 999999, Value: float64(s.Start),
							SpinButtonsVisible: true, OnValueChanged: app.optionsChanged},
						Label{Text: "Порядок:"},
						ComboBox{AssignTo: &app.orderCB, Model: orderNames, CurrentIndex: clampInt(s.Order, 0, len(orderNames)-1),
							OnCurrentIndexChanged: app.optionsChanged},
						CheckBox{AssignTo: &app.contCB, ColumnSpan: 2, Text: "Следующее фото продолжит нумерацию", Checked: s.Continue},
					}},
					GroupBox{Title: "Вид чисел", Layout: Grid{Columns: 2}, Children: []Widget{
						Label{Text: "Шрифт:"},
						ComboBox{AssignTo: &app.fontCB, Model: app.fontList, CurrentIndex: 0, OnCurrentIndexChanged: app.fontChanged},
						Label{Text: "Высота цифр, px:"},
						NumberEdit{AssignTo: &app.sizeEdit, Decimals: 0, MinValue: 6, MaxValue: 2000, Value: float64(s.DigitHeight),
							SpinButtonsVisible: true, ReadOnly: s.AutoSize, OnValueChanged: app.sizeChanged},
						CheckBox{AssignTo: &app.autoCB, ColumnSpan: 2, Text: "Подбирать размер автоматически", Checked: s.AutoSize,
							OnCheckedChanged: app.autoChanged},
					}},
					GroupBox{Title: "Результат", Layout: VBox{}, Children: []Widget{
						PushButton{Text: "Сохранить как…  (Ctrl+S)", OnClicked: app.saveDialog},
						PushButton{Text: "Копировать в буфер  (Ctrl+C)", OnClicked: app.copyResult},
					}},
					TextLabel{Text: helpText},
					VSpacer{},
				},
			},
		},
		StatusBarItems: []StatusBarItem{
			{AssignTo: &app.status, Text: "Откройте или вставьте фото с чёрными линиями-выносками.", Width: 520},
			{AssignTo: &app.info, Text: "", Width: 420},
		},
	}.Create()
	if err != nil {
		return err
	}
	if s.Maximized {
		win.ShowWindow(app.mw.Handle(), win.SW_MAXIMIZE)
	}
	app.canvas.MouseWheel().Attach(app.mouseWheel)
	app.mw.DropFiles().Attach(func(files []string) {
		if len(files) > 0 {
			app.openFile(files[0])
		}
	})
	app.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) { app.storeSettings() })
	app.buildContextMenu()
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (app *App) setStatus(format string, a ...interface{}) {
	app.status.SetText(fmt.Sprintf(format, a...))
}

func (app *App) storeSettings() {
	s := &app.settings
	s.Start = int(app.startEdit.Value())
	if app.pendingNext > 0 && app.contCB.Checked() {
		s.Start = app.pendingNext
	}
	s.Order = app.orderCB.CurrentIndex()
	s.AutoSize = app.autoCB.Checked()
	s.DigitHeight = int(app.sizeEdit.Value())
	s.Continue = app.contCB.Checked()
	if app.font != nil {
		s.FontName = app.font.Name
	}
	s.Maximized = win.IsZoomed(app.mw.Handle())
	if !s.Maximized {
		sz := app.mw.Size()
		s.Width, s.Height = sz.Width, sz.Height
	}
	saveSettings(*s)
}

// ---------------------------------------------------------------------------
// Fonts

func (app *App) loadFontsAsync() {
	list := core.ListFonts()
	app.mw.Synchronize(func() {
		app.fonts = list
		names := []string{core.EmbeddedFontName}
		for _, fi := range list {
			names = append(names, fi.Name)
		}
		app.fontList = names
		want := app.settings.FontName
		idx := -1
		if want != "" {
			for i, n := range names {
				if strings.EqualFold(n, want) {
					idx = i
					break
				}
			}
		}
		if idx < 0 {
			if p := core.PreferredFont(list); p >= 0 {
				idx = p + 1
			} else {
				idx = 0
			}
		}
		app.updating = true
		app.fontCB.SetModel(names)
		app.fontCB.SetCurrentIndex(idx)
		app.updating = false
		app.applyFont(idx)
	})
}

func (app *App) fontChanged() {
	if app.updating {
		return
	}
	app.applyFont(app.fontCB.CurrentIndex())
}

func (app *App) applyFont(idx int) {
	if idx <= 0 || idx-1 >= len(app.fonts) {
		app.font = core.LoadEmbeddedFont()
	} else {
		f, err := core.LoadFont(app.fonts[idx-1])
		if err != nil {
			app.setStatus("Не удалось загрузить шрифт: %v", err)
			return
		}
		app.font = f
	}
	app.relayout()
}

// ---------------------------------------------------------------------------
// Options

func (app *App) options() core.Options {
	opt := core.Options{
		Start: int(app.startEdit.Value()),
		Order: core.OrderMode(app.orderCB.CurrentIndex()),
		Font:  app.font,
	}
	if opt.Order < 0 {
		opt.Order = core.OrderAuto
	}
	if !app.autoCB.Checked() {
		opt.DigitHeight = app.sizeEdit.Value()
	}
	return opt
}

func (app *App) optionsChanged() {
	if app.updating {
		return
	}
	app.relayout()
}

func (app *App) sizeChanged() {
	if app.updating || app.autoCB.Checked() {
		return
	}
	app.relayout()
}

func (app *App) autoChanged() {
	app.sizeEdit.SetReadOnly(app.autoCB.Checked())
	if app.updating {
		return
	}
	app.relayout()
}

// ---------------------------------------------------------------------------
// Document handling

func (app *App) openDialog() {
	dlg := &walk.FileDialog{
		Title:          "Открыть фото",
		Filter:         "Изображения (*.jpg;*.jpeg;*.png;*.bmp;*.gif;*.tif;*.tiff;*.webp)|*.jpg;*.jpeg;*.png;*.bmp;*.gif;*.tif;*.tiff;*.webp|Все файлы (*.*)|*.*",
		InitialDirPath: app.settings.LastDir,
	}
	if ok, err := dlg.ShowOpen(app.mw); err != nil || !ok {
		return
	}
	app.openFile(dlg.FilePath)
}

func (app *App) openFile(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	img, err := core.LoadImage(path)
	if err != nil {
		walk.MsgBox(app.mw, appTitle, fmt.Sprintf("Не удалось открыть «%s»:\n%v", filepath.Base(path), err), walk.MsgBoxIconError)
		return
	}
	app.settings.LastDir = filepath.Dir(path)
	app.setDocument(img, path)
}

func (app *App) paste() {
	img, path, err := clipboardImage(app.mw.Handle())
	if err != nil {
		app.setStatus("%v", err)
		walk.MsgBox(app.mw, appTitle, err.Error(), walk.MsgBoxIconInformation)
		return
	}
	if path != "" {
		app.settings.LastDir = filepath.Dir(path)
	}
	app.setDocument(img, path)
}

func (app *App) setDocument(img *image.RGBA, path string) {
	app.gen++
	gen := app.gen
	app.img = img
	app.srcPath = path
	app.analysis = nil
	app.labels = nil
	app.layout = nil
	app.result = img
	app.bgra = toBGRA(img)
	app.undoStack = nil
	app.hover, app.dragLabel = nil, nil
	app.zoom = 1
	app.busy = true
	if app.contCB.Checked() && app.pendingNext > 0 {
		app.updating = true
		app.startEdit.SetValue(float64(app.pendingNext))
		app.updating = false
	}
	app.pendingNext = 0
	name := "фото из буфера"
	if path != "" {
		name = filepath.Base(path)
	}
	app.mw.SetTitle(fmt.Sprintf("%s — %s", name, appTitle))
	app.setStatus("Поиск линий на %s (%d×%d)…", name, img.Rect.Dx(), img.Rect.Dy())
	app.info.SetText("")
	app.canvas.Invalidate()
	go func() {
		a := core.Analyze(img)
		app.mw.Synchronize(func() {
			if gen != app.gen {
				return
			}
			app.busy = false
			app.analysis = a
			app.labels = core.NewLabels(a)
			app.relayout()
			if len(app.labels) == 0 {
				app.setStatus("Чёрные линии не найдены. Щёлкните по линии, чтобы добавить число вручную.")
			} else {
				app.setStatus("Готово. Проверьте числа и сохраните результат (Ctrl+S).")
			}
		})
	}()
}

func (app *App) relayout() {
	if app.analysis == nil || app.img == nil || app.font == nil {
		return
	}
	opt := app.options()
	app.layout = core.MakeLayout(app.analysis, app.labels, opt)
	if app.autoCB.Checked() {
		app.updating = true
		app.sizeEdit.SetValue(app.layout.DigitHeight)
		app.updating = false
	}
	app.render()
}

func (app *App) render() {
	if app.layout == nil {
		return
	}
	opt := app.options()
	app.result = core.Render(app.img, app.layout, opt)
	app.bgra = toBGRA(app.result)
	app.canvas.Invalidate()
	app.updateInfo()
}

func (app *App) updateInfo() {
	if app.layout == nil {
		app.info.SetText("")
		return
	}
	n := len(app.layout.Labels)
	txt := ""
	if n == 0 {
		txt = "Чисел нет"
	} else {
		first := int(app.startEdit.Value())
		txt = fmt.Sprintf("Чисел: %d (%d–%d) · высота цифр %.0f px", n, first, first+n-1, app.layout.DigitHeight)
	}
	if app.layout.Conflicts > 0 {
		txt += fmt.Sprintf(" · не удалось разместить без наложений: %d", app.layout.Conflicts)
	}
	app.info.SetText(txt)
}

func (app *App) pushUndo() {
	app.undoStack = append(app.undoStack, core.CloneLabels(app.labels))
	if len(app.undoStack) > 100 {
		app.undoStack = app.undoStack[1:]
	}
}

func (app *App) undo() {
	if len(app.undoStack) == 0 {
		return
	}
	app.labels = app.undoStack[len(app.undoStack)-1]
	app.undoStack = app.undoStack[:len(app.undoStack)-1]
	app.hover, app.dragLabel = nil, nil
	app.relayout()
}

func (app *App) resetEdits() {
	if app.analysis == nil {
		return
	}
	app.pushUndo()
	app.labels = core.NewLabels(app.analysis)
	app.hover = nil
	app.relayout()
}

func (app *App) saveDialog() {
	if app.layout == nil || app.result == nil {
		walk.MsgBox(app.mw, appTitle, "Сначала откройте фото.", walk.MsgBoxIconInformation)
		return
	}
	def := "фото_num.jpg"
	dir := app.settings.LastDir
	if app.srcPath != "" {
		def = filepath.Base(DefaultOutputName(app.srcPath))
		dir = filepath.Dir(app.srcPath)
	}
	path, ok := saveFileDialog(app.mw.Handle(), "Сохранить фото с числами", dir, def)
	if !ok {
		return
	}
	if err := core.SaveImage(path, app.result); err != nil {
		walk.MsgBox(app.mw, appTitle, "Не удалось сохранить файл:\n"+err.Error(), walk.MsgBoxIconError)
		return
	}
	app.settings.LastDir = filepath.Dir(path)
	n := len(app.layout.Labels)
	if n > 0 {
		app.pendingNext = int(app.startEdit.Value()) + n
	}
	if app.contCB.Checked() && n > 0 {
		app.setStatus("Сохранено: %s. Следующее фото начнётся с %d.", filepath.Base(path), app.pendingNext)
	} else {
		app.setStatus("Сохранено: %s", filepath.Base(path))
	}
}

func (app *App) copyResult() {
	if app.result == nil {
		return
	}
	if err := copyImageToClipboard(app.mw.Handle(), app.result); err != nil {
		app.setStatus("%v", err)
		return
	}
	app.setStatus("Изображение с числами скопировано в буфер обмена.")
}

// saveFileDialog shows the standard "Save as" dialog (JPEG/PNG).
func saveFileDialog(owner win.HWND, title, dir, name string) (string, bool) {
	buf := make([]uint16, 1024)
	copy(buf, syscall.StringToUTF16(name))
	// The filter is a list of NUL-separated strings, so it cannot go through
	// syscall.StringToUTF16 (which rejects NUL).
	filter := utf16.Encode([]rune("JPEG (*.jpg)\x00*.jpg;*.jpeg\x00PNG (*.png)\x00*.png\x00\x00"))
	filterIdx := uint32(1)
	if strings.EqualFold(filepath.Ext(name), ".png") {
		filterIdx = 2
	}
	ofn := win.OPENFILENAME{
		LStructSize:     uint32(unsafe.Sizeof(win.OPENFILENAME{})),
		HwndOwner:       owner,
		LpstrFilter:     &filter[0],
		NFilterIndex:    filterIdx,
		LpstrFile:       &buf[0],
		NMaxFile:        uint32(len(buf)),
		LpstrInitialDir: syscall.StringToUTF16Ptr(dir),
		LpstrTitle:      syscall.StringToUTF16Ptr(title),
		LpstrDefExt:     syscall.StringToUTF16Ptr("jpg"),
		Flags:           win.OFN_OVERWRITEPROMPT | win.OFN_PATHMUSTEXIST | win.OFN_NOCHANGEDIR | win.OFN_EXPLORER,
	}
	if !win.GetSaveFileName(&ofn) {
		return "", false
	}
	path := syscall.UTF16ToString(buf)
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		if ofn.NFilterIndex == 2 {
			path += ".png"
		} else {
			path += ".jpg"
		}
	}
	return path, true
}

// ---------------------------------------------------------------------------
// View

func (app *App) imageSize() (float64, float64) {
	if app.result == nil {
		return 1, 1
	}
	return float64(app.result.Rect.Dx()), float64(app.result.Rect.Dy())
}

// view returns the display scale and offset of the image.
func (app *App) view() (s, ox, oy float64) {
	b := app.canvas.ClientBoundsPixels()
	bw, bh := float64(b.Width), float64(b.Height)
	W, H := app.imageSize()
	fit := math.Min((bw-16)/W, (bh-16)/H)
	if fit <= 0 {
		fit = 0.01
	}
	s = fit * app.zoom
	dw, dh := W*s, H*s
	if dw <= bw {
		ox = (bw - dw) / 2
	} else {
		app.cx = math.Max(bw/2/s, math.Min(W-bw/2/s, app.cx))
		ox = bw/2 - app.cx*s
	}
	if dh <= bh {
		oy = (bh - dh) / 2
	} else {
		app.cy = math.Max(bh/2/s, math.Min(H-bh/2/s, app.cy))
		oy = bh/2 - app.cy*s
	}
	return
}

func (app *App) toImage(x, y int) core.Pt {
	s, ox, oy := app.view()
	return core.Pt{X: (float64(x) - ox) / s, Y: (float64(y) - oy) / s}
}

func (app *App) zoomAt(mx, my, factor float64) {
	if app.result == nil {
		return
	}
	s0, ox0, oy0 := app.view()
	px, py := (mx-ox0)/s0, (my-oy0)/s0
	old := app.zoom
	app.zoom = math.Max(1, math.Min(16, old*factor))
	if app.zoom == old {
		return
	}
	// keep the image point under the cursor in place
	s1 := s0 / old * app.zoom
	b := app.canvas.ClientBoundsPixels()
	app.cx = (float64(b.Width)/2 - (mx - px*s1)) / s1
	app.cy = (float64(b.Height)/2 - (my - py*s1)) / s1
	app.canvas.Invalidate()
}

func (app *App) zoomCenter(factor float64) {
	b := app.canvas.ClientBoundsPixels()
	app.zoomAt(float64(b.Width)/2, float64(b.Height)/2, factor)
}

func (app *App) paint(c *walk.Canvas, _ walk.Rectangle) error {
	b := app.canvas.ClientBoundsPixels()
	c.FillRectanglePixels(app.bgBrush, b)
	if app.bgra == nil || app.result == nil {
		r := walk.Rectangle{X: b.X, Y: b.Y + b.Height/2 - 40, Width: b.Width, Height: 80}
		c.DrawTextPixels("Перетащите сюда фото, нажмите Ctrl+V\nили щёлкните, чтобы открыть файл",
			app.hintFont, walk.RGB(220, 220, 220), r, walk.TextCenter|walk.TextWordbreak)
		return nil
	}
	s, ox, oy := app.view()
	W, H := app.imageSize()
	hdc := c.HDC()
	win.SetStretchBltMode(hdc, win.HALFTONE)
	stretchBGRA(hdc, int(math.Round(ox)), int(math.Round(oy)), int(math.Round(W*s)), int(math.Round(H*s)),
		app.bgra, app.result.Rect.Dx(), app.result.Rect.Dy())
	if l := app.hover; l != nil && app.layout != nil {
		r := l.Box.Inset(-0.12 * app.layout.DigitHeight)
		rect := walk.Rectangle{
			X: int(ox + r.X0*s), Y: int(oy + r.Y0*s),
			Width: int(r.W()*s) + 1, Height: int(r.H()*s) + 1,
		}
		c.DrawRectanglePixels(app.hoverPen, rect)
	}
	if app.busy {
		c.DrawTextPixels("Поиск линий…", app.hintFont, walk.RGB(255, 255, 255),
			walk.Rectangle{X: b.X, Y: b.Y + 10, Width: b.Width, Height: 40}, walk.TextCenter|walk.TextTop)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mouse

func (app *App) hitPad() float64 {
	s, _, _ := app.view()
	return 6 / s
}

func (app *App) mouseDown(x, y int, button walk.MouseButton) {
	app.canvas.SetFocus()
	app.downX, app.downY = x, y
	app.moved = false
	if app.img == nil {
		return
	}
	p := app.toImage(x, y)
	var hit *core.Label
	if app.layout != nil {
		hit = app.layout.HitTest(p, app.hitPad())
	}
	switch button {
	case walk.LeftButton:
		if hit != nil {
			app.pushUndo()
			app.dragLabel = hit
			app.dragExtra = hit.Extra
			app.dragStart = p
		} else {
			app.panning = true
			app.panCX, app.panCY = app.cx, app.cy
		}
	case walk.MiddleButton:
		app.panning = true
		app.panCX, app.panCY = app.cx, app.cy
	case walk.RightButton:
		app.ctxLabel = hit
		app.ctxPoint = p
		app.updateContextMenu()
	}
}

func (app *App) mouseMove(x, y int, button walk.MouseButton) {
	if app.img == nil {
		return
	}
	if abs(x-app.downX) > 3 || abs(y-app.downY) > 3 {
		if button&(walk.LeftButton|walk.MiddleButton) != 0 {
			app.moved = true
		}
	}
	if app.dragLabel != nil && button&walk.LeftButton != 0 {
		if !app.moved {
			return
		}
		p := app.toImage(x, y)
		l := app.dragLabel
		e := app.dragExtra + p.Sub(app.dragStart).Dot(l.Line.Dir())
		e = math.Max(e, core.MinExtra(app.layout.DigitHeight))
		l.Extra = e
		l.UserExtra = true
		app.layout.Reposition(l, app.options())
		app.layout.KeepInside(l, app.options(), app.img.Rect.Dx(), app.img.Rect.Dy())
		app.render()
		return
	}
	if app.panning && button&(walk.LeftButton|walk.MiddleButton) != 0 {
		if app.zoom > 1 && app.moved {
			s, _, _ := app.view()
			app.cx = app.panCX - float64(x-app.downX)/s
			app.cy = app.panCY - float64(y-app.downY)/s
			app.canvas.Invalidate()
		}
		return
	}
	var hit *core.Label
	if app.layout != nil {
		hit = app.layout.HitTest(app.toImage(x, y), app.hitPad())
	}
	if hit != app.hover {
		app.hover = hit
		if hit != nil {
			app.canvas.SetCursor(walk.CursorSizeAll())
		} else {
			app.canvas.SetCursor(walk.CursorArrow())
		}
		app.canvas.Invalidate()
	}
}

func (app *App) mouseUp(x, y int, button walk.MouseButton) {
	if app.img == nil {
		if button == walk.LeftButton {
			app.openDialog()
		}
		return
	}
	switch button {
	case walk.LeftButton:
		if app.dragLabel != nil {
			if !app.moved && len(app.undoStack) > 0 {
				app.undoStack = app.undoStack[:len(app.undoStack)-1]
			} else {
				app.relayout()
				app.setStatus("Число сдвинуто вдоль линии. Ctrl+Z — отменить.")
			}
			app.dragLabel = nil
		} else if app.panning && !app.moved {
			app.addLabelAt(app.toImage(x, y), false)
		}
		app.panning = false
	case walk.MiddleButton:
		app.panning = false
	}
}

func (app *App) mouseWheel(x, y int, button walk.MouseButton) {
	delta := int16(uint32(button) >> 16)
	pt := win.POINT{X: int32(x), Y: int32(y)}
	win.ScreenToClient(app.canvas.Handle(), &pt)
	f := 1.2
	if delta < 0 {
		f = 1 / 1.2
	}
	app.zoomAt(float64(pt.X), float64(pt.Y), f)
}

func (app *App) keyDown(key walk.Key) {
	switch key {
	case walk.KeyDelete, walk.KeyBack:
		if app.hover != nil {
			app.deleteLabel(app.hover)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ---------------------------------------------------------------------------
// Editing

func (app *App) buildContextMenu() {
	menu, err := walk.NewMenu()
	if err != nil {
		return
	}
	mk := func(text string, f func()) *walk.Action {
		a := walk.NewAction()
		a.SetText(text)
		a.Triggered().Attach(f)
		menu.Actions().Add(a)
		return a
	}
	app.actDelete = mk("Удалить это число", func() {
		if app.ctxLabel != nil {
			app.deleteLabel(app.ctxLabel)
		}
	})
	app.actFlip = mk("Перенести число на другой конец линии", func() {
		if l := app.ctxLabel; l != nil {
			app.pushUndo()
			l.Line = l.Line.Swapped()
			l.Extra, l.UserExtra = 0, false
			app.relayout()
		}
	})
	app.actNumber = mk("Задать номер этого числа…", app.askNumber)
	app.actResetPos = mk("Вернуть число к концу линии", func() {
		if l := app.ctxLabel; l != nil {
			app.pushUndo()
			l.Extra, l.UserExtra = 0, false
			app.relayout()
		}
	})
	app.actAdd = mk("Добавить число к линии здесь", func() { app.addLabelAt(app.ctxPoint, true) })
	menu.Actions().Add(walk.NewSeparatorAction())
	app.actRedetect = mk("Найти линии заново (сбросить правки)", app.resetEdits)
	app.canvas.SetContextMenu(menu)
}

func (app *App) updateContextMenu() {
	onLabel := app.ctxLabel != nil
	hasDoc := app.analysis != nil
	app.actDelete.SetVisible(onLabel)
	app.actFlip.SetVisible(onLabel)
	app.actNumber.SetVisible(onLabel)
	app.actResetPos.SetVisible(onLabel && app.ctxLabel.UserExtra)
	app.actAdd.SetVisible(!onLabel && hasDoc)
	app.actRedetect.SetEnabled(hasDoc)
}

func (app *App) deleteLabel(l *core.Label) {
	app.pushUndo()
	out := app.labels[:0:0]
	for _, x := range app.labels {
		if x != l {
			out = append(out, x)
		}
	}
	app.labels = out
	app.hover = nil
	app.relayout()
	app.setStatus("Число удалено. Ctrl+Z — отменить.")
}

func (app *App) addLabelAt(p core.Pt, explicit bool) {
	if app.analysis == nil {
		return
	}
	ln, ok := app.analysis.LineAt(p)
	if !ok {
		if explicit {
			app.setStatus("В этом месте не найдена чёрная линия.")
		}
		return
	}
	if core.HasLine(app.labels, ln, 2*app.analysis.Stroke+3) {
		if explicit {
			app.setStatus("У этой линии уже есть число.")
		}
		return
	}
	app.pushUndo()
	app.labels = append(app.labels, &core.Label{Line: ln})
	app.relayout()
	app.setStatus("Число добавлено. Если оно не на том конце, щёлкните по нему правой кнопкой и выберите «Перенести на другой конец».")
}

func (app *App) askNumber() {
	l := app.ctxLabel
	if l == nil {
		return
	}
	start := int(app.startEdit.Value())
	var dlg *walk.Dialog
	var ne *walk.NumberEdit
	var okPB, cancelPB *walk.PushButton
	value := l.Number
	_, err := Dialog{
		AssignTo:      &dlg,
		Title:         "Номер числа",
		DefaultButton: &okPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 300, Height: 150},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "Какой номер поставить? Остальные числа сдвинутся."},
			NumberEdit{AssignTo: &ne, Decimals: 0, MinValue: float64(start), MaxValue: float64(start + len(app.labels) - 1),
				Value: float64(l.Number), SpinButtonsVisible: true,
				OnKeyDown: func(key walk.Key) {
					if key == walk.KeyReturn {
						value = int(ne.Value())
						dlg.Accept()
					}
				}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				HSpacer{},
				PushButton{AssignTo: &okPB, Text: "OK", OnClicked: func() { value = int(ne.Value()); dlg.Accept() }},
				PushButton{AssignTo: &cancelPB, Text: "Отмена", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}.Run(app.mw)
	if err != nil || dlg.Result() != walk.DlgCmdOK {
		return
	}
	pos := clampInt(value-start+1, 1, len(app.labels))
	app.pushUndo()
	for _, x := range app.labels {
		if x != l && x.Pin == pos {
			x.Pin = 0
		}
	}
	l.Pin = pos
	app.relayout()
}
