package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type sentMessage struct {
	At                         time.Time
	Conversation, Action, Text string
	Raw                        json.RawMessage
}

const peerToken = "fictional-e2e-access"

type fakePeer struct {
	server    *httptest.Server
	mu        sync.Mutex
	conn      *websocket.Conn
	downUntil time.Time
	ready     chan struct{}
	sends     []sentMessage
	actions   []string
	history   map[string]json.RawMessage
	conns     []*websocket.Conn
	wg        sync.WaitGroup
}

func newPeer() *fakePeer {
	p := &fakePeer{ready: make(chan struct{}, 8), history: map[string]json.RawMessage{}}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	return p
}
func (p *fakePeer) url() string { return "ws" + strings.TrimPrefix(p.server.URL, "http") }
func (p *fakePeer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+peerToken {
		http.Error(w, "unauthorized", 401)
		return
	}
	p.mu.Lock()
	down := time.Now().Before(p.downUntil)
	p.mu.Unlock()
	if down {
		http.Error(w, "scenario disconnect", 503)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	p.wg.Add(1)
	defer p.wg.Done()
	defer c.CloseNow()
	p.mu.Lock()
	p.conn = c
	p.conns = append(p.conns, c)
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if err = p.write(ctx, c, map[string]any{"post_type": "meta_event", "meta_event_type": "heartbeat", "self_id": 100, "interval": 1000, "status": map[string]bool{"online": true, "good": true}}); err != nil {
		return
	}
	// 真协议端持续心跳，避免模型慢时被适配器误判成死连接。
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if p.write(ctx, c, map[string]any{"post_type": "meta_event", "meta_event_type": "heartbeat", "self_id": 100, "interval": 1000}) != nil {
					return
				}
			}
		}
	}()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var a struct {
			Action string          `json:"action"`
			Echo   json.RawMessage `json:"echo"`
			Params struct {
				User    json.RawMessage `json:"user_id"`
				Group   json.RawMessage `json:"group_id"`
				ID      json.RawMessage `json:"message_id"`
				Message json.RawMessage `json:"message"`
			} `json:"params"`
		}
		if json.Unmarshal(b, &a) != nil {
			return
		}
		p.mu.Lock()
		p.actions = append(p.actions, a.Action)
		p.mu.Unlock()
		var data any
		ret := 0
		switch a.Action {
		case "get_login_info":
			data = map[string]any{"user_id": 100, "nickname": "岚禾"}
		case "get_status":
			data = map[string]bool{"online": true, "good": true}
		case "send_group_msg", "send_private_msg", "send_msg":
			conv := "qq:private:100:" + rawID(a.Params.User)
			if len(a.Params.Group) > 0 && rawID(a.Params.Group) != "0" {
				conv = "qq:group:" + rawID(a.Params.Group)
			}
			p.mu.Lock()
			p.sends = append(p.sends, sentMessage{At: time.Now(), Conversation: conv, Action: a.Action, Text: messageText(a.Params.Message), Raw: append(json.RawMessage(nil), a.Params.Message...)})
			id := 97000 + len(p.sends)
			p.mu.Unlock()
			data = map[string]int{"message_id": id}
		case "get_msg":
			p.mu.Lock()
			frame, found := p.history[rawID(a.Params.ID)]
			p.mu.Unlock()
			data = frame
			if !found {
				ret = 100
			}
		case "get_group_msg_history", "get_friend_msg_history":
			p.mu.Lock()
			msgs := []json.RawMessage{}
			for _, f := range p.history {
				msgs = append(msgs, f)
			}
			p.mu.Unlock()
			data = map[string]any{"messages": msgs}
		default:
			ret = 1404
			data = map[string]string{"reason": "fake peer does not implement this action"}
		}
		if err = p.write(ctx, c, map[string]any{"status": map[bool]string{true: "ok", false: "failed"}[ret == 0], "retcode": ret, "echo": a.Echo, "data": data}); err != nil {
			return
		}
		if a.Action == "get_status" {
			select {
			case p.ready <- struct{}{}:
			default:
			}
		}
	}
}
func rawID(b []byte) string { return strings.Trim(string(b), "\"") }
func messageText(b []byte) string {
	var text string
	if json.Unmarshal(b, &text) == nil {
		return text
	}
	var segs []struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &segs) != nil {
		return string(b)
	}
	var out strings.Builder
	for _, s := range segs {
		if s.Type == "text" {
			out.WriteString(s.Data.Text)
		}
	}
	return out.String()
}
func (p *fakePeer) write(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}
func (p *fakePeer) send(ctx context.Context, id string, s step, ids map[string]string) error {
	f := messageFrame(id, s, ids)
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.history[id] = b
	c := p.conn
	p.mu.Unlock()
	if s.Kind == "offline" {
		return nil
	}
	if c == nil {
		return fmt.Errorf("假协议端尚未连接")
	}
	return p.write(ctx, c, f)
}
func messageFrame(id string, s step, ids map[string]string) map[string]any {
	uid := 200
	name := "编的主人"
	if s.Actor == "friend" {
		uid = 400
		name = "编的苔舟"
	}
	segs := []any{}
	if s.Reply != "" {
		segs = append(segs, map[string]any{"type": "reply", "data": map[string]string{"id": ids[s.Reply]}})
	}
	if s.At {
		segs = append(segs, map[string]any{"type": "at", "data": map[string]string{"qq": "100"}})
	}
	if s.Text != "" {
		segs = append(segs, map[string]any{"type": "text", "data": map[string]string{"text": s.Text}})
	}
	if s.Image {
		segs = append(segs, map[string]any{"type": "image", "data": map[string]string{"file": "fictional.png", "url": "http://e2e-image.example/fixture.png"}})
	}
	f := map[string]any{"post_type": "message", "message_type": s.Scene, "sub_type": "friend", "self_id": 100, "user_id": uid, "message_id": id, "time": time.Now().Unix(), "sender": map[string]any{"user_id": uid, "nickname": name}, "message": segs}
	if s.Scene == "group" {
		f["group_id"] = 300
		f["sub_type"] = "normal"
	}
	return f
}
func (p *fakePeer) recall(ctx context.Context, id string, s step) error {
	typ := "friend_recall"
	if s.Scene == "group" {
		typ = "group_recall"
	}
	uid := 200
	if s.Actor == "friend" {
		uid = 400
	}
	f := map[string]any{"post_type": "notice", "notice_type": typ, "self_id": 100, "user_id": uid, "operator_id": uid, "message_id": id, "time": time.Now().Unix()}
	if s.Scene == "group" {
		f["group_id"] = 300
	}
	p.mu.Lock()
	c := p.conn
	p.mu.Unlock()
	return p.write(ctx, c, f)
}
func (p *fakePeer) disconnect(d time.Duration) {
	p.mu.Lock()
	p.downUntil = time.Now().Add(d)
	c := p.conn
	p.mu.Unlock()
	if c != nil {
		c.CloseNow()
	}
}
func (p *fakePeer) waitReady(ctx context.Context) error {
	select {
	case <-p.ready:
		return pause(ctx, 200*time.Millisecond)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *fakePeer) snapshot() ([]sentMessage, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sentMessage(nil), p.sends...), append([]string(nil), p.actions...)
}
func (p *fakePeer) close() {
	p.server.Close()
	p.mu.Lock()
	for _, c := range p.conns {
		c.CloseNow()
	}
	p.mu.Unlock()
	p.wg.Wait()
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
