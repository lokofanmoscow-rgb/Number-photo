package core

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "image/gif"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// LoadImage reads an image file and applies the EXIF orientation of JPEGs.
func LoadImage(path string) (*image.RGBA, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeImage(data)
}

// DecodeImage decodes image bytes (JPEG, PNG, BMP, GIF, TIFF, WebP).
func DecodeImage(data []byte) (*image.RGBA, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать изображение: %w", err)
	}
	rgba := ToRGBA(img)
	if o := exifOrientation(data); o > 1 {
		rgba = applyOrientation(rgba, o)
	}
	return rgba, nil
}

// SaveImage writes img as JPEG (quality 95) or PNG depending on the extension.
func SaveImage(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := EncodeImage(f, path, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// EncodeImage encodes img in the format implied by name.
func EncodeImage(w io.Writer, name string, img image.Image) error {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return png.Encode(w, img)
	default:
		return jpeg.Encode(w, img, &jpeg.Options{Quality: 95})
	}
}

// exifOrientation returns the EXIF orientation tag of a JPEG (1 if absent).
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan / end
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[i+2:]))
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return parseTIFFOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func parseTIFFOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			v := int(bo.Uint16(t[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// applyOrientation transforms the image according to an EXIF orientation.
func applyOrientation(src *image.RGBA, o int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			so := y*src.Stride + x*4
			do := dy*dst.Stride + dx*4
			copy(dst.Pix[do:do+4], src.Pix[so:so+4])
		}
	}
	return dst
}
