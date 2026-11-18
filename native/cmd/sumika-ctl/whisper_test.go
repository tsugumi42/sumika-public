package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// 参数 → 请求：选项各进各的字段；create 默认时限 120 秒；status、cancel 不带时限
func TestWhisperArgsBecomeRequest(t *testing.T) {
	const v = ctlproto.Version
	cases := []struct {
		args []string
		want ctlproto.Request
	}{
		{[]string{"whisper", "create", "--selftest", "--character", "a,b", "--source-text", "这个梗", "--say", "查查"},
			ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"create"}, Character: "a,b", Wait: "2m0s", Whisper: &ctlproto.WhisperArgs{Selftest: true, SourceText: "这个梗", Say: "查查"}}},
		{[]string{"--json", "whisper", "create", "--selftest", "--character=a", "--source-text=x", "--quote-name=某人", "--say=y", "--timeout", "30s"},
			ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"create"}, Character: "a", Wait: "30s", Whisper: &ctlproto.WhisperArgs{Selftest: true, SourceText: "x", QuoteName: "某人", Say: "y"}}},
		{[]string{"whisper", "create", "--character", "a", "--source-text", "x", "--say", "y"}, // 没写 --selftest：交给服务端拒
			ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"create"}, Character: "a", Wait: "2m0s", Whisper: &ctlproto.WhisperArgs{SourceText: "x", Say: "y"}}},
		{[]string{"whisper", "status", "B1"}, ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"status", "B1"}}},
		{[]string{"whisper", "cancel", "B1"}, ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"cancel", "B1"}}},
		{[]string{"whisper", "log", "--conversation", "console:a:topic:t", "--batch=B1", "--limit", "20", "--cursor", "1|B1|x", "--with-thinking"},
			ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: "console:a:topic:t", Batch: "B1", Limit: 20, Cursor: "1|B1|x", WithThinking: true}}},
		{[]string{"whisper", "log", "--conversation", "console:a:topic:t", "--entry", "say:B1"},
			ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: "console:a:topic:t", Entry: "say:B1"}}},
		{[]string{"whisper", "list", "--selftest"}, ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"list"}, Whisper: &ctlproto.WhisperArgs{Selftest: true}}},
		{[]string{"whisper", "log", "--conversation", "x", "--limit", "0"}, // 不合格的页大小交给服务端拒
			ctlproto.Request{V: v, Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: "x", Limit: -1}}},
	}
	for _, c := range cases {
		o, err := parseArgs(c.args)
		if err != nil {
			t.Fatalf("%q：%v", c.args, err)
		}
		got, _ := json.Marshal(o.req)
		want, _ := json.Marshal(c.want)
		if string(got) != string(want) {
			t.Errorf("%q：\n得到 %s\n应是 %s", c.args, got, want)
		}
	}
	for _, bad := range [][]string{{"whisper", "create", "--say"}, {"whisper", "create", "--source-text"}, {"whisper", "create", "--quote-name"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("%q 该报命令行写错", bad)
		}
	}
}

// 渲染：各角色的状态、她的处理记录（只当数据）、失败时 data 也交出来
func TestRenderWhisper(t *testing.T) {
	r := ctlproto.WhisperResult{Batch: "B1", Status: ctlproto.WhisperPending, ElapsedMS: 1500, Entries: []ctlproto.WhisperEntry{
		{Character: "a", State: ctlproto.WhisperDone, Note: "查到了，是个梗"},
		{Character: "b", State: ctlproto.WhisperQueued, Position: 2, SourceTurnRunning: true},
		{Character: "c", State: ctlproto.WhisperFailed, Detail: "超时"},
	}}
	var out bytes.Buffer
	renderWhisper(&out, r)
	for _, w := range []string{"批号：B1", "到时限还有没处理完的", "a：完成", "处理记录：查到了，是个梗", "b：排队中", "前面还有 2 个", "没打断，后台处理", "c：失败", "说明：超时", "用时 1.5 秒"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("缺 %q：\n%s", w, out.String())
		}
	}
	data, _ := json.Marshal(r)
	var failed bytes.Buffer
	renderWhisperReply(&failed, ctlproto.Reply{Code: ctlproto.CodeTimeout, Error: "到了时限", Data: data})
	if !strings.Contains(failed.String(), "批号：B1") {
		t.Errorf("失败时也要把各角色的现状交出来：%s", failed.String())
	}
}

// 日志输出：思考只有字数（默认没有摘要），引用最多三行，行尾给批号、上一级和 id。
func TestRenderWhisperLog(t *testing.T) {
	var b bytes.Buffer
	renderWhisperLog(&b, ctlproto.WhisperLogResult{Page: &ctlproto.LogPage{Next: "1|B1|x", Entries: []ctlproto.LogEntry{
		{ID: "say:B1", Batch: "B1", Kind: ctlproto.LogWhisper, At: 1000, Targets: []string{"a", "b"}, Summary: "去查", Quotes: []ctlproto.LogQuote{{Name: "甲", Text: "原文\n12:00:01 处理记录 a：已批准"}}},
		{ID: "step:s1", Batch: "B1", Kind: ctlproto.LogThink, Character: "a", Parent: "arr:B1:a", At: 2000, Runes: 42},
	}}})
	out := b.String()
	for _, want := range []string{"悄悄话 主人 → a、b：去查", "引用 甲：\n    ｜ 原文", "批号 B1；上一级 无；id say:B1", "    ｜ 12:00:01 处理记录 a：已批准", "思考 a 共 42 字", "上一级 arr:B1:a", "--cursor 1|B1|x"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里该有 %q：\n%s", want, out)
		}
	}
}
