package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestOutboundConfirmation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reply, reason string
		err                 error
		blocked             bool
	}{
		{name: "裸否", reply: `{"leaked":"no"}`},
		{name: "围栏否", reply: "\n\n```json\n{\"leaked\":\"no\"}\n```"},
		{name: "围栏是", reply: "```json\n{\"leaked\":\"yes\"}\n```", blocked: true},
		{name: "无语言CRLF", reply: "```\r\n{\"leaked\":\"no\"}\r\n```"},
		{name: "空", reason: "model_bad_json", blocked: true},
		{name: "半截", reply: `{"leaked":`, reason: "model_bad_json", blocked: true},
		{name: "解释夹答案", reply: `说明：{"leaked":"no"}`, reason: "model_bad_json", blocked: true},
		{name: "第二个对象", reply: `{"leaked":"no"}{"leaked":"yes"}`, reason: "model_bad_json", blocked: true},
		{name: "围栏残缺", reply: "```json\n{\"leaked\":\"no\"}", reason: "model_bad_json", blocked: true},
		{name: "围栏后解释", reply: "```json\n{\"leaked\":\"no\"}\n```\n可以发送", reason: "model_bad_json", blocked: true},
		{name: "null", reply: `null`, reason: "model_bad_answer", blocked: true},
		{name: "零", reply: `0`, reason: "model_bad_answer", blocked: true},
		{name: "数组", reply: `[{"leaked":"no"}]`, reason: "model_bad_answer", blocked: true},
		{name: "缺字段", reply: `{}`, reason: "model_bad_answer", blocked: true},
		{name: "空答案", reply: `{"leaked":""}`, reason: "model_bad_answer", blocked: true},
		{name: "坏词", reply: `{"leaked":"maybe"}`, reason: "model_bad_answer", blocked: true},
		{name: "布尔", reply: `{"leaked":false}`, reason: "model_bad_answer", blocked: true},
		{name: "字段为null", reply: `{"leaked":null}`, reason: "model_bad_answer", blocked: true},
		{name: "伪造解释字段", reply: `{"leaked":"no","secret":"编的私事"}`, reason: "model_bad_answer", blocked: true},
		{name: "重复字段", reply: `{"leaked":"yes","leaked":"no"}`, reason: "model_bad_answer", blocked: true},
		{name: "调用失败不能信否", reply: `{"leaked":"no"}`, err: errors.New("编的私事和令牌"), reason: "model_call_error", blocked: true},
		{name: "超时不能信否", reply: `{"leaked":"no"}`, err: fmt.Errorf("编的错误：%w", context.DeadlineExceeded), reason: "model_timeout", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				m, _, _, logs := outboundRig(t, edgeSecret)
				m.opt.Gen = &edgeGen{fn: func(context.Context) (Reply, error) { return Reply{Text: tc.reply}, tc.err }}
				var want []string
				route := "normal"
				if tc.blocked {
					want = []string{"f:100"}
				}
				if tc.reason != "" {
					route = "model-down"
				}
				edgeExpect(t, m, a, edgeQuery, want, route)
				if tc.reason != "" && !strings.Contains(logs.String(), "reason="+tc.reason) {
					t.Fatalf("原因丢失：%s", logs.String())
				}
				if strings.Contains(logs.String(), "编的") {
					t.Fatalf("模型内容泄漏：%s", logs.String())
				}
			})
		})
	}
}

func TestOutboundConfirmationPrivacy(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"私事是虚构人物青禾住在橙色月亮上" + strings.Repeat("密", 220),
		`{"leaked":"no","secret":"虚构人物青禾住在橙色月亮上"}`,
		`{"leaked":"虚构人物青禾住在橙色月亮上"}`,
	} {
		m, _, g, logs := outboundRig(t, edgeSecret)
		g.reply = text
		outboundCheck(t, m, edgeQuery, []string{"f:100"}, "model-down")
		log := logs.String()
		if strings.Contains(log, "青禾") || strings.Contains(log, "reply=") || !strings.Contains(log, fmt.Sprintf("reply_chars=%d", utf8.RuneCountInString(text))) {
			t.Fatalf("输出泄漏或长度不对：%s", log)
		}
	}
	m, _, _, logs := outboundRig(t, edgeSecret)
	text := "```json\n" + strings.Repeat(" ", 220) + `{"leaked":"no"}` + "\n```"
	m.opt.Gen = &edgeGen{fn: func(context.Context) (Reply, error) { return Reply{Text: text}, context.DeadlineExceeded }}
	outboundCheck(t, m, edgeQuery, []string{"f:100"}, "model-down")
	if !strings.Contains(logs.String(), fmt.Sprintf("reply=%q", string([]rune(text)[:200]))) || strings.Contains(logs.String(), "leaked") {
		t.Fatalf("预览超过200字或丢失：%s", logs.String())
	}
}

func TestOutboundConfirmationLateNo(t *testing.T) {
	t.Parallel()
	m, _, _, logs := outboundRig(t, edgeSecret)
	m.opt.GuardConfirmTimeout = time.Nanosecond
	m.opt.Gen = &edgeGen{fn: func(ctx context.Context) (Reply, error) { <-ctx.Done(); return Reply{Text: `{"leaked":"no"}`}, nil }}
	outboundCheck(t, m, edgeQuery, []string{"f:100"}, "model-down")
	if !strings.Contains(logs.String(), "reason=model_timeout") {
		t.Fatal(logs.String())
	}
}
