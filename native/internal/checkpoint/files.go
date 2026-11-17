package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func relative(root, path string) (string, error) {
	name, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if err = validName(name); err != nil {
		return "", err
	}
	return name, nil
}
func validName(name string) error {
	if !filepath.IsLocal(name) || name == "." {
		return ErrUnavailable
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part || strings.ContainsAny(part, ":\x00") {
			return ErrUnavailable
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
			return ErrUnavailable
		}
	}
	return nil
}
func excludedName(name string) bool {
	for _, p := range strings.Split(filepath.ToSlash(name), "/") {
		switch strings.ToLower(p) {
		case ".git", "node_modules", ".venv", "vendor":
			return true
		}
	}
	return false
}
func checkParents(root *os.Root, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	for i := range parts {
		p := filepath.Join(parts[:i+1]...)
		fi, err := root.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if linkInfo(fi) {
			return ErrLink
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return fmt.Errorf("检查点父路径不是目录：%s", p)
		}
	}
	return nil
}
func openWorkspace(cwd string) (*os.Root, string, error) {
	if !filepath.IsAbs(cwd) {
		return nil, "", ErrUnavailable
	}
	root, err := os.OpenRoot(filepath.Clean(cwd))
	if err != nil {
		return nil, "", err
	}
	f, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, "", err
	}
	identity, err := checkHandle(f, root.Name())
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, identity, nil
}

// os.Root 约束打开范围，再核验实际句柄；不在同一次读取前重复逐项 Lstat。
func readState(ctx context.Context, root *os.Root, name string, limit int64, buffer *[]byte, save func(string, []byte) error) (FileState, error) {
	if err := ctx.Err(); err != nil {
		return FileState{}, err
	}
	if err := validName(name); err != nil {
		return FileState{}, err
	}
	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return FileState{}, nil
	}
	if err != nil {
		return FileState{}, err
	}
	defer f.Close()
	if _, err := checkHandle(f, filepath.Join(root.Name(), name)); err != nil {
		return FileState{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		return FileState{}, err
	}
	if !fi.Mode().IsRegular() {
		return FileState{}, fmt.Errorf("%w：只保存普通文件：%s", errUncapturable, name)
	}
	if fi.Size() > limit {
		return FileState{}, fmt.Errorf("%w：文件 %s", ErrLimit, name)
	}
	need := int(fi.Size() + 1)
	if cap(*buffer) < need {
		*buffer = make([]byte, need)
	}
	body := (*buffer)[:need]
	n, readErr := io.ReadFull(f, body)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return FileState{}, readErr
	}
	if int64(n) != fi.Size() {
		return FileState{}, fmt.Errorf("%w：捕获期间文件大小变化 %s", ErrConflict, name)
	}
	after, err := f.Stat()
	if err != nil {
		return FileState{}, err
	}
	if after.Size() != fi.Size() || after.ModTime() != fi.ModTime() {
		return FileState{}, fmt.Errorf("%w：捕获期间文件变化 %s", ErrConflict, name)
	}
	hash := digestBytes(body[:n])
	if save != nil {
		if err := save(hash, body[:n]); err != nil {
			return FileState{}, err
		}
	}
	return FileState{Exists: true, Hash: hash, Bytes: int64(n), Mode: fi.Mode().Perm()}, nil
}

type scanBudget struct {
	files, entries int
	bytes          int64
}

func scan(ctx context.Context, root *os.Root, paths []string, opts Options, buffer *[]byte, save func(string, []byte) error) (map[string]FileState, error) {
	states := map[string]FileState{}
	budget := scanBudget{}
	capture := func(name string) error {
		if budget.files >= opts.MaxFiles {
			return ErrLimit
		}
		state, err := readState(ctx, root, name, opts.MaxFileBytes, buffer, func(hash string, body []byte) error {
			if budget.bytes+int64(len(body)) > opts.MaxScanBytes {
				return ErrLimit
			}
			if save != nil {
				return save(hash, body)
			}
			return nil
		})
		if err != nil {
			return err
		}
		budget.files++
		budget.bytes += state.Bytes
		if budget.bytes > opts.MaxScanBytes {
			return ErrLimit
		}
		states[name] = state
		return nil
	}
	if paths != nil {
		for _, path := range paths {
			name, err := relative(root.Name(), path)
			if err != nil {
				return nil, err
			}
			if excludedName(name) {
				return nil, ErrUnavailable
			}
			if err := capture(name); err != nil {
				return nil, err
			}
		}
		return states, nil
	}
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > opts.MaxDepth {
			return ErrLimit
		}
		f, err := root.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := checkHandle(f, filepath.Join(root.Name(), dir)); err != nil {
			return err
		}
		for {
			entries, readErr := f.ReadDir(128)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, entry := range entries {
				budget.entries++
				if budget.entries > opts.MaxEntries {
					return ErrLimit
				}
				name := filepath.Join(dir, entry.Name())
				if excludedName(name) || opts.Exclude != nil && opts.Exclude(filepath.Join(root.Name(), name)) {
					continue
				}
				if err := validName(name); err != nil {
					return fmt.Errorf("%w：文件名不能保存：%s", errUncapturable, name)
				}
				fi, err := entry.Info()
				if err != nil {
					return err
				}
				if linkInfo(fi) {
					return ErrLink
				}
				if fi.IsDir() {
					if err := walk(name, depth+1); err != nil {
						return err
					}
				} else if err := capture(name); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
		}
	}
	if err := walk(".", 0); err != nil {
		return nil, err
	}
	return states, nil
}
