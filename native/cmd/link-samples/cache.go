package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

// cachedLoader 包住线上的 MediaManager：同一条链接的几种样式各准备一次，媒体只真下载一次，
// 之后的样式拿同样的字节重新包一份资源（每次 Prepare 完都会 Release，所以每次都要新的）。
// 只省重复下载，不改结果：缓存的是 MediaManager 原样给出的字节、宽高、MIME、Kind、Note 和错误。
type cachedLoader struct {
	inner linkexpand.MediaLoader
	dir   string
	mu    sync.Mutex
	hits  map[string]cached
}

type cached struct {
	err              error
	mime, kind, note string
	w, h             int
	received         int64
	data             []byte // 图片放内存
	file             string // 视频、动图转出的文件拷在 dir 里
}

func newCachedLoader(inner linkexpand.MediaLoader, dir string) *cachedLoader {
	return &cachedLoader{inner: inner, dir: dir, hits: map[string]cached{}}
}

func cacheKey(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

func (c *cachedLoader) Image(ctx context.Context, platform string, src linkreader.Media) (linkexpand.Loaded, error) {
	fb := ""
	if src.Fallback != nil {
		fb = src.Fallback.URL.Value
	}
	return c.load("img|"+cacheKey(platform, src.URL.Value, fb), func() (linkexpand.Loaded, error) { return c.inner.Image(ctx, platform, src) }, ctx)
}

func (c *cachedLoader) Video(ctx context.Context, platform string, r *linkreader.Result, src linkreader.Media, limits linkexpand.VideoLimits, toGIF bool) (linkexpand.Loaded, error) {
	key := "vid|" + cacheKey(platform, r.CleanURL, src.URL.Value, src.Thumbnail.Value, strconv.FormatBool(toGIF), strconv.FormatBool(limits.Enabled), strconv.FormatInt(limits.Bytes, 10))
	return c.load(key, func() (linkexpand.Loaded, error) { return c.inner.Video(ctx, platform, r, src, limits, toGIF) }, ctx)
}

func (c *cachedLoader) Ugoira(ctx context.Context, p *linkreader.UgoiraInfo) (linkexpand.Loaded, error) {
	z := ""
	if p != nil {
		z = p.ZIP.Value
	}
	return c.load("ugo|"+cacheKey(z), func() (linkexpand.Loaded, error) { return c.inner.Ugoira(ctx, p) }, ctx)
}

// FileImage 照转给 MediaManager（Prepare 用它把大图换成文件发送）；它会 Release 传进来的资源，那份是我们新包的，不影响缓存。
func (c *cachedLoader) FileImage(ctx context.Context, l linkexpand.Loaded) (linkexpand.Loaded, error) {
	if f, ok := c.inner.(interface {
		FileImage(context.Context, linkexpand.Loaded) (linkexpand.Loaded, error)
	}); ok {
		return f.FileImage(ctx, l)
	}
	return l, nil
}

func (c *cachedLoader) load(key string, fetch func() (linkexpand.Loaded, error), ctx context.Context) (linkexpand.Loaded, error) {
	c.mu.Lock()
	hit, ok := c.hits[key]
	c.mu.Unlock()
	if ok {
		return hit.loaded(key)
	}
	l, err := fetch()
	if err != nil {
		if ctx.Err() == nil { // 取消、超时是这一次的事，不记
			c.put(key, cached{err: err})
		}
		return l, err
	}
	entry, e := c.capture(key, l)
	if e != nil {
		// 拷不下来就不缓存，原样交回这一次的结果
		return l, nil
	}
	_ = l.Resource.Release()
	c.put(key, entry)
	return entry.loaded(key)
}

func (c *cachedLoader) put(key string, v cached) {
	c.mu.Lock()
	c.hits[key] = v
	c.mu.Unlock()
}

func (c *cachedLoader) capture(key string, l linkexpand.Loaded) (cached, error) {
	r := l.Resource
	if r == nil {
		return cached{}, errors.New("没有资源")
	}
	v := cached{mime: r.MIME, kind: l.Kind, note: l.Note, w: r.Width, h: r.Height, received: l.Received}
	if r.FileAvailable() {
		if err := os.MkdirAll(c.dir, 0o755); err != nil {
			return cached{}, err
		}
		v.file = filepath.Join(c.dir, key[4:])
		out, err := os.Create(v.file)
		if err != nil {
			return cached{}, err
		}
		err = r.Read(func(rd io.Reader) error { _, e := io.Copy(out, rd); return e })
		if e := out.Close(); err == nil {
			err = e
		}
		return v, err
	}
	err := r.Read(func(rd io.Reader) error {
		b, e := io.ReadAll(rd)
		v.data = b
		return e
	})
	return v, err
}

func (v cached) loaded(key string) (linkexpand.Loaded, error) {
	if v.err != nil {
		return linkexpand.Loaded{}, v.err
	}
	l := linkexpand.Loaded{Kind: v.kind, Note: v.note, Received: v.received}
	if v.file == "" {
		l.Resource = types.MemoryMedia(key, v.mime, v.w, v.h, v.data)
		return l, nil
	}
	f, err := os.Open(v.file)
	if err != nil {
		return linkexpand.Loaded{}, err
	}
	res, err := types.FileMedia(key, v.mime, f, f.Close)
	if err != nil {
		_ = f.Close()
		return linkexpand.Loaded{}, err
	}
	res.Width, res.Height = v.w, v.h
	l.Resource = res
	return l, nil
}
