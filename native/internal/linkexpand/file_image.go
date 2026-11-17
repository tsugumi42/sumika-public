package linkexpand

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"sumika-native/internal/channel/types"
)

// FileImage 仅在已声明文件图片能力时使用，消费旧资源并交出同一目录的新文件持有权。
func (m *MediaManager) FileImage(ctx context.Context, loaded Loaded) (out Loaded, err error) {
	original := loaded.Resource
	handed := false
	dir := ""
	defer func() {
		if !handed {
			err = errors.Join(err, original.Release())
			if dir != "" {
				err = errors.Join(err, os.RemoveAll(dir))
			}
		}
	}()
	extension := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[original.MIME]
	if extension == "" || original.Size > imageLimit {
		return Loaded{}, ErrContainer
	}
	if err = ctx.Err(); err != nil {
		return Loaded{}, err
	}
	dir, err = m.job()
	if err != nil {
		return Loaded{}, err
	}
	path := filepath.Join(dir, "image"+extension)
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return Loaded{}, e
	}
	e = original.Read(func(r io.Reader) error { _, e := io.Copy(f, contextReader{ctx: ctx, r: r}); return e })
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return Loaded{}, e
	}
	if e = original.Release(); e != nil {
		return Loaded{}, e
	}
	file, e := os.Open(path)
	if e != nil {
		return Loaded{}, e
	}
	resource, e := types.FileMedia(original.ID, original.MIME, file, func() error { return os.RemoveAll(dir) })
	if e != nil {
		return Loaded{}, errors.Join(e, file.Close())
	}
	loaded.Resource = resource
	handed = true
	return loaded, nil
}
