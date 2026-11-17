package storagestat

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"

	"sumika-native/internal/ctlproto"
)

// yieldEvery 是每处理多少项检查一次取消、让出一次
const yieldEvery = 1000

// walkOut 是量一处位置的结果
type walkOut struct {
	bytes, files int64
	links, errs  int
	absent       bool
	items        map[string]*ctlproto.StorageItem
}

func (o *walkOut) item(name string) *ctlproto.StorageItem {
	if o.items == nil {
		o.items = map[string]*ctlproto.StorageItem{}
	}
	it := o.items[name]
	if it == nil {
		it = &ctlproto.StorageItem{Name: name}
		o.items[name] = it
	}
	return it
}

func (o *walkOut) add(item string, size int64) {
	o.bytes += size
	o.files++
	if item != "" {
		it := o.item(item)
		it.Bytes += size
		it.Files++
	}
}

func (o *walkOut) merge(b walkOut) {
	o.bytes += b.bytes
	o.files += b.files
	o.links += b.links
	o.errs += b.errs
	for k, v := range b.items {
		it := o.item(k)
		it.Bytes += v.Bytes
		it.Files += v.Files
	}
}

func (o *walkOut) itemList() []ctlproto.StorageItem {
	if len(o.items) == 0 {
		return nil
	}
	out := make([]ctlproto.StorageItem, 0, len(o.items))
	for _, v := range o.items {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// walker 遍历目录算大小：不跟联接和符号链接，不碰文件内容，只读目录项
type walker struct {
	progress *atomic.Int64 // 已经处理的项数，页面显示进度；可为 nil
	n        int64
}

func isLink(m fs.FileMode) bool { return m&(fs.ModeSymlink|fs.ModeIrregular) != 0 }

// walk 量 root（文件或目录）。skip 里的路径（别的类别认领的）整个跳过，root 自己除外。
// root 本身是联接或符号链接时不跟进去，只记一个 link。ctx 取消返回 ctx 的错误，结果作废
func (w *walker) walk(ctx context.Context, root string, skip map[string]bool, itemOf func(string) string) (walkOut, error) {
	var out walkOut
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		out.absent = true
		return out, nil
	}
	if err != nil {
		out.errs++
		return out, nil
	}
	switch {
	case isLink(info.Mode()):
		out.links++
		return out, nil
	case info.Mode().IsRegular():
		out.add("", info.Size())
		return out, nil
	case !info.IsDir():
		return out, nil
	}
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := ctx.Err(); err != nil {
			return out, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			out.errs++
			continue
		}
		for _, e := range entries {
			w.n++
			if w.progress != nil {
				w.progress.Add(1)
			}
			if w.n%yieldEvery == 0 {
				if err := ctx.Err(); err != nil {
					return out, err
				}
				runtime.Gosched()
			}
			p := filepath.Join(dir, e.Name())
			if skip[lowerKey(p)] {
				continue
			}
			t := e.Type()
			switch {
			case isLink(t):
				out.links++
			case t.IsDir():
				stack = append(stack, p)
			case t.IsRegular():
				fi, err := e.Info()
				if err != nil {
					out.errs++
					continue
				}
				item := ""
				if itemOf != nil {
					if rel, err := filepath.Rel(root, p); err == nil {
						item = itemOf(rel)
					}
				}
				out.add(item, fi.Size())
			}
		}
	}
	return out, nil
}
