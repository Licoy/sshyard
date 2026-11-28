package rdp

import (
	"fmt"
	"image"
	"image/color"
)

// decodePlanarBitmap decodes the planar stream described by MS-RDPEGDI
// 2.2.2.5.1 and 3.1.9. The first wire scanline is the bottom of the bitmap.
func decodePlanarBitmap(data []byte, f bitmapFormat) (*image.RGBA, error) {
	if _, err := f.pixelCount(); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("short RDP planar bitmap")
	}
	header := data[0]
	loss := uint(header & 7)
	subsampled, rle, alpha := header&8 != 0, header&16 != 0, header&32 == 0
	if subsampled && loss == 0 {
		return nil, fmt.Errorf("invalid RDP planar chroma subsampling")
	}

	chromaWidth, chromaHeight := f.width, f.height
	chromaScale := 1
	if subsampled {
		chromaWidth, chromaHeight = (f.width+1)/2, (f.height+1)/2
		chromaScale = 2
	}
	data = data[1:]
	var planes [4][]byte
	for i := range planes {
		if i == 0 && !alpha {
			continue
		}
		width, height := f.width, f.height
		if i >= 2 {
			width, height = chromaWidth, chromaHeight
		}
		var err error
		planes[i], data, err = decodePlanarPlane(data, width, height, rle)
		if err != nil {
			return nil, fmt.Errorf("RDP planar bitmap plane %d: %w", i, err)
		}
	}
	if len(data) != 0 && (rle || len(data) != 1) {
		return nil, fmt.Errorf("trailing RDP planar bitmap data")
	}
	return combinePlanarBitmap(f, planes, planarChroma{chromaWidth, chromaHeight, chromaScale, loss}), nil
}

type planarChroma struct {
	width, height, scale int
	loss                 uint
}

func combinePlanarBitmap(f bitmapFormat, planes [4][]byte, chroma planarChroma) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, f.width, f.height))
	for y := range f.height {
		row := (f.height - 1 - y) * f.width
		chromaRow := (chroma.height - 1 - y/chroma.scale) * chroma.width
		for x := range f.width {
			i := row + x
			j := chromaRow + x/chroma.scale
			r, g, b := planes[1][i], planes[2][j], planes[3][j]
			if chroma.loss != 0 {
				r, g, b = planarRGB(r, g, b, chroma.loss)
			}
			// Bitmap Updates replace desktop pixels, even if the server sent
			// an alpha plane in its planar representation.
			putBitmapColor(img.Pix[y*img.Stride+x*4:], color.RGBA{r, g, b, 255})
		}
	}
	return img
}

func planarRGB(y, co, cg byte, loss uint) (byte, byte, byte) {
	orange := int(int8(co)) << (loss - 1)
	green := int(int8(cg)) << (loss - 1)
	t := int(y) - green
	return byte(clampByte(t + orange)), byte(clampByte(int(y) + green)), byte(clampByte(t - orange))
}

func clampByte(v int) int { return min(max(v, 0), 255) }

func decodePlanarPlane(data []byte, width, height int, rle bool) ([]byte, []byte, error) {
	size := width * height
	if !rle {
		if len(data) < size {
			return nil, nil, fmt.Errorf("short raw color plane")
		}
		return data[:size], data[size:], nil
	}
	if len(data) < height*((width+46)/47) {
		return nil, nil, fmt.Errorf("short RLE color plane")
	}
	plane := make([]byte, size)
	for y := range height {
		row := plane[y*width : (y+1)*width]
		var previous []byte
		if y > 0 {
			previous = plane[(y-1)*width : y*width]
		}
		var err error
		data, err = decodePlanarRow(data, row, previous)
		if err != nil {
			return nil, nil, err
		}
	}
	return plane, data, nil
}

func decodePlanarRow(data, row, previous []byte) ([]byte, error) {
	var last byte
	for x := 0; x < len(row); {
		if len(data) == 0 {
			return nil, fmt.Errorf("short planar RLE segment header")
		}
		raw, repeat := int(data[0]>>4), int(data[0]&15)
		data = data[1:]
		if repeat == 1 || repeat == 2 {
			raw, repeat = 0, raw+16*repeat
		}
		if raw+repeat == 0 || raw+repeat > len(row)-x || raw > len(data) {
			return nil, fmt.Errorf("invalid planar RLE segment length")
		}
		for i := range raw + repeat {
			if i < raw {
				last = data[i]
				if previous != nil {
					last = (last >> 1) ^ (0 - (last & 1))
				}
			}
			row[x] = last
			if previous != nil {
				row[x] += previous[x]
			}
			x++
		}
		data = data[raw:]
	}
	return data, nil
}
