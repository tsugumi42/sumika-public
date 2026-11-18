package mediastore

// 磁盘缩略图是派生数据；授权和源文件打开由调用者先完成（规格 5.2、ADR-260）。
import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"sumika-native/internal/fileatomic"
)

const thumbMaxBytes = 20 << 20

// Thumbnail 只处理受支持的静态大图；nil 字节表示沿用原文件及界面原有的解码错误状态。
// source 必须是本 Reader.Open 返回的已授权源句柄，不能凭缓存名跳过源文件存在与路径检查。
func (r *Reader) Thumbnail(ctx context.Context, source *os.File, platform, hash string, tier int) ([]byte, error) {
	if tier == 0 {
		return nil, nil
	}
	name := hash + ".v1." + strconv.Itoa(tier) + ".png"
	parts := []string{"go", "media-thumbs", platform, mediaShard(hash), name}
	cached, err := r.readThumb(parts, tier)
	if err != nil || cached != nil {
		return cached, err
	}
	// 同分片的 miss 才串行；命中不排队，二次检查避免同时生成同一张。
	mu := &r.thumbs[(int(hash[0])*16+int(hash[1]))%len(r.thumbs)]
	mu.Lock()
	defer mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cached, err = r.readThumb(parts, tier)
	if err != nil || cached != nil {
		return cached, err
	}
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > thumbMaxBytes {
		return nil, ErrMediaTooBig
	}
	cfg, decode, animated := thumbDecoder(source, info.Size())
	if decode == nil || animated || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 100e6 {
		return nil, nil
	}
	orientation := 1
	var signature [2]byte
	if _, err := source.ReadAt(signature[:], 0); err != nil {
		return nil, err
	}
	if signature == [2]byte{255, 216} && max(cfg.Width, cfg.Height) <= 2048 {
		orientation = jpegOrientation(source)
	}
	w, h := cfg.Width, cfg.Height
	if orientation >= 5 {
		w, h = h, w
	}
	scale := min(1, float64(tier)/float64(w), float64(4096)/float64(h))
	if scale == 1 {
		return nil, nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, err := decode(source)
	if err != nil {
		return nil, nil
	} // 坏原件交给界面报 imgBad，不把它当成缓存写入失败。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if orientation > 1 {
		img = orientedThumbnail{Image: img, orientation: orientation, bounds: image.Rect(0, 0, w, h)}
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&encoded, dst); err != nil {
		return nil, err
	}
	if encoded.Len() > thumbMaxBytes {
		return nil, ErrMediaTooBig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 缩略图是派生数据：缓存文件被别的程序占着、改名等不到时，这次不留缓存，生成好的照样返回，下次再写。
	// 联接、目录检查失败这类不是"暂时占用"的错误照旧报出去。
	if err := r.writeThumb(ctx, parts, encoded.Bytes()); err != nil && !fileatomic.Transient(err) {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func (r *Reader) readThumb(parts []string, tier int) ([]byte, error) {
	f, err := openCheckedFile(r.root, parts, nil)
	if errors.Is(err, ErrMediaMissing) || fileatomic.Transient(err) {
		return nil, nil // 缓存别的程序暂时占着：和没有缓存一样，照源图现生成
	}
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, thumbMaxBytes+1))
	err = errors.Join(err, f.Close())
	if fileatomic.Transient(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > thumbMaxBytes || !validThumbPNG(b, tier) {
		return nil, nil
	}
	return b, nil
}

// PNG 的压缩块 CRC 可发现截断与位翻转，不必为每次命中再分配、解码一份像素。
func validThumbPNG(b []byte, tier int) bool {
	if len(b) < 8 || string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		return false
	}
	first, data := true, false
	for rest := b[8:]; len(rest) >= 12; {
		n := int64(binary.BigEndian.Uint32(rest[:4]))
		if n > int64(len(rest)-12) {
			return false
		}
		chunk := rest[4 : 8+int(n)]
		if crc32.ChecksumIEEE(chunk) != binary.BigEndian.Uint32(rest[8+int(n):12+int(n)]) {
			return false
		}
		kind := string(chunk[:4])
		if first {
			if kind != "IHDR" || n != 13 {
				return false
			}
			w, h := binary.BigEndian.Uint32(chunk[4:8]), binary.BigEndian.Uint32(chunk[8:12])
			if w == 0 || w > uint32(tier) || h == 0 || h > 4096 {
				return false
			}
			first = false
		}
		if kind == "IDAT" {
			data = true
		}
		rest = rest[12+int(n):]
		if kind == "IEND" {
			return n == 0 && data && len(rest) == 0
		}
	}
	return false
}

func (r *Reader) writeThumb(ctx context.Context, parts []string, b []byte) error {
	dirs := parts[:len(parts)-1]
	if err := ensureMediaDir(r.root, dirs...); err != nil {
		return err
	}
	dir := filepath.Join(append([]string{r.root}, dirs...)...)
	prefix := parts[len(parts)-1] + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".partial") {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && info.ModTime().Before(time.Now().Add(-24*time.Hour)) {
				if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	tmp, err := os.CreateTemp(dir, prefix+"*.partial")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := checkMediaHandle(tmp, tmp.Name()); err != nil {
		tmp.Close()
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureMediaDir(r.root, dirs...); err != nil {
		return err
	}
	// 目标若是链接必须报错，不能用删除目标绕过保护。
	old, err := openCheckedFile(r.root, parts, nil)
	if err == nil {
		err = old.Close()
	} else if errors.Is(err, ErrMediaMissing) {
		err = nil
	}
	if err != nil {
		return err
	}
	return fileatomic.Replace(tmp.Name(), filepath.Join(dir, parts[len(parts)-1]), 2*time.Second)
}

// 显式分派，避免主程序导入 pngn/vpx 后 image.Decode 注册顺序改变静态解码器。
func thumbDecoder(f *os.File, size int64) (image.Config, func(io.Reader) (image.Image, error), bool) {
	var head [12]byte
	if _, err := f.ReadAt(head[:], 0); err != nil {
		return image.Config{}, nil, false
	}
	var config func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	animated := false
	switch {
	case string(head[:8]) == "\x89PNG\r\n\x1a\n":
		config, decode = png.DecodeConfig, png.Decode
		var chunk [8]byte
		for at := int64(8); at+8 <= size; {
			if _, err := f.ReadAt(chunk[:], at); err != nil {
				break
			}
			if string(chunk[4:]) == "acTL" {
				animated = true
				break
			}
			if string(chunk[4:]) == "IDAT" {
				break
			}
			at += 12 + int64(binary.BigEndian.Uint32(chunk[:4]))
		}
	case head[0] == 255 && head[1] == 216:
		config, decode = jpeg.DecodeConfig, jpeg.Decode
	case string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a":
		config, decode = gif.DecodeConfig, gif.Decode
		animated = GIFFrameCount(f) > 1
	case string(head[:4]) == "RIFF" && string(head[8:]) == "WEBP":
		config, decode = webp.DecodeConfig, webp.Decode
		_, animated = WebPFrameCount(f, size)
	case string(head[:2]) == "BM":
		config, decode = bmp.DecodeConfig, bmp.Decode
	default:
		return image.Config{}, nil, false
	}
	cfg, err := config(io.NewSectionReader(f, 0, size))
	if err != nil {
		return image.Config{}, nil, animated
	}
	return cfg, decode, animated
}

type orientedThumbnail struct {
	image.Image
	orientation int
	bounds      image.Rectangle
}

func (o orientedThumbnail) Bounds() image.Rectangle { return o.bounds }
func (o orientedThumbnail) At(x, y int) color.Color {
	w, h := o.Image.Bounds().Dx(), o.Image.Bounds().Dy()
	switch o.orientation {
	case 2:
		x = w - 1 - x
	case 3:
		x, y = w-1-x, h-1-y
	case 4:
		y = h - 1 - y
	case 5:
		x, y = y, x
	case 6:
		x, y = y, h-1-x
	case 7:
		x, y = w-1-y, h-1-x
	case 8:
		x, y = w-1-y, x
	}
	return o.Image.At(x+o.Image.Bounds().Min.X, y+o.Image.Bounds().Min.Y)
}
