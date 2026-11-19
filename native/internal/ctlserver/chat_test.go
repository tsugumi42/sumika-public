package ctlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// chatFake 在假后端上加自检对话：记下收到的请求，回预先给的结果
type chatFake struct {
	fakeBackend
	cmu  sync.Mutex
	reqs []ChatRequest
	res  ctlproto.ChatResult
	err  error
}

func (c *chatFake) Chat(_ context.Context, req ChatRequest) (ctlproto.ChatResult, error) {
	c.rec("chat")
	c.cmu.Lock()
	c.reqs = append(c.reqs, req)
	c.cmu.Unlock()
	return c.res, c.err
}

// ───────────── chat 的形状表（服务端这一层）─────────────
// 行：空文本、全空白、超长、带控制字符、多余参数、角色写法不合格、时限不合格、别的命令夹带 chat 的字段、会话 id 想从各处混进来；
// 每一格都真发一次请求，全部 invalid（退出码 3），而且一个后端函数都没调到。

func TestChatShapeTable(t *testing.T) {
	long := strings.Repeat("长", ctlproto.MaxChatText+1)
	type row struct {
		what string
		req  ctlproto.Request
	}
	rows := []row{
		{"空文本", ctlproto.Request{Cmd: "chat"}},
		{"全空白", ctlproto.Request{Cmd: "chat", Text: " \r\n\t "}},
		{"超长", ctlproto.Request{Cmd: "chat", Text: long}},
		{"带 ESC", ctlproto.Request{Cmd: "chat", Text: "你好\x1b[2J"}},
		{"带 NUL", ctlproto.Request{Cmd: "chat", Text: "你好\x00"}},
		{"带 DEL", ctlproto.Request{Cmd: "chat", Text: "你好\x7f"}},
		{"文字方向覆盖符", ctlproto.Request{Cmd: "chat", Text: "你好‮"}},
		{"多余参数", ctlproto.Request{Cmd: "chat", Text: "你好", Args: []string{"第二句"}}},
		{"把会话 id 当参数塞进来", ctlproto.Request{Cmd: "chat", Text: "你好", Args: []string{"qq:group:10001"}}},
		{"把会话 id 塞进角色", ctlproto.Request{Cmd: "chat", Text: "你好", Character: "qq:group:10001"}},
		{"角色带冒号", ctlproto.Request{Cmd: "chat", Text: "你好", Character: "example:topic:x"}},
		{"角色带斜杠", ctlproto.Request{Cmd: "chat", Text: "你好", Character: "../x"}},
		{"角色带反斜杠", ctlproto.Request{Cmd: "chat", Text: "你好", Character: `a\b`}},
		{"角色带空格", ctlproto.Request{Cmd: "chat", Text: "你好", Character: " "}},
		{"角色过长", ctlproto.Request{Cmd: "chat", Text: "你好", Character: strings.Repeat("a", 129)}},
		{"时限不是时长", ctlproto.Request{Cmd: "chat", Text: "你好", Wait: "很久"}},
		{"时限为负", ctlproto.Request{Cmd: "chat", Text: "你好", Wait: "-5s"}},
		{"时限为零", ctlproto.Request{Cmd: "chat", Text: "你好", Wait: "0s"}},
		{"时限超过上限", ctlproto.Request{Cmd: "chat", Text: "你好", Wait: "6m"}},
		{"别的命令夹带 text", ctlproto.Request{Cmd: "status", Text: "你好"}},
		{"别的命令夹带 character", ctlproto.Request{Cmd: "logs", Character: "example"}},
		{"别的命令夹带 new", ctlproto.Request{Cmd: "quit", New: true}},
		{"gate set 夹带 text", ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}, Text: "你好"}},
	}
	f := &chatFake{}
	name, _ := start(t, f, Options{})
	for _, r := range rows {
		got := ask(t, name, r.req)
		if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != ctlproto.ExitDenied || got.Error == "" {
			t.Errorf("%s：应被拒（invalid、退出码 3），得到 %+v", r.what, got)
		}
	}
	// 请求里根本没有"会话"字段：硬塞一个，整个请求因不认识的字段被拒
	for _, field := range []string{"conversation", "conv", "session", "key", "channel"} {
		got := raw(t, name, []byte(fmt.Sprintf(`{"v":1,"cmd":"chat","text":"你好","%s":"qq:group:10001"}`, field)))
		if got.OK || got.Code != ctlproto.CodeInvalid {
			t.Errorf("会话字段 %s：应被拒，得到 %+v", field, got)
		}
	}
	wantCalls(t, &f.fakeBackend) // 被拒的一格都没碰到后端
	t.Logf("被拒的格子 %d 个", len(rows)+5)
}

// 正常的一格：原样把一句话、角色、new、时限交给后端；默认时限 2 分钟；回复里她的话保留换行、去掉终端控制序列
func TestChatPassesAndCleans(t *testing.T) {
	f := &chatFake{res: ctlproto.ChatResult{Status: ctlproto.ChatDone, Character: "example", Topic: "selftest-example", Conversation: "console:example:topic:selftest-example",
		Reply: "第一行\r\n第二行\x1b[2J\t制表", Tools: []ctlproto.ChatTool{{Name: "ls", Args: "{\"path\":\"a\nb\"}", Result: "ok\x1b]0;x\x07"}}, Model: "m", ElapsedMS: 12}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "chat", Text: "你好\n第二行"})
	if !r.OK || ctlproto.ExitCode(r) != 0 {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.ChatResult](t, r)
	if v.Reply != "第一行\n第二行?[2J\t制表" {
		t.Fatalf("回复该保留换行、去掉 ESC：%q", v.Reply)
	}
	if strings.ContainsAny(v.Tools[0].Args, "\n\x1b") || strings.ContainsAny(v.Tools[0].Result, "\n\x1b\x07") {
		t.Fatalf("工具字段是单行、不带控制序列：%+v", v.Tools[0])
	}
	f.cmu.Lock()
	got := f.reqs[0]
	f.cmu.Unlock()
	if got.Text != "你好\n第二行" || got.Character != "" || got.New || got.Timeout != ctlproto.DefaultChatTTL {
		t.Fatalf("交给后端的请求：%+v", got)
	}
	r = ask(t, name, ctlproto.Request{Cmd: "chat", Text: "你好", Character: "示例角色", New: true, Wait: "7s"})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	f.cmu.Lock()
	got = f.reqs[1]
	f.cmu.Unlock()
	if got.Character != "示例角色" || !got.New || got.Timeout != 7*time.Second {
		t.Fatalf("交给后端的请求：%+v", got)
	}
}

// 非 done 的结局：回复 ok 为假、退出码 1，data 里带完整结果（审批卡、半截、工具），文字照样清过
func TestChatNonDoneKeepsData(t *testing.T) {
	for _, tc := range []struct {
		status, code string
	}{
		{ctlproto.ChatWaitingApproval, ctlproto.CodeFailed},
		{ctlproto.ChatTimeout, ctlproto.CodeTimeout},
		{ctlproto.ChatError, ctlproto.CodeFailed},
		{ctlproto.ChatBusy, ctlproto.CodeFailed},
		{ctlproto.ChatStopped, ctlproto.CodeFailed},
	} {
		t.Run(tc.status, func(t *testing.T) {
			res := ctlproto.ChatResult{Status: tc.status, Draft: "半截\x1b[2J", Approval: &ctlproto.ChatApproval{ID: "a1", Tool: "write", Subject: "x\ny", Preview: "行一\n行二\x1b"}}
			f := &chatFake{res: res, err: &ctlproto.Error{Code: tc.code, Msg: "没做完", Data: res}}
			name, _ := start(t, f, Options{})
			r := ask(t, name, ctlproto.Request{Cmd: "chat", Text: "你好"})
			if r.OK || r.Code != tc.code || ctlproto.ExitCode(r) != 1 {
				t.Fatalf("%+v", r)
			}
			v := dataOf[ctlproto.ChatResult](t, r)
			if v.Status != tc.status || v.Approval == nil || v.Approval.ID != "a1" || strings.Contains(v.Draft, "\x1b") || strings.ContainsAny(v.Approval.Subject, "\n") || strings.Contains(v.Approval.Preview, "\x1b") || v.Approval.Preview != "行一\n行二?" {
				t.Fatalf("data 不对：%+v", v)
			}
		})
	}
}

// 后端的带码错误原样变成回复码：只看历史（readonly）、不存在的角色（invalid）；没带码的按 failed
func TestChatBackendErrorsBecomeCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
		exit int
	}{
		{ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史"), ctlproto.CodeReadOnly, 1},
		{ctlproto.Errorf(ctlproto.CodeInvalid, "没有这个角色"), ctlproto.CodeInvalid, 3},
		{ctlproto.Errorf(ctlproto.CodeDemo, "演示"), ctlproto.CodeDemo, 3},
		{fmt.Errorf("库打不开"), ctlproto.CodeFailed, 1},
	} {
		f := &chatFake{err: tc.err}
		name, _ := start(t, f, Options{})
		r := ask(t, name, ctlproto.Request{Cmd: "chat", Text: "你好"})
		if r.OK || r.Code != tc.code || ctlproto.ExitCode(r) != tc.exit {
			t.Fatalf("%v：%+v", tc.err, r)
		}
	}
}

// 演示模式：回 demo，不碰后端；后端没实现 ChatBackend（没有可写桌面）：回 readonly
func TestChatDemoAndNoBackend(t *testing.T) {
	f := &chatFake{}
	name, _ := start(t, f, Options{Demo: true})
	r := ask(t, name, ctlproto.Request{Cmd: "chat", Text: "你好"})
	if r.OK || r.Code != ctlproto.CodeDemo || ctlproto.ExitCode(r) != 3 {
		t.Fatalf("%+v", r)
	}
	wantCalls(t, &f.fakeBackend)
	plain := &fakeBackend{}
	name2, _ := start(t, plain, Options{})
	r = ask(t, name2, ctlproto.Request{Cmd: "chat", Text: "你好"})
	if r.OK || r.Code != ctlproto.CodeReadOnly {
		t.Fatalf("%+v", r)
	}
	wantCalls(t, plain)
}

// 日志：chat 不是只读命令，会记一行；日志里不带她的话也不带请求里的文字
func TestChatIsLoggedWithoutText(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	f := &chatFake{res: ctlproto.ChatResult{Status: ctlproto.ChatDone, Reply: "回复里的机密"}}
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}})
	ask(t, name, ctlproto.Request{Cmd: "chat", Text: "请求里的机密"})
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "chat → 成功") || strings.Contains(joined, "机密") {
		t.Fatalf("日志不对：%q", joined)
	}
}

// 回复里的 data 是 JSON 对象且 tools 永远是数组（空也不是 null）
func TestChatToolsNeverNull(t *testing.T) {
	f := &chatFake{res: ctlproto.ChatResult{Status: ctlproto.ChatDone}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "chat", Text: "你好"})
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &m); err != nil || string(m["tools"]) != "[]" {
		t.Fatalf("%s %v", r.Data, err)
	}
}
