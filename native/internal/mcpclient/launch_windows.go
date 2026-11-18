package mcpclient

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// launchCommand 决定怎么启动一个解析好的程序：.exe、.com 直接启动；.cmd、.bat（npx、npm、yarn 在 Windows 上都是这种）
// 走 cmd.exe /d /v:off /s /c，参数逐个加双引号。cmd 在双引号里仍会展开 %变量%，引号和换行没法安全转义，
// 所以带这些字符的参数直接拒绝（BatBadBut 一类问题，Codex 对 .cmd 也是显式处理）；参数末尾的反斜杠按 C 运行库的规矩加倍。
// 别的后缀（.ps1、.py、.js）不能直接当程序：写成解释器加脚本。
func launchCommand(program string, args []string) (*exec.Cmd, error) {
	switch strings.ToLower(filepath.Ext(program)) {
	case ".exe", ".com":
		return exec.Command(program, args...), nil
	case ".cmd", ".bat":
		var parts []string
		for _, a := range append([]string{program}, args...) {
			q, err := cmdQuote(a)
			if err != nil {
				return nil, fmt.Errorf("经 cmd 启动 %s 时，%w", filepath.Base(program), err)
			}
			parts = append(parts, q)
		}
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		}
		cmd := exec.Command(comspec)
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + comspec + `" /d /v:off /s /c "` + strings.Join(parts, " ") + `"`}
		return cmd, nil
	}
	return nil, fmt.Errorf("%s 不能直接当程序启动；写成解释器加脚本（例如 command 写 python 或 node，脚本放进 args）", filepath.Base(program))
}

// cmdQuote 给 cmd.exe 的命令行里的一个参数加双引号。
func cmdQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\"%\r\n\x00") {
		return "", fmt.Errorf("参数里不能有双引号、百分号或换行（cmd 没法安全转义）：%q", s)
	}
	trail := len(s) - len(strings.TrimRight(s, `\`))
	return `"` + s + strings.Repeat(`\`, trail) + `"`, nil
}
