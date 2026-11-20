// Package cli 是 sumika-dev 的子命令分派（标准库手工分派，和 cmd/logotool 一样，不引 CLI 框架）。
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sumika-native/internal/devtools/conflicts"
	"sumika-native/internal/devtools/docs"
	"sumika-native/internal/devtools/gofmt"
	"sumika-native/internal/devtools/hook"
	"sumika-native/internal/devtools/notice"
	"sumika-native/internal/devtools/private"
	"sumika-native/internal/devtools/selfbuild"
	"sumika-native/internal/devtools/uiskill"
)

const usage = `sumika-dev：Sumika 的开发工具（仓库脚本和钩子的 Go 版，规格-脚本迁Go）
  sumika-dev version    打印编译时写进去的源码摘要
  sumika-dev help       打印这段说明
  sumika-dev check docs [--staged] [--update]
                        文档一致性检查（原 scripts/check-docs.ts）；--staged 读暂存区，--update 写 scripts/docs-baseline.json

  sumika-dev check private --staged | --range A..B | --allow 编号… | --tree 目录 [--allow-missing]
                        隐私扫描（原 scripts/scan-private.ts）；--tree 在扫描规则改了以后手动扫一遍当前全部文件

  sumika-dev check conflicts
                        暂存区新增的行里有没有没解决的合并冲突标记（原 scripts/check-conflict-markers.ts）
  sumika-dev check gofmt 文件…
                        暂存的 .go 文件（路径从仓库根算）有没有 gofmt，查暂存区里的内容（原 scripts/check-gofmt.ts）
  sumika-dev check ui-skill
                        界面技能入口页（.claude/skills/mygo-ui/SKILL.md）点名的包、函数和 mygo 版本要和代码对得上（原 scripts/check-ui-skill.ts）

  sumika-dev hook pre-commit
                        提交前检查的统一入口（lefthook 的 pre-commit 只调它）：按暂存区文件挑检查，一个进程里并行跑；任何一项红退出码就是 1
  sumika-dev hook commit-msg 说明文件
                        提交说明的标题检查（原 scripts/check-commit-msg.ts）：超 40 字、带分号、带 ADR 编号就拦
  sumika-dev notice [--reports 目录] [--no-ci]
                        主会话的报告提醒（原 scripts/report-notice.ts，Claude Code 的 UserPromptSubmit 钩子）：从标准输入读钩子 JSON，
                        把近两天复盘、巡检、早报里这个会话没看过的“## 给主会话”，加上 gh 查到的公开仓库 main 近 48 小时 CI 失败（--no-ci 不查）打出来；永远退出码 0，出错不挡发消息

环境变量：
  SUMIKA_DEV_ROOT=<native 或仓库根>   指定模块根（默认从程序所在位置往上找 native/go.mod）
  SUMIKA_DEV_NO_REBUILD=1            不检查源码摘要、不自动重编（给测试用）
`

// checks 是 check 下的检查：名字 → 入口（参数是名字后面的部分，root 是仓库根）。新迁进来的关卡在这里加一行。
var checks = map[string]func(args []string, root string, stdout, stderr io.Writer) int{
	"docs":      docs.Run,
	"private":   private.Run,
	"conflicts": conflicts.Run,
	"gofmt":     gofmt.Run,
	"ui-skill":  uiskill.Run,
}

func checkNames() []string {
	var names []string
	for n := range checks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func hookNames() []string {
	var names []string
	for n := range hook.Hooks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

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
	case "__srcdigest":
		// 隐藏子命令：算磁盘上现在这份源码的摘要（不是编进来的那个），给 settings.json 的兜底 go build 带 -ldflags 用，
		// 编出来的程序摘要就是对的，以后别的关卡不用再重编一次
		root, err := modRoot()
		if err != nil {
			fmt.Fprintln(stderr, "错误：", err)
			return 1
		}
		d, err := selfbuild.Digest(root)
		if err != nil {
			fmt.Fprintln(stderr, "错误：", err)
			return 1
		}
		fmt.Fprintln(stdout, d)
		return 0
	case "check":
		if len(args) >= 2 {
			if run, ok := checks[args[1]]; ok {
				root, err := repoRoot()
				if err != nil {
					fmt.Fprintln(stderr, "错误：", err)
					return 1
				}
				return run(args[2:], root, stdout, stderr)
			}
		}
		fmt.Fprintf(stderr, "check 后面要跟检查名（现有：%s）\n%s", strings.Join(checkNames(), "、"), usage)
		return 2
	case "hook":
		if len(args) >= 2 {
			if run, ok := hook.Hooks[args[1]]; ok {
				root, err := repoRoot()
				if err != nil {
					fmt.Fprintln(stderr, "错误：", err)
					return 1
				}
				return run(args[2:], root, stdout, stderr)
			}
		}
		fmt.Fprintf(stderr, "hook 后面要跟钩子名（现有：%s）\n%s", strings.Join(hookNames(), "、"), usage)
		return 2
	case "notice":
		// 钩子：任何错误都不能挡住用户发消息，找不到仓库根也只写一行 stderr、退出码 0
		root, err := repoRoot()
		if err != nil {
			fmt.Fprintln(stderr, "notice：找不到仓库根，已忽略：", err)
			return 0
		}
		return notice.Run(args[1:], root, os.Stdin, stdout, stderr)
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
	mod, err := modRoot()
	if err != nil {
		return "", err
	}
	return filepath.Dir(mod), nil
}

// modRoot 是模块根（native/，含 go.mod）：按程序自己所在的位置找。
func modRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, e := filepath.EvalSymlinks(exe); e == nil {
		exe = r
	}
	return selfbuild.FindModRoot(exe)
}
