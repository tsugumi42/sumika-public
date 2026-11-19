package main

// 后台模型这一头：花钱的门、固定回复的假生成器、抓一批的提示词和回复（报告里漏记抽查要用）。
//
// 三种模式（规格 W6 第 6 点）：
//   - 估算（默认）：用固定的空回复在副本上把整理的循环真跑一遍，数批数和提示词字数，一次模型都不调；
//     切批用的就是生产那套 SummarizeBatch，没有第二份切批逻辑；
//   - fake：固定回复（或测试注入的脚本），整条流水线走一遍，报告里的"新事实"是假的；
//   - real：真的调后台模型，必须同时给 -spend（最多调几次），工具自己不设默认值。
//     今晚（2026-11-02）这条路是关着的：见 realGenReady。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"sumika-native/internal/memory"
)

// errBudget：-spend 的上限到了，这一次调用没有发出去
var errBudget = errors.New("花费上限到了（-spend）")

// realGenReady：-gen real 能不能用。现在关着：当时没有调付费模型的授权，真实生成器也还没接。
// 两道场合门都已经接上了（W5 的 SummarizeBatch 场合门 SkipPlace 是第一道，classify.go 的筛选是第二道，shadow.go 把 -places 交给前者），
// 缺的是：真实生成器（照 internal/app/guarddeps.go 的 memoryGenerator：llm.New(llm.Magpie(baseURL)).Stream，模型 id 取角色的
// Background 或 defaults.backgroundModel）、W5 合进 main、主人看过估算同意 -spend 的数。
// 临时：这几样齐了以后由主会话改成 true 并写进待办
var realGenReady = false

// 估算模式和 fake 模式的固定回复：这一批什么都不记。格式是 prompts/summarize-v2.md 现在的写法
const emptyReply = `{"episode_record":"no","episode_reason":"estimate","ops":[]}`

// fixedGen 永远回同一句话
type fixedGen struct{ reply string }

func (g fixedGen) Call(context.Context, string, string, string) (memory.Reply, error) {
	return memory.Reply{Text: g.reply}, nil
}

var seqLineRE = regexp.MustCompile(`(?m)^\[(\d+)\] `)

// transcriptOf 取提示词里"对话："后面的聊天记录（整理的提示词最后一段）；找不到就整段
func transcriptOf(prompt string) string {
	if i := strings.LastIndex(prompt, "\n对话：\n"); i >= 0 {
		return prompt[i+len("\n对话：\n"):]
	}
	return prompt
}

// firstSeqOf 取聊天记录里第一行的消息序号，没有返回 0
func firstSeqOf(prompt string) int64 {
	m := seqLineRE.FindStringSubmatch(transcriptOf(prompt))
	if m == nil {
		return 0
	}
	var n int64
	fmt.Sscan(m[1], &n)
	return n
}

// demoGen 是命令行的 -gen fake：每一批写一条"主人"的候选事实，引用这一批第一句话，再写一段经历。
// 只用来看报告长什么样，内容是假的
type demoGen struct{}

func (demoGen) Call(_ context.Context, _ string, prompt string, _ string) (memory.Reply, error) {
	seq := firstSeqOf(prompt)
	if seq == 0 {
		return memory.Reply{Text: emptyReply}, nil
	}
	b, _ := json.Marshal(map[string]any{
		"episode_record": "yes",
		"episode":        fmt.Sprintf("（假生成器）这一批从第 %d 条说起。", seq),
		"ops": []any{map[string]any{
			"op": "candidate", "kind": "fact", "subject": "owner", "src": []int64{seq},
			"text": fmt.Sprintf("（假生成器）第 %d 条那一批里主人说了些话", seq),
		}},
	})
	return memory.Reply{Text: string(b)}, nil
}

// captured 是最近一次调用的提示词和回复
type captured struct {
	Called bool
	Prompt string
	Reply  string
}

// spendGen 包住真正要调的生成器：数调用次数和字数，到上限就拒绝，并记下最近一次调用的提示词和回复。
// limit 为 0 表示不限（估算和 fake 不花钱）
type spendGen struct {
	inner memory.Generator
	limit int

	mu          sync.Mutex
	calls       int
	promptRunes int
	replyRunes  int
	exhausted   bool
	last        captured
}

func (g *spendGen) Call(ctx context.Context, model, prompt, cacheKey string) (memory.Reply, error) {
	g.mu.Lock()
	if g.limit > 0 && g.calls >= g.limit {
		g.exhausted = true
		g.mu.Unlock()
		return memory.Reply{}, errBudget
	}
	g.calls++
	g.promptRunes += utf8.RuneCountInString(prompt)
	g.last = captured{Called: true, Prompt: prompt}
	g.mu.Unlock()
	r, err := g.inner.Call(ctx, model, prompt, cacheKey)
	g.mu.Lock()
	g.replyRunes += utf8.RuneCountInString(r.Text)
	g.last.Reply = r.Text
	g.mu.Unlock()
	return r, err
}

// take 取走并清掉最近一次调用的记录（整理循环每批前后各调一次）
func (g *spendGen) take() captured {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.last
	g.last = captured{}
	return c
}

func (g *spendGen) stats() (calls, promptRunes, replyRunes int, exhausted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls, g.promptRunes, g.replyRunes, g.exhausted
}

// episodeReasonOf 从模型的回复里取 episode_reason（memory 包只数、不存它）；取不到返回空
func episodeReasonOf(reply string) string {
	i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if i < 0 || j < i {
		return ""
	}
	var o struct {
		Reason any `json:"episode_reason"`
	}
	if json.Unmarshal([]byte(reply[i:j+1]), &o) != nil {
		return ""
	}
	s, _ := o.Reason.(string)
	return s
}
