package clientinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/turn"
	"testing"
)

func fixture(t *testing.T) (*Plugin, turn.ToolCall) {
	t.Helper()
	in := kernel.Invocation{Scope: kernel.Scope{CharacterID: "fixture", Conversation: "console:fixture"}}
	return &Plugin{Host: Host{Visible: func(a audience.Audience) bool { return a.PrivateOK }, Read: func(context.Context, kernel.Invocation, audience.Audience, bool) (View, error) {
		v := View{Version: "v1", Capabilities: []Capability{{ID: "link-reader", State: "已注册"}}}
		for i := 0; i < 32; i++ {
			v.Settings = append(v.Settings, Setting{Key: fmt.Sprintf("key%d", i), Capability: "client"})
		}
		return v, nil
	}}}, turn.ToolCall{Identity: in, CharacterID: "fixture", Conversation: "console:fixture", Audience: audience.Audience{PrivateOK: true}}
}
func TestInspectPaginationAndRejectInvalid(t *testing.T) {
	p, c := fixture(t)
	for _, args := range []string{`null`, `{"owner":true}`, `{"offset":-1}`, `{"offset":1.5}`, `{"offset":1}`, `{"keys":[false]}`, `{"keys":["secret"]}`, `{"version":"stale"}`, `{"plugin":"unknown"}`} {
		c.Args, _ = jsjson.Parse(args)
		r, e := p.inspect(t.Context(), c)
		if e != nil || !r.IsError {
			t.Fatalf("accepted %s: %+v %v", args, r, e)
		}
	}
	c.Args, _ = jsjson.Parse(`{}`)
	r, _ := p.inspect(t.Context(), c)
	var out struct {
		Settings []Setting
		Total    int `json:"total_settings"`
		Next     int `json:"next_offset"`
	}
	if e := json.Unmarshal([]byte(r.Text), &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Settings) != 25 || out.Total != 32 || out.Next != 25 {
		t.Fatalf("bad page %+v", out)
	}
	c.Args, _ = jsjson.Parse(`{"offset":25,"version":"v1"}`)
	r, _ = p.inspect(t.Context(), c)
	if r.IsError || !strings.Contains(r.Text, "key31") {
		t.Fatal(r)
	}
	c.Audience.PrivateOK = false
	r, _ = p.inspect(t.Context(), c)
	if !r.IsError {
		t.Fatal("denied audience accepted")
	}
}
func TestContextAcceptRefreshAndBound(t *testing.T) {
	p, c := fixture(t)
	in := turn.ContextIn{Identity: c.Identity, CharacterID: c.CharacterID, Conversation: c.Conversation, Audience: c.Audience, ContextID: "one", State: map[string]any{}}
	v, e := p.context(t.Context(), in)
	if e != nil || v == nil {
		t.Fatal(e)
	}
	again, _ := p.context(t.Context(), in)
	if again == nil {
		t.Fatal("marked before acceptance")
	}
	v.OnAccepted()
	again, _ = p.context(t.Context(), in)
	if again != nil {
		t.Fatal("duplicate summary")
	}
	in.ContextID = "two"
	again, _ = p.context(t.Context(), in)
	if again == nil {
		t.Fatal("missing after compaction")
	}
	p.Host.Read = func(context.Context, kernel.Invocation, audience.Audience, bool) (View, error) {
		v := View{}
		for i := 0; i < 100; i++ {
			v.Capabilities = append(v.Capabilities, Capability{ID: fmt.Sprint(i), Reason: strings.Repeat("长", 100)})
		}
		return v, nil
	}
	bounded, _ := p.context(t.Context(), in)
	if n := len([]rune(strings.Join(bounded.Lines, ""))); n > 1500 {
		t.Fatalf("summary length %d", n)
	}
	if !strings.Contains(bounded.Lines[0], "未全部展示") {
		t.Fatal("silent truncation")
	}
}

func TestPluginFilterDoesNotReturnOtherSettings(t *testing.T) {
	p, c := fixture(t)
	c.Args, _ = jsjson.Parse(`{"plugin":"link-reader"}`)
	r, _ := p.inspect(t.Context(), c)
	var v View
	if err := json.Unmarshal([]byte(r.Text), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Settings) != 0 {
		t.Fatal("unrelated settings returned")
	}
	c.Args, _ = jsjson.Parse(`{"plugin":"link-reader","keys":["key0"]}`)
	r, _ = p.inspect(t.Context(), c)
	if !r.IsError {
		t.Fatal("cross plugin key accepted")
	}
}
