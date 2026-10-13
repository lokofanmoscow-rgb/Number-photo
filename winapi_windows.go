//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"

	"numberphoto/core"
)

var (
	modgdi32    = syscall.NewLazyDLL("gdi32.dll")
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	moduser32   = syscall.NewLazyDLL("user32.dll")

	procStretchDIBits           = modgdi32.NewProc("StretchDIBits")
	procGlobalSize              = modkernel32.NewProc("GlobalSize")
	procGlobalUnlock            = modkernel32.NewProc("GlobalUnlock")
	procGlobalFree              = modkernel32.NewProc("GlobalFree")
	procAttachConsole           = modkernel32.NewProc("AttachConsole")
	procRegisterClipboardFormat = moduser32.NewProc("RegisterClipboardFormatW")
	procCloseClipboard          = moduser32.NewProc("CloseClipboard")
)

const (
	cfDIB   = 8
	cfDIBV5 = 17
	cfHDROP = 15
)

func attachConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	procAttachConsole.Call(attachParentProcess)
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// stretchBGRA draws a top-down 32-bit BGRA buffer into hdc.
func stretchBGRA(hdc win.HDC, dx, dy, dw, dh int, bits []byte, w, h int) {
	if len(bits) == 0 {
		return
	}
	bi := bitmapInfoHeader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	procStretchDIBits.Call(uintptr(hdc),
		uintptr(int32(dx)), uintptr(int32(dy)), uintptr(int32(dw)), uintptr(int32(dh)),
		0, 0, uintptr(w), uintptr(h),
		uintptr(unsafe.Pointer(&bits[0])), uintptr(unsafe.Pointer(&bi)),
		0 /*DIB_RGB_COLORS*/, 0x00CC0020 /*SRCCOPY*/)
}

// toBGRA converts an RGBA image to a top-down BGRA buffer.
func toBGRA(img *image.RGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+w*4]
		dst := out[y*w*4 : (y+1)*w*4]
		for x := 0; x < w*4; x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], 255
		}
	}
	return out
}

func registerFormat(name string) uint32 {
	p, _ := syscall.UTF16PtrFromString(name)
	r, _, _ := procRegisterClipboardFormat.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

func globalBytes(h win.HANDLE) []byte {
	if h == 0 {
		return nil
	}
	size, _, _ := procGlobalSize.Call(uintptr(h))
	ptr := win.GlobalLock(win.HGLOBAL(h))
	if ptr == nil || size == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(uintptr(h))
	out := make([]byte, size)
	copy(out, unsafe.Slice((*byte)(ptr), size))
	return out
}

func openClipboard(hwnd win.HWND) bool {
	for i := 0; i < 10; i++ {
		if win.OpenClipboard(hwnd) {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

// clipboardImage reads an image (or the path of a copied image file) from the
// clipboard.
func clipboardImage(hwnd win.HWND) (*image.RGBA, string, error) {
	if !openClipboard(hwnd) {
		return nil, "", errors.New("буфер обмена занят другой программой")
	}
	defer procCloseClipboard.Call()

	// Files copied in Explorer.
	if win.IsClipboardFormatAvailable(cfHDROP) {
		h := win.GetClipboardData(cfHDROP)
		if h != 0 {
			n := win.DragQueryFile(win.HDROP(h), 0xFFFFFFFF, nil, 0)
			for i := uint(0); i < n; i++ {
				l := win.DragQueryFile(win.HDROP(h), i, nil, 0)
				buf := make([]uint16, l+1)
				win.DragQueryFile(win.HDROP(h), i, &buf[0], l+1)
				path := syscall.UTF16ToString(buf)
				if img, err := core.LoadImage(path); err == nil {
					return img, path, nil
				}
			}
		}
	}
	// PNG (browsers, Office, many editors) keeps the exact pixels.
	if f := registerFormat("PNG"); f != 0 && win.IsClipboardFormatAvailable(f) {
		if data := globalBytes(win.GetClipboardData(f)); len(data) > 0 {
			if img, err := core.DecodeImage(data); err == nil {
				return img, "", nil
			}
		}
	}
	for _, f := range []uint32{cfDIBV5, cfDIB} {
		if !win.IsClipboardFormatAvailable(f) {
			continue
		}
		if data := globalBytes(win.GetClipboardData(f)); len(data) > 0 {
			if img, err := decodeDIB(data); err == nil {
				return img, "", nil
			}
		}
	}
	return nil, "", errors.New("в буфере обмена нет изображения")
}

// decodeDIB decodes a packed DIB (BITMAPINFOHEADER/V5 followed by pixels).
func decodeDIB(data []byte) (*image.RGBA, error) {
	if len(data) < 40 {
		return nil, errors.New("bad DIB")
	}
	le := binary.LittleEndian
	hdrSize := int(le.Uint32(data[0:]))
	w := int(int32(le.Uint32(data[4:])))
	hRaw := int(int32(le.Uint32(data[8:])))
	bpp := int(le.Uint16(data[14:]))
	comp := le.Uint32(data[16:])
	clrUsed := int(le.Uint32(data[32:]))
	if w <= 0 || hRaw == 0 || hdrSize < 40 || hdrSize > len(data) {
		return nil, errors.New("bad DIB")
	}
	topDown := hRaw < 0
	h := hRaw
	if h < 0 {
		h = -h
	}
	off := hdrSize
	masks := [3]uint32{0x00FF0000, 0x0000FF00, 0x000000FF}
	if comp == 3 { // BI_BITFIELDS
		if hdrSize == 40 {
			if len(data) < off+12 {
				return nil, errors.New("bad DIB")
			}
			masks = [3]uint32{le.Uint32(data[off:]), le.Uint32(data[off+4:]), le.Uint32(data[off+8:])}
			off += 12
		} else if hdrSize >= 52 {
			masks = [3]uint32{le.Uint32(data[40:]), le.Uint32(data[44:]), le.Uint32(data[48:])}
		}
	} else if comp != 0 {
		return nil, errors.New("сжатый DIB не поддерживается")
	}
	var palette [][3]byte
	if bpp <= 8 {
		n := clrUsed
		if n == 0 {
			n = 1 << bpp
		}
		for i := 0; i < n && off+4 <= len(data); i++ {
			palette = append(palette, [3]byte{data[off+2], data[off+1], data[off]})
			off += 4
		}
	}
	stride := ((w*bpp + 31) / 32) * 4
	if off+stride*h > len(data) {
		return nil, errors.New("bad DIB size")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	shift := func(m uint32) (uint, uint32) {
		if m == 0 {
			return 0, 0
		}
		s := uint(0)
		for m&1 == 0 {
			m >>= 1
			s++
		}
		return s, m
	}
	if bpp == 16 && comp == 0 {
		masks = [3]uint32{0x7C00, 0x03E0, 0x001F}
	}
	var sh [3]uint
	var mx [3]uint32
	for i := range masks {
		sh[i], mx[i] = shift(masks[i])
	}
	for y := 0; y < h; y++ {
		sy := y
		if !topDown {
			sy = h - 1 - y
		}
		row := data[off+sy*stride:]
		for x := 0; x < w; x++ {
			var r, g, b byte
			switch bpp {
			case 32, 16:
				var v uint32
				if bpp == 32 {
					v = le.Uint32(row[x*4:])
				} else {
					v = uint32(le.Uint16(row[x*2:]))
				}
				c := [3]byte{}
				for i := 0; i < 3; i++ {
					if mx[i] == 0 {
						continue
					}
					c[i] = byte(((v >> sh[i]) & mx[i]) * 255 / mx[i])
				}
				r, g, b = c[0], c[1], c[2]
			case 24:
				b, g, r = row[x*3], row[x*3+1], row[x*3+2]
			case 8, 4, 1:
				bit := x * bpp
				idx := int(row[bit/8]>>(8-bpp-bit%8)) & (1<<bpp - 1)
				if idx < len(palette) {
					r, g, b = palette[idx][0], palette[idx][1], palette[idx][2]
				}
			default:
				return nil, errors.New("формат DIB не поддерживается")
			}
			o := y*img.Stride + x*4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = r, g, b, 255
		}
	}
	return img, nil
}

func setClipboardBytes(format uint32, data []byte) bool {
	const gmemMoveable = 0x0002
	h := win.GlobalAlloc(gmemMoveable, uintptr(len(data)))
	if h == 0 {
		return false
	}
	ptr := win.GlobalLock(h)
	if ptr == nil {
		procGlobalFree.Call(uintptr(h))
		return false
	}
	copy(unsafe.Slice((*byte)(ptr), len(data)), data)
	procGlobalUnlock.Call(uintptr(h))
	if win.SetClipboardData(format, win.HANDLE(h)) == 0 {
		procGlobalFree.Call(uintptr(h))
		return false
	}
	return true
}

// copyImageToClipboard puts img on the clipboard as DIB and PNG.
func copyImageToClipboard(hwnd win.HWND, img *image.RGBA) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	stride := ((w*24 + 31) / 32) * 4
	dib := make([]byte, 40+stride*h)
	le := binary.LittleEndian
	le.PutUint32(dib[0:], 40)
	le.PutUint32(dib[4:], uint32(w))
	le.PutUint32(dib[8:], uint32(h)) // bottom-up
	le.PutUint16(dib[12:], 1)
	le.PutUint16(dib[14:], 24)
	le.PutUint32(dib[20:], uint32(stride*h))
	for y := 0; y < h; y++ {
		row := dib[40+(h-1-y)*stride:]
		src := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			row[x*3], row[x*3+1], row[x*3+2] = src[x*4+2], src[x*4+1], src[x*4]
		}
	}
	var pngBuf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	_ = enc.Encode(&pngBuf, img)

	if !openClipboard(hwnd) {
		return errors.New("буфер обмена занят другой программой")
	}
	defer procCloseClipboard.Call()
	win.EmptyClipboard()
	ok := setClipboardBytes(cfDIB, dib)
	if f := registerFormat("PNG"); f != 0 && pngBuf.Len() > 0 {
		setClipboardBytes(f, pngBuf.Bytes())
	}
	if !ok {
		return errors.New("не удалось скопировать изображение")
	}
	return nil
}
