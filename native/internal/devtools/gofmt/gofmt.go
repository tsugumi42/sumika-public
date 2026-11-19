// Package gofmt 是 sumika-dev check gofmt：提交前查暂存的 .go 文件有没有 gofmt（原 scripts/check-gofmt.ts，lefthook.yml 的“Go格式”）。
// 参数是暂存的 .go 文件，路径从仓库根算，如 native/internal/turn/hub.go。
// 查的是暂存区里的内容（git show :<文件>，从标准输入交给 gofmt），不是工作区的文件：这台机器开着 core.autocrlf，
// 签出来的 .go 是 CRLF，gofmt -l 会把格式没问题的文件全报出来（2026-10-26 mygo-v2 合 main 时撞上）；暂存区里存的是 LF，也正是要提交的内容。
// 必须在仓库根跑：worktree 的钩子里 git 设了 GIT_DIR、没设 GIT_WORK_TREE，当前目录就被当成工作区的根；主仓库的钩子里 GIT_INDEX_FILE 是相对路径 .git/index。
// （程序按自己所在的那棵树找仓库根，所以钩子在哪棵树里跑就查哪棵树。）
// gofmt -l 有问题时退出码照样是 0，所以要看它有没有输出。
package gofmt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"sumika-native/internal/devtools/gitrun"
	"sumika-native/internal/devtools/jstext"
)

// Program 是 gofmt 程序名或绝对路径（internal/ 里 exec.Command 的第一个参数不许是字面量，和 gitrun.Program 一样用变量）。
var Program = "gofmt"

// Run 是 sumika-dev check gofmt 的入口：args 是暂存的 .go 文件，root 是仓库根。
func Run(args []string, root string, stdout, _ io.Writer) int {
	if len(args) == 0 {
		return 0
	}
	env := os.Environ() // 有意继承钩子环境：GIT_INDEX_FILE 指向“这次要提交的索引”
	var bad []string
	for _, f := range args {
		blob, errOut, code, err := gitrun.RunStderr(root, env, nil, "show", ":"+strings.TrimPrefix(f, "./"))
		if err != nil {
			fmt.Fprintf(stdout, "读不到暂存区里的 %s：%s\n", f, err)
			return 1
		}
		if code != 0 {
			fmt.Fprintf(stdout, "读不到暂存区里的 %s：%s\n", f, jstext.Trim(jstext.DecodeLossy(errOut)))
			return 1
		}
		cmd := exec.Command(Program, "-l")
		cmd.Dir = root
		cmd.Env = env
		cmd.Stdin = bytes.NewReader(blob)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		runErr := cmd.Run()
		var ee *exec.ExitError
		switch {
		case errors.As(runErr, &ee):
			fmt.Fprintf(stdout, "gofmt 自己报错了（%s，退出码 %d）：\n%s\n", f, ee.ExitCode(), jstext.Trim(jstext.DecodeLossy(se.Bytes())))
			return 1
		case runErr != nil:
			// 旧版在这里会抛异常、带着调用栈退出 1；新版说清原因，同样不放行
			fmt.Fprintf(stdout, "gofmt 起不来（%s）：%s\n", f, runErr)
			return 1
		}
		if jstext.Trim(jstext.DecodeLossy(so.Bytes())) != "" {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		fmt.Fprintf(stdout, "这些文件没 gofmt：\n%s\n", strings.Join(bad, "\n"))
		return 1
	}
	return 0
}
