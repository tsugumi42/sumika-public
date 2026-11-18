package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
)

// Targets 把命令分析出的写入目标整理成可拍快照的绝对路径。
// clear 为 false 表示有目标说不清会动哪些文件（通配符、目录、链接、不能保存的文件名），调用方不应拍快照。
// 工作目录外的、.git / node_modules 等不在检查点范围内的、受保护的目标直接略去。
func (s *Service) Targets(cwd string, abs []string) (paths []string, clear bool) {
	seen := map[string]bool{}
	for _, path := range abs {
		path = filepath.Clean(path)
		name, err := filepath.Rel(cwd, path)
		if err != nil || name == "." || !filepath.IsLocal(name) {
			if err == nil && name == "." {
				return nil, false // 目标是工作目录本身
			}
			continue
		}
		if strings.ContainsAny(name, "*?[") {
			return nil, false
		}
		if excludedName(name) || s.opts.Exclude != nil && s.opts.Exclude(path) {
			continue
		}
		if validName(name) != nil {
			return nil, false
		}
		if fi, err := os.Lstat(path); err == nil && (fi.IsDir() || linkInfo(fi) || !fi.Mode().IsRegular()) {
			return nil, false
		}
		if key := strings.ToLower(path); !seen[key] {
			seen[key] = true
			paths = append(paths, path)
		}
	}
	return paths, true
}
