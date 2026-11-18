// Package gitrun 是开发工具起 git 子进程的唯一出口。
// 程序名由 cmd/sumika-dev 的 main 注入（Program）：internal/ 下非测试代码里 exec.Command 的第一个参数不许是字面量
// （archcheck 的外部程序规则，规格-外部程序清单），cmd/ 不受这条管。没注入时按 "git" 走，只给本包测试用。
package gitrun

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// Program 是 git 程序名或绝对路径。
var Program = "git"

// CleanEnv 返回去掉全部 GIT_* 的环境变量副本。
// 钩子里继承来的 GIT_DIR、GIT_INDEX_FILE 会让子进程里的 git 去查别的仓库或索引（LESSONS：测试和钩子里起 git 要清 GIT_*）。
func CleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Run 在 dir 里跑 git（总带 -c core.quotepath=false），返回标准输出和退出码。
// env 为 nil 时用 CleanEnv()；传 os.Environ() 表示有意继承钩子环境（读暂存区时需要 GIT_INDEX_FILE）。
// 起不来（找不到 git 等）返回 err；git 自己退出非零不算 err，看退出码。
func Run(dir string, env []string, stdin []byte, args ...string) (out []byte, code int, err error) {
	out, _, code, err = RunStderr(dir, env, stdin, args...)
	return out, code, err
}

// RunStderr 和 Run 一样，另外把 git 的标准错误也带回来（隐私扫描在 git 失败时要原样打印它）。
func RunStderr(dir string, env []string, stdin []byte, args ...string) (out, errOut []byte, code int, err error) {
	if env == nil {
		env = CleanEnv()
	}
	cmd := exec.Command(Program, append([]string{"-c", "core.quotepath=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	runErr := cmd.Run()
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return so.Bytes(), se.Bytes(), ee.ExitCode(), nil
	}
	if runErr != nil {
		return nil, nil, -1, runErr
	}
	return so.Bytes(), se.Bytes(), 0, nil
}
