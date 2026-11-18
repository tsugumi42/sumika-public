package linkexpand

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/toolchain"
)

const imageLimit int64 = 20_000_000

// MediaManager 不持有协议端。一个实例最多两个媒体任务，文件只进自己的实例目录。
type MediaManager struct {
	fetch          *linkreader.Fetcher
	root, instance string
	slots          chan struct{}
}

func NewMedia(fetch *linkreader.Fetcher, dataDir string) *MediaManager {
	return &MediaManager{fetch: fetch, root: filepath.Join(dataDir, "go", "tmp", "link-expand"), instance: rand.Text(), slots: make(chan struct{}, 2)}
}
func (m *MediaManager) acquire(ctx context.Context) error {
	select {
	case m.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *MediaManager) done() { <-m.slots }

// Sweep 只能由已持有后台独占运行锁的宿主调用；不跟随目录中的符号链接。
func (m *MediaManager) Sweep(now time.Time) error {
	entries, e := os.ReadDir(m.root)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var out error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() == m.instance {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			out = errors.Join(out, e)
			continue
		}
		if now.Sub(info.ModTime()) > time.Hour {
			out = errors.Join(out, os.RemoveAll(filepath.Join(m.root, entry.Name())))
		}
	}
	return out
}
func (m *MediaManager) job() (string, error) {
	p := filepath.Join(m.root, m.instance, rand.Text())
	return p, os.MkdirAll(p, 0700)
}
func (m *MediaManager) Image(ctx context.Context, platform string, src linkreader.Media) (Loaded, error) {
	if e := m.acquire(ctx); e != nil {
		return Loaded{}, e
	}
	defer m.done()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if src.URL.State != "available" {
		return Loaded{}, ErrPlayback
	}
	raw, received, e := m.read(ctx, platform, src.URL.Value, imageLimit, src.Size)
	note := ""
	// 原图超限有两种情形：响应头里的 Content-Length 就超了（一个字节都没收，received 是 0，pixiv 的真实响应都是这样），
	// 或没有长度、流到一半才超。两种都改取 regular；以前只认后一种，真实的 24 MB 原图根本不会退档。
	if errors.Is(e, linkreader.ErrMediaLimit) && platform == "pixiv" && src.Fallback != nil && src.Fallback.URL.State == "available" && src.Fallback.URL.Value != "" {
		originalReceived := received
		raw, received, e = m.read(ctx, platform, src.Fallback.URL.Value, imageLimit, linkreader.Field[int64]{})
		received += originalReceived
		note = "原图超过20 MB，使用regular"
	}
	if e != nil {
		return Loaded{}, e
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return Loaded{}, ErrContainer
	}
	mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp"}[format]
	if mime == "" {
		return Loaded{}, ErrContainer
	}
	kind := types.BodyImage
	if format == "gif" {
		kind = types.BodyGIF
	}
	return Loaded{Resource: types.MemoryMedia(rand.Text(), mime, cfg.Width, cfg.Height, raw), Kind: kind, Note: note, Received: received}, nil
}
func (m *MediaManager) read(ctx context.Context, platform, raw string, limit int64, size linkreader.Field[int64]) ([]byte, int64, error) {
	if size.State == "available" && size.Value > limit {
		return nil, 0, linkreader.ErrMediaLimit
	}
	var b bytes.Buffer
	budget := &linkreader.ByteBudget{Limit: limit}
	e := m.fetch.Stream(ctx, platform, raw, &b, budget)
	return b.Bytes(), budget.Received, e
}
func (m *MediaManager) download(ctx context.Context, platform string, src linkreader.Media, path string, budget *linkreader.ByteBudget) error {
	if src.URL.State != "available" || src.URL.Value == "" {
		return ErrPlayback
	}
	if src.Size.State == "available" && src.Size.Value > budget.Remaining() {
		return linkreader.ErrMediaLimit
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = m.fetch.Stream(ctx, platform, src.URL.Value, f, budget)
	return errors.Join(e, f.Close())
}
func (m *MediaManager) Video(ctx context.Context, platform string, r *linkreader.Result, src linkreader.Media, limits VideoLimits, toGIF bool) (loaded Loaded, err error) {
	if !limits.Enabled {
		return Loaded{}, ErrVideoDisabled
	}
	if e := m.acquire(ctx); e != nil {
		return Loaded{}, e
	}
	defer m.done()
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	budget := &linkreader.ByteBudget{Limit: limits.Bytes}
	playback := linkreader.Playback{}
	if platform == "bilibili" && src.URL.State != "available" {
		playback, err = m.fetch.BiliPlayback(ctx, r)
		if err != nil {
			return Loaded{}, err
		}
	} else {
		// 候选必须是取数层给出的 MP4；清单不交给外部程序联网。
		if src.URL.State != "available" {
			for _, v := range src.Variants.Value {
				if v.Container == "video/mp4" || v.Container == "mp4" {
					src.URL, src.Size = v.URL, v.Size
					break
				}
			}
		}
		playback.Video = []linkreader.Media{src}
	}
	dir, e := m.job()
	if e != nil {
		return Loaded{}, e
	}
	owned := false
	defer func() {
		if !owned {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	var videoPaths, audioPaths []string
	for group, tracks := range [][]linkreader.Media{playback.Video, playback.Audio} {
		for i, t := range tracks {
			p := filepath.Join(dir, fmt.Sprintf("track-%d-%d.mp4", group, i))
			if e = m.download(ctx, platform, t, p, budget); e != nil {
				return Loaded{}, e
			}
			if e = mp4(p); e != nil {
				return Loaded{}, e
			}
			if group == 0 {
				videoPaths = append(videoPaths, p)
			} else {
				audioPaths = append(audioPaths, p)
			}
		}
	}
	if len(videoPaths) == 0 {
		return Loaded{}, ErrPlayback
	}
	output := videoPaths[0]
	if len(videoPaths) > 1 || len(audioPaths) > 0 {
		args := []string{"-v", "error", "-nostdin", "-threads", "1", "-y"}
		if len(videoPaths) > 1 {
			var list strings.Builder
			for _, p := range videoPaths {
				fmt.Fprintf(&list, "file '%s'\n", filepath.Base(p))
			}
			manifest := filepath.Join(dir, "concat.txt")
			if e = os.WriteFile(manifest, []byte(list.String()), 0600); e != nil {
				return Loaded{}, e
			}
			args = append(args, "-protocol_whitelist", "file,pipe", "-f", "concat", "-safe", "1", "-i", manifest)
		} else {
			args = append(args, movInput(output)...)
		}
		if len(audioPaths) > 0 {
			args = append(args, movInput(audioPaths[0])...)
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
		}
		output = filepath.Join(dir, "combined.mp4")
		args = append(args, "-c", "copy", output)
		if _, e = m.command(ctx, "ffmpeg", args...); e != nil {
			return Loaded{}, e
		}
	}
	if e = m.probe(ctx, output); e != nil {
		return Loaded{}, e
	}
	if toGIF {
		gifPath := filepath.Join(dir, "animation.gif")
		args := []string{"-v", "error", "-nostdin", "-threads", "1", "-filter_complex_threads", "1", "-y"}
		args = append(args, movInput(output)...)
		args = append(args, "-filter_complex", "fps=15,scale=w='min(720,iw)':h='min(720,ih)':force_original_aspect_ratio=decrease,split[a][b];[a]palettegen=stats_mode=single[p];[b][p]paletteuse=new=1", "-loop", "0", gifPath)
		if _, e = m.command(ctx, "ffmpeg", args...); e != nil {
			return Loaded{}, e
		}
		st, e := os.Stat(gifPath)
		if e != nil {
			return Loaded{}, e
		}
		if st.Size() > imageLimit {
			return Loaded{}, linkreader.ErrMediaLimit
		}
		raw, e := os.ReadFile(gifPath)
		if e != nil {
			return Loaded{}, e
		}
		cfg, _, e := image.DecodeConfig(bytes.NewReader(raw))
		if e != nil {
			return Loaded{}, ErrContainer
		}
		return Loaded{Resource: types.MemoryMedia(rand.Text(), "image/gif", cfg.Width, cfg.Height, raw), Kind: types.BodyGIF, Received: budget.Received}, nil
	}
	st, e := os.Stat(output)
	if e != nil {
		return Loaded{}, e
	}
	if st.Size() > limits.Bytes {
		return Loaded{}, linkreader.ErrMediaLimit
	}
	f, e := os.Open(output)
	if e != nil {
		return Loaded{}, e
	}
	resource, e := types.FileMedia(rand.Text(), "video/mp4", f, func() error { return os.RemoveAll(dir) })
	if e != nil {
		f.Close()
		return Loaded{}, e
	}
	owned = true
	return Loaded{Resource: resource, Kind: types.BodyVideo, Received: budget.Received}, nil
}
func mp4(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	var b [12]byte
	if _, e = io.ReadFull(f, b[:]); e != nil || string(b[4:8]) != "ftyp" {
		return ErrContainer
	}
	return nil
}
func movInput(path string) []string {
	return []string{"-protocol_whitelist", "file,pipe", "-enable_drefs", "0", "-use_absolute_path", "0", "-i", path}
}

type diagnostic struct{ bytes.Buffer }

func (b *diagnostic) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, errors.New("转换诊断超过64 KiB")
	}
	return b.Buffer.Write(p)
}

// mediaTool 向登记表要：自带 → qq.media 配置 → PATH。
func mediaTool(name string) (string, error) {
	if r, ok := toolchain.Lookup(name); ok {
		return r.Path, nil
	}
	return "", fmt.Errorf("没找到 %s", name)
}

func (m *MediaManager) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, e := mediaTool(name)
	if e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, path, args...)
	mediaPriority(cmd)
	cmd.WaitDelay = time.Second
	var out, stderr diagnostic
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("媒体转换或验证失败")
	}
	return out.Bytes(), nil
}
func (m *MediaManager) probe(ctx context.Context, path string) error {
	args := []string{"-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-enable_drefs", "0", "-use_absolute_path", "0", "-show_entries", "stream=codec_type,width,height", "-of", "json", path}
	raw, e := m.command(ctx, "ffprobe", args...)
	if e != nil {
		return e
	}
	var p struct {
		Streams []struct {
			Type          string `json:"codec_type"`
			Width, Height int
		}
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return ErrContainer
	}
	for _, s := range p.Streams {
		if s.Type == "video" && s.Width > 0 && s.Height > 0 && int64(s.Width)*int64(s.Height) <= 40_000_000 {
			return nil
		}
	}
	return ErrContainer
}
