// sumika-dev：Sumika 的开发工具程序，仓库脚本和钩子逐个改写成它的子命令（规格-脚本迁Go、ADR-389）。
// 编好放在 native/bin/sumika-dev.exe；启动时先核对自己的源码摘要，对不上就重编自己再执行（internal/devtools/selfbuild）。
// 逻辑都在 internal/devtools/ 下，这里只做重编检查和分派。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"sumika-native/internal/devtools/cli"
	"sumika-native/internal/devtools/gitrun"
	"sumika-native/internal/devtools/notice"
	"sumika-native/internal/devtools/selfbuild"
)

func main() { os.Exit(run()) }

func run() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误： 找不到自己的路径：", err)
		return 1
	}
	if r, e := filepath.EvalSymlinks(exe); e == nil {
		exe = r
	}
	gitrun.Program = "git" // internal/ 里不许写字面量程序名（archcheck 外部程序规则），由这里注入
	notice.GhProgram = "gh"
	if len(os.Args) > 1 && (os.Args[1] == "notice" || os.Args[1] == "__srcdigest") {
		// 报告提醒是每条用户消息都跑的钩子（__srcdigest 是它兜底编译时问源码摘要用的，同样不能先重编）：不做前台重编（重编要等锁、编译，最长几分钟，会拖住用户发消息），程序旧一点也照跑；
		// 程序由别的关卡（提交钩子）或 settings.json 里的兜底 go build 保持最新
		return cli.Run(os.Args[1:], os.Stdout, os.Stderr)
	}
	selfbuild.CleanOld(exe)
	if done, code := selfbuild.Prepare(exe, selfbuild.Env{
		Exe: exe, Args: os.Args[1:], Stderr: os.Stderr,
		Build: selfbuild.GoBuild, Exec: selfbuild.RunExe, DiskDigest: selfbuild.DiskDigest,
	}); done {
		return code
	}
	return cli.Run(os.Args[1:], os.Stdout, os.Stderr)
}
