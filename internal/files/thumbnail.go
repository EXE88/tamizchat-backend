package files

import (
	"image"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	_ "image/png" // registers the PNG decoder
	"log/slog"
	"os"
)

// thumbQuality is a deliberate trade: a thumbnail is a preview, not the file.
const thumbQuality = 80

// makeThumbnail writes a downscaled JPEG preview next to the original and
// reports the source dimensions. A failure is not fatal — an image that Go
// cannot decode is simply served without a preview, so an exotic format never
// costs the user their upload.
func makeThumbnail(srcPath, dstPath string, maxPx int) (width, height int, ok bool) {
	src, err := os.Open(srcPath)
	if err != nil {
		return 0, 0, false
	}
	defer src.Close()

	img, _, err := image.Decode(src)
	if err != nil {
		slog.Debug("thumbnail skipped: undecodable image", "err", err)
		return 0, 0, false
	}

	bounds := img.Bounds()
	width, height = bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}

	tw, th := fit(width, height, maxPx)
	thumb := downscale(img, tw, th)

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return width, height, false
	}
	defer dst.Close()

	if err := jpeg.Encode(dst, thumb, &jpeg.Options{Quality: thumbQuality}); err != nil {
		slog.Debug("thumbnail encode failed", "err", err)
		removeQuietly(dstPath)
		return width, height, false
	}
	return width, height, true
}

// fit scales the dimensions down so the longest side is at most maxPx. An image
// already smaller than that is left alone.
func fit(w, h, maxPx int) (int, int) {
	if w <= maxPx && h <= maxPx {
		return w, h
	}
	if w >= h {
		return maxPx, max(1, h*maxPx/w)
	}
	return max(1, w*maxPx/h), maxPx
}

// downscale resizes by averaging each destination pixel over the source area it
// covers. That is more work than picking one source pixel, but nearest-neighbour
// downscaling of a photo produces the sparkling artefacts that make a preview
// look broken.
func downscale(src image.Image, w, h int) image.Image {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		y0 := bounds.Min.Y + y*sh/h
		y1 := bounds.Min.Y + (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}

		for x := 0; x < w; x++ {
			x0 := bounds.Min.X + x*sw/w
			x1 := bounds.Min.X + (x+1)*sw/w
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

			i := dst.PixOffset(x, y)
			// RGBA() returns 16-bit values; the destination wants 8-bit.
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(b / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}
