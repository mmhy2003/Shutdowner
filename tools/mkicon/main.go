// Command mkicon renders logo.png into the multi-resolution .ico files the app
// is branded with: assets/icon.ico, which is linked into shutdowner.exe as its
// application icon, and internal/web/static/favicon.ico, which the web UI
// serves. They differ only in which sizes and which entry encoding they carry.
//
// Every output is committed, so this only needs re-running when the logo
// changes:
//
//	make icon
//
// Two crops of the logo are used. The full mark — monitor, power symbol and
// wireless arcs — is 2.4 times wider than it is tall, so once it is letterboxed
// into a square icon and scaled to 16 or 24 pixels the monitor collapses into a
// blue smudge. Below -small-max the icon therefore switches to the power symbol
// and arcs alone, which is close to square and stays legible in the title bar
// and the taskbar. Windows picks whichever size the surface asks for. The
// wordmark is left out of both: at the sizes Windows actually renders an
// executable icon it is never readable.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var (
	flagIn       = flag.String("in", "logo.png", "source PNG")
	flagOut      = flag.String("out", filepath.Join("assets", "icon.ico"), "destination .ico")
	flagFull     = flag.String("full", "0,150,700,470", "x0,y0,x1,y1 of the artwork used above -small-max")
	flagSmall    = flag.String("small", "383,150,700,470", "x0,y0,x1,y1 of the artwork used at -small-max and below")
	flagSmallMax = flag.Int("small-max", 32, "largest icon size that uses the -small artwork")
	flagPad      = flag.Float64("pad", 0.02, "margin left around the artwork, as a fraction of the icon side")
	flagWidth    = flag.Int("width", 0, "width of a .png output; 0 keeps the cropped artwork's own width")

	// The default is what Windows asks for: 16 in the title bar and tree views,
	// 32 on the desktop, 48 in Explorer's medium view, 256 for the extra large
	// view and the Alt-Tab switcher, plus the DPI-scaled variants of those.
	flagSizes = flag.String("sizes", "16,20,24,32,40,48,64,128,256", "comma-separated icon sizes to write")

	// The shell asks for the small sizes constantly and reads them from disk
	// each time, so they are left uncompressed; only the two large entries,
	// where the saving is worth it, are stored as PNG. A favicon is fetched once
	// and cached by the browser, and every browser that has shipped this decade
	// reads PNG entries, so it passes 0 here and comes out a quarter of the size.
	flagDIBMax = flag.Int("dib-max", 64, "largest size stored as an uncompressed DIB; larger ones are stored as PNG")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		os.Exit(1)
	}
}

func run() error {
	src, err := readPNG(*flagIn)
	if err != nil {
		return err
	}
	// Trimming to the opaque bounding box means the crop flags only have to be
	// roughly right: they select which part of the logo to keep, and the trim
	// takes care of centring it and filling the canvas.
	full, err := cropFlag("-full", *flagFull, src)
	if err != nil {
		return err
	}

	var data []byte
	var summary string
	switch ext := strings.ToLower(filepath.Ext(*flagOut)); ext {
	case ".ico":
		data, summary, err = buildICO(src, full)
	case ".png":
		data, summary, err = buildPNG(src, full)
	default:
		err = fmt.Errorf("-out: %q is neither .ico nor .png", ext)
	}
	if err != nil {
		return err
	}

	if dir := filepath.Dir(*flagOut); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*flagOut, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %s, %d bytes\n", *flagOut, summary, len(data))
	return nil
}

// cropFlag turns one of the rectangle flags into the artwork it selects.
func cropFlag(name, value string, src image.Image) (image.Rectangle, error) {
	r, err := parseRect(value)
	if err != nil {
		return r, fmt.Errorf("%s: %w", name, err)
	}
	r, err = trim(src, r.Intersect(src.Bounds()))
	if err != nil {
		return r, fmt.Errorf("%s: %w", name, err)
	}
	return r, nil
}

func buildICO(src image.Image, full image.Rectangle) ([]byte, string, error) {
	sizes, err := parseSizes(*flagSizes)
	if err != nil {
		return nil, "", fmt.Errorf("-sizes: %w", err)
	}
	small, err := cropFlag("-small", *flagSmall, src)
	if err != nil {
		return nil, "", err
	}

	images := make([]*image.NRGBA, len(sizes))
	for i, size := range sizes {
		art := full
		if size <= *flagSmallMax {
			art = small
		}
		images[i] = renderIcon(src, art, size, *flagPad)
	}
	data, err := encodeICO(images, *flagDIBMax)
	if err != nil {
		return nil, "", err
	}
	return data, fmt.Sprintf("%d entries (%v)", len(sizes), sizes), nil
}

// buildPNG writes the -full artwork at its own aspect ratio rather than
// letterboxed into a square, because the page that displays it lays it out as a
// picture and not as an icon.
func buildPNG(src image.Image, full image.Rectangle) ([]byte, string, error) {
	w := *flagWidth
	if w <= 0 {
		w = full.Dx()
	}
	h := max(int(float64(w)*float64(full.Dy())/float64(full.Dx())+0.5), 1)
	data, err := encodePNG(resample(src, full, w, h))
	if err != nil {
		return nil, "", err
	}
	return data, fmt.Sprintf("%dx%d", w, h), nil
}

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// parseSizes reads a comma-separated size list, sorted ascending so that the
// directory entries come out in a predictable order. The one-byte width field
// in an .ico directory entry is what caps a size at 256.
func parseSizes(s string) ([]int, error) {
	var sizes []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", p)
		}
		if n < 1 || n > 256 {
			return nil, fmt.Errorf("%d is outside 1-256", n)
		}
		sizes = append(sizes, n)
	}
	if len(sizes) == 0 {
		return nil, errors.New("no sizes given")
	}
	slices.Sort(sizes)
	return slices.Compact(sizes), nil
}

// parseRect reads an "x0,y0,x1,y1" flag value as a half-open rectangle.
func parseRect(s string) (image.Rectangle, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("want x0,y0,x1,y1, got %q", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("%q is not a number", p)
		}
		v[i] = n
	}
	r := image.Rect(v[0], v[1], v[2], v[3])
	if r.Empty() {
		return image.Rectangle{}, fmt.Errorf("%q is empty", s)
	}
	return r, nil
}

// alphaFloor is the alpha above which a pixel counts as artwork rather than
// background. The logo's edges are antialiased into full transparency, so a
// strict "any alpha at all" test would trim to a halo several pixels wider than
// the shape.
const alphaFloor = 0x2000

// trim shrinks r to the bounding box of the artwork inside it.
//
// The bounds are tracked as plain ints rather than as a Rectangle seeded
// inside out, because image.Rect canonicalises its arguments: it would swap the
// inverted corners back the right way round and hand back a box that already
// covers everything, which no min or max could then shrink.
func trim(img image.Image, r image.Rectangle) (image.Rectangle, error) {
	minX, minY := r.Max.X, r.Max.Y
	maxX, maxY := r.Min.X, r.Min.Y
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > alphaFloor {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x+1), max(maxY, y+1)
			}
		}
	}
	if minX >= maxX || minY >= maxY {
		return image.Rectangle{}, fmt.Errorf("%v holds no opaque pixels", r)
	}
	return image.Rect(minX, minY, maxX, maxY), nil
}

// renderIcon scales src[art] to fit a size x size canvas, preserving the aspect
// ratio and centring the result, with pad of the side left as margin.
func renderIcon(src image.Image, art image.Rectangle, size int, pad float64) *image.NRGBA {
	side := float64(size) * (1 - 2*pad)
	sw, sh := float64(art.Dx()), float64(art.Dy())
	scale := min(side/sw, side/sh)
	dw, dh := max(int(sw*scale+0.5), 1), max(int(sh*scale+0.5), 1)

	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	at := image.Pt((size-dw)/2, (size-dh)/2)
	draw.Draw(dst, image.Rectangle{Min: at, Max: at.Add(image.Pt(dw, dh))},
		resample(src, art, dw, dh), image.Point{}, draw.Src)
	return dst
}

// resample scales src[art] to exactly dw x dh.
//
// The filter is a plain box average over the source pixels each destination
// pixel covers. Every reduction here is by a factor of three or more, which is
// the range where a box filter is both the cheapest and the least prone to the
// ringing a sharper kernel would introduce along the logo's hard colour edges.
// Averaging happens in premultiplied space so that transparent pixels along the
// artwork's edge do not drag their (undefined) colour into the result.
func resample(src image.Image, art image.Rectangle, dw, dh int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	sw, sh := float64(art.Dx()), float64(art.Dy())

	for dy := 0; dy < dh; dy++ {
		y0 := art.Min.Y + int(float64(dy)*sh/float64(dh))
		y1 := max(art.Min.Y+int(float64(dy+1)*sh/float64(dh)), y0+1)
		for dx := 0; dx < dw; dx++ {
			x0 := art.Min.X + int(float64(dx)*sw/float64(dw))
			x1 := max(art.Min.X+int(float64(dx+1)*sw/float64(dw)), x0+1)

			var sumR, sumG, sumB, sumA, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, b, a := src.At(x, y).RGBA()
					sumR, sumG, sumB, sumA, n = sumR+uint64(r), sumG+uint64(g), sumB+uint64(b), sumA+uint64(a), n+1
				}
			}
			avg := color.RGBA64{
				R: uint16(sumR / n), G: uint16(sumG / n),
				B: uint16(sumB / n), A: uint16(sumA / n),
			}
			dst.SetNRGBA(dx, dy, color.NRGBAModel.Convert(avg).(color.NRGBA))
		}
	}
	return dst
}

type iconDirEntry struct {
	Width       byte // 0 means 256; the field is one byte wide
	Height      byte
	ColorCount  byte // 0 at more than 8 bits per pixel
	Reserved    byte
	Planes      uint16
	BitCount    uint16
	BytesInRes  uint32
	ImageOffset uint32
}

// encodeICO packs the rendered images into an .ico file, storing entries up to
// dibMax pixels as uncompressed DIBs and the rest as PNG. The images must
// already be in the order they should appear in the directory.
func encodeICO(images []*image.NRGBA, dibMax int) ([]byte, error) {
	blobs := make([][]byte, len(images))
	for i, img := range images {
		var err error
		if img.Bounds().Dx() > dibMax {
			blobs[i], err = encodePNG(img)
		} else {
			blobs[i] = encodeDIB(img)
		}
		if err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	// ICONDIR: reserved, type 1 = icon, image count.
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	offset := uint32(6 + 16*len(images))
	for i, img := range images {
		side := img.Bounds().Dx()
		entry := iconDirEntry{
			Width:       byte(side),
			Height:      byte(side),
			Planes:      1,
			BitCount:    32,
			BytesInRes:  uint32(len(blobs[i])),
			ImageOffset: offset,
		}
		if side >= 256 {
			entry.Width, entry.Height = 0, 0
		}
		binary.Write(&buf, binary.LittleEndian, entry)
		offset += uint32(len(blobs[i]))
	}
	for _, b := range blobs {
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

func encodePNG(img *image.NRGBA) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32 // The XOR and AND masks stacked, so twice the icon's height
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// encodeDIB writes the bottom-up 32bpp BGRA bitmap an .ico entry expects,
// followed by the 1bpp AND mask. The alpha channel is what a modern shell
// composites with; the mask is only consulted by code paths that ignore alpha,
// where the best it can do is describe the silhouette.
func encodeDIB(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, bitmapInfoHeader{
		Size: 40, Width: int32(w), Height: int32(h * 2), Planes: 1, BitCount: 32,
	})
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	// Mask rows are padded to a four-byte boundary, and a set bit means the
	// pixel is background.
	row := make([]byte, ((w+31)/32)*4)
	for y := h - 1; y >= 0; y-- {
		clear(row)
		for x := 0; x < w; x++ {
			if img.NRGBAAt(x, y).A < 128 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		buf.Write(row)
	}
	return buf.Bytes()
}
