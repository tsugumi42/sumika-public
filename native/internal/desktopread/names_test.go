package desktopread

import (
	"context"
	"sort"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestSameDisplayNamesKeepActorIdentity(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	local, _ := set.Platform("local")
	for _, id := range []string{"a", "b"} {
		if _, e := local.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:" + id, ActorKind: "character", ActorID: id, Kind: "message", Text: "编造消息", CreatedAt: 1}, nil); e != nil {
			t.Fatal(e)
		}
		v, e := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf("console:" + id), Limit: 200}, "")
		if e != nil {
			t.Fatal(e)
		}
		if len(v.Value.Items) != 1 || v.Value.Items[0].DisplayName != "同名" || v.Value.Items[0].ActorID != id || v.Value.Items[0].ActorKind != "character" {
			t.Fatalf("同名角色身份混合: %+v", v)
		}
	}
	qq, _ := set.Platform("qq")
	if _, e := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname,cards_json) VALUES ('90001','编造昵称','{"90002":"编造群名片"}')`); e != nil {
		t.Fatal(e)
	}
	conv := "qq:group:90002"
	id := add(t, qq, conv, "message", "human", 1, []string{"a"})
	v, e := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200}, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Value.Items) != 1 || v.Value.Items[0].DisplayName != "编造群名片" || v.Value.Items[0].ActorKind != "human" || v.Value.Items[0].Ref.Event.EventID != id {
		t.Fatalf("名片冒充身份或丢失: %+v", v)
	}
}

// 真实数据里人的 ActorID 是 "qq:<号>"、主人是 "owner"（channel.Roster.Resolve）；qq_people 的 uid 是裸号。
// 旧代码拿 "qq:<号>" 去查名册，查不到，界面上每条都显示 qq:<号>、主人显示 owner。
func TestMessageSpeakerNamesUseRealActorShapes(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	if _, e := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname,cards_json) VALUES ('90001','编造昵称','{"90002":"编造群名片"}'),('90003','','{}')`); e != nil {
		t.Fatal(e)
	}
	say := func(conv, kind, actor string, at int64) {
		t.Helper()
		if _, e := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: kind, ActorID: actor, Kind: "message", Text: "编造正文", CreatedAt: at}, []string{"a"}); e != nil {
			t.Fatal(e)
		}
	}
	group, private := "qq:group:90002", "qq:private:80001:90001"
	say(group, "human", "qq:90001", 1)
	say(group, "human", "qq:90003", 2)
	say(group, "owner", "owner", 3)
	say(private, "human", "qq:90001", 1)
	say(private, "owner", "owner", 2)
	names := func(conv string) []string {
		t.Helper()
		v, e := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200}, "")
		if e != nil {
			t.Fatal(e)
		}
		var out []string
		for _, m := range v.Value.Items {
			out = append(out, m.DisplayName)
		}
		return out
	}
	if got := strings.Join(names(group), "|"); got != "编造群名片|qq:90003|主人" {
		t.Fatalf("群里的发言人：%s（有群名片用群名片，名册里查不到退回号码，主人叫主人）", got)
	}
	if got := strings.Join(names(private), "|"); got != "编造昵称|主人" {
		t.Fatalf("私聊的发言人：%s（只用昵称）", got)
	}
}

// 会话列表的标题：群是群名，私聊是对方的昵称；群改名以后下一次订阅快照就是新的
func TestOverviewTitlesFromQQNames(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	for _, q := range []string{
		`INSERT INTO qq_groups(gid,name,updated_at) VALUES ('90002','编造的群',1)`,
		`INSERT INTO qq_people(uid,nickname) VALUES ('90001','编造的朋友')`,
	} {
		if _, e := qq.Events.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	for _, conv := range []string{"qq:group:90002", "qq:group:90003", "qq:private:80001:90001"} {
		if _, e := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:90001", Kind: "message", Text: "编造正文", CreatedAt: 1}, []string{"a"}); e != nil {
			t.Fatal(e)
		}
	}
	sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	titles := func(snap api.Snapshot[api.Overview]) string {
		var out []string
		for _, c := range snap.Result.Value.Conversations.Items {
			if c.Key.Platform == "qq" {
				out = append(out, c.Key.Conversation+"="+c.Title)
			}
		}
		sort.Strings(out)
		return strings.Join(out, "|")
	}
	if got := titles(next(t, sub)); got != "qq:group:90002=编造的群|qq:group:90003=|qq:private:80001:90001=编造的朋友" {
		t.Fatalf("会话标题：%s", got)
	}
	// 群改名后来了新消息，快照刷新，标题跟上
	if _, e := qq.Events.Exec(`UPDATE qq_groups SET name='改过的群名',updated_at=2 WHERE gid='90002'`); e != nil {
		t.Fatal(e)
	}
	if _, e := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:90002", ActorKind: "human", ActorID: "qq:90001", Kind: "message", Text: "又一条", CreatedAt: 2}, []string{"a"}); e != nil {
		t.Fatal(e)
	}
	if got := titles(next(t, sub)); !strings.Contains(got, "qq:group:90002=改过的群名") {
		t.Fatalf("改名后的会话标题：%s", got)
	}
}
