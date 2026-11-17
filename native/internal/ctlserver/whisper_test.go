package ctlserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// whisperFake 在假后端上加悄悄话：记下收到的请求，回预先给的结果
type whisperFake struct {
	fakeBackend
	wmu  sync.Mutex
	reqs []WhisperCreateRequest
	res  ctlproto.WhisperResult
	err  error
}

func (w *whisperFake) WhisperCreate(_ context.Context, req WhisperCreateRequest) (ctlproto.WhisperResult, error) {
	w.rec("whisper create")
	w.wmu.Lock()
	w.reqs = append(w.reqs, req)
	w.wmu.Unlock()
	return w.res, w.err
}
func (w *whisperFake) WhisperStatus(_ context.Context, batch string) (ctlproto.WhisperResult, error) {
	w.rec("whisper status %s", batch)
	return w.res, w.err
}
func (w *whisperFake) WhisperCancel(_ context.Context, batch string) (ctlproto.WhisperResult, error) {
	w.rec("whisper cancel %s", batch)
	return w.res, w.err
}

func goodWhisper() ctlproto.Request {
	return ctlproto.Request{Cmd: "whisper", Args: []string{"create"}, Character: "a,b", Whisper: &ctlproto.WhisperArgs{Selftest: true, SourceText: "这个梗什么意思", Say: "帮我查查"}}
}

// 形状表：没有 --selftest、空文字、超长、控制字符、角色写法、真实会话的 id 想从各处混进来、批号写法、别的命令夹带悄悄话字段；
// 每一格都 invalid（退出码 3），一个后端函数都没调到。
func TestWhisperShapeTable(t *testing.T) {
	long := strings.Repeat("长", ctlproto.MaxChatText+1)
	mod := func(f func(*ctlproto.Request)) ctlproto.Request { r := goodWhisper(); f(&r); return r }
	rows := []struct {
		what string
		req  ctlproto.Request
	}{
		{"没有子命令", ctlproto.Request{Cmd: "whisper"}},
		{"不认识的子命令", ctlproto.Request{Cmd: "whisper", Args: []string{"send"}}},
		{"没有 selftest", mod(func(r *ctlproto.Request) { r.Whisper.Selftest = false })},
		{"没有参数块", mod(func(r *ctlproto.Request) { r.Whisper = nil })},
		{"来源文字空", mod(func(r *ctlproto.Request) { r.Whisper.SourceText = " " })},
		{"说的话空", mod(func(r *ctlproto.Request) { r.Whisper.Say = "" })},
		{"来源文字超长", mod(func(r *ctlproto.Request) { r.Whisper.SourceText = long })},
		{"说的话带 ESC", mod(func(r *ctlproto.Request) { r.Whisper.Say = "你好\x1b[2J" })},
		{"名字带换行", mod(func(r *ctlproto.Request) { r.Whisper.QuoteName = "甲\n乙" })},
		{"名字过长", mod(func(r *ctlproto.Request) { r.Whisper.QuoteName = strings.Repeat("名", ctlproto.MaxQuoteName+1) })},
		{"没有角色", mod(func(r *ctlproto.Request) { r.Character = "" })},
		{"角色里有空项", mod(func(r *ctlproto.Request) { r.Character = "a,,b" })},
		{"把会话 id 塞进角色", mod(func(r *ctlproto.Request) { r.Character = "qq:group:10001" })},
		{"角色带斜杠", mod(func(r *ctlproto.Request) { r.Character = "../x" })},
		{"角色太多", mod(func(r *ctlproto.Request) {
			var ids []string
			for i := 0; i <= ctlproto.MaxWhisperCharacters; i++ {
				ids = append(ids, fmt.Sprintf("c%d", i))
			}
			r.Character = strings.Join(ids, ",")
		})},
		{"create 带位置参数", mod(func(r *ctlproto.Request) { r.Args = []string{"create", "qq:group:10001"} })},
		{"夹带 chat 的 text", mod(func(r *ctlproto.Request) { r.Text = "你好" })},
		{"夹带 new", mod(func(r *ctlproto.Request) { r.New = true })},
		{"时限不是时长", mod(func(r *ctlproto.Request) { r.Wait = "很久" })},
		{"时限超过上限", mod(func(r *ctlproto.Request) { r.Wait = "6m" })},
		{"status 没批号", ctlproto.Request{Cmd: "whisper", Args: []string{"status"}}},
		{"status 批号带冒号", ctlproto.Request{Cmd: "whisper", Args: []string{"status", "qq:group:1"}}},
		{"cancel 批号带路径", ctlproto.Request{Cmd: "whisper", Args: []string{"cancel", "../x"}}},
		{"cancel 批号过长", ctlproto.Request{Cmd: "whisper", Args: []string{"cancel", strings.Repeat("a", 65)}}},
		{"status 夹带参数块", ctlproto.Request{Cmd: "whisper", Args: []string{"status", "B1"}, Whisper: &ctlproto.WhisperArgs{Selftest: true}}},
		{"status 夹带角色", ctlproto.Request{Cmd: "whisper", Args: []string{"status", "B1"}, Character: "a"}},
		{"别的命令夹带悄悄话字段", ctlproto.Request{Cmd: "status", Whisper: &ctlproto.WhisperArgs{Selftest: true}}},
		{"chat 夹带悄悄话字段", ctlproto.Request{Cmd: "chat", Text: "你好", Whisper: &ctlproto.WhisperArgs{Say: "x"}}},
		{"别的命令夹带 wait", ctlproto.Request{Cmd: "whisper", Args: []string{"status", "B1"}, Wait: "5s"}},
	}
	f := &whisperFake{}
	name, _ := start(t, f, Options{})
	for _, r := range rows {
		got := ask(t, name, r.req)
		if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != ctlproto.ExitDenied || got.Error == "" {
			t.Errorf("%s：应被拒（invalid、退出码 3），得到 %+v", r.what, got)
		}
	}
	// 请求里根本没有"会话"字段：硬塞一个，整个请求因不认识的字段被拒
	for _, field := range []string{"conversation", "conv", "session", "key", "channel", "source"} {
		got := raw(t, name, []byte(fmt.Sprintf(`{"v":1,"cmd":"whisper","args":["create"],"character":"a","whisper":{"selftest":true,"sourceText":"x","say":"y"},"%s":"qq:group:10001"}`, field)))
		if got.OK || got.Code != ctlproto.CodeInvalid {
			t.Errorf("会话字段 %s：应被拒，得到 %+v", field, got)
		}
	}
	got := raw(t, name, []byte(`{"v":1,"cmd":"whisper","args":["create"],"character":"a","whisper":{"selftest":true,"sourceText":"x","say":"y","conversation":"qq:group:10001"}}`))
	if got.OK || got.Code != ctlproto.CodeInvalid {
		t.Errorf("参数块里的会话字段：应被拒，得到 %+v", got)
	}
	wantCalls(t, &f.fakeBackend) // 被拒的一格都没碰到后端
}

// 正常的一格：角色去重、默认时限 2 分钟；回复里的外来文字清过控制序列；status、cancel 把批号交给后端
func TestWhisperPassesAndCleans(t *testing.T) {
	f := &whisperFake{res: ctlproto.WhisperResult{Batch: "B1", Status: ctlproto.WhisperDone,
		Entries: []ctlproto.WhisperEntry{{Character: "a", State: ctlproto.WhisperDone, Note: "第一行\r\n第二行\x1b[2J", Detail: "x\ny"}}}}
	name, _ := start(t, f, Options{})
	req := goodWhisper()
	req.Character = "a,b,a"
	r := ask(t, name, req)
	if !r.OK || ctlproto.ExitCode(r) != 0 {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.WhisperResult](t, r)
	if v.Entries[0].Note != "第一行\n第二行?[2J" || strings.ContainsAny(v.Entries[0].Detail, "\n") {
		t.Fatalf("处理记录保留换行、去掉 ESC，说明是单行：%+v", v.Entries[0])
	}
	f.wmu.Lock()
	got := f.reqs[0]
	f.wmu.Unlock()
	if strings.Join(got.Characters, ",") != "a,b" || got.SourceText != "这个梗什么意思" || got.Say != "帮我查查" || got.Timeout != ctlproto.DefaultChatTTL {
		t.Fatalf("交给后端的请求：%+v", got)
	}
	req.Wait = "7s"
	if r = ask(t, name, req); !r.OK {
		t.Fatalf("%+v", r)
	}
	f.wmu.Lock()
	got = f.reqs[1]
	f.wmu.Unlock()
	if got.Timeout != 7*time.Second {
		t.Fatalf("时限：%+v", got)
	}
	for _, sub := range []string{"status", "cancel"} {
		if r = ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{sub, "B1"}}); !r.OK {
			t.Fatalf("%s：%+v", sub, r)
		}
	}
	calls := f.got()
	if len(calls) < 4 || calls[2] != "whisper status B1" || calls[3] != "whisper cancel B1" {
		t.Fatalf("后端调用：%v", calls)
	}
	// status 是只读的，cancel 不是
	if !readOnly(ctlproto.Request{Cmd: "whisper", Args: []string{"status", "B1"}}) || readOnly(ctlproto.Request{Cmd: "whisper", Args: []string{"cancel", "B1"}}) || readOnly(goodWhisper()) {
		t.Fatal("只有 whisper status 是只读")
	}
}

// 没有接悄悄话的宿主：回 failed，不落到别的路径
func TestWhisperWithoutBackend(t *testing.T) {
	name, _ := start(t, &fakeBackend{}, Options{})
	if r := ask(t, name, goodWhisper()); r.OK || r.Code != ctlproto.CodeFailed {
		t.Fatalf("%+v", r)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"status", "B1"}}); r.OK || r.Code != ctlproto.CodeFailed {
		t.Fatalf("%+v", r)
	}
}

type logFake struct {
	whisperFake
	args  []ctlproto.WhisperLogArgs
	lists []bool
	res   ctlproto.WhisperLogResult
}

func (l *logFake) WhisperLog(_ context.Context, a ctlproto.WhisperLogArgs) (ctlproto.WhisperLogResult, error) {
	l.rec("whisper log")
	l.wmu.Lock()
	l.args = append(l.args, a)
	l.wmu.Unlock()
	return l.res, nil
}
func (l *logFake) WhisperList(_ context.Context, selftest bool) (ctlproto.WhisperLogResult, error) {
	l.rec("whisper list")
	l.lists = append(l.lists, selftest)
	return l.res, nil
}

const selfConv = "console:suzu:topic:selftest-1"

// 对话日志命令的形状表：只认自检会话；真实会话 id、塞进别的字段、乱批号、乱条目 id、越界页大小都 invalid，后端一次也没碰到。
func TestWhisperLogShapeTable(t *testing.T) {
	log := func(a ctlproto.WhisperLogArgs) ctlproto.Request {
		return ctlproto.Request{Cmd: "whisper", Args: []string{"log"}, WhisperLog: &a}
	}
	rows := []struct {
		what string
		req  ctlproto.Request
	}{
		{"没有任何参数", ctlproto.Request{Cmd: "whisper", Args: []string{"log"}}},
		{"QQ 群会话", log(ctlproto.WhisperLogArgs{Conversation: "qq:group:10001"})},
		{"本地群会话", log(ctlproto.WhisperLogArgs{Conversation: "console:group:g1"})},
		{"悄悄话会话串", log(ctlproto.WhisperLogArgs{Conversation: "console:suzu:whisper:abc"})},
		{"会话带空白", log(ctlproto.WhisperLogArgs{Conversation: "console:suzu:topic:a b"})},
		{"会话带控制字符", log(ctlproto.WhisperLogArgs{Conversation: "console:suzu:topic:a\x1b"})},
		{"批号带冒号", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Batch: "a:b"})},
		{"条目 id 乱写", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Entry: "../../x"})},
		{"条目种类不认识", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Entry: "sql:1"})},
		{"页大小越界", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Limit: ctlproto.LogPageMax + 1})},
		{"页大小为负", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Limit: -1})},
		{"游标带换行", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Cursor: "1|b|x\n"})},
		{"条目和游标一起", log(ctlproto.WhisperLogArgs{Conversation: selfConv, Entry: "say:B1", Cursor: "1|b|x"})},
		{"夹带创建参数", ctlproto.Request{Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: selfConv}, Whisper: &ctlproto.WhisperArgs{Selftest: true}}},
		{"夹带角色", ctlproto.Request{Cmd: "whisper", Args: []string{"log"}, Character: "a", WhisperLog: &ctlproto.WhisperLogArgs{Conversation: selfConv}}},
		{"别的命令夹带日志字段", ctlproto.Request{Cmd: "status", WhisperLog: &ctlproto.WhisperLogArgs{Conversation: selfConv}}},
		{"list 夹带日志参数", ctlproto.Request{Cmd: "whisper", Args: []string{"list"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: selfConv}}},
		{"list 夹带位置参数", ctlproto.Request{Cmd: "whisper", Args: []string{"list", "x"}}},
	}
	f := &logFake{}
	name, _ := start(t, f, Options{})
	for _, r := range rows {
		got := ask(t, name, r.req)
		if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != ctlproto.ExitDenied {
			t.Errorf("%s：应被拒（invalid、退出码 3），得到 %+v", r.what, got)
		}
	}
	wantCalls(t, &f.fakeBackend)
}

// 正常的：自检会话、批号、条目都交给后端；外来文字清过控制字符；list 只给批号和状态。
func TestWhisperLogPassesAndCleans(t *testing.T) {
	f := &logFake{res: ctlproto.WhisperLogResult{Page: &ctlproto.LogPage{Entries: []ctlproto.LogEntry{
		{ID: "say:B1", Batch: "B1", Kind: ctlproto.LogWhisper, Summary: "第一行\n第二行\x1b[2J", Quotes: []ctlproto.LogQuote{{Name: "甲\x1b", Text: "引\r\n文\x07"}}},
	}}}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: selfConv, Batch: "B1", Limit: 20, WithThinking: true}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.WhisperLogResult](t, r)
	e := v.Page.Entries[0]
	if strings.ContainsAny(e.Summary, "\n\x1b") || strings.ContainsAny(e.Quotes[0].Name, "\x1b") || strings.ContainsAny(e.Quotes[0].Text, "\x07\r") {
		t.Fatalf("外来文字没清干净：%+v", e)
	}
	if len(f.args) != 1 || f.args[0].Conversation != selfConv || f.args[0].Batch != "B1" || f.args[0].Limit != 20 || !f.args[0].WithThinking {
		t.Fatalf("交给后端的参数：%+v", f.args)
	}
	if r = ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"list"}, Whisper: &ctlproto.WhisperArgs{Selftest: true}}); !r.OK || len(f.lists) != 1 || !f.lists[0] {
		t.Fatalf("list --selftest：%+v %v", r, f.lists)
	}
	if r = ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"list"}}); !r.OK || f.lists[1] {
		t.Fatalf("list 不带 --selftest 只列真实的：%+v %v", r, f.lists)
	}
}

// 读日志也记一行“读日志：成功 / 没做成”（沿用 chat 的约定，规格 15.6）：只有命令名和子命令，不带会话、批号、日志内容。
func TestWhisperLogIsLoggedWithoutContent(t *testing.T) {
	lc := &logCapture{}
	f := &logFake{res: ctlproto.WhisperLogResult{Page: &ctlproto.LogPage{Entries: []ctlproto.LogEntry{{ID: "say:B1", Batch: "B1", Kind: ctlproto.LogWhisper, Summary: "不该进日志的正文"}}}}}
	name, _ := start(t, f, Options{Logf: lc.logf})
	ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: selfConv, Batch: "B1"}})
	ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"list"}})
	ask(t, name, ctlproto.Request{Cmd: "whisper", Args: []string{"log"}, WhisperLog: &ctlproto.WhisperLogArgs{Conversation: "qq:group:10001"}})
	want := "控制接口：whisper log → 成功\n控制接口：whisper list → 成功\n控制接口：whisper log → 被拒（invalid）"
	if got := lc.all(); got != want {
		t.Fatalf("日志：\n%s\n应是\n%s", got, want)
	}
	if strings.Contains(lc.all(), "B1") || strings.Contains(lc.all(), "不该进日志") || strings.Contains(lc.all(), selfConv) {
		t.Fatal("日志里不该有批号、会话和日志内容")
	}
}
