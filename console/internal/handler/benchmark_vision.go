package handler

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/fs"
	"math"
	"strings"
	"sync"

	"github.com/lynxlee/lynx-ollama-console/internal/service"
)

// ── Vision Test Image Generator ──────────────────────────────────
// Uses Go standard library to generate test images at compile time.
// testdata/ images are loaded via embed FS.

var (
	testImagesOnce sync.Once
	testImages     map[string]string // id → base64 encoded PNG
)

// getTestImages returns pre-generated base64-encoded test images.
func getTestImages() map[string]string {
	testImagesOnce.Do(func() {
		testImages = map[string]string{
			"shapes":       generateShapesImage(),
			"text":         generateTextImage(),
			"text_cn":      generateChineseTextImage(),
			"text_mixed":   generateMixedTextImage(),
			"text_table":   generateTableTextImage(),
			"colors":       generateColorsImage(),
			"counting":     generateCountingImage(),
			"bar_chart":    generateBarChartImage(),
		}
		// Load real image from embedded testdata
		if data, err := fs.ReadFile(staticFS, "static/testdata/scene_natural.png"); err == nil {
			testImages["scene_natural"] = base64.StdEncoding.EncodeToString(data)
		}
	})
	return testImages
}

// generateShapesImage creates a 200x150 PNG with a red circle, blue square, and green triangle.
func generateShapesImage() string {
	w, h := 200, 150
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	// Red circle (center 50,75, radius 30)
	red := color.RGBA{220, 38, 38, 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-50), float64(y-75)
			if dx*dx+dy*dy <= 30*30 {
				img.SetRGBA(x, y, red)
			}
		}
	}

	// Blue square (top-left 75,45, size 60x60)
	blue := color.RGBA{37, 99, 235, 255}
	for y := 45; y < 105; y++ {
		for x := 75; x < 135; x++ {
			img.SetRGBA(x, y, blue)
		}
	}

	// Green triangle (vertices: 160,105  190,105  175,45)
	green := color.RGBA{22, 163, 74, 255}
	for y := 45; y < 106; y++ {
		for x := 140; x < 200; x++ {
			if pointInTriangle(float64(x), float64(y), 160, 105, 190, 105, 175, 45) {
				img.SetRGBA(x, y, green)
			}
		}
	}

	return encodeImageBase64(img)
}

// generateTextImage creates a 200x100 PNG with large block-letter text "HELLO" using pixel art.
func generateTextImage() string {
	w, h := 200, 100
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	black := color.RGBA{0, 0, 0, 255}
	drawString(img, "HELLO", 15, 25, 3, black)
	return encodeImageBase64(img)
}

// ── Pixel Font: Chinese Characters ──────────────────────────────────
// 8x8 dot-matrix patterns for Chinese characters used in OCR tests.
// Each character is an 8x8 grid stored as 8 uint8 bitmask rows.
var cnPixelFont = map[rune][8]uint8{
	// 你 (nǐ)
	'你': {0b00100100, 0b00101000, 0b01111110, 0b10101001, 0b00101010, 0b01101100, 0b10100010, 0b00100100},
	// 好 (hǎo)
	'好': {0b01000100, 0b01111100, 0b01000100, 0b00011110, 0b11110010, 0b00010100, 0b00011000, 0b01110000},
	// 世 (shì)
	'世': {0b00100100, 0b00100100, 0b11111110, 0b00100100, 0b00100100, 0b11111110, 0b00000100, 0b00000100},
	// 界 (jiè)
	'界': {0b01111110, 0b01000010, 0b01111110, 0b01000010, 0b01111110, 0b00011000, 0b00100100, 0b01000010},
	// 中 (zhōng)
	'中': {0b00010000, 0b01111110, 0b01010010, 0b01010010, 0b01111110, 0b00010000, 0b00010000, 0b00010000},
	// 国 (guó)
	'国': {0b01111110, 0b01000010, 0b01011010, 0b01010010, 0b01011010, 0b01000010, 0b01111110, 0b00000000},
	// 人 (rén)
	'人': {0b00010000, 0b00010000, 0b00101000, 0b00101000, 0b01000100, 0b01000100, 0b10000010, 0b00000000},
	// 工 (gōng)
	'工': {0b01111110, 0b00010000, 0b00010000, 0b00010000, 0b00010000, 0b00010000, 0b01111110, 0b00000000},
	// 智 (zhì)
	'智': {0b01111010, 0b00101010, 0b01111010, 0b00000000, 0b01111110, 0b01000010, 0b01111110, 0b00000000},
	// 能 (néng)
	'能': {0b00100010, 0b11111010, 0b00100010, 0b00001110, 0b01111010, 0b01001010, 0b01111010, 0b00000000},
	// 北 (běi)
	'北': {0b00010100, 0b00010100, 0b11110100, 0b00011100, 0b00010100, 0b01010100, 0b10010100, 0b00010100},
	// 京 (jīng)
	'京': {0b00010000, 0b01111110, 0b00010000, 0b01111110, 0b00101000, 0b01000100, 0b00010000, 0b00010000},
	// 上 (shàng)
	'上': {0b00010000, 0b00010000, 0b00011110, 0b00010000, 0b00010000, 0b00010000, 0b01111110, 0b00000000},
	// 海 (hǎi)
	'海': {0b01001010, 0b00111110, 0b01000010, 0b00111110, 0b01001010, 0b00111110, 0b01000010, 0b00000000},
}

// drawPixelChar draws a single character glyph at (ox, oy) with given scale.
func drawPixelChar(img *image.RGBA, ch rune, ox, oy, scale int, clr color.RGBA) {
	// Try Chinese pixel font
	if glyph, ok := cnPixelFont[ch]; ok {
		for row := 0; row < 8; row++ {
			for col := 0; col < 8; col++ {
				if glyph[row]&(1<<(7-col)) != 0 {
					for dy := 0; dy < scale; dy++ {
						for dx := 0; dx < scale; dx++ {
							img.SetRGBA(ox+col*scale+dx, oy+row*scale+dy, clr)
						}
					}
				}
			}
		}
		return
	}
	// Try ASCII pixel font
	if ch < 128 {
		if pixels, ok := asciiPixelFont[byte(ch)]; ok {
			for _, p := range pixels {
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.SetRGBA(ox+p[0]*scale+dx, oy+p[1]*scale+dy, clr)
					}
				}
			}
		}
	}
}

// asciiPixelFont is a 5x7 pixel font for ASCII characters (reused from original code).
var asciiPixelFont = map[byte][][2]int{
	'H': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {2, 3}, {4, 0}, {4, 1}, {4, 2}, {4, 3}, {4, 4}, {4, 5}, {4, 6}},
	'E': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {1, 0}, {2, 0}, {3, 0}, {1, 3}, {2, 3}, {1, 6}, {2, 6}, {3, 6}},
	'L': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {1, 6}, {2, 6}, {3, 6}},
	'O': {{0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {1, 0}, {1, 6}, {2, 0}, {2, 6}, {3, 0}, {3, 6}, {4, 1}, {4, 2}, {4, 3}, {4, 4}, {4, 5}},
	'A': {{0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {1, 0}, {2, 0}, {3, 0}, {1, 3}, {2, 3}, {3, 3}, {4, 1}, {4, 2}, {4, 3}, {4, 4}, {4, 5}, {4, 6}},
	'I': {{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}, {2, 1}, {2, 2}, {2, 3}, {2, 4}, {2, 5}, {0, 6}, {1, 6}, {2, 6}, {3, 6}, {4, 6}},
	'B': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {1, 0}, {2, 0}, {3, 0}, {1, 3}, {2, 3}, {3, 3}, {1, 6}, {2, 6}, {3, 6}, {4, 1}, {4, 2}, {4, 4}, {4, 5}},
	'C': {{0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {1, 0}, {2, 0}, {3, 0}, {4, 1}, {1, 6}, {2, 6}, {3, 6}, {4, 5}},
	'D': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {1, 0}, {2, 0}, {3, 1}, {1, 6}, {2, 6}, {3, 5}, {4, 2}, {4, 3}, {4, 4}},
	'N': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {0, 6}, {1, 1}, {2, 2}, {2, 3}, {3, 4}, {3, 5}, {4, 0}, {4, 1}, {4, 2}, {4, 3}, {4, 4}, {4, 5}, {4, 6}},
	'G': {{0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {1, 0}, {2, 0}, {3, 0}, {4, 1}, {1, 6}, {2, 6}, {3, 6}, {4, 3}, {4, 4}, {4, 5}, {3, 3}},
	'T': {{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}, {2, 1}, {2, 2}, {2, 3}, {2, 4}, {2, 5}, {2, 6}},
	' ': {},
	':': {{2, 2}, {2, 5}},
	'1': {{1, 0}, {2, 0}, {2, 1}, {2, 2}, {2, 3}, {2, 4}, {2, 5}, {2, 6}, {1, 6}, {3, 6}},
	'2': {{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}, {4, 1}, {4, 2}, {3, 3}, {2, 3}, {1, 3}, {0, 3}, {0, 4}, {0, 5}, {1, 6}, {2, 6}, {3, 6}, {4, 6}, {0, 6}},
	'3': {{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 1}, {4, 2}, {3, 3}, {2, 3}, {4, 4}, {4, 5}, {3, 6}, {2, 6}, {1, 6}, {0, 6}},
	'5': {{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}, {0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3}, {4, 4}, {4, 5}, {3, 6}, {2, 6}, {1, 6}, {0, 6}},
	'8': {{1, 0}, {2, 0}, {3, 0}, {0, 1}, {0, 2}, {4, 1}, {4, 2}, {1, 3}, {2, 3}, {3, 3}, {0, 4}, {0, 5}, {4, 4}, {4, 5}, {1, 6}, {2, 6}, {3, 6}},
	'0': {{1, 0}, {2, 0}, {3, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}, {0, 5}, {4, 1}, {4, 2}, {4, 3}, {4, 4}, {4, 5}, {1, 6}, {2, 6}, {3, 6}},
	'4': {{0, 0}, {0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3}, {4, 3}, {4, 0}, {4, 1}, {4, 2}, {4, 4}, {4, 5}, {4, 6}},
	'6': {{1, 0}, {2, 0}, {3, 0}, {0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3}, {0, 4}, {0, 5}, {4, 4}, {4, 5}, {1, 6}, {2, 6}, {3, 6}},
	'7': {{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}, {4, 1}, {3, 2}, {3, 3}, {2, 4}, {2, 5}, {2, 6}},
	'9': {{1, 0}, {2, 0}, {3, 0}, {0, 1}, {0, 2}, {4, 1}, {4, 2}, {1, 3}, {2, 3}, {3, 3}, {4, 3}, {4, 4}, {4, 5}, {1, 6}, {2, 6}, {3, 6}},
}

// drawString renders a string using the pixel fonts at (ox, oy) with given scale.
func drawString(img *image.RGBA, text string, ox, oy, scale int, clr color.RGBA) {
	x := ox
	for _, ch := range text {
		if ch < 128 {
			drawPixelChar(img, ch, x, oy, scale, clr)
			x += 6 * scale // 5 wide + 1 gap for ASCII
		} else {
			drawPixelChar(img, ch, x, oy, scale, clr)
			x += 9 * scale // 8 wide + 1 gap for CJK
		}
	}
}

// generateChineseTextImage creates a 240x80 PNG with Chinese text "你好世界".
func generateChineseTextImage() string {
	w, h := 240, 80
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	black := color.RGBA{0, 0, 0, 255}
	drawString(img, "你好世界", 20, 20, 4, black)
	return encodeImageBase64(img)
}

// generateMixedTextImage creates a 300x100 PNG with mixed Chinese/English text.
// Content: "AI人工智能" on line 1, "BEIJING北京" on line 2.
func generateMixedTextImage() string {
	w, h := 300, 100
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	black := color.RGBA{0, 0, 0, 255}
	drawString(img, "AI人工智能", 15, 10, 3, black)
	drawString(img, "上海HELLO", 15, 50, 3, black)
	return encodeImageBase64(img)
}

// generateTableTextImage creates a 280x160 PNG with a simple table containing Chinese text.
// Table layout:
//   ┌──────┬──────┐
//   │ 中国 │  人  │
//   ├──────┼──────┤
//   │ 北京 │ 2185 │
//   │ 上海 │ 2538 │
//   └──────┴──────┘
func generateTableTextImage() string {
	w, h := 280, 160
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	black := color.RGBA{0, 0, 0, 255}
	gray := color.RGBA{180, 180, 180, 255}

	// Draw table grid
	hLines := []int{10, 50, 90, 130}
	for _, ly := range hLines {
		for x := 10; x <= 270; x++ {
			img.SetRGBA(x, ly, gray)
			img.SetRGBA(x, ly+1, gray)
		}
	}
	vLines := []int{10, 140, 270}
	for _, lx := range vLines {
		for y := 10; y <= 131; y++ {
			img.SetRGBA(lx, y, gray)
			img.SetRGBA(lx+1, y, gray)
		}
	}

	scale := 3
	// Header row: 中国 | 人
	drawString(img, "中国", 30, 18, scale, black)
	drawString(img, "人", 180, 18, scale, black)
	// Row 1: 北京 | 2185
	drawString(img, "北京", 30, 58, scale, black)
	drawString(img, "2185", 160, 58, scale, black)
	// Row 2: 上海 | 2538
	drawString(img, "上海", 30, 98, scale, black)
	drawString(img, "2538", 160, 98, scale, black)

	return encodeImageBase64(img)
}

// generateBarChartImage creates a 300x200 PNG with a bar chart.
// Chart data: A=8, B=5, C=3, D=12
func generateBarChartImage() string {
	w, h := 300, 200
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	darkGray := color.RGBA{80, 80, 80, 255}
	barColors := []color.RGBA{
		{59, 130, 246, 255},  // blue
		{34, 197, 94, 255},   // green
		{249, 115, 22, 255},  // orange
		{168, 85, 247, 255},  // purple
	}

	bars := []struct {
		label string
		value int
	}{
		{"A", 8},
		{"B", 5},
		{"C", 3},
		{"D", 12},
	}

	maxVal := 12
	chartLeft, chartBottom, chartTop := 50, 170, 20
	barWidth := 40
	gap := 15

	// Draw Y-axis
	for y := chartTop; y <= chartBottom; y++ {
		img.SetRGBA(chartLeft, y, darkGray)
	}
	// Draw X-axis
	for x := chartLeft; x < chartLeft+len(bars)*(barWidth+gap)+gap; x++ {
		img.SetRGBA(x, chartBottom, darkGray)
	}

	// Draw bars and labels
	for i, bar := range bars {
		barHeight := int(float64(bar.value) / float64(maxVal) * float64(chartBottom-chartTop))
		bx := chartLeft + gap + i*(barWidth+gap)
		by := chartBottom - barHeight
		clr := barColors[i%len(barColors)]

		for y := by; y < chartBottom; y++ {
			for x := bx; x < bx+barWidth; x++ {
				img.SetRGBA(x, y, clr)
			}
		}

		// Draw value on top of bar
		drawString(img, fmt.Sprintf("%d", bar.value), bx+10, by-14, 2, darkGray)
		// Draw label below bar
		drawPixelChar(img, rune(bar.label[0]), bx+15, chartBottom+5, 2, darkGray)
	}

	return encodeImageBase64(img)
}

// generateColorsImage creates a 200x100 PNG with 4 colored rectangles labeled by color.
func generateColorsImage() string {
	w, h := 200, 100
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	colors := []struct {
		c    color.RGBA
		x, y int
	}{
		{color.RGBA{220, 38, 38, 255}, 10, 10},   // red
		{color.RGBA{37, 99, 235, 255}, 105, 10},   // blue
		{color.RGBA{250, 204, 21, 255}, 10, 55},   // yellow
		{color.RGBA{22, 163, 74, 255}, 105, 55},   // green
	}

	for _, c := range colors {
		for y := c.y; y < c.y+35; y++ {
			for x := c.x; x < c.x+85; x++ {
				img.SetRGBA(x, y, c.c)
			}
		}
	}

	return encodeImageBase64(img)
}

// generateCountingImage creates a 200x150 PNG with multiple small circles to count.
func generateCountingImage() string {
	w, h := 200, 150
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	orange := color.RGBA{249, 115, 22, 255}

	// 7 circles at specific positions
	centers := [][2]int{{30, 30}, {80, 25}, {150, 35}, {45, 75}, {110, 80}, {170, 70}, {90, 120}}
	radius := 12

	for _, c := range centers {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx, dy := float64(x-c[0]), float64(y-c[1])
				if dx*dx+dy*dy <= float64(radius*radius) {
					img.SetRGBA(x, y, orange)
				}
			}
		}
	}

	return encodeImageBase64(img)
}

func pointInTriangle(px, py, x1, y1, x2, y2, x3, y3 float64) bool {
	d1 := (px-x2)*(y1-y2) - (x1-x2)*(py-y2)
	d2 := (px-x3)*(y2-y3) - (x2-x3)*(py-y3)
	d3 := (px-x1)*(y3-y1) - (x3-x1)*(py-y1)
	hasNeg := (d1 < 0) || (d2 < 0) || (d3 < 0)
	hasPos := (d1 > 0) || (d2 > 0) || (d3 > 0)
	return !(hasNeg && hasPos)
}

func encodeImageBase64(img image.Image) string {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// ── Vision Benchmark Dimensions ──────────────────────────────────

// benchmarkVisionDimensions defines multimodal evaluation tests.
// Only applied to models with "vision" capability.
var benchmarkVisionDimensions = []struct {
	ID     string
	Name   string
	// ImageID references a key in getTestImages()
	ImageID string
	Prompt  string
	Check   func(response string) (float64, string)
}{
	{
		ID:      "vision_shapes",
		Name:    "图形识别",
		ImageID: "shapes",
		Prompt:  "请仔细观察这张图片，描述图片中有哪些几何图形，每个图形是什么颜色？请列出所有你看到的图形和对应的颜色。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			lower := strings.ToLower(resp)
			// 红色圆形
			if (strings.Contains(lower, "圆") || strings.Contains(lower, "circle")) &&
				(strings.Contains(lower, "红") || strings.Contains(lower, "red")) {
				score += 3
			}
			// 蓝色方形
			if (strings.Contains(lower, "方") || strings.Contains(lower, "正方") || strings.Contains(lower, "矩形") || strings.Contains(lower, "square") || strings.Contains(lower, "rectangle")) &&
				(strings.Contains(lower, "蓝") || strings.Contains(lower, "blue")) {
				score += 3
			}
			// 绿色三角形
			if (strings.Contains(lower, "三角") || strings.Contains(lower, "triangle")) &&
				(strings.Contains(lower, "绿") || strings.Contains(lower, "green")) {
				score += 3
			}
			// 列举了三个形状
			shapeCount := 0
			for _, kw := range []string{"圆", "circle", "方", "square", "rectangle", "三角", "triangle"} {
				if strings.Contains(lower, kw) {
					shapeCount++
				}
			}
			if shapeCount >= 3 {
				score += 1
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("图形识别准确度: %.0f/10", score)
		},
	},
	{
		ID:      "vision_ocr",
		Name:    "英文OCR",
		ImageID: "text",
		Prompt:  "这张图片中有文字，请准确读出图片中所有的文字内容。只输出你看到的文字，不要添加其他内容。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			upper := strings.ToUpper(resp)
			if strings.Contains(upper, "HELLO") {
				score += 8
			} else {
				matched := 0
				for _, ch := range "HELLO" {
					if strings.ContainsRune(upper, ch) {
						matched++
					}
				}
				score += float64(matched) * 1.2
			}
			words := strings.Fields(resp)
			if len(words) <= 5 {
				score += 2
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("英文OCR: %.0f/10", score)
		},
	},
	{
		ID:      "vision_ocr_cn",
		Name:    "中文OCR",
		ImageID: "text_cn",
		Prompt:  "这张图片中有中文文字，请准确读出图片中所有的文字内容。只输出你看到的文字，不要添加其他内容。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			// 正确答案: "你好世界"
			if strings.Contains(resp, "你好世界") {
				score += 8
			} else {
				for _, ch := range "你好世界" {
					if strings.ContainsRune(resp, ch) {
						score += 1.5
					}
				}
			}
			// 简洁度
			if len([]rune(resp)) <= 20 {
				score += 2
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("中文OCR: %.0f/10", score)
		},
	},
	{
		ID:      "vision_ocr_mixed",
		Name:    "中英混合OCR",
		ImageID: "text_mixed",
		Prompt:  "这张图片中有中英文混合的文字，请准确读出图片中所有的文字内容。按行输出，不要添加额外内容。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			upper := strings.ToUpper(resp)
			// 第一行: "AI人工智能"
			if strings.Contains(upper, "AI") {
				score += 1.5
			}
			if strings.Contains(resp, "人工智能") {
				score += 2.5
			}
			// 第二行: "上海HELLO"
			if strings.Contains(resp, "上海") {
				score += 2
			}
			if strings.Contains(upper, "HELLO") {
				score += 2
			}
			// 简洁度
			if len([]rune(resp)) <= 40 {
				score += 2
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("中英混合OCR: %.0f/10", score)
		},
	},
	{
		ID:      "vision_ocr_table",
		Name:    "表格文字识别",
		ImageID: "text_table",
		Prompt:  "这张图片中有一个表格，请读出表格中的所有内容，包括表头和数据。按表格结构输出。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			// 表头: 中国 | 人
			if strings.Contains(resp, "中国") {
				score += 1.5
			}
			if strings.Contains(resp, "北京") {
				score += 2
			}
			if strings.Contains(resp, "上海") {
				score += 2
			}
			if strings.Contains(resp, "2185") {
				score += 1.5
			}
			if strings.Contains(resp, "2538") {
				score += 1.5
			}
			// 识别出表格结构
			if strings.Contains(resp, "表") || strings.Contains(resp, "|") ||
				strings.Contains(resp, "行") || strings.Contains(resp, "列") {
				score += 1.5
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("表格OCR: %.0f/10", score)
		},
	},
	{
		ID:      "vision_color",
		Name:    "颜色理解",
		ImageID: "colors",
		Prompt:  "这张图片中有4个彩色矩形区域。请告诉我：\n1. 每个矩形分别是什么颜色？\n2. 这4个颜色中哪个最亮？",
		Check: func(resp string) (float64, string) {
			score := 0.0
			lower := strings.ToLower(resp)
			colorFound := 0
			if strings.Contains(lower, "红") || strings.Contains(lower, "red") {
				colorFound++
			}
			if strings.Contains(lower, "蓝") || strings.Contains(lower, "blue") {
				colorFound++
			}
			if strings.Contains(lower, "黄") || strings.Contains(lower, "yellow") {
				colorFound++
			}
			if strings.Contains(lower, "绿") || strings.Contains(lower, "green") {
				colorFound++
			}
			score += float64(colorFound) * 2.0
			if strings.Contains(lower, "黄") || strings.Contains(lower, "yellow") {
				score += 2
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("颜色识别: %.0f/10", score)
		},
	},
	{
		ID:      "vision_counting",
		Name:    "视觉计数",
		ImageID: "counting",
		Prompt:  "请数一数这张图片中有多少个圆形（圆点）？给出你的计数结果和计数过程。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			if strings.Contains(resp, "7") {
				score += 7
			} else if strings.Contains(resp, "6") || strings.Contains(resp, "8") {
				score += 4
			} else if strings.Contains(resp, "5") || strings.Contains(resp, "9") {
				score += 2
			}
			if strings.Contains(resp, "从") || strings.Contains(resp, "第") || strings.Contains(resp, "分别") ||
				strings.Contains(resp, "左") || strings.Contains(resp, "右") || strings.Contains(resp, "上") ||
				strings.Contains(resp, "下") || strings.Contains(resp, "count") || strings.Contains(resp, "逐") {
				score += 3
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("视觉计数: %.0f/10", score)
		},
	},
	{
		ID:      "vision_chart",
		Name:    "图表数据读取",
		ImageID: "bar_chart",
		Prompt:  "这张图片是一个柱状图，包含4根柱子，每根柱子上方标注了数值。请读出每根柱子的标签（字母）和对应的数值。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			upper := strings.ToUpper(resp)
			// A=8, B=5, C=3, D=12
			pairs := []struct {
				label string
				value string
			}{{"A", "8"}, {"B", "5"}, {"C", "3"}, {"D", "12"}}
			for _, p := range pairs {
				if strings.Contains(upper, p.label) && strings.Contains(resp, p.value) {
					score += 2.5
				}
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("图表数据读取: %.0f/10", score)
		},
	},
	{
		ID:      "vision_scene",
		Name:    "自然场景理解",
		ImageID: "scene_natural",
		Prompt:  "请仔细观察这张图片，描述你看到的自然场景。包括：天空、地面、建筑物、植物等主要元素。",
		Check: func(resp string) (float64, string) {
			score := 0.0
			lower := strings.ToLower(resp)
			// 场景元素：天空/云、草地、房子/小屋、太阳、树
			elements := []struct {
				keywords []string
				points   float64
			}{
				{[]string{"天空", "sky", "蓝天", "蓝色"}, 2},
				{[]string{"云", "cloud", "白云"}, 1},
				{[]string{"草", "grass", "草地", "草坪", "绿地"}, 2},
				{[]string{"房", "house", "屋", "建筑", "小屋", "木屋"}, 2},
				{[]string{"太阳", "sun", "阳光"}, 1.5},
				{[]string{"树", "tree", "植物"}, 1.5},
			}
			for _, elem := range elements {
				for _, kw := range elem.keywords {
					if strings.Contains(lower, kw) {
						score += elem.points
						break
					}
				}
			}
			if score > 10 {
				score = 10
			}
			return score, fmt.Sprintf("场景理解: %.0f/10", score)
		},
	},
}

// isVisionModel checks if a model has vision capability.
// Priority: 1) cached model_meta (already enriched by ListModels)
//           2) live /api/show + InferCapabilitiesFromShowModel
func (h *APIHandler) isVisionModel(modelName string) bool {
	// 1. Check cached metadata first (fast path, already correctly detected)
	if meta := h.metaStore().GetModelMeta(modelName); meta != nil {
		for _, c := range meta.Capabilities {
			if c == "vision" {
				return true
			}
		}
		// Cache exists but no vision — trust it
		return false
	}

	// 2. Fallback: query /api/show and use the canonical detection function
	info, err := h.ollama.ShowModel(modelName)
	if err != nil {
		return false
	}
	caps, _ := service.InferCapabilitiesFromShowModel(info)
	for _, c := range caps {
		if c == "vision" {
			return true
		}
	}
	return false
}

// suppress unused import warning
var _ = math.Pi
