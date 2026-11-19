package mediastore

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"

	vpxwebp "github.com/gen2brain/vpx/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const imagePrepMaxPixels = 100_000_000

// PrepareImage decodes an image's first frame and encodes it for a vision model.
func PrepareImage(r io.Reader) ([]byte, string, error) {
	data, err := io.ReadAll(io.LimitReader(r, thumbMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	if len(data) > thumbMaxBytes {
		return nil, "", ErrMediaTooBig
	}
	var config func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	orientation := 1
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		config, decode = png.DecodeConfig, png.Decode
	case len(data) >= 2 && data[0] == 0xff && data[1] == 0xd8:
		config, decode = jpeg.DecodeConfig, jpeg.Decode
		orientation = jpegOrientation(bytes.NewReader(data))
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		config, decode = gif.DecodeConfig, gif.Decode
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		if _, animated := WebPFrameCount(bytes.NewReader(data), int64(len(data))); animated {
			config = func(r io.Reader) (image.Config, error) { return vpxwebp.DecodeConfig(r) }
			decode = func(r io.Reader) (image.Image, error) { return vpxwebp.Decode(r) }
		} else {
			config, decode = webp.DecodeConfig, webp.Decode
		}
	case len(data) >= 2 && string(data[:2]) == "BM":
		config, decode = bmp.DecodeConfig, bmp.Decode
	default:
		return nil, "", fmt.Errorf("unsupported image format")
	}
	cfg, err := config(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode image dimensions: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > imagePrepMaxPixels/int64(cfg.Height) {
		return nil, "", fmt.Errorf("image dimensions exceed pixel limit")
	}
	var img image.Image
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		img, err = decodePNGFirstFrame(data, cfg)
	} else {
		img, err = decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	// GIF frames can occupy a subrectangle of the logical screen.
	if _, ok := img.(*image.Paletted); ok && (img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height || img.Bounds().Min != (image.Point{})) {
		canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		draw.Draw(canvas, img.Bounds(), img, img.Bounds().Min, draw.Src)
		img = canvas
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	if orientation >= 2 && orientation <= 8 {
		img = orientedThumbnail{Image: img, orientation: orientation, bounds: image.Rect(0, 0, w, h)}
	}
	if max(w, h) > 1024 {
		scale := 1024 / float64(max(w, h))
		dst := image.NewNRGBA(image.Rect(0, 0, max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		img = dst
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, "", fmt.Errorf("encode image: %w", err)
	}
	return out.Bytes(), "image/png", nil
}

// APNG may have a separate default image before its first animation frame.
// Rebuild that frame as a PNG and place it on the animation canvas.
func decodePNGFirstFrame(data []byte, cfg image.Config) (image.Image, error) {
	var prefix bytes.Buffer
	prefix.Write(data[:8])
	var frame bytes.Buffer
	var ihdr []byte
	var x, y, w, h uint32
	seenIDAT, separate, haveFrame, haveData := false, false, false, false
	for pos := 8; pos+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if n > len(data)-pos-12 {
			return nil, fmt.Errorf("truncated PNG chunk")
		}
		chunk := data[pos : pos+12+n]
		kind := string(chunk[4:8])
		payload := chunk[8 : 8+n]
		if crc32.ChecksumIEEE(chunk[4:8+n]) != binary.BigEndian.Uint32(chunk[8+n:]) {
			return nil, fmt.Errorf("invalid PNG chunk checksum")
		}
		switch kind {
		case "IHDR":
			if n != 13 {
				return nil, fmt.Errorf("invalid PNG header")
			}
			ihdr = append([]byte(nil), payload...)
		case "fcTL":
			if n != 26 {
				return nil, fmt.Errorf("invalid APNG frame control")
			}
			if haveFrame {
				break
			}
			haveFrame = true
			if !seenIDAT {
				return png.Decode(bytes.NewReader(data))
			}
			separate = true
			w, h = binary.BigEndian.Uint32(payload[4:8]), binary.BigEndian.Uint32(payload[8:12])
			x, y = binary.BigEndian.Uint32(payload[12:16]), binary.BigEndian.Uint32(payload[16:20])
			if w == 0 || h == 0 || uint64(x)+uint64(w) > uint64(cfg.Width) || uint64(y)+uint64(h) > uint64(cfg.Height) || ihdr == nil {
				return nil, fmt.Errorf("invalid APNG frame dimensions")
			}
			binary.BigEndian.PutUint32(ihdr[0:4], w)
			binary.BigEndian.PutUint32(ihdr[4:8], h)
			frame.Write(data[:8])
			writePNGChunk(&frame, "IHDR", ihdr)
			frame.Write(prefix.Bytes()[8:])
		case "IDAT":
			seenIDAT = true
		case "fdAT":
			if separate && haveFrame && !haveData && n < 4 {
				return nil, fmt.Errorf("invalid APNG frame data")
			}
			if separate && haveFrame {
				if n < 4 {
					return nil, fmt.Errorf("invalid APNG frame data")
				}
				writePNGChunk(&frame, "IDAT", payload[4:])
				haveData = true
			}
		case "IEND":
			if separate && haveData {
				writePNGChunk(&frame, "IEND", nil)
				first, err := png.Decode(bytes.NewReader(frame.Bytes()))
				if err != nil {
					return nil, err
				}
				canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
				draw.Draw(canvas, image.Rect(int(x), int(y), int(x+w), int(y+h)), first, first.Bounds().Min, draw.Src)
				return canvas, nil
			}
			if separate {
				return nil, fmt.Errorf("missing APNG frame data")
			}
			return png.Decode(bytes.NewReader(data))
		default:
			if !seenIDAT && kind != "acTL" {
				prefix.Write(chunk)
			}
		}
		if kind == "fcTL" && separate && haveData {
			break
		}
		pos += 12 + n
	}
	if separate && haveData {
		writePNGChunk(&frame, "IEND", nil)
		first, err := png.Decode(bytes.NewReader(frame.Bytes()))
		if err != nil {
			return nil, err
		}
		canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		draw.Draw(canvas, image.Rect(int(x), int(y), int(x+w), int(y+h)), first, first.Bounds().Min, draw.Src)
		return canvas, nil
	}
	return nil, fmt.Errorf("incomplete PNG image")
}

func writePNGChunk(out *bytes.Buffer, kind string, payload []byte) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(len(payload)))
	out.Write(b[:])
	out.WriteString(kind)
	out.Write(payload)
	h := crc32.NewIEEE()
	h.Write([]byte(kind))
	h.Write(payload)
	binary.BigEndian.PutUint32(b[:], h.Sum32())
	out.Write(b[:])
}
