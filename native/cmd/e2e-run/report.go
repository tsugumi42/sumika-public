package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type round struct {
	Step           step
	Started, Ended time.Time
	State, Error   string
	Sent           []sentMessage
	Calls          []modelCall
}
type report struct {
	Name, Model, Gateway, Data, Error, Backfill string
	Started, Ended                              time.Time
	CPU                                         time.Duration
	Peak                                        uint64
	Rounds                                      []round
	Calls                                       []modelCall
	Evidence                                    map[string][]map[string]any
	Logs                                        []string
	Actions                                     []string
	Files                                       []string
	Deleted                                     bool
}
type logbook struct {
	mu    sync.Mutex
	lines []string
}

func (l *logbook) printf(f string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, time.Now().Format(time.RFC3339Nano)+" "+fmt.Sprintf(f, args...))
}
func (l *logbook) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}
func percentile(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[max(0, int(math.Ceil(float64(len(s))*p))-1)]
}
func tokenTotals(calls []modelCall) (prompt, output, cache, write, reason int64, unknown int) {
	for _, c := range calls {
		prompt += c.Prompt
		output += c.Output
		cache += c.CacheRead
		write += c.CacheWrite
		reason += c.Reasoning
		if !c.UsageSeen {
			unknown++
		}
	}
	return
}
func fenced(b *strings.Builder, text string) {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	b.WriteString(fence + "\n" + text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(fence + "\n\n")
}
func renderReport(r report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 后台真跑报告：%s\n\n开始：%s\n\n结束：%s；墙钟：%s\n\n模型：%s；网关：%s\n\n数据：%s；已删除：%t\n\n", r.Name, r.Started.Format(time.RFC3339), r.Ended.Format(time.RFC3339), r.Ended.Sub(r.Started).Round(time.Millisecond), r.Model, r.Gateway, r.Data, r.Deleted)
	if r.Error != "" {
		fmt.Fprintf(&b, "运行失败：%s\n\n", r.Error)
	}
	fmt.Fprintf(&b, "进程 CPU：%s；OS 工作集峰值：%d bytes（%.2f MiB）。范围：e2e-run 内的 app、假协议端、图片和网关计数代理；不含编译、网关服务及 app 管理的嵌入子进程。\n\n", r.CPU.Round(time.Millisecond), r.Peak, float64(r.Peak)/(1<<20))
	prompt, output, cache, write, reason, unknown := tokenTotals(r.Calls)
	fmt.Fprintf(&b, "模型调用：%d；输入总 token：%d（含缓存读 %d、写 %d）；输出：%d（含思考 %d）；未取得 usage：%d。未取得用量不代表免费或零 token。\n\n", len(r.Calls), prompt, cache, write, output, reason, unknown)
	first, last := []int64{}, []int64{}
	for _, v := range r.Rounds {
		if len(v.Sent) > 0 {
			first = append(first, v.Sent[0].At.Sub(v.Started).Milliseconds())
			last = append(last, v.Sent[len(v.Sent)-1].At.Sub(v.Started).Milliseconds())
		}
	}
	if len(first) == 0 {
		b.WriteString("回复延迟：无实际发送样本，P50/P95 不可用。\n\n")
	} else {
		fmt.Fprintf(&b, "回复延迟（%d 轮，nearest rank）：首段 P50 %d ms / P95 %d ms；末段 P50 %d ms / P95 %d ms。起点是假协议端开始写入入站帧，终点是该假端收到发送 action；无回复轮次不计零值。\n\n", len(first), percentile(first, .5), percentile(first, .95), percentile(last, .5), percentile(last, .95))
	}
	fmt.Fprintf(&b, "断线补录：%s\n\n", r.Backfill)
	b.WriteString("我定的：命令名 e2e-run；角色岚禾；真实小型 gte 嵌入以满足现有发送门（未加载本地生成式大模型）；remember 开、后台整理关；群聊隐私只记录，不自动判对错。\n\n")
	for _, v := range r.Rounds {
		fmt.Fprintf(&b, "## %s · %s\n\n状态：%s；耗时：%d ms；错误：%s\n\n输入：\n\n", v.Step.ID, v.Step.Kind, v.State, v.Ended.Sub(v.Started).Milliseconds(), v.Error)
		fenced(&b, v.Step.Text)
		if len(v.Sent) == 0 {
			b.WriteString("没有发出回复。\n\n")
		}
		for _, s := range v.Sent {
			fmt.Fprintf(&b, "回复原文（%d ms，%s）：\n\n", s.At.Sub(v.Started).Milliseconds(), s.Action)
			fenced(&b, s.Text)
			b.WriteString("发送段原始 JSON：\n\n")
			fenced(&b, string(s.Raw))
		}
		p, o, c, w, re, u := tokenTotals(v.Calls)
		fmt.Fprintf(&b, "本步骤调用 %d；输入 %d、输出 %d、缓存读 %d、写 %d、思考 %d；usage 未取得 %d。\n\n", len(v.Calls), p, o, c, w, re, u)
	}
	b.WriteString("## 模型调用明细\n\n")
	js, _ := json.MarshalIndent(r.Calls, "", "  ")
	fenced(&b, string(js))
	keys := make([]string, 0, len(r.Evidence))
	for k := range r.Evidence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "## %s\n\n", k)
		js, _ := json.MarshalIndent(r.Evidence[k], "", "  ")
		fenced(&b, string(js))
	}
	b.WriteString("## 下载文件\n\n")
	for _, file := range r.Files {
		fmt.Fprintf(&b, "- %s\n", file)
	}
	b.WriteByte('\n')
	b.WriteString("## 警告和错误\n\n日志没有统一级别字段；以下按 warn/error/失败/出错/错误/警告/保持/未/没有 等关键词摘录，完整日志见 backend.log。\n\n")
	var warnings []string
	for _, line := range r.Logs {
		lower := strings.ToLower(line)
		for _, key := range []string{"warn", "error", "失败", "出错", "错误", "警告", "保持", "尚未", "没有"} {
			if strings.Contains(lower, key) {
				warnings = append(warnings, line)
				break
			}
		}
	}
	fenced(&b, strings.Join(warnings, "\n"))
	b.WriteString("## OneBot 动作\n\n")
	fenced(&b, strings.Join(r.Actions, "\n"))
	return b.String()
}
