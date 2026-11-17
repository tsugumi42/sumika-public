package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"sumika-native/internal/jsjson"
)

// ───────────── write（write.js） ─────────────

// write 写整份文件：上级目录不在就建，在就覆盖。结果里照原样写参数里的路径（不是解析以后的）。
// 报错的写法照 Bun 的 fs（工具层对拍量出来的几种）；别的错（没权限、磁盘满……）是 Go 的原文（规格第七节 T11）
func write(ctx context.Context, cwd string, args jsjson.Value) (string, error) {
	p, _ := argString(args, "path")
	content, _ := argString(args, "content")
	abs := resolveToCwd(p, cwd)
	return withFileMutation(abs, func() (string, error) {
		if err := aborted(ctx); err != nil {
			return "", err
		}
		if err := mkdirAll(filepath.Dir(abs)); err != nil {
			return "", err
		}
		if err := aborted(ctx); err != nil {
			return "", err
		}
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			return "", errors.New("EISDIR: illegal operation on a directory, write")
		}
		// 读过或写过之后被别人改了：不写，让她先重读（队列锁里查，审批等待期间的改动也能发现）
		if err := checkNotChanged(ctx, abs, p); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, []byte(content), 0o666); err != nil {
			return "", err
		}
		rememberFileAt(ctx, abs)
		if err := aborted(ctx); err != nil {
			return "", err
		}
		return "Successfully wrote to " + p, nil
	})
}

// mkdirAll 像 fs.mkdir(dir, { recursive: true })：已经是目录就算成；dir 自己是个文件报 EEXIST，
// 上面哪一级是文件报 ENOTDIR，路径都写要建的这个 dir（Bun 的写法）
func mkdirAll(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if fi.IsDir() {
			return nil
		}
		return fmt.Errorf("EEXIST: file already exists, mkdir '%s'", dir)
	}
	err := os.MkdirAll(dir, 0o777)
	if errors.Is(err, syscall.ENOTDIR) {
		return fmt.Errorf("ENOTDIR: not a directory, mkdir '%s'", dir)
	}
	return err
}
