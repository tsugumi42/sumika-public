package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/audience"
	"sumika-native/internal/clientinfo"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
	"testing"
)

func qqClientIdentity(a *App) (kernel.Invocation, audience.Audience) {
	conv := "qq:private:" + a.account + ":" + string(a.cfg.QQ.Owner)
	return kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{CharacterID: a.character, Conversation: conv, ScopeID: "fixture", Platform: "qq", Channel: "qq", AccountID: a.account, Scene: "private", ActorKind: "owner", ActorID: "owner"}}, audience.Audience{Channel: "qq", Conversation: conv, Scene: "private", PrivateOK: true, OwnerAsked: true}
}
func TestClientOwnerBoundary(t *testing.T) {
	t.Parallel()
	a := &App{account: "501", character: "fixture"}
	a.cfg.QQ.Owner = config.QQID("502")
	in, aud := qqClientIdentity(a)
	if !a.clientInfoOwner(in, aud) {
		t.Fatal("owner denied")
	}
	cases := []struct {
		name   string
		change func(*kernel.Invocation, *audience.Audience)
	}{
		{"missing", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.ActorID = "" }},
		{"zero", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.ActorID = "0" }},
		{"spoof", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.ActorKind = "human" }},
		{"bot", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.ActorID = "501" }},
		{"peer", func(i *kernel.Invocation, a *audience.Audience) {
			i.Scope.Conversation = "qq:private:501:503"
			a.Conversation = i.Scope.Conversation
		}},
		{"account", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.AccountID = "504" }},
		{"character", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.CharacterID = "other" }},
		{"QQ group", func(i *kernel.Invocation, a *audience.Audience) { i.Scope.Scene = "group"; a.Scene = "group" }},
		{"local group", func(i *kernel.Invocation, a *audience.Audience) {
			i.Scope.Channel = "console"
			a.Channel = "console"
			i.Scope.Scene = "group"
			a.Scene = "group"
		}},
		{"child", func(i *kernel.Invocation, a *audience.Audience) { i.Kind = kernel.KindChild }},
		{"not asked", func(i *kernel.Invocation, a *audience.Audience) { a.OwnerAsked = false }},
		{"mismatch", func(i *kernel.Invocation, a *audience.Audience) { a.Conversation = "elsewhere" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y := in, aud
			tc.change(&x, &y)
			if a.clientInfoOwner(x, y) {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	for _, bad := range []string{"", "0", "501", "0502", "502x"} {
		a.cfg.QQ.Owner = config.QQID(bad)
		badIn, badAud := qqClientIdentity(a)
		if a.clientInfoOwner(badIn, badAud) {
			t.Fatalf("bad owner %q", bad)
		}
	}
	a.cfg.QQ.Owner = config.QQID("502")
	for _, bad := range []string{"", "0", "502", "0501", "501x", "18446744073709551616"} {
		a.account = bad
		badIn, badAud := qqClientIdentity(a)
		if a.clientInfoOwner(badIn, badAud) {
			t.Fatalf("bad account %q", bad)
		}
	}
	for _, key := range []string{"qq.owner", "qq.accounts", "tools.workdir", "linkReader.proxy", "characters.other.model", "linkReader.expand.overrides.qq:private:501:503.allowSensitive", "characters.fixture.aliases"} {
		if clientSettingAllowed(key, "fixture", in.Scope.Conversation) {
			t.Fatal("exposed", key)
		}
	}
}
func TestInspectClientRoundTrip(t *testing.T) {
	t.Parallel()
	for _, scene := range []string{"local", "qq"} {
		t.Run(scene, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Replies: []llmtest.Reply{llmtest.Call("inspect", "inspect_client", `{"keys":["linkReader.expand.maxLinks","agent.context.window"]}`), llmtest.Text("已读取只读设置")}}, &e.bodies, &e.mu))
			defer srv.Close()
			e.llm = srv
			a := e.open()
			defer a.Close()
			file := filepath.Join(e.data, "config", "sumika.yml")
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if scene == "local" {
				out := <-a.hub.Send(testChar, "查看客户端设置")
				if out.Err != nil {
					t.Fatal(out.Err)
				}
			} else {
				in, aud := qqClientIdentity(a)
				out := <-a.hub.Respond(turn.Input{Identity: in, CharacterID: a.character, Conversation: in.Scope.Conversation, Scope: in.Scope.ScopeID, Audience: aud, Text: "查看客户端设置"})
				if out.Err != nil {
					t.Fatal(out.Err)
				}
			}
			e.mu.Lock()
			bodies := append([]string(nil), e.bodies...)
			e.mu.Unlock()
			if len(bodies) < 2 {
				t.Fatalf("missing result request: %d", len(bodies))
			}
			if !strings.Contains(bodies[0], "inspect_client") || !strings.Contains(bodies[0], "[客户端能力]") {
				t.Fatal("tool or summary not wired")
			}
			last := bodies[len(bodies)-1]
			for _, marker := range []string{"linkReader.expand.maxLinks", "running_known", "当前阶段只读", "可以用 preview_setting_change 提议修改"} {
				if !strings.Contains(last, marker) {
					t.Fatalf("missing %s in result: %s", marker, last)
				}
			}
			for _, bad := range []string{"qq.accounts", "linkReader.proxy", "tools.workdir"} {
				if strings.Contains(last, bad) {
					t.Fatal("unexpected capability", bad)
				}
			}
			after, _ := os.ReadFile(file)
			if string(before) != string(after) {
				t.Fatal("read changed config")
			}
			forged, aud := qqClientIdentity(a)
			if _, err := a.inspectClient(context.Background(), forged, aud, true); err == nil {
				t.Fatal("fabricated invocation accepted")
			}
		})
	}
}

func TestInspectClientDeniedScenes(t *testing.T) {
	t.Parallel()
	for _, scene := range []string{"local-group", "qq-group", "qq-peer"} {
		t.Run(scene, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			defer a.Close()
			in, aud := qqClientIdentity(a)
			switch scene {
			case "local-group":
				in.Scope.Platform = "local"
				in.Scope.Channel = "console"
				in.Scope.Conversation = "console:group:fixture"
				in.Scope.Scene = "group"
				aud.Channel = "console"
				aud.Scene = "group"
			case "qq-group":
				in.Scope.Conversation = "qq:group:" + testGroup
				in.Scope.Scene = "group"
				aud.Scene = "group"
			case "qq-peer":
				in.Scope.ActorKind = "human"
				in.Scope.ActorID = "qq:503"
				in.Scope.Conversation = "qq:private:" + testAccount + ":503"
				aud.PrivateOK = false
				aud.OwnerAsked = false
			}
			aud.Conversation = in.Scope.Conversation
			out := <-a.hub.Respond(turn.Input{Identity: in, CharacterID: a.character, Conversation: in.Scope.Conversation, Scope: in.Scope.ScopeID, Audience: aud, Text: "你有什么设置"})
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			e.mu.Lock()
			bodies := append([]string(nil), e.bodies...)
			e.mu.Unlock()
			if len(bodies) == 0 {
				t.Fatal("no model request")
			}
			var request struct {
				Tools []struct{ Function struct{ Name string } }
			}
			if err := json.Unmarshal([]byte(bodies[0]), &request); err != nil {
				t.Fatal(err)
			}
			for _, tool := range request.Tools {
				if tool.Function.Name == "inspect_client" {
					t.Fatal("management tool exposed")
				}
			}
		})
	}
}

// 项目说明开了但工作目录不在信任根里：说明文件一个都不会读，模型却看不出来。
// 旧代码对这项永远报"未核验、不可用"，既不会说"已生效"，也不会说"为什么没生效"。
func TestClientInfoProjectContextTrust(t *testing.T) {
	t.Parallel()
	trusted, outside := t.TempDir(), t.TempDir()
	scope := kernel.Scope{CharacterID: testChar, Conversation: "console:" + testChar, ScopeID: "owner", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner"}
	for _, tc := range []struct {
		name, workdir string
		want          bool
		reason        string
	}{
		{"工作目录在信任根里", filepath.Join(trusted, "work"), true, "已经信任"},
		{"工作目录没有信任", outside, false, "还没有信任"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := os.MkdirAll(tc.workdir, 0o755); err != nil {
				t.Fatal(err)
			}
			e := newEnv(t)
			e.extraConfig = "tools:\n  workdir: '" + tc.workdir + "'\nprojectContext:\n  enabled: true\n  trustedRoots:\n    - '" + trusted + "'\n"
			e.writeConfig(testToken)
			a := e.open()
			defer a.Close()
			var got *clientinfo.Capability
			for _, c := range a.clientCapabilities(scope, audience.Audience{Channel: "console", Scene: "private", Conversation: scope.Conversation, PrivateOK: true, OwnerAsked: true}, a.currentLinks()) {
				if c.ID == "projectcontext" {
					got = &c
				}
			}
			if got == nil || !got.AvailabilityKnown || got.Available != tc.want || !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("projectcontext 状态不对：%+v", got)
			}
			if ok, why := a.projectContextState(kernel.Scope{Channel: "qq", Scene: "private"}); ok || !strings.Contains(why, "本地私聊") {
				t.Fatalf("QQ 不该报可用：%v %s", ok, why)
			}
		})
	}
}
