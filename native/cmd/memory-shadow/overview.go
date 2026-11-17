package main

// 估算 / 总览文字（报告 A 节，也是命令行打印的东西）。会话 id、范围里的号码一律只留末四位（maskNumbers）；这里不出现聊天正文

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/memory"
)

var longDigits = regexp.MustCompile(`\d{5,}`)

// maskNumbers：5 位以上的连续数字只留末四位（QQ 号、群号）。报告给主人抽检，号码不用完整的
func maskNumbers(s string) string {
	return longDigits.ReplaceAllStringFunc(s, func(m string) string { return "…" + m[len(m)-4:] })
}

// 估算输出按每批多少 token 算（起步值，没量过；真跑以后用实际的）
const (
	estOutTokensPerBatch = 400
)

func modeLabel(m mode) string {
	switch m {
	case modeFake:
		return "假生成器试跑"
	case modeReal:
		return "真跑"
	}
	return "估算"
}

// sweepText 是启动补提升这一趟的账，总览、报告 C 节、运行日志用同一句。
// Checked 只数范围在放行场合里（-places）、种类能自动提升、证据行数够的待确认候选；这三样都够了、范围却不放行的另数在 Skipped
// （不进事务，启动这一刻不能悄悄把群里、别人私聊里的候选变成有效）
func sweepText(s memory.ConfirmSweepResult) string {
	return fmt.Sprintf("看了 %d 条范围在放行场合里的待确认，提升 %d 条，%d 条因为想取代的旧事实受保护没提升，另有 %d 条范围不在放行的场合里、没碰",
		s.Checked, s.Confirmed, s.Blocked, s.Skipped)
}

func yesNo(b bool, y, n string) string {
	if b {
		return y
	}
	return n
}

// estimateText 是 A 节：窗口、游标模式、放行的场合、会话数、批数、花费、每个会话落后多少条
func estimateText(r *runResult) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	p("影子整理（%s）", modeLabel(r.Mode))
	switch r.Mode {
	case modeEstimate:
		p("一次后台模型都没调。批数和提示词字数是用固定的空回复，把整理的循环在副本上真跑了一遍数出来的，切批走的是生产同一套代码。")
	case modeFake:
		p("用的是假生成器：报告里的\"新事实\"和\"片段\"都是假的，只用来看流水线和报告长什么样。")
	case modeReal:
		p("调了后台模型，花了钱：调用次数和字数见下面的\"花费\"。")
	}
	p("角色：%s", r.Char)
	if r.Snap != nil {
		p("拷贝：%d 个文件，第 %d 次拷到一致，耗时 %s（拷贝期间后台是退出的，这段时间就是 QQ 的空档）；副本 integrity_check %s",
			len(r.Snap.Files), r.Snap.Attempts, r.Snap.Elapsed.Round(time.Millisecond), yesNo(r.Snap.IntegrityOK, "通过", "没过"))
		p("原库只在拷贝那一段被读；工具只证明\"拷贝那一刻是一致的\"，不声称原库全程没动。")
	}
	if r.CursorMode == "seed" {
		p("【游标不是真实的】每个放行的会话的游标是种的：窗口从 %s 起，所以下面的批数不是\"开后台整理第一刻真会处理的那一段\"。", r.SeedSince.Local().Format("2006-01-02 15:04"))
	} else {
		p("游标：用副本里的真实游标（导入搬过来的，加上只读期新来的聊天），所以批数就是开后台整理的第一刻真会处理的那一段。")
	}
	p("放行的场合（-places）：%s", strings.Join(r.Places, "、"))
	p("整理提示词：%s（内容哈希 %s，换行统一成 LF 以后算）", r.PromptSource, r.PromptSHA)
	if n := r.Counts["prompt_stale"]; n > 0 {
		p("【这份提示词里没有 {{candidates}}】候选块和 evidence 动作是关着的（prompt_stale %d 次）：待确认的事实攒不到证据，报告 C 节不会有\"会提升的\"。正式开的时候用这份提示词也一样。", n)
	}

	p("autoConfirm：副本里强制打开（正式配置里默认关）；启动补提升：%s（提升的条目见报告 C 节）", sweepText(r.Sweep))

	p("")
	p("按场合（只数放行的会话；人数是游标之后说过话的不同的真人）：")
	for _, k := range summarizeKinds(r.Sessions) {
		p("  %s：会话 %d 个，放行 %d 个，游标落后 %d 条，涉及 %d 人", k.Kind, k.Sessions, k.Allowed, k.Behind, k.People)
	}

	p("")
	p("会话明细（号码只留末四位）：")
	noCursor := 0
	capHit := 0
	for _, s := range r.Sessions {
		if !s.Allowed {
			continue
		}
		var tags []string
		switch {
		case !s.HasCursor:
			noCursor++
			tags = append(tags, "没有游标（新会话，真开时从当时的最新一条起、不回填）")
		default:
			tags = append(tags, fmt.Sprintf("游标 %d，最新 %d，落后 %d 条", s.Cursor, s.LastSeq, s.Behind))
		}
		if s.Batches > 0 || r.Mode != modeEstimate || s.HasCursor {
			tags = append(tags, fmt.Sprintf("跑了 %d 批", s.Batches))
		}
		if s.CapHit {
			capHit++
			tags = append(tags, "到 -max-batches 就停了，后面还有")
		}
		if s.NotRun {
			tags = append(tags, "花费上限到了，没跑")
		}
		if s.PlaceSkipped {
			tags = append(tags, "停在 W5 的场合门（不是主人的私聊，或会话 id 认不出），一批也没整理")
		}
		if s.Err != "" {
			tags = append(tags, "出错（原因 "+s.Err+"）")
		}
		p("  [%s] %s", s.Kind, maskNumbers(s.Conv)+"："+strings.Join(tags, "；"))
	}
	if noCursor > 0 {
		p("没有游标的放行会话 %d 个，这次一批也没整理。", noCursor)
	}

	p("")
	p("批数和花费：")
	modelBatches := 0
	for _, bt := range r.Batches {
		if bt.ModelCalled {
			modelBatches++
		}
	}
	if r.Mode == modeEstimate {
		p("  整理了 %d 批（其中该调后台模型的 %d 批），该调模型 %d 次（估算模式没有真调）", len(r.Batches), modelBatches, r.Calls)
	} else {
		p("  整理了 %d 批（其中调了后台模型的 %d 批），模型调用 %d 次", len(r.Batches), modelBatches, r.Calls)
	}
	p("  给模型的提示词合计 %d 个码点，约 %d token（按 1 个码点 ≈ 1 token 往多里估，实际更少）", r.PromptRunes, r.PromptRunes)
	if r.Mode == modeEstimate {
		p("  模型输出没算（估算不调模型），按每批 %d token 估约 %d token（起步值，没量过）", estOutTokensPerBatch, r.Calls*estOutTokensPerBatch)
	} else {
		p("  模型回复合计 %d 个码点", r.ReplyRunes)
	}
	if r.CallCap > 0 {
		p("  花费上限 -spend %d 次；%s", r.CallCap, yesNo(r.Truncated, "被预算截断：后面的批没跑，不能当成\"整理完了\"", "没有碰到上限"))
	}
	if capHit > 0 {
		p("  到 -max-batches 就停的会话 %d 个：后面还有批没跑", capHit)
	}

	if len(r.Counts) > 0 {
		p("")
		p("各类计数（照 memory 包的键）：")
		keys := make([]string, 0, len(r.Counts))
		for k := range r.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p("  %s = %d", k, r.Counts[k])
		}
	}
	return b.String()
}
