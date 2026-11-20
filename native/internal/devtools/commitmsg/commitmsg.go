// Package commitmsg 是 sumika-dev hook commit-msg（2026-11-19 前叫 check commit-msg，旧名已删）：提交说明的标题检查（原 scripts/check-commit-msg.ts，lefthook.yml 的 commit-msg，参数是 git 给的说明文件路径）。
// 上级 AGENTS「提交」第 1 条：标题 30 个汉字左右、只写一件事、不用分号。
// 主人 2026-10-27 嫌标题和说明太长、“很多有的没的也写进去了”，光靠记守不住，所以拦标题；正文写什么、不写什么靠第 2 条，这里不查。
// 字数和分号只查“类型(模块): ”后面那段，按 Unicode 码点数；ADR 编号整行都查（模块名里也不写 ADR 号）。合并、回退（Merge、Revert）和 fixup! / squash! 不查。
//
// 和别的 check 不同，这里不按程序所在的树找仓库根，而是和旧脚本一样按当前工作目录（钩子启动时的目录）：
// 参数可能是相对路径（主仓库里 git 给的是 .git/COMMIT_EDITMSG），问 git 说明文件在哪也是在当前目录问。
package commitmsg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/devtools/gitrun"
	"sumika-native/internal/devtools/jstext"
)

// MaxLen 是标题（去掉“类型(模块): ”以后）的字数上限。
const MaxLen = 40

// jsSpace 是 JS 的 \s 认的空白，放进方括号用。
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	skipRe  = regexp.MustCompile(`^(Merge |Revert |fixup! |squash! |合并 )`)
	prefix  = regexp.MustCompile(`^[a-z]+(\([^)]*\))?!?:[` + jsSpace + `]*`)
	adrRe   = regexp.MustCompile(`(?i)ADR-?[0-9]`)
	ompTail = regexp.MustCompile(`\.?omp-commit-msg-[0-9-]+$`)
)

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// messageFile 找说明文件。
// lefthook 把 git 给的路径不加引号拼进 sh 命令：在 omp 这类环境里 git 给的是 D:\… 反斜杠绝对路径，反斜杠被 sh 吃掉，
// 参数就成了不存在的路径（2026-11-15 连着五次拦住代理提交）。这时问 git 这个仓库（含 worktree）的说明文件在哪，照样检查；
// 两边都找不到就报错退出——绝不在拿不到说明时放行。
func messageFile(p string) string {
	if exists(p) {
		return p
	}
	env := os.Environ()
	gitOut := func(args ...string) string {
		out, code, err := gitrun.Run("", env, nil, args...)
		if err != nil || code != 0 {
			return ""
		}
		return jstext.Trim(jstext.DecodeLossy(out))
	}
	// 被吃掉的路径结尾还留着文件名：合并提交是 MERGE_MSG（这时 COMMIT_EDITMSG 里是上一次的旧说明，不能拿来查），
	// 普通提交是 COMMIT_EDITMSG；两者都不是就不认（复核 2026-11-16）
	name := ""
	switch {
	case strings.HasSuffix(p, "MERGE_MSG"):
		name = "MERGE_MSG"
	case strings.HasSuffix(p, "COMMIT_EDITMSG"):
		name = "COMMIT_EDITMSG"
	}
	// omp 的提交助手把说明写在 .git/worktrees/<名字>/.omp-commit-msg-<pid>-<时间>-0，路径被 sh 吃掉以后
	// 结尾还留着这个文件名；按结尾去 git 目录里找，找到就照常检查（2026-11-17，worktree 里连续拦住提交）
	if omp := ompTail.FindString(p); omp != "" {
		if dir := gitOut("rev-parse", "--git-dir"); dir != "" {
			if !strings.HasPrefix(omp, ".") {
				omp = "." + omp
			}
			if f := filepath.Join(dir, omp); exists(f) {
				return f
			}
		}
	}
	if name == "" {
		return ""
	}
	if q := gitOut("rev-parse", "--git-path", name); q != "" && exists(q) {
		return q
	}
	return ""
}

// firstTitle 取说明文件里第一行非空、不以 # 开头的行（去掉两头空白）；# 开头的是 git 的注释，不算。
func firstTitle(text string) string {
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if jstext.Trim(l) != "" && !strings.HasPrefix(l, "#") {
			return jstext.Trim(l)
		}
	}
	return ""
}

// Problems 返回标题违反的规则（没有问题返回 nil）；合并、回退和 fixup!/squash! 开头的标题不查。
func Problems(title string) []string {
	// “合并 ”是合并队列写的标题（“合并 <分支名>”），分支名里带 adr-359 这类编号时以前会被下面的 ADR 检查拦住、合并失败（2026-11-12）
	if skipRe.MatchString(title) {
		return nil
	}
	body := title
	if loc := prefix.FindStringIndex(title); loc != nil {
		body = title[loc[1]:]
	}
	var bad []string
	if n := utf8.RuneCountInString(body); n > MaxLen {
		bad = append(bad, fmt.Sprintf("标题 %d 个字，超过 %d：只写一件事、写\"现在会怎样\"，编号、理由、数字放正文", n, MaxLen))
	}
	if strings.ContainsAny(body, "；;") {
		bad = append(bad, "标题里有分号：一个提交只做一件事，两件就拆开")
	}
	if adrRe.MatchString(title) {
		bad = append(bad, "标题里有 ADR 编号：编号放正文第一行，标题写现在会怎样（docs/archive/notes/对照-提交说明写法-2026-10-27.md）")
	}
	return bad
}

// Run 是 sumika-dev hook commit-msg 的入口；args[0] 是 git 给的说明文件路径。root 不用（见包注释）。
// 退出码：0 通过；1 标题不合规（读不出说明文件的怪情形也是 1）；2 没给参数，或拿不到说明文件、没检查（按失败处理）。
func Run(args []string, _ string, stdout, _ io.Writer) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(stdout, "用法：sumika-dev hook commit-msg <说明文件>")
		return 2
	}
	arg := args[0]
	file := messageFile(arg)
	if file == "" {
		fmt.Fprintf(stdout, "读不到提交说明：参数 %s 不存在，git 也报不出说明文件。没检查，按失败处理\n", arg)
		return 2
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		// 旧版这里是没接住的异常（带调用栈、退出码 1）；新版说清原因，同样不放行
		fmt.Fprintf(stdout, "读不出提交说明 %s：%v\n", file, err)
		return 1
	}
	title := firstTitle(jstext.DecodeLossy(raw))
	bad := Problems(title)
	if len(bad) == 0 {
		return 0
	}
	fmt.Fprintf(stdout, "提交说明的标题：%s\n", title)
	for _, b := range bad {
		fmt.Fprintf(stdout, "  %s\n", b)
	}
	return 1
}
