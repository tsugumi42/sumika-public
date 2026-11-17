package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

const chatDone = `{"status":"done","character":"example","topic":"selftest-example","conversation":"console:example:topic:selftest-example","turnId":"r1","reply":"第一行\n第二行","tools":[{"name":"ls","args":"{\"path\":\".\"}","result":"a.txt","status":"done"}],"model":"m1","elapsedMs":1500}`

// 参数 → 请求：第一个位置参数是要说的话，选项各进各的字段；默认时限 120 秒；多余的位置参数留在 Args 里交服务端拒
func TestChatArgsBecomeRequest(t *testing.T) {
	const v = ctlproto.Version
	cases := []struct {
		args []string
		want ctlproto.Request
	}{
		{[]string{"chat", "你好"}, ctlproto.Request{V: v, Cmd: "chat", Text: "你好", Wait: "2m0s"}},
		{[]string{"chat", "--new", "你好"}, ctlproto.Request{V: v, Cmd: "chat", Text: "你好", New: true, Wait: "2m0s"}},
		{[]string{"--json", "chat", "--character", "example", "你好"}, ctlproto.Request{V: v, Cmd: "chat", Text: "你好", Character: "example", Wait: "2m0s"}},
		{[]string{"chat", "--character=example", "--timeout", "30s", "你好"}, ctlproto.Request{V: v, Cmd: "chat", Text: "你好", Character: "example", Wait: "30s"}},
		{[]string{"chat", "--timeout=5m", "--", "--new 是文字"}, ctlproto.Request{V: v, Cmd: "chat", Text: "--new 是文字", Wait: "5m0s"}},
		{[]string{"chat", ""}, ctlproto.Request{V: v, Cmd: "chat", Text: "", Wait: "2m0s"}},
		{[]string{"chat", "一", "二"}, ctlproto.Request{V: v, Cmd: "chat", Text: "一", Args: []string{"二"}, Wait: "2m0s"}},
		{[]string{"chat"}, ctlproto.Request{V: v, Cmd: "chat", Wait: "2m0s"}},
		{[]string{"chat", "--character", "", "你好"}, ctlproto.Request{V: v, Cmd: "chat", Text: "你好", Character: " ", Wait: "2m0s"}}, // 空角色不悄悄当成没给
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
	for _, bad := range [][]string{{"chat", "--timeout"}, {"chat", "--timeout", "很久", "你好"}, {"chat", "--timeout=0s", "你好"}, {"chat", "--character"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("%q 该报命令行写错", bad)
		}
	}
}

// 成功：退出码 0，给人看的输出里有她的话、工具、模型和用时
func TestChatRenderDone(t *testing.T) {
	f := newFakeServer(t, okReply(chatDone))
	code, out, errb := runWith(t, f, "chat", "你好")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d stderr=%q", code, errb)
	}
	for _, want := range []string{"说完了（done）", "example", "第一行\n第二行", "ls（done）", "a.txt", "模型：m1", "1.5 秒"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里该有 %q：\n%s", want, out)
		}
	}
	req := f.only(t)
	if req.Cmd != "chat" || req.Text != "你好" || req.Wait != "2m0s" {
		t.Fatalf("请求：%+v", req)
	}
}

// 失败的几种结局：退出码 1，stderr 写原因，stdout 仍交出她说了什么和审批卡；--json 原样是一个 JSON 对象，status 在 data 里
func TestChatRenderNonDone(t *testing.T) {
	waiting := `{"ok":false,"code":"failed","error":"停在审批上了","data":{"status":"waiting_approval","character":"example","topic":"t","conversation":"c","reply":"","draft":"先说半句","tools":[],"approval":{"id":"a1","title":"写文件","tool":"write","subject":"x.txt","why":"测试"},"elapsedMs":900}}`
	f := newFakeServer(t, func(ctlproto.Request) string { return waiting })
	code, out, errb := runWith(t, f, "chat", "写个文件")
	if code != 1 || !strings.Contains(errb, "停在审批上了") {
		t.Fatalf("code=%d stderr=%q", code, errb)
	}
	for _, want := range []string{"停在审批上（waiting_approval）", "a1", "write", "x.txt", "没有替用户批", "先说半句"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里该有 %q：\n%s", want, out)
		}
	}
	f2 := newFakeServer(t, func(ctlproto.Request) string { return waiting })
	code, out, _ = runWith(t, f2, "--json", "chat", "写个文件")
	var rep ctlproto.Reply
	if code != 1 || json.Unmarshal([]byte(out), &rep) != nil || rep.OK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	var data ctlproto.ChatResult
	if json.Unmarshal(rep.Data, &data) != nil || data.Status != ctlproto.ChatWaitingApproval || data.Approval == nil || data.Approval.ID != "a1" {
		t.Fatalf("data：%s", rep.Data)
	}
}

// 程序没在运行：退出码 2；被拒（形状不合格）：退出码 3
func TestChatExitCodes(t *testing.T) {
	f := newFakeServer(t, func(ctlproto.Request) string {
		return `{"ok":false,"code":"invalid","error":"要说的话不能是空的"}`
	})
	if code, _, errb := runWith(t, f, "chat", ""); code != 3 || !strings.Contains(errb, "不能是空的") {
		t.Fatalf("code=%d stderr=%q", code, errb)
	}
}

// 等回复的上限跟着 --timeout 走：服务端最多要 timeout 加十来秒（取消的收尾），不能让客户端先放弃
func TestChatReplyLimitFollowsTimeout(t *testing.T) {
	f := newFakeServer(t, nil)
	f.hang = true
	oldLimit, oldSlack := replyLimit, waitSlack
	replyLimit, waitSlack = time.Hour, 100*time.Millisecond
	defer func() { replyLimit, waitSlack = oldLimit, oldSlack }()
	start := time.Now()
	code, _, errb := runWith(t, f, "chat", "--timeout", "200ms", "你好")
	if code != 1 || !strings.Contains(errb, "没有收到回复") || time.Since(start) > 5*time.Second {
		t.Fatalf("code=%d stderr=%q 用时 %v", code, errb, time.Since(start))
	}
}

// 回复里的终端控制序列：给人看的输出会换掉（她的话保留换行）
func TestChatRenderCleansEscapes(t *testing.T) {
	data := strings.Replace(chatDone, `"reply":"第一行\n第二行"`, `"reply":"第一行\n\u001b[2J第二行"`, 1)
	f := newFakeServer(t, okReply(data))
	_, out, _ := runWith(t, f, "chat", "你好")
	if strings.Contains(out, "\x1b") || !strings.Contains(out, "第一行\n?[2J第二行") {
		t.Fatalf("输出：%q", out)
	}
}

// 管道连不上：退出码 2，--json 也给一个对象，code 是 not-running
func TestChatPipeUnreachable(t *testing.T) {
	dial := func(context.Context, bool) (net.Conn, error) { return nil, errors.New("没有这条管道") }
	var out, errb bytes.Buffer
	if code := run([]string{"--json", "chat", "你好"}, &out, &errb, dial); code != ctlproto.ExitNotRunning {
		t.Fatalf("code=%d", code)
	}
	var rep ctlproto.Reply
	if json.Unmarshal(out.Bytes(), &rep) != nil || rep.OK || rep.Code != ctlproto.CodeNotRunning {
		t.Fatalf("%q", out.String())
	}
}
