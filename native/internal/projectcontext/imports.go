package projectcontext

import (
	"fmt"
	"path/filepath"
	"strings"
)

type reference struct {
	start, end int
	path       string
}

// 这里只解析规格列出的 @文件子集，不把 Markdown 变成另一套执行语言。
func references(text string) []reference {
	var refs []reference
	fence := byte(0)
	fenceLen := 0
	for line := 0; line < len(text); {
		end := strings.IndexByte(text[line:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += line + 1
		}
		p := line
		for p < end && p-line < 3 && text[p] == ' ' {
			p++
		}
		q := p
		if p < end && (text[p] == '`' || text[p] == '~') {
			for q < end && text[q] == text[p] {
				q++
			}
		}
		if fence != 0 {
			if p < end && text[p] == fence && q-p >= fenceLen && strings.TrimSpace(text[q:end]) == "" {
				fence = 0
			}
			line = end
			continue
		}
		if q-p >= 3 {
			fence = text[p]
			fenceLen = q - p
			line = end
			continue
		}
		for i := line; i < end; {
			if text[i] == '\\' && i+1 < end && (text[i+1] == '@' || text[i+1] == '`' || text[i+1] == '\\') {
				i += 2
				continue
			}
			if text[i] == '`' {
				n := 1
				for i+n < len(text) && text[i+n] == '`' {
					n++
				}
				close := i + n
				found := false
				for close < len(text) {
					k := strings.IndexByte(text[close:], '`')
					if k < 0 {
						break
					}
					close += k
					run := 1
					for close+run < len(text) && text[close+run] == '`' {
						run++
					}
					if run == n {
						i = close + run
						found = true
						break
					}
					close += run
				}
				if found {
					// inline code 可以跨行；下一次从关闭位置所在行继续。
					if i > end {
						end = i
						for end < len(text) && text[end-1] != '\n' {
							end++
						}
					}
					continue
				}
				i += n
				continue
			}
			if text[i] != '@' || (i > 0 && !strings.ContainsRune(" \t\r\n([{:>", rune(text[i-1]))) {
				i++
				continue
			}
			start := i
			i++
			var path strings.Builder
			for i < end {
				ch := text[i]
				if ch == '\\' && i+1 < end && text[i+1] == ' ' {
					path.WriteByte(' ')
					i += 2
					continue
				}
				if strings.ContainsRune(" \t\r\n`\"'<>[](){},;!?", rune(ch)) {
					break
				}
				path.WriteByte(ch)
				i++
			}
			if path.Len() > 0 {
				refs = append(refs, reference{start, i, path.String()})
			}
		}
		line = end
	}
	return refs
}

func importPath(path, dir, root string, user bool) (string, error) {
	if strings.ContainsRune(path, 0) || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") || strings.Contains(path, "://") {
		return "", fmt.Errorf("不支持 URL、UNC 或设备引用：%q", path)
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if !user {
			return "", fmt.Errorf("项目说明不能引用用户根：%q", path)
		}
		path = filepath.Join(root, path[2:])
	}
	// 盘符以外不允许冒号，防止 ADS；每个路径成分也不能是 Windows 设备名。
	withoutVolume := strings.TrimPrefix(path, filepath.VolumeName(path))
	if strings.ContainsRune(withoutVolume, ':') {
		return "", fmt.Errorf("引用含 ADS 或非法冒号：%q", path)
	}
	for _, part := range strings.FieldsFunc(withoutVolume, func(c rune) bool { return c == '/' || c == '\\' }) {
		if part != "." && part != ".." && !safeName(part) {
			return "", fmt.Errorf("引用含非法文件名：%q", path)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if !inside(root, path) {
		return "", fmt.Errorf("引用越出授权根：%q", path)
	}
	return path, nil
}
