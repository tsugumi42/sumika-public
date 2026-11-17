package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"sumika-native/internal/toolchain"
)

// ShellCommand 是宿主替用户跑的一条配置好的命令（用户钩子）：不走审批、结果不进工具历史，由调用方决定怎么用。
// Env 追加在 powershell 工具那份环境后面，同名的以这里为准。
type ShellCommand struct {
	Shell          string // ""、"powershell"、"bash"（Git Bash）
	Dir            string
	Command        string
	Env            []string
	Stdin          []byte
	Stdout, Stderr io.Writer
}

// pwsh -Command 会把外部程序的退出码压成 0 或 1（2026-11-09 实测 cmd /c exit 2 得 1），要退出码 2 表示"检查没过"的话传不回来。
// 另起一行接在命令后面（命令最后一行可能是注释）：最后一条成功就是 0，失败优先用外部程序自己的退出码。5.1 和 7 都实测过。
const exitCodeSuffix = "\nif ($?) { exit 0 } elseif ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE } else { exit 1 }"

// RunShellCommand 跑完才返回，退出码是命令真实的退出码。取消时杀整棵进程树并等它退出；正常退出时关掉 Job，命令留下的子进程一起结束。
// 起不来才返回 error（文件找不到、Job 建不了）；被杀的退出码没有意义，调用方先看 ctx。
func RunShellCommand(ctx context.Context, c ShellCommand) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	path, args, err := shellInvocation(c.Shell, c.Command)
	if err != nil {
		return -1, err
	}
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = c.Dir, append(shellEnv(), c.Env...), c.Stdout, c.Stderr
	cmd.Stdin = bytes.NewReader(c.Stdin) // 空也给一个：不继承后台自己的标准输入，读它的钩子马上读到结尾
	cmd.WaitDelay = exitStdioGrace
	process, err := startShell(cmd)
	if err != nil {
		return -1, err
	}
	stop := context.AfterFunc(ctx, func() { process.killTree() })
	waitErr := cmd.Wait()
	stop()
	closeErr := process.close()
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return -1, errors.Join(waitErr, closeErr)
	}
	return cmd.ProcessState.ExitCode(), nil
}

func shellInvocation(shell, command string) (string, []string, error) {
	switch shell {
	case "", "powershell":
		path, err := findPowerShell()
		if err != nil {
			return "", nil, err
		}
		return path, append(append([]string(nil), powershellArgs...), utf8OutputPrefix+command+exitCodeSuffix), nil
	case "bash":
		path, err := findGitBash()
		if err != nil {
			return "", nil, err
		}
		return path, []string{"-c", command}, nil
	}
	return "", nil, fmt.Errorf("不认识的 shell：%s", shell)
}

// findGitBash 找 Git Bash。System32 和 WindowsApps 里的 bash.exe 是 WSL 的入口，路径规则不一样，不要。
// 登记表只登记 git；从 git 的安装根推 bash，标准安装和 PATH 里的 git 都覆盖。
func findGitBash() (string, error) {
	if git, ok := toolchain.Lookup("git"); ok {
		dir := filepath.Dir(filepath.Dir(git.Path)) // git 在 <根>\cmd\git.exe 或 <根>\bin\git.exe
		for _, rel := range []string{`bin\bash.exe`, `usr\bin\bash.exe`} {
			if p := filepath.Join(dir, rel); exists(p) {
				return p, nil
			}
		}
	}
	return "", errors.New("没找到 Git Bash：装 Git for Windows，或者把这条钩子改成 PowerShell 写法")
}
