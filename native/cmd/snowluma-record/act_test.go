package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel/snowluma"
)

// actPeer 是会回查询的假协议端：get_login_info 回 login 号，get_status 回 good；
// 别的动作按 reply 回（nil 就不回）。收到的每一帧都记下，用来断言发了什么。
type actPeer struct {
	srv   *httptest.Server
	login any
	good  bool
	reply func(echo string) map[string]any
	got   chan map[string]any
}

func newActPeer(t *testing.T, login any, good bool) *actPeer {
	t.Helper()
	p := &actPeer{login: login, good: good, got: make(chan map[string]any, 16)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			p.got <- m
			echo, _ := m["echo"].(string)
			var out map[string]any
			switch m["action"] {
			case "get_login_info":
				out = map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"user_id": p.login, "nickname": "编的"}, "echo": echo}
			case "get_status":
				out = map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"online": true, "good": p.good}, "echo": echo}
			default:
				if p.reply != nil {
					out = p.reply(echo)
				}
			}
			if out != nil {
				b, _ := json.Marshal(out)
				_ = c.Write(ctx, websocket.MessageText, b)
			}
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *actPeer) cfg() snowluma.Config {
	return snowluma.Config{URL: "ws" + strings.TrimPrefix(p.srv.URL, "http"), Token: testToken}
}

// actions 是客户端发来的全部 action 名，按顺序。
func (p *actPeer) actions() []string {
	var names []string
	for {
		select {
		case m := <-p.got:
			names = append(names, m["action"].(string))
		default:
			return names
		}
	}
}

var testAct = actFile{Action: "send_private_msg", Params: json.RawMessage(`{"user_id":90002,"message":"编的一句"}`)}

func runAct(t *testing.T, p *actPeer, opt actOptions) (string, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "act.jsonl")
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := actOnce(ctx, p.cfg(), testAct, opt, path, &out)
	b, _ := os.ReadFile(path)
	return string(b), out.String(), err
}

func TestActRefusesWrongLogin(t *testing.T) {
	p := newActPeer(t, 90009, true)
	_, out, err := runAct(t, p, actOptions{account: "90001", send: true, wait: time.Second})
	if err == nil {
		t.Fatal("登录号不对也没报错")
	}
	if got := p.actions(); strings.Join(got, ",") != "get_login_info" {
		t.Fatalf("登录号不对时只该问一次登录号，实际发了 %v", got)
	}
	if strings.Contains(out+err.Error(), "90009") || strings.Contains(out+err.Error(), "90001") {
		t.Fatalf("输出里带了号：%q %v", out, err)
	}
}

func TestActLoginLeadingZeroNotEqual(t *testing.T) {
	p := newActPeer(t, 90001, true)
	if _, _, err := runAct(t, p, actOptions{account: "090001", send: true, wait: time.Second}); err == nil {
		t.Fatal("配置 090001 和回包 90001 被当成相等")
	}
	if got := p.actions(); len(got) != 1 {
		t.Fatalf("发了 %v", got)
	}
}

func TestActRefusesUnhealthy(t *testing.T) {
	p := newActPeer(t, 90001, false)
	if _, _, err := runAct(t, p, actOptions{account: "90001", send: true, wait: time.Second}); err == nil {
		t.Fatal("good=false 也没报错")
	}
	if got := p.actions(); strings.Join(got, ",") != "get_login_info,get_status" {
		t.Fatalf("不健康时不该发动作，实际 %v", got)
	}
}

func TestActDryRunDoesNotSend(t *testing.T) {
	p := newActPeer(t, 90001, true)
	_, out, err := runAct(t, p, actOptions{account: "90001", send: false, wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.actions(); strings.Join(got, ",") != "get_login_info,get_status" {
		t.Fatalf("没给 -send 也发了：%v", got)
	}
	if !strings.Contains(out, "不发") {
		t.Fatalf("没说不发：%q", out)
	}
}

func TestActSendsOnceAndRecordsReply(t *testing.T) {
	p := newActPeer(t, 90001, true)
	p.reply = func(echo string) map[string]any {
		return map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 12345}, "echo": echo}
	}
	file, out, err := runAct(t, p, actOptions{account: "90001", send: true, wait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.actions(); strings.Join(got, ",") != "get_login_info,get_status,send_private_msg" {
		t.Fatalf("应该核验两次再发一次：%v", got)
	}
	if !strings.Contains(out, "status=ok retcode=0 data 的键=[message_id]") {
		t.Fatalf("回包摘要不对：%q", out)
	}
	lines := strings.Split(strings.TrimSpace(file), "\n")
	var outs, replies int
	for _, l := range lines {
		var m struct {
			Dir   string         `json:"dir"`
			Frame map[string]any `json:"frame"`
		}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("坏行 %q: %v", l, err)
		}
		if m.Dir == "out" {
			outs++
			if m.Frame["action"] != "send_private_msg" || m.Frame["echo"] != actEcho {
				t.Fatalf("录下的发出帧不对：%v", m.Frame)
			}
		}
		if m.Frame["echo"] == actEcho && m.Dir == "" {
			replies++
		}
	}
	if outs != 1 || replies != 1 {
		t.Fatalf("发出帧 %d 条、回包 %d 条，应该各 1：\n%s", outs, replies, file)
	}
}

func TestActNoReplyStillEnds(t *testing.T) {
	p := newActPeer(t, 90001, true)
	_, out, err := runAct(t, p, actOptions{account: "90001", send: true, wait: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "没等到") {
		t.Fatalf("不回包时没说：%q", out)
	}
}

func TestLoadActRejects(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no-action":  `{"params":{}}`,
		"arr-params": `{"action":"x","params":[]}`,
		"extra":      `{"action":"x","params":{},"echo":"y"}`,
	} {
		path := filepath.Join(dir, name+".json")
		_ = os.WriteFile(path, []byte(body), 0o644)
		if _, err := loadAct(path); err == nil {
			t.Errorf("%s 应该被拒", name)
		}
	}
}
