package policy

import (
	"regexp"
	"sort"
	"strings"
)

// Sumika 自己的命令行遥控器 sumika-ctl（规格-本机控制接口）：它经本机管道对开着的程序做事，会改掉安全边界的那些
// （解除 MCP 隔离、改群档位、退出、开始或停止接收、做备份、恢复检查点……）不能让模型在最宽档（yolo）里不经主人点卡就跑。
// 做法（ADR-382，主人 2026-11-15 拍板“只钉死几个关键的”）：只读的子命令照原来的档位走；其余的、认不出的、新加还没登记的，
// 任何档位都要主人看过，只能批这一次，不吃 yolo、不吃已有的授权，也不产生能被“这个对话”授权的键。
//
// 这是一道减速带，不是墙。它防的是：模型顺手跑、或被提示注入后照技能页里的命令去跑 sumika-ctl。
// 它防不了存心绕的：工作区里改名的副本、8.3 短名、通配符、指向不含 sumika-ctl 字样的程序的别名、先写脚本再跑——
// 这些在最宽档本来就是“能跑工作区里的任意程序”。也管不到 mcp_call（它交给判定的是空参数，主人若配了能跑 shell 的 MCP 服务，
// yolo 下可以经它绕过）。直连控制管道、改数据目录这两条路由别的规则挡着（三档都问）。后续由 P4-3 用
// “管道服务端判断调用方是不是 Sumika 的子孙进程”补上。
//
// 怎么认（原则：分析器看得清的，按它看到的程序和参数判；文字规则只留给看不清的）：
//   - 分析器拆得清（SelfCtlNeedsAsk 里 Verdict 不 opaque）：逐条简单命令各自判，任何一条命中整条就问。命中的只有：
//     被执行的程序是 sumika-ctl（各种路径、.exe、引号、& 调用、续行、串联、$( )、脚本块里的都算）且子命令不在只读名单；
//     go run 一个路径含 cmd/sumika-ctl 的包；schtasks、Set-Alias 这类“把命令行当参数去起进程或存成别名”的命令的参数里有 sumika-ctl。
//     别的程序的参数里只是提到这个词（rg、git commit -m、go build/test/vet、Set-Content、Remove-Item、Get-Process……）不算。
//   - 分析器看不清（cmd /c、bash -c、pwsh -c、iex、变量当程序名、语法错、分析失败）：退回文字规则，文字里出现 sumika-ctl 且后面不是只读子命令就问。
//
// 子命令分两张表。新加子命令必须登记进其中一张：selfctl_test.go 的守卫读命令分发表和用法文字核对，
// ctlserver 的对拍测试核对只读名单和服务端的 readOnly 表一致；运行时没登记的一律当“要问”。

// ctlReadOnlyList 是不改任何状态的子命令（和 ctlserver 的 readOnly 表一致，对拍测试守着）。键是“命令”或“命令 子命令”。
// backup restore（往主人指定的位置写一整份数据目录的副本，含明文配置和聊天库）和 backup verify（写几个 GB 的临时文件、占着备份那把锁、最后删一个目录）
// 都不是只读，进“要问”（规格-数据目录与备份 10.10.5）。
// 两条有副作用但不碰安全边界、所以留在这里：gate list 首次使用会生成本机句柄密钥并写进设置文件；
// link preview 会联网取链接并写 24 小时缓存。拿不准的不放进来。
var ctlReadOnlyList = []string{"status", "doctor", "logs",
	"stats usage", "stats quality", "stats spans", "stats rebuild", "stats resources", "stats storage",
	"link preview", "link template", "proactive log", "artist list", "backup list", "gate list",
	"backfill status", "backfill gaps", "checkpoint list", "checkpoint preview", "checkpoint status",
	"whisper status", "goal show", "loop list", "mcp status", "db status", "kernel faults", "secrets status", "secrets list"}

// ctlAskList 是已经看过、会改状态或拿不准的子命令：任何档位都要问。运行时认不出的也走“要问”，这张表是给守卫测试核对用的。
// 光写 goal、loop（不带子命令）也在这里：它们是同类里唯一靠“后面没有词”判只读的，不留特例。
var ctlAskList = []string{"quit", "receive start", "receive stop", "window show", "gate set",
	"backfill retry", "backfill run", "checkpoint restore", "checkpoint cancel", "chat", "trust",
	"goal", "goal set", "goal edit", "goal pause", "goal resume", "goal clear",
	"loop", "loop add", "loop edit", "loop pause", "loop resume", "loop remove",
	"whisper create", "whisper cancel", "whisper log", "whisper list",
	"artist import", "artist confirm", "artist remove", "backup now", "backup restore", "backup verify",
	"mcp release", "mcp reconnect", "kernel retry",
	"secrets set", "secrets delete", "secrets set-passphrase", "secrets unlock",
	"auto-receive on", "auto-receive off", "autostart on", "autostart off"}

var ctlReadOnly, ctlAsk = keySet(ctlReadOnlyList), keySet(ctlAskList)

func keySet(l []string) map[string]bool {
	m := map[string]bool{}
	for _, k := range l {
		m[k] = true
	}
	return m
}

// CtlReadOnlyKeys、CtlAskKeys 给别的包的对拍测试用（ctlserver 核对只读名单和服务端 readOnly 表）
func CtlReadOnlyKeys() []string { return sortedKeys(ctlReadOnly) }
func CtlAskKeys() []string      { return sortedKeys(ctlAsk) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ctlNoSub 是后面不跟子命令的命令：键就是命令名本身，后面是参数
var ctlNoSub = keySet([]string{"status", "logs", "quit", "chat", "trust"})

// CtlKey 是子命令在两张表里的键
func CtlKey(cmd, sub string) string {
	if sub == "" {
		return cmd
	}
	return cmd + " " + sub
}

// ctlWord 是命令文字里的 sumika-ctl（可带 .exe）；前面是什么都认
var ctlWord = regexp.MustCompile(`(?i)sumika-ctl(\.exe)?`)

// goRunCtl 是 go run 一个路径含 cmd/sumika-ctl 的包（含末尾带斜杠的写法）
var goRunCtl = regexp.MustCompile(`go\s+run\b[^;&|\n]*cmd/sumika-ctl`)

// ctlSep 是一条命令的边界：到这里为止是这一次 sumika-ctl 调用的参数
var ctlSep = regexp.MustCompile("[;&|(){}\r\n]")

// ctlQuotes 去掉引号和反引号：'sumika-c'+'tl'、sumika-c`tl、& 'D:\x\sumika-ctl.exe' 都还原成能认的样子
var ctlQuotes = strings.NewReplacer("'", "", `"`, "", "`", "")

// ctlContinuation 是续行：PowerShell 的反引号加换行、bash 的反斜杠加换行。实际跑的时候它们是一个空格，
// 不先折掉，换行会把 goal 和后面的 clear 切成两段（`sumika-ctl goal` 反引号换行 `clear` 就是 goal clear）
var ctlContinuation = regexp.MustCompile("(`|\\\\)[ \t]*\r?\n[ \t]*")

// TouchesSelfCtlWrite 是文字规则（分析器看不清时的退路）：命令文字里有没有一处 sumika-ctl 调用不是明确的只读子命令。
func TouchesSelfCtlWrite(command string) bool {
	text := strings.ToLower(ctlQuotes.Replace(ctlContinuation.ReplaceAllString(command, " ")))
	if goRunCtl.MatchString(strings.ReplaceAll(text, `\`, "/")) {
		return true
	}
	norm := strings.ReplaceAll(text, `\`, "/")
	for _, loc := range ctlWord.FindAllStringIndex(text, -1) {
		if strings.HasSuffix(norm[:loc[0]], "cmd/") {
			continue // 源码目录 cmd/sumika-ctl（go build、go test 的参数）；go run 它由上面的 goRunCtl 认
		}
		end := loc[1]
		if end < len(text) {
			switch c := text[end]; {
			case c == '\\' || c == '/':
				continue // 是目录名（cmd\sumika-ctl\run.go），不是在运行它
			case c == '.':
				return true // sumika-ctl.cmd 之类：认不准，问
			case c == '-':
				// sumika-ctl-new.exe、sumika-ctl-old-abc 这类改名副本：是同一个程序，把这个词读完再判后面的参数
				for end < len(text) && (text[end] == '-' || text[end] == '_' || text[end] == '.' || (text[end] >= 'a' && text[end] <= 'z') || (text[end] >= '0' && text[end] <= '9')) {
					end++
				}
			case c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
				continue // 另一个程序名的一部分
			}
		}
		rest := text[end:]
		if i := ctlSep.FindStringIndex(rest); i != nil {
			rest = rest[:i[0]]
		}
		if !ctlReadOnlyInvocation(strings.Fields(rest)) {
			return true
		}
	}
	return false
}
