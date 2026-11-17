package mediastore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const videoTmpDirName = "video-open"

// PrepareVideoCopy 只接调用方已核验的媒体句柄。副本有真实后缀，播放器不能通过硬链接修改媒体原件。
func PrepareVideoCopy(ctx context.Context, data, platform string, source *os.File) (path string, err error) {
	var head [512]byte
	n, err := source.ReadAt(head[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	ext := VideoExtension(head[:n])
	if ext == "" {
		return "", errors.New("不是已支持的视频容器")
	}
	root, err := mediaRootPath(data)
	if err != nil {
		return "", err
	}
	dir, err := activeMediaDir(root, platform)
	if err != nil {
		return "", err
	}
	if err := ensureMediaDir(root, "go", "media", platform, TmpDirName, videoTmpDirName); err != nil {
		return "", err
	}
	dst, err := os.CreateTemp(filepath.Join(dir, TmpDirName, videoTmpDirName), "video-*"+ext)
	if err != nil {
		return "", err
	}
	path = dst.Name()
	defer func() {
		if err != nil {
			dst.Close()
			os.Remove(path)
		}
	}()
	if err = checkMediaHandle(dst, path); err != nil {
		return path, err
	}
	_, err = io.Copy(dst, contextVideoReader{ctx, source})
	if err != nil {
		return path, err
	}
	err = dst.Close()
	return path, err
}

type contextVideoReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextVideoReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

// CleanVideoTmp 只清专用目录的旧普通文件；不建目录、不跟联接，不碰正在新建的副本。
func CleanVideoTmp(data, platform string, cutoff time.Time) (int, error) {
	root, err := mediaRootPath(data)
	if err != nil {
		return 0, err
	}
	if _, err := activeMediaDir(root, platform); err != nil {
		return 0, err
	}
	cur := root
	for _, part := range []string{"go", "media", platform, TmpDirName, videoTmpDirName} {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !info.IsDir() {
			return 0, ErrMediaLink
		}
	}
	entries, err := os.ReadDir(cur)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return count, err
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(cur, entry.Name())); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
