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
	selfbuild.CleanOld(exe)
	if done, code := selfbuild.Prepare(exe, selfbuild.Env{
		Exe: exe, Args: os.Args[1:], Stderr: os.Stderr,
		Build: selfbuild.GoBuild, Exec: selfbuild.RunExe, DiskDigest: selfbuild.DiskDigest,
	}); done {
		return code
	}
	return cli.Run(os.Args[1:], os.Stdout, os.Stderr)
}
