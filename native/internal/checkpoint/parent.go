package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
)

// 每个 worker 只持有一个父目录，避免 Windows 为每个叶子操作重新打开整条父路径。
// 句柄复用不缓存安全结论：读文件及发布前仍核对实际路径。
type restoreParent struct {
	name string
	root *os.Root
	file *os.File
}

func (p *restoreParent) resolve(work *os.Root, name string) (*os.Root, string, error) {
	dir := filepath.Dir(name)
	if p.file != nil && p.name == dir {
		return p.root, filepath.Base(name), nil
	}
	if err := p.close(); err != nil {
		return nil, "", err
	}
	root, err := work.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		// 逆检查点已持久发布；只补目标所需的空目录，不递归删除任何目录。
		if err = work.MkdirAll(dir, 0700); err != nil {
			return nil, "", err
		}
		root, err = work.OpenRoot(dir)
	}
	if err != nil {
		return nil, "", err
	}
	file, err := root.Open(".")
	if err != nil {
		return nil, "", errors.Join(err, root.Close())
	}
	if _, err := checkHandle(file, root.Name()); err != nil {
		return nil, "", errors.Join(err, file.Close(), root.Close())
	}
	p.name, p.root, p.file = dir, root, file
	return root, filepath.Base(name), nil
}

func (p *restoreParent) close() error {
	if p.file == nil {
		return nil
	}
	err := errors.Join(p.file.Close(), p.root.Close())
	*p = restoreParent{}
	return err
}
