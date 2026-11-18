package docs

// 规格状态头还写着“待合并 / 在分支上”，而头里点名的分支早已合进 main：只提醒（check docs 的 D14 调用）。
// 证据：2026-11-02、11-03、11-06、11-12 四天都出现过（规格-链接发送改版在 6dedba43、9326f140 合进 main 后仍写待合并）；
// AGENTS 文档分层表里已有这一行，靠自觉不够，2026-11-13 主人确认改成工具层。
// 照搬自 scripts/check-spec-merged.ts（规格-脚本迁Go 第 2 步）。

import (
	"regexp"
	"strings"

	"sumika-native/internal/devtools/gitrun"
)

// statusHead 取文件开头的第一个 > 引用块（允许前面有标题和空行，只往下看 15 行）。
func statusHead(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 15 {
		lines = lines[:15]
	}
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, ">") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	var out []string
	for _, l := range lines[start:] {
		if !strings.HasPrefix(l, ">") {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

var (
	pendingWords = regexp.MustCompile(`待合并|待合入|在分支上|未合并|还没合|没合进`)
	branchRe     = regexp.MustCompile("\x60((?:feat|fix|chore|docs|refactor|perf|test|night|exp)/[\\w.\\-/]+)\x60")
)

// branchesIn 取头里反引号包着的分支名（feat/、fix/ 等开头），去重、保持出现顺序；反引号里的普通路径不算分支。
func branchesIn(head string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range branchRe.FindAllStringSubmatch(head, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// staleMergeNote：头里写着“待合并”一类字样，点名了分支，而这些分支全都已合或已删：返回这些分支，否则 nil。
func staleMergeNote(text string, merged func(branch string) bool) []string {
	head := statusHead(text)
	if !pendingWords.MatchString(head) {
		return nil
	}
	branches := branchesIn(head)
	if len(branches) == 0 {
		return nil
	}
	for _, b := range branches {
		if !merged(b) {
			return nil
		}
	}
	return branches
}

// mergedIntoMain：分支是 main 的祖先，或者分支已不存在（合完删掉了）都算“已合或已删”。
// 子进程清掉全部 GIT_*（gitrun 默认）：钩子里继承来的 GIT_DIR、GIT_INDEX_FILE 会让 git 去查别的仓库。
func mergedIntoMain(cwd, branch, main string) bool {
	_, code, err := gitrun.Run(cwd, nil, nil, "merge-base", "--is-ancestor", branch, main)
	if err != nil {
		return false
	}
	return code == 0 || code == 128 // 128：分支名不存在
}
