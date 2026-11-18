// Package conflicts 是 sumika-dev check conflicts：提交前查暂存区新增的行里有没有没解决的合并冲突标记
// （原 scripts/check-conflict-markers.ts，lefthook.yml 的“冲突标记”，LESSONS「合并解决冲突以后」那条）。
// 2026-10-30 合 ui-go-2 时 80062b2c 把待办和装配规格里各一处冲突连标记一起提交了，check docs 不查这种标记。
// 只看 git diff --cached 加进来的行：二进制文件自动跳过，也不用经 lefthook 传文件名（Windows 上会拆坏引号）。
// <<<<<<< 和 >>>>>>> 单独出现就算；=======、|||||||（diff3）可能是 Markdown 标题下划线这类正常内容，同一个文件里还有前两种才算。
package conflicts

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"sumika-native/internal/devtools/gitrun"
	"sumika-native/internal/devtools/jstext"
)

var (
	strong = regexp.MustCompile(`^(<{7}|>{7})( |\r?$)`)
	weak   = regexp.MustCompile(`^(={7}|\|{7})( |\r?$)`)
	hunkRe = regexp.MustCompile(`^@@ -\S+ \+(\d+)`)
)

type hit struct {
	strong bool
	lines  []string
}

// Scan 解析 `git diff --cached -U0` 的输出，返回有“强标记”的文件里的命中行（按文件首次命中的顺序）。
func Scan(diff string) []string {
	var order []string
	hits := map[string]*hit{}
	file, line, header := "", 0, false // header：diff --git 到第一个 @@ 之间是文件头；新增的行本身以 "++ " 开头时不能当成文件头
	for _, raw := range strings.Split(diff, "\n") {
		if strings.HasPrefix(raw, "diff --git ") {
			header = true
			continue
		}
		if header {
			if strings.HasPrefix(raw, "+++ ") {
				file = strings.TrimPrefix(strings.TrimPrefix(raw, "+++ "), "b/")
			}
			if !strings.HasPrefix(raw, "@@") {
				continue
			}
			header = false
		}
		if m := hunkRe.FindStringSubmatch(raw); m != nil {
			line, _ = strconv.Atoi(m[1])
			continue
		}
		if !strings.HasPrefix(raw, "+") {
			continue
		}
		text := raw[1:]
		isStrong := strong.MatchString(text)
		if isStrong || weak.MatchString(text) {
			h := hits[file]
			if h == nil {
				h = &hit{}
				hits[file] = h
				order = append(order, file)
			}
			h.strong = h.strong || isStrong
			h.lines = append(h.lines, fmt.Sprintf("%s:%d: %s", file, line, jstext.TrimEnd(text)))
		}
		line++
	}
	var bad []string
	for _, f := range order {
		if hits[f].strong {
			bad = append(bad, hits[f].lines...)
		}
	}
	return bad
}

// Run 是 sumika-dev check conflicts 的入口，root 是仓库根；不收参数。
func Run(_ []string, root string, stdout, _ io.Writer) int {
	// 有意继承钩子环境：GIT_INDEX_FILE 指向“这次要提交的索引”
	out, errOut, code, err := gitrun.RunStderr(root, os.Environ(), nil,
		"diff", "--cached", "-U0", "--no-color", "--no-ext-diff", "--diff-filter=ACMR")
	if err != nil {
		fmt.Fprintf(stdout, "读不到暂存区的改动：%s\n", err)
		return 1
	}
	if code != 0 {
		fmt.Fprintf(stdout, "读不到暂存区的改动：%s\n", jstext.Trim(jstext.DecodeLossy(errOut)))
		return 1
	}
	if bad := Scan(jstext.DecodeLossy(out)); len(bad) > 0 {
		fmt.Fprintf(stdout, "这些地方还有没解决的合并冲突：\n%s\n", strings.Join(bad, "\n"))
		return 1
	}
	return 0
}
