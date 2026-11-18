// Package mediastore 提供 Go 平台媒体库的文件读写和派生缓存。
// 显示只核对路径和句柄；全文 SHA-256 留给写入和只读诊断。
package mediastore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"sumika-native/internal/store"
)

var (
	ErrMediaMissing         = errors.New("本地媒体文件缺失")
	ErrMediaInvalidHash     = errors.New("媒体 hash 必须是 64 位小写十六进制")
	errMediaContentMismatch = errors.New("媒体内容与 hash 不符")
	ErrMediaLink            = errors.New("媒体路径不接受符号链接或联接")
)

// Reader 绑定数据根。调用方须持有数据组排他锁直到所有读取结束。
// 文件路径和句柄仍在每次 Open 时核对，不缓存文件或读取结果（ADR-260）。
type Reader struct {
	root   string
	thumbs [64]sync.Mutex // 命中不拿锁；生成按分片合并，不保留不断增长的哈希表。
}

func NewReader(data string) (*Reader, error) {
	root, err := mediaRootPath(data)
	if err != nil {
		return nil, err
	}
	return &Reader{root: root}, nil
}

func (r *Reader) Open(platform, hash string) (*os.File, error) {
	if !validHash(hash) {
		return nil, ErrMediaInvalidHash
	}
	if !store.ValidPlatform(platform) {
		return nil, errors.New("平台不认识")
	}
	return openMediaFile(r.root, platform, hash, nil)
}

// OpenMedia 只读指定平台的 Go 媒体，不重算内容哈希。
// 宿主负责事件可见性和 hash 归属；调用方关闭返回的文件。
func OpenMedia(data, platform, hash string) (*os.File, error) {
	return openMedia(data, platform, hash, nil)
}

// beforeOpen 只供测试交换路径，生产调用不设置。
func openMedia(data, platform, hash string, beforeOpen func()) (*os.File, error) {
	if !validHash(hash) {
		return nil, ErrMediaInvalidHash
	}
	if !store.ValidPlatform(platform) {
		return nil, errors.New("平台不认识")
	}
	root, err := mediaRootPath(data)
	if err != nil {
		return nil, err
	}
	return openMediaFile(root, platform, hash, beforeOpen)
}

func verifyMedia(f *os.File, hash string) (*os.File, error) {
	h := sha256.New()
	var err error
	if _, err = io.Copy(h, f); err == nil && hex.EncodeToString(h.Sum(nil)) != hash {
		err = errMediaContentMismatch
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func openMediaFile(root, platform, hash string, beforeOpen func()) (*os.File, error) {
	return openCheckedFile(root, []string{"go", "media", platform, mediaShard(hash), hash}, beforeOpen)
}

// 原件和派生缓存共用逐级路径与最终句柄检查，缓存命中不能绕过文件边界。
func openCheckedFile(root string, parts []string, beforeOpen func()) (*os.File, error) {
	path := filepath.Join(append([]string{root}, parts...)...)
	cur := root
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrMediaMissing
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil, ErrMediaLink
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("媒体父路径不是目录")
		}
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMediaMissing
	}
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		return nil, errors.Join(err, f.Close())
	}
	if err := checkMediaHandle(f, path); err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("媒体不是普通文件"))
	}
	return f, nil
}

func activeMediaDir(data, platform string) (string, error) {
	if !store.ValidPlatform(platform) {
		return "", errors.New("平台不认识")
	}
	return filepath.Join(data, "go", "media", platform), nil
}

// mediaShard 是 hash 的前两位；不是 64 位小写十六进制的 hash 不能拼进路径
func mediaShard(hash string) string {
	if len(hash) < 2 {
		return ""
	}
	return hash[:2]
}

// validHash 查一个 hash 能不能当文件名（64 位小写十六进制，内容和它核对）
func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
