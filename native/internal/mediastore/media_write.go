package mediastore

// 往媒体库写一个文件（规格-QQ收图下载和断线补录 4.1"写文件的入口放在 mediastore"）。
// 和 OpenMedia 同一个布局、同一套“不接受联接”的规矩；不联网，数据由调用方给。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var (
	ErrMediaTooBig = errors.New("媒体超过大小上限")
	errMediaEmpty  = errors.New("媒体内容是空的")
)

// TmpDirName 是平台目录下放写到一半的临时文件的子目录；名字以点开头，不会和两位十六进制的分片目录撞
const TmpDirName = ".tmp"

// 改名被杀毒软件短暂占着时的重试（规格 4.1：3 次、间隔 50 毫秒）
const (
	renameTries = 3
	renameWait  = 50 * time.Millisecond
)

// Put 把 r 的内容写进 <data>/go/media/<platform>/<hash 前两位>/<hash> 并返回 hash（SHA-256 小写十六进制）和字节数。
//   - 临时文件放在同一平台目录下的 .tmp/，边写边算 hash，超过 maxBytes 就中止并删掉临时文件（ErrMediaTooBig）；
//     r 不必有长度，没有 Content-Length 的无限流也停得住；空内容不收（errMediaEmpty）；
//   - 写完 fsync、关闭，再改名进最终位置；目标已经在、是普通文件且大小一样就当成功（内容寻址，同 hash 同内容），
//     大小不一样的是坏文件，用新内容盖掉；
//   - 逐级拒绝符号链接和联接（ErrMediaLink），数据根本身允许是联接（和读一样）。
//
// 文件名永远是 hash，调用方给的任何名字都不参与路径。
func Put(data, platform string, r io.Reader, maxBytes int64) (hash string, size int64, err error) {
	return put(data, platform, r, maxBytes, nil)
}

// beforeRename 只供测试：在改名之前插一步（建好目录以后换路径）
func put(data, platform string, r io.Reader, maxBytes int64, beforeRename func()) (hash string, size int64, err error) {
	if maxBytes <= 0 {
		return "", 0, errors.New("媒体大小上限要大于 0")
	}
	root, err := mediaRootPath(data)
	if err != nil {
		return "", 0, err
	}
	dir, err := activeMediaDir(root, platform)
	if err != nil {
		return "", 0, err
	}
	tmpDir := filepath.Join(dir, TmpDirName)
	if err := ensureMediaDir(root, "go", "media", platform, TmpDirName); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(tmpDir, "put-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if n > maxBytes {
		return "", 0, ErrMediaTooBig
	}
	if n == 0 {
		return "", 0, errMediaEmpty
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	hash = hex.EncodeToString(h.Sum(nil))
	if err := ensureMediaDir(root, "go", "media", platform, mediaShard(hash)); err != nil {
		return "", 0, err
	}
	dst := filepath.Join(dir, mediaShard(hash), hash)
	if beforeRename != nil {
		beforeRename()
	}
	// 改名前再认一遍分片目录：算 hash 和写临时文件可能花了一阵，其间目录被换成联接的话，改名会跟着联接走
	if err := ensureMediaDir(root, "go", "media", platform, mediaShard(hash)); err != nil {
		return "", 0, err
	}
	for i := 0; ; i++ {
		info, serr := os.Lstat(dst)
		if serr == nil {
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return "", 0, ErrMediaLink
			}
			if info.Mode().IsRegular() && info.Size() == n {
				return hash, n, nil // 已经有了（tmp 由上面的 defer 删）
			}
		} else if !errors.Is(serr, os.ErrNotExist) {
			return "", 0, serr
		}
		err = os.Rename(tmpName, dst)
		if err == nil {
			done = true
			notifyNewFile(NewFile{Data: data, Platform: platform, Bytes: n})
			return hash, n, nil
		}
		if i+1 >= renameTries {
			return "", 0, fmt.Errorf("改名进媒体库：%w", err)
		}
		time.Sleep(renameWait)
	}
}

// CleanTmp 删掉平台目录 .tmp/ 里修改时间早于 cutoff 的残留（上次被杀留下的写到一半的文件）。目录不在就什么也不做；
// 只删 .tmp 下的普通文件，不跟联接走。返回删了几个
func CleanTmp(data, platform string, cutoff time.Time) (int, error) {
	root, err := mediaRootPath(data)
	if err != nil {
		return 0, err
	}
	dir, err := activeMediaDir(root, platform)
	if err != nil {
		return 0, err
	}
	tmpDir := filepath.Join(dir, TmpDirName)
	info, err := os.Lstat(tmpDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return 0, ErrMediaLink
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || !fi.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(tmpDir, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

// ensureMediaDir 从已解析的数据根往下，一级一级确认是真目录（不是符号链接或联接），没有就建
func ensureMediaDir(root string, parts ...string) error {
	cur := root
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(cur, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(cur)
		}
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return ErrMediaLink
		}
		if !info.IsDir() {
			return errors.New("媒体父路径不是目录")
		}
	}
	return nil
}

// Path 是 Put 写下的文件在磁盘上的位置（只拼路径，不检查文件在不在）。hash 要是 64 位小写十六进制。
func Path(data, platform, hash string) (string, error) {
	if !validHash(hash) {
		return "", errors.New("媒体 hash 不合法")
	}
	root, err := mediaRootPath(data)
	if err != nil {
		return "", err
	}
	dir, err := activeMediaDir(root, platform)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, mediaShard(hash), hash), nil
}
