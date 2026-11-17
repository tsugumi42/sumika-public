package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
)

type directorySemantics struct {
	key       string
	sensitive bool
}
type pathKeys map[string]directorySemantics

// 键绑定实际父目录身份及该目录的大小写规则；展示名始终另存，不全局折叠路径。
func (keys pathKeys) key(path string) (string, error) {
	dir, err := keys.directory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	leaf := filepath.Base(path)
	if !dir.sensitive {
		leaf, err = insensitiveNameKey(leaf)
		if err != nil {
			return "", err
		}
	}
	return dir.key + "/" + leaf, nil
}
func (keys pathKeys) directory(path string) (directorySemantics, error) {
	path = filepath.Clean(path)
	if cached, ok := keys[path]; ok {
		return cached, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if filepath.Dir(path) == path {
			return directorySemantics{}, err
		}
		parent, err := keys.directory(filepath.Dir(path))
		if err != nil {
			return directorySemantics{}, err
		}
		leaf := filepath.Base(path)
		if !parent.sensitive {
			leaf, err = insensitiveNameKey(leaf)
			if err != nil {
				return directorySemantics{}, err
			}
		}
		// 已删除目录没有可核验的规则，剩余组件按敏感处理，不能误合并两个潜在目标。
		dir := directorySemantics{parent.key + "/" + leaf, true}
		keys[path] = dir
		return dir, nil
	}
	if err != nil {
		return directorySemantics{}, err
	}
	defer f.Close()
	identity, err := checkHandle(f, path)
	if err != nil {
		return directorySemantics{}, err
	}
	sensitive, err := directoryCaseSensitive(f)
	if err != nil {
		return directorySemantics{}, err
	}
	dir := directorySemantics{identity, sensitive}
	keys[path] = dir
	return dir, nil
}

func recordChanges(keys pathKeys, r *Record) (map[string]Change, error) {
	changes := make(map[string]Change, len(r.Before))
	for name, b := range r.Before {
		key, err := keys.key(filepath.Join(r.CWD, name))
		if err != nil {
			return nil, err
		}
		if _, exists := changes[key]; exists {
			return nil, ErrConflict
		}
		changes[key] = Change{Path: name, CurrentPath: name, Before: b, SourceID: r.ID}
	}
	seen := make(map[string]bool, len(r.After))
	for name, a := range r.After {
		key, err := keys.key(filepath.Join(r.CWD, name))
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, ErrConflict
		}
		seen[key] = true
		change, exists := changes[key]
		if !exists {
			change = Change{Path: name, SourceID: r.ID}
		}
		change.CurrentPath, change.After = name, a
		changes[key] = change
	}
	return changes, nil
}
