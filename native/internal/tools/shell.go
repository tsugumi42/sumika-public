package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/toolchain"
)

// ───────────── powershell（powershell.js、bash.js、utils/shell.js，Sumika 的 withShellTimeout 和 trash.ts） ─────────────

// 照 powershell.js：先把控制台输出设成 UTF-8；pi 的 POWERSHELL_ARGS
// 同一行再把 pwsh 的输出设成纯文本（ADR-182，有意和 Bun 不一样）：她看的是文字，颜色码只是乱码；显式设、不靠 NO_COLOR，
// 后台从哪个环境起来结果都一样。代价：PowerShell 自己输出的字符串里的转义字符也去掉（Get-Content 读到的和文件不同），
// 外部程序的输出、read 工具不受影响；plaintext_test.go 钉着这几条。5.1 没有 $PSStyle、本来就不上色。放在同一行，报错里的行号不变
const utf8OutputPrefix = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}; if ($PSStyle) { $PSStyle.OutputRendering = 'PlainText' }\n"

var powershellArgs = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}

// 命令自己的超时（秒，tools.ts withShellTimeout）：没写或不是正数补 120，超过 600 压到 600
const shellDefaultS, shellMaxS = 120, 600

// 进程退出以后还等多久把管道读完（pi 的 waitForChildProcess：退出后管道空闲 100 毫秒就不等了，每来一块重新计时；
// Go 的 WaitDelay 从退出时算起、不重新计时，规格第七节 A27）
const exitStdioGrace = 100 * time.Millisecond

func shellTimeout(args jsjson.Value) float64 {
	t := float64(shellDefaultS)
	if v := args.Obj["timeout"]; v.Kind == jsjson.Number && v.Num > 0 {
		t = v.Num
	}
	return min(t, shellMaxS)
}

// startPowerShell 照 getPowerShellConfig 的顺序：PATH 的 pwsh → Program Files 的 7 → PATH 的 powershell → 系统自带的 5.1。
func startPowerShell(cwd, command string, output io.Writer) (*exec.Cmd, *shellProcess, error) {
	path, err := findPowerShell()
	if err != nil {
		return nil, nil, err
	}
	args := make([]string, len(powershellArgs)+1)
	copy(args, powershellArgs)
	args[len(powershellArgs)] = command
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = cwd, shellEnv(), output, output
	cmd.WaitDelay = exitStdioGrace
	process, err := startShell(cmd)
	return cmd, process, err
}

func findPowerShell() (string, error) {
	if r, ok := toolchain.Lookup("powershell"); ok {
		return r.Path, nil
	}
	return "", errors.New("No PowerShell executable found. Install PowerShell or add powershell.exe/pwsh.exe to PATH.")
}

// AgentBinDir 是 pi 放 rg、fd 这类工具的目录（getBinDir：PI_CODING_AGENT_DIR 或 ~/.pi/agent，下面的 bin）
func AgentBinDir() string {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".pi", "agent")
	} else {
		dir = normalizePath(dir, false)
	}
	return filepath.Join(dir, "bin")
}

// shellEnv 照 getShellEnv + resolveSpawnContext：PATH 前面加上 pi 的 bin 目录（已经有了就不加），去掉 pi 会话的几个变量
// （Sumika 调工具时不给 pi 的会话，这几个不会再加回去）
func shellEnv() []string {
	drop := map[string]bool{"PI_SESSION_ID": true, "PI_SESSION_FILE": true, "PI_PROVIDER": true, "PI_MODEL": true, "PI_REASONING_LEVEL": true}
	env := os.Environ()
	out := make([]string, 0, len(env))
	bin := AgentBinDir()
	pathDone := false
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		if !pathDone && strings.EqualFold(k, "path") {
			pathDone = true
			has := false
			for _, p := range strings.Split(v, string(os.PathListSeparator)) {
				if p == bin {
					has = true
				}
			}
			if !has {
				if v == "" {
					v = bin
				} else {
					v = bin + string(os.PathListSeparator) + v
				}
			}
			kv = k + "=" + v
		}
		out = append(out, kv)
	}
	if !pathDone {
		out = append(out, "PATH="+bin)
	}
	return out
}

// accWriter 把 stdout、stderr 按到达的先后写进同一个累积器
type accWriter struct {
	mu  *sync.Mutex
	acc *outputAcc
}

func (w accWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.acc.append(p)
	return len(p), w.acc.err
}

func powershell(ctx context.Context, cwd string, args jsjson.Value) (string, error) {
	command, _ := argString(args, "command")
	if v := args.Obj["background"]; v.Kind == jsjson.Bool && v.Bool {
		return startBackground(ctx, cwd, command, args)
	}
	timeout := shellTimeout(args)
	// trash.ts 的 spawnHook 先接上"删除进回收站"的定义，powershell.js 再在最前面接上 UTF-8 输出
	full := utf8OutputPrefix + trashPrefix + "\n" + command
	if err := ctx.Err(); err != nil {
		return "", errors.New("Command aborted")
	}
	if !exists(cwd) {
		return "", fmt.Errorf("Working directory does not exist: %s\nCannot execute PowerShell commands.", cwd)
	}
	acc := newOutputAcc()
	acc.sink, _ = ctx.Value(outputSinkKey{}).(io.Writer)
	var mu sync.Mutex
	w := accWriter{&mu, acc}
	cmd, process, err := startPowerShell(cwd, full, w)
	if err != nil {
		return "", err
	}
	var timedOut, aborted, finished bool
	var killErr error
	var flagMu sync.Mutex
	timer := time.AfterFunc(time.Duration(timeout*float64(time.Second)), func() {
		flagMu.Lock()
		defer flagMu.Unlock()
		if finished {
			return
		}
		timedOut = true
		killErr = errors.Join(killErr, process.killTree())
	})
	stop := context.AfterFunc(ctx, func() {
		flagMu.Lock()
		defer flagMu.Unlock()
		if finished {
			return
		}
		aborted = true
		killErr = errors.Join(killErr, process.killTree())
	})
	waitErr := cmd.Wait()
	timer.Stop()
	stop()
	flagMu.Lock()
	finished = true
	gotAbort, gotTimeout, stopErr := aborted, timedOut, killErr
	flagMu.Unlock()
	closeErr := process.close()
	mu.Lock()
	acc.finish()
	snap := acc.snapshot()
	mu.Unlock()
	if err := errors.Join(stopErr, closeErr, acc.err); err != nil {
		return "", err
	}
	if gotAbort {
		return "", errors.New(appendStatus(formatShellOutput(acc, snap, ""), "Command aborted"))
	}
	if gotTimeout {
		return "", errors.New(appendStatus(formatShellOutput(acc, snap, ""), fmt.Sprintf("Command timed out after %s seconds", jsjson.Number_(timeout))))
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return "", waitErr
	}
	text := formatShellOutput(acc, snap, "(no output)")
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		return "", errors.New(appendStatus(text, fmt.Sprintf("Command exited with code %d", code)))
	}
	return text, nil
}

func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}

// formatShellOutput 只说明预览的截断；全文引用由宿主 finalizeTool 发布。
func formatShellOutput(acc *outputAcc, t tailTruncation, empty string) string {
	text := t.content
	if text == "" {
		text = empty
	}
	if !t.truncated {
		return text
	}
	start, end := t.totalLines-t.outputLines+1, t.totalLines
	switch {
	case t.lastLinePartial:
		// "这一行多大"用的是最后一个换行后面那一段（以换行结尾时是 0B，pi 就是这么写的）
		text += fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s); preview truncated]", formatSize(t.outputBytes), end, formatSize(acc.curLineBytes))
	case t.byLines:
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d; preview truncated]", start, end, t.totalLines)
	default:
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit); preview truncated]", start, end, t.totalLines, formatSize(maxBytes))
	}
	return text
}
