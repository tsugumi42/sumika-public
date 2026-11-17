// Package safepath 提供解析实际文件位置后的路径包含检查。
package safepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Inside 查 target 在 root 里面：两边都先解到已有的最长的真实路径再比，不拿字符串前缀当保证。
// cache 不为 nil 时，同一次核对里同一路径只解一次。
func Inside(cache map[string]string, root, target string) error {
	r, err := resolveCached(cache, root)
	if err != nil {
		return err
	}
	t, err := resolveCached(cache, target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(r, t)
	if err != nil {
		return fmt.Errorf("%s 和 %s 不在同一个卷上：%w", target, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s 不在 %s 里", target, root)
	}
	return nil
}

func resolveCached(cache map[string]string, path string) (string, error) {
	if cache == nil {
		return resolveExisting(path)
	}
	if r, ok := cache[path]; ok {
		return r, nil
	}
	r, err := resolveExisting(path)
	if err == nil {
		cache[path] = r
	}
	return r, err
}

// resolveExisting 把已有的最长的前缀换成真实路径（联接、符号链接都解开），再接上还没建的部分
func resolveExisting(path string) (string, error) {
	var tail []string
	cur := path
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{real}, tail...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path, nil
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}
