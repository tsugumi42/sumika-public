package bench

import (
	"path/filepath"
	"regexp"
	"strings"
)

// WorkdirDenied 是两个评测壳共用的规则拒绝反馈；不冒充主人取消或拒绝。
const WorkdirDenied = "这一步没有执行：评测环境只自动批准工作目录里的操作，这条操作涉及或无法确认不涉及工作目录外的路径（如 $env:TEMP）。不要停下来问，换一种只在工作目录里的做法接着完成任务。"

// outsideHint 是命令里指向工作目录外的写法：盘符路径、环境变量（$env:TEMP 这类拆命令时看不清去向）、往上一级、
// 斜杠开头的绝对路径（/tmp）、家目录、UNC。宁可多拦：批准版拦下时说明边界，让模型在工作目录里换法继续。
var outsideHint = regexp.MustCompile(`(?i)\b[a-z]:[\\/]|\$env:|\$home\b|\.\.[\\/]|(^|[\s'"(=;|&,])[\\/][a-z_]|(^|[\s'"(=])~[\\/]|\\\\`)

// 判之前先去掉的三种不是路径的写法（2026-10-28 批准版 B2、B3 误拦过 6 次，规格-表二执行器 第十一节）：
//   - 正则：-match、-split、-replace、[regex]、::Match(…) 这些后面的单引号字面量（'\\\.git\\'、'\s+'、'\[([^\]]*)\]'）；
//   - Markdown 链接的目标（写进笔记正文的 [番茄](../garden/x.md)）；
//   - 她自己工作目录的绝对路径（cd 'C:\…\run1'）在 ApproveInWorkdir 里按工作目录去掉
var notPaths = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(-[ic]?(not)?match|-[ic]?replace|-[ic]?split|-pattern|\[regex\]|::(is)?match(es)?\s*\([^']*|::replace\s*\([^']*|::split\s*\([^']*)\s*'(?:[^']|'')*'`),
	regexp.MustCompile(`\]\([^)\s]*\)`),
}

// ApproveInWorkdir 判一张审批卡片要不要替主人批准一次（Task.ApproveInWorkdir，B2、B3 量长任务本身）：
// 碰到外部目录的（gate 算出的 dirs）不批；PowerShell 命令去掉 notPaths 和工作目录本身的绝对路径以后还有 outsideHint 的不批；
// 读写类工具（subject 第一行是绝对路径）只批工作目录里的。Bun 壳（sumika-bun 仓库的 scripts/bench-t2/approve.ts）照同一套规则写了一份
func ApproveInWorkdir(tool, subject, workdir string, dirs []string) bool {
	if len(dirs) > 0 {
		return false
	}
	if tool == "powershell" {
		s := subject
		for _, re := range notPaths {
			s = re.ReplaceAllString(s, "''")
		}
		for _, w := range []string{filepath.Clean(workdir), filepath.ToSlash(filepath.Clean(workdir))} {
			s = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(w)+`(?:[\\/]|\b)`).ReplaceAllString(s, "./")
		}
		return !outsideHint.MatchString(s)
	}
	path, _, _ := strings.Cut(subject, "\n")
	rel, err := filepath.Rel(filepath.Clean(workdir), filepath.Clean(path))
	return err == nil && filepath.IsAbs(path) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
