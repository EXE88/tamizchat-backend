package avatars

import (
	"bytes"
	"image"
	"io"
)

func newReader(raw []byte) io.Reader { return bytes.NewReader(raw) }

// squareAndScale crops the picture to its centre square and scales that to the
// requested side.
//
// Cropping to the centre rather than squashing: every client draws an avatar in
// a circle, and a portrait squashed into a square puts a distorted face in it.
// The middle of a photograph is where the subject almost always is.
func squareAndScale(src image.Image, side int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return image.NewRGBA(image.Rect(0, 0, side, side))
	}

	edge := min(width, height)
	x0 := bounds.Min.X + ((width - edge) / 2)
	y0 := bounds.Min.Y + ((height - edge) / 2)
	square := image.Rect(x0, y0, x0+edge, y0+edge)

	return downscale(src, square, side)
}

// downscale resizes by averaging every source pixel a destination pixel covers.
//
// The same choice as the file thumbnails, for the same reason: nearest
// neighbour is cheaper and produces the sparkle that makes a shrunken
// photograph look broken. A face at 44 pixels has no detail to spare.
//
// It is not shared with internal/files on purpose — that one is a package-level
// helper tied to a thumbnail's own bounds, and reaching across for it would
// couple permanent profile pictures to temporary room content.
func downscale(src image.Image, from image.Rectangle, side int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	edge := from.Dx()

	for y := 0; y < side; y++ {
		y0 := from.Min.Y + (y * edge / side)
		y1 := from.Min.Y + ((y + 1) * edge / side)
		if y1 <= y0 {
			y1 = y0 + 1
		}

		for x := 0; x < side; x++ {
			x0 := from.Min.X + (x * edge / side)
			x1 := from.Min.X + ((x + 1) * edge / side)
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			if n == 0 {
				continue
			}

			// RGBA() returns premultiplied 16-bit values, so what is left of the
			// alpha has to be filled in with something: JPEG has none. White,
			// not the black that falling through would give — a logo saved as a
			// transparent PNG is nearly always dark ink meant to sit on a light
			// background, and leaving it black makes it a black disc.
			const full = 0xFFFF
			clear := uint64(full) - (a / n)
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8((r/n + clear) >> 8)
			dst.Pix[i+1] = uint8((g/n + clear) >> 8)
			dst.Pix[i+2] = uint8((b/n + clear) >> 8)
			dst.Pix[i+3] = 0xFF
		}
	}

	return dst
}
