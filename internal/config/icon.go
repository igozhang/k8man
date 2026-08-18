package config

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
)

// 马里奥风格默认站点 icon：蓝底圆角砖 + 白描边红 8（NES 数字字形）。
var (
	defaultIconOnce sync.Once
	defaultIconPNG  []byte
)

func DefaultIconPNG() []byte {
	defaultIconOnce.Do(func() {
		defaultIconPNG = renderMario8()
	})
	return defaultIconPNG
}

func renderMario8() []byte {
	const s = 64
	navy := color.NRGBA{0x0A, 0x3D, 0x70, 0xFF}
	blue := color.NRGBA{0x04, 0x9C, 0xD8, 0xFF}
	light := color.NRGBA{0x4E, 0xB8, 0xF0, 0xFF}
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	red := color.NRGBA{0xE5, 0x25, 0x21, 0xFF}

	img := image.NewNRGBA(image.Rect(0, 0, s, s))
	const radius = 12

	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			if outsideRoundRect(x, y, s, radius) {
				continue
			}
			c := blue
			switch {
			case onRoundRectRim(x, y, s, radius):
				c = navy
			case y <= 14:
				c = light
			case y >= s-6:
				c = navy
			}
			img.SetNRGBA(x, y, c)
		}
	}

	// 白内框（避开圆角）
	for y := 6; y < s-6; y++ {
		for x := 6; x < s-6; x++ {
			if outsideRoundRect(x, y, s, radius-4) {
				continue
			}
			if x == 6 || x == s-7 || y == 6 || y == s-7 {
				img.SetNRGBA(x, y, white)
			}
		}
	}

	// NES 风格数字 8，6x 放大后白描边 + 红填充
	eight := []string{
		"0111110",
		"1100011",
		"1100011",
		"0111110",
		"1100011",
		"1100011",
		"0111110",
	}
	const scale = 6
	ox, oy := 11, 11

	paintScaled := func(col color.NRGBA, expand int) {
		for row, line := range eight {
			for colx, ch := range line {
				if ch != '1' {
					continue
				}
				x0 := ox + colx*scale - expand
				y0 := oy + row*scale - expand
				x1 := ox + (colx+1)*scale + expand
				y1 := oy + (row+1)*scale + expand
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						if x < 0 || y < 0 || x >= s || y >= s {
							continue
						}
						if outsideRoundRect(x, y, s, radius) {
							continue
						}
						img.SetNRGBA(x, y, col)
					}
				}
			}
		}
	}
	paintScaled(white, 2)
	paintScaled(red, 0)

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func outsideRoundRect(x, y, size, r int) bool {
	if r <= 0 {
		return false
	}
	cx := []int{r - 1, size - r, r - 1, size - r}
	cy := []int{r - 1, r - 1, size - r, size - r}
	inCorner := []bool{x < r && y < r, x >= size-r && y < r, x < r && y >= size-r, x >= size-r && y >= size-r}
	rr := r * r
	for i := 0; i < 4; i++ {
		if !inCorner[i] {
			continue
		}
		dx := x - cx[i]
		dy := y - cy[i]
		if dx*dx+dy*dy > rr {
			return true
		}
	}
	return false
}

func onRoundRectRim(x, y, size, r int) bool {
	if outsideRoundRect(x, y, size, r) {
		return false
	}
	inset := 2
	if size >= 48 {
		inset = 3
	}
	return outsideRoundRect(x, y, size, r-inset) || x == 0 || y == 0 || x == size-1 || y == size-1
}
