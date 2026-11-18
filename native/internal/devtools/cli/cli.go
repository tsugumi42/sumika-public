// Package cli 是 sumika-dev 的子命令分派（标准库手工分派，和 cmd/logotool 一样，不引 CLI 框架）。
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"sumika-native/internal/devtools/docs"
	"sumika-native/internal/devtools/private"
	"sumika-native/internal/devtools/selfbuild"
)

const usage = `sumika-dev：Sumika 的开发工具（仓库脚本和钩子的 Go 版，规格-脚本迁Go）
  sumika-dev version    打印编译时写进去的源码摘要
  sumika-dev help       打印这段说明
  sumika-dev check docs [--staged] [--update]
                        文档一致性检查（原 scripts/check-docs.ts）；--staged 读暂存区，--update 写 scripts/docs-baseline.json

  sumika-dev check private --staged | --range A..B | --allow 编号… | --tree 目录 [--allow-missing]
                        隐私扫描（原 scripts/scan-private.ts）；--tree 给导出公开版的自检用

环境变量：
  SUMIKA_DEV_ROOT=<native 或仓库根>   指定模块根（默认从程序所在位置往上找 native/go.mod）
  SUMIKA_DEV_NO_REBUILD=1            不检查源码摘要、不自动重编（给测试用）
`

// Run 执行子命令，返回退出码。
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		d := selfbuild.BuiltDigest
		if d == "" {
			d = "（未写入：不是用 sumika-dev 自己的方式编的）"
		}
		fmt.Fprintf(stdout, "sumika-dev 源码摘要 %s\n", d)
		return 0
	case "__digest":
		// 隐藏子命令：只打编进来的源码摘要，给 selfbuild 拿到锁后问磁盘上的 exe 是不是已经被别的进程换新了
		fmt.Fprintln(stdout, selfbuild.BuiltDigest)
		return 0
	case "check":
		if len(args) >= 2 && args[1] == "docs" {
			root, err := repoRoot()
			if err != nil {
				fmt.Fprintln(stderr, "错误：", err)
				return 1
			}
			return docs.Run(args[2:], root, stdout, stderr)
		}
		if len(args) >= 2 && args[1] == "private" {
			root, err := repoRoot()
			if err != nil {
				fmt.Fprintln(stderr, "错误：", err)
				return 1
			}
			return private.Run(args[2:], root, stdout, stderr)
		}
		fmt.Fprintf(stderr, "check 后面要跟检查名（现有：docs、private）\n%s", usage)
		return 2
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "不认识的子命令 %q\n%s", args[0], usage)
		return 2
	}
}

// repoRoot 是仓库根：模块根（native/）的上一级。和旧脚本“按自己的位置找仓库根”一致，不看当前目录。
func repoRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, e := filepath.EvalSymlinks(exe); e == nil {
		exe = r
	}
	mod, err := selfbuild.FindModRoot(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(mod), nil
}
