package ctlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// 四种场合与敌对输入由后端可读行集限定；不使用显示名授权。
type backfillBackendFixture struct {
	fakeBackend
	rows     []BackfillRow
	attempts int
	mode     string
	pageSize int
}

func (b *backfillBackendFixture) BackfillStatus(context.Context) (ctlproto.BackfillStatus, error) {
	return ctlproto.BackfillStatus{Scope: "current-account", RemainingOpen: 2, Media: ctlproto.BackfillMedia{State: "waiting", Queued: 1}}, nil
}
func (b *backfillBackendFixture) BackfillGaps(_ context.Context, cursor string, n int) (BackfillPage, error) {
	if b.pageSize > 0 {
		n = min(n, b.pageSize)
	}
	start := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "page-%d", &start); err != nil {
			return BackfillPage{}, ctlproto.Errorf(ctlproto.CodeInvalid, "游标错误")
		}
	}
	end := min(start+n, len(b.rows))
	next := ""
	if end < len(b.rows) {
		next = fmt.Sprintf("page-%d", end)
	}
	return BackfillPage{Items: b.rows[start:end], NextCursor: next, HasMore: end < len(b.rows), Scope: "current-account", AsOfMS: 100}, nil
}
func (b *backfillBackendFixture) BackfillRetry(_ context.Context, r BackfillRef) (ctlproto.BackfillAccepted, error) {
	if b.mode == "readonly" {
		return ctlproto.BackfillAccepted{}, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史")
	}
	for _, row := range b.rows {
		if row.Ref == r && row.View.CanRetry {
			b.attempts++
			return ctlproto.BackfillAccepted{Accepted: true, JobID: "test-job", Scope: "gap", State: "queued"}, nil
		}
	}
	return ctlproto.BackfillAccepted{}, ctlproto.Errorf(ctlproto.CodeInvalid, "不在当前恢复范围")
}
func (b *backfillBackendFixture) BackfillRun(context.Context) (ctlproto.BackfillAccepted, error) {
	if b.mode == "readonly" {
		return ctlproto.BackfillAccepted{}, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史")
	}
	b.attempts++
	return ctlproto.BackfillAccepted{Accepted: true, JobID: "test-job", Scope: "account", State: "queued"}, nil
}
func backfillRows() []BackfillRow {
	account := "981" + strings.Repeat("0", 5) + "1"
	group := "982" + strings.Repeat("0", 5)
	peer := "983" + strings.Repeat("0", 5) + "1"
	return []BackfillRow{
		{Ref: BackfillRef{Platform: "qq", Account: account, Conversation: "qq:group:" + group + "1", ID: 1, FromMS: 10, ToMS: 20}, View: ctlproto.BackfillGap{State: "open", Reason: "outage", CanRetry: true}},
		{Ref: BackfillRef{Platform: "qq", Account: account, Conversation: "qq:private:" + account + ":" + peer, ID: 2, FromMS: 30, ToMS: 40}, View: ctlproto.BackfillGap{State: "abandoned", Reason: "page-cap", CanRetry: true}},
		{Ref: BackfillRef{Platform: "qq", Account: account, Conversation: "qq:group:" + group + "2", ID: 3, FromMS: 50, ToMS: 60}, View: ctlproto.BackfillGap{State: "open", Reason: "outage", CanRetry: false}},
	}
}
func serveBackfill(t *testing.T, s *Server, req ctlproto.Request) ctlproto.Reply {
	t.Helper()
	req.V = ctlproto.Version
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.serve(data)
	return r
}

// S03：每个拒绝分支断言没有动作；碰撞必须扫描全部分页，不能选第一条。
func TestCtlBackfillScopeAndHandleMatrix(t *testing.T) {
	b := &backfillBackendFixture{rows: backfillRows()}
	key := []byte("编造本机密钥32字节xxxxxxxxxxxx")
	s := New(b, Options{HandleKey: func() ([]byte, error) { return key, nil }})
	good := GapHandleOf(key, b.rows[0].Ref)
	for _, args := range [][]string{{}, {"retry"}, {"retry", ""}, {"retry", "0"}, {"retry", "1"}, {"retry", strings.ToUpper(good)}, {"retry", b.rows[0].Ref.Conversation}, {"retry", "console:example"}, {"retry", "console:group:pretend"}, {"retry", "https://example.invalid"}, {"retry", good, "force"}, {"run", "all"}, {"run", "speak"}, {"status", "wait"}, {"unknown"}} {
		r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: args})
		if r.OK || r.Code != ctlproto.CodeInvalid || b.attempts != 0 {
			t.Fatalf("参数放宽 %q %+v attempts=%d", args, r, b.attempts)
		}
	}
	for _, req := range []ctlproto.Request{{Cmd: "backfill", Args: []string{"run"}, N: 1}, {Cmd: "backfill", Args: []string{"retry", good}, Wait: "1s"}, {Cmd: "backfill", Args: []string{"status"}, Cursor: new("page-1")}, {Cmd: "status", Cursor: new("page-1")}, {Cmd: "backfill", Args: []string{"gaps"}, N: 201}, {Cmd: "backfill", Args: []string{"gaps"}, N: -1}} {
		r := serveBackfill(t, s, req)
		if r.OK || r.Code != "invalid" || b.attempts != 0 {
			t.Fatal(req, r, b.attempts)
		}
	}
	for _, line := range []string{`{"v":1,"cmd":"backfill","args":["run"],"n":0}`, `{"v":1,"cmd":"backfill","args":["run"],"cursor":null}`, `{"v":1,"cmd":"backfill","args":["run"],"wait":""}`, `{"v":1,"cmd":"backfill","args":["retry",0]}`, `{"v":1,"cmd":"backfill","args":["run"],"force":true}`} {
		r, _ := s.serve([]byte(line))
		if r.OK || r.Code != "invalid" || b.attempts != 0 {
			t.Fatal("零值或错误类型未拒绝", line, r)
		}
	}
	// 当前范围外群的句柄可读，动作仍须由协调器拒绝。
	excluded := GapHandleOf(key, b.rows[2].Ref)
	if r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"retry", excluded}}); r.OK || b.attempts != 0 {
		t.Fatal("范围外群可写", r)
	}
	old := b.rows[0].Ref
	b.rows[0].Ref.ToMS++
	if r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"retry", good}}); r.OK || r.Code != "invalid" || b.attempts != 0 {
		t.Fatal("旧范围句柄仍有效", r)
	}
	b.rows[0].Ref = old
	b.rows[0].Ref.Account = old.Account + "2"
	if r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"retry", good}}); r.OK || b.attempts != 0 {
		t.Fatal("跨账号句柄有效", r)
	}
	b.rows[0].Ref = old
	b.pageSize = 1
	b.rows[2].View.CanRetry = true
	prior := computeGapHandle
	computeGapHandle = func([]byte, BackfillRef) string { return good }
	defer func() { computeGapHandle = prior }()
	if r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"retry", good}}); r.OK || r.Code != "invalid" || b.attempts != 0 {
		t.Fatal("碰撞选择第一条", r)
	}
	computeGapHandle = prior
	b.pageSize = 0
	b.rows[2].View.CanRetry = false
	b.mode = "readonly"
	if r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"retry", good}}); r.Code != "readonly" || b.attempts != 0 {
		t.Fatal("archive可写", r)
	}
	demo := New(b, Options{Demo: true})
	for _, sub := range []string{"retry", "run"} {
		args := []string{sub}
		if sub == "retry" {
			args = append(args, good)
		}
		if r := serveBackfill(t, demo, ctlproto.Request{Cmd: "backfill", Args: args}); r.Code != "demo" || b.attempts != 0 {
			t.Fatal("demo可写", r)
		}
	}
	if r := serveBackfill(t, demo, ctlproto.Request{Cmd: "backfill", Args: []string{"status"}}); !r.OK || !strings.Contains(string(r.Data), `"scope":"demo"`) {
		t.Fatal("demo读真实状态", r)
	}
	b.mode = ""
	for _, row := range b.rows[:2] {
		h := GapHandleOf(key, row.Ref)
		r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"retry", h}})
		if !r.OK {
			t.Fatal("QQ私聊或群不能恢复", r)
		}
	}
	if b.attempts != 2 {
		t.Fatal(b.attempts)
	}
}

// S04：reason 只给固定代码；陌生名字和外部错误不会进入补录传输字段。
func TestCtlBackfillRedaction(t *testing.T) {
	var logs strings.Builder
	b := &backfillBackendFixture{rows: backfillRows()}
	b.rows[0].View.Reason = "\x1b[31mhttps://example.invalid/?rkey=编造令牌 " + strings.Repeat("9", 12)
	s := New(b, Options{HandleKey: func() ([]byte, error) { return []byte("编造密钥"), nil }, Logf: func(f string, a ...any) { fmt.Fprintf(&logs, f, a...) }})
	r := serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: []string{"gaps"}})
	if !r.OK {
		t.Fatal(r)
	}
	var gaps ctlproto.BackfillGaps
	if err := json.Unmarshal(r.Data, &gaps); err != nil {
		t.Fatal(err)
	}
	if len(gaps.Items) != 3 || gaps.Items[0].Reason != "unknown" || gaps.Items[0].MaskedConversation != "qq:group:***01" {
		t.Fatal(gaps)
	}
	for _, secret := range []string{b.rows[0].Ref.Account, strings.TrimPrefix(b.rows[0].Ref.Conversation, "qq:group:"), strings.Split(b.rows[1].Ref.Conversation, ":")[3], strings.Repeat("9", 12), "rkey", "编造令牌", "\x1b", "example.invalid"} {
		if strings.Contains(string(r.Data)+logs.String(), secret) {
			t.Fatalf("泄漏 %q", secret)
		}
	}
	for _, args := range [][]string{{"status"}, {"retry", strings.Repeat("9", 12) + "\x1b[0m"}, {"run", "https://example.invalid/?rkey=编造令牌"}} {
		r = serveBackfill(t, s, ctlproto.Request{Cmd: "backfill", Args: args})
		for _, secret := range []string{strings.Repeat("9", 12), "rkey", "编造令牌", "\x1b"} {
			if strings.Contains(string(r.Data)+r.Error+logs.String(), secret) {
				t.Fatalf("拒绝或日志泄漏 %q", secret)
			}
		}
	}
	backfillKeyFailureRedaction(t)
}
func backfillKeyFailureRedaction(t *testing.T) {
	for _, args := range [][]string{{"gaps"}, {"retry", "1234abcd"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			var logs strings.Builder
			b := &backfillBackendFixture{rows: backfillRows()}
			s := New(b, Options{HandleKey: func() ([]byte, error) {
				return nil, fmt.Errorf("D:\\编造目录\\state.json https://example.invalid/?rkey=编造令牌 %s", strings.Repeat("9", 12))
			}, Logf: func(f string, a ...any) { fmt.Fprintf(&logs, f, a...) }})
			r := serveBackfill(t, s, ctlproto.Request{Cmd: ctlproto.CmdBackfill, Args: args})
			if r.OK || r.Code != ctlproto.CodeFailed || b.attempts != 0 {
				t.Fatal("密钥失败仍接受动作", r, b.attempts)
			}
			for _, secret := range []string{"rkey", "编造令牌", "编造目录", "state.json", "example.invalid", strings.Repeat("9", 12)} {
				if strings.Contains(string(r.Data)+r.Error+logs.String(), secret) {
					t.Fatalf("密钥I/O错误泄漏 %q", secret)
				}
			}
			var failure ctlproto.BackfillFailure
			if err := json.Unmarshal(r.Data, &failure); err != nil || failure.ReasonCode != "io" {
				t.Fatal("读取失败未保留明确I/O状态", r, err)
			}
		})
	}
}
