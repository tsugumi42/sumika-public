package linkexpand

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// ugoira 不调用全帧 EncodeAll；标准库单帧编码后接本地色表，内存只留当前帧。
func (m *MediaManager) Ugoira(ctx context.Context, p *linkreader.UgoiraInfo) (loaded Loaded, err error) {
	if e := m.acquire(ctx); e != nil {
		return Loaded{}, e
	}
	defer m.done()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if p != nil && (p.ZIP.State != "available" || p.Frames.State != "available") {
		// 取数阶段就没拿到 ZIP 或帧表（匿名被拒、不存在……）：把原因说出来，不当成容器坏了
		if reason := sourceReason(p.ZIP.Reason, p.Frames.Reason); reason != "" {
			return Loaded{}, &SourceError{Reason: reason}
		}
	}
	if p == nil || p.ZIP.State != "available" || p.Frames.State != "available" || len(p.Frames.Value) == 0 || len(p.Frames.Value) > 300 {
		return Loaded{}, ErrContainer
	}
	dir, e := m.job()
	if e != nil {
		return Loaded{}, e
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	source := linkreader.Media{URL: p.ZIP}
	budget := &linkreader.ByteBudget{Limit: 100_000_000}
	file := filepath.Join(dir, "frames.zip")
	if e = m.download(ctx, "pixiv", source, file, budget); e != nil {
		return Loaded{}, e
	}
	z, e := zip.OpenReader(file)
	if e != nil {
		return Loaded{}, ErrContainer
	}
	defer z.Close()
	files := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !fs.ValidPath(name) || path.IsAbs(name) || strings.ContainsAny(name, "\\:") || f.Mode()&fs.ModeSymlink != 0 {
			return Loaded{}, ErrContainer
		}
		if _, ok := files[f.Name]; ok {
			return Loaded{}, ErrContainer
		}
		files[f.Name] = f
	}
	frames := p.Frames.Value
	var totalMS int64
	var totalSize uint64
	seen := map[string]bool{}
	starts := make([]int64, len(frames))
	for i, f := range frames {
		entry := files[f.File]
		if entry == nil || entry.FileInfo().IsDir() || seen[f.File] || f.DelayMS <= 0 || int64(f.DelayMS) > math.MaxInt64-totalMS {
			return Loaded{}, ErrContainer
		}
		seen[f.File] = true
		starts[i] = totalMS
		totalMS += int64(f.DelayMS)
		if entry.UncompressedSize64 > 100_000_000-totalSize {
			return Loaded{}, linkreader.ErrMediaLimit
		}
		totalSize += entry.UncompressedSize64
	}
	// 时间先采样再累计舍入；丢帧降低帧率，不截掉动画尾部或改变总时长。
	count := len(frames)
	if totalMS < 20_000 {
		count = min(count, max(1, int(totalMS*15/1000)))
	}
	type sample struct{ index, delay int }
	samples := make([]sample, 0, count)
	index := 0
	for i := range count {
		target := int64(i)*(totalMS/int64(count)) + int64(i)*(totalMS%int64(count))/int64(count)
		for index+1 < len(frames) && starts[index+1] <= target {
			index++
		}
		end := int64(math.Round(float64(i+1) * float64(totalMS) / float64(count) / 10))
		begin := int64(math.Round(float64(i) * float64(totalMS) / float64(count) / 10))
		delay := end - begin
		if delay < 1 || delay > 65535 {
			return Loaded{}, ErrContainer
		}
		if len(samples) > 0 && samples[len(samples)-1].index == index {
			samples[len(samples)-1].delay += int(delay)
			if samples[len(samples)-1].delay > 65535 {
				return Loaded{}, ErrContainer
			}
		} else {
			samples = append(samples, sample{index, int(delay)})
		}
	}
	out := limitedGIF{max: imageLimit}
	width, height := 0, 0
	for _, s := range samples {
		if e = ctx.Err(); e != nil {
			return Loaded{}, e
		}
		entry := files[frames[s.index].File]
		rc, e := entry.Open()
		if e != nil {
			return Loaded{}, ErrContainer
		}
		raw, e := io.ReadAll(io.LimitReader(contextReader{ctx, rc}, 100_000_001))
		closeErr := rc.Close()
		if e != nil || closeErr != nil {
			return Loaded{}, errors.Join(e, closeErr)
		}
		if len(raw) > 100_000_000 {
			return Loaded{}, linkreader.ErrMediaLimit
		}
		cfg, _, e := image.DecodeConfig(bytes.NewReader(raw))
		if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			return Loaded{}, ErrContainer
		}
		img, _, e := image.Decode(bytes.NewReader(raw))
		if e != nil {
			return Loaded{}, ErrContainer
		}
		w, h := cfg.Width, cfg.Height
		if max(w, h) > 720 {
			ratio := 720 / float64(max(w, h))
			w, h = max(1, int(float64(w)*ratio)), max(1, int(float64(h)*ratio))
		}
		if width == 0 {
			width, height = w, h
			header := []byte("GIF89a\x00\x00\x00\x00\x70\x00\x00")
			binary.LittleEndian.PutUint16(header[6:8], uint16(w))
			binary.LittleEndian.PutUint16(header[8:10], uint16(h))
			out.Write(header)
			out.Write([]byte("\x21\xff\x0bNETSCAPE2.0\x03\x01\x00\x00\x00"))
		} else if w != width || h != height {
			return Loaded{}, ErrContainer
		}
		resized := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.ApproxBiLinear.Scale(resized, resized.Bounds(), img, img.Bounds(), xdraw.Src, nil)
		var one bytes.Buffer
		if e = gif.Encode(&one, resized, nil); e != nil {
			return Loaded{}, e
		}
		b := one.Bytes()
		if len(b) < 14 || b[10]&0x80 == 0 {
			return Loaded{}, ErrContainer
		}
		paletteSize := 3 * (1 << ((b[10] & 7) + 1))
		at := 13 + paletteSize
		// 单帧编码可能含透明度控制段；组合动画统一使用自己的延时和 disposal。
		for at < len(b) && b[at] == 0x21 {
			at += 2
			for at < len(b) {
				n := int(b[at])
				at++
				if n == 0 {
					break
				}
				at += n
			}
		}
		if at+10 >= len(b) || b[at] != 0x2c {
			return Loaded{}, ErrContainer
		}
		control := []byte{0x21, 0xf9, 4, 4, byte(s.delay), byte(s.delay >> 8), 0, 0}
		descriptor := append([]byte(nil), b[at:at+10]...)
		descriptor[9] = (descriptor[9] & 0x40) | 0x80 | (b[10] & 7)
		for _, piece := range [][]byte{control, descriptor, b[13 : 13+paletteSize], b[at+10 : len(b)-1]} {
			if _, e = out.Write(piece); e != nil {
				return Loaded{}, e
			}
		}
	}
	if _, e = out.Write([]byte{0x3b}); e != nil {
		return Loaded{}, e
	}
	return Loaded{Resource: types.MemoryMedia(rand.Text(), "image/gif", width, height, out.Bytes()), Kind: types.BodyGIF, Received: budget.Received}, nil
}

type limitedGIF struct {
	bytes.Buffer
	max int64
}

func (b *limitedGIF) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.max {
		return 0, linkreader.ErrMediaLimit
	}
	return b.Buffer.Write(p)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
