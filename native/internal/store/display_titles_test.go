package store

import (
	"context"
	"strings"
	"testing"
)

// 会话标题：群用 qq_groups 的群名，私聊用对方在 qq_people 的昵称（会话号是 qq:private:<她的号>:<对方的号>）。
// 旧代码 QQ 会话一律没有标题，界面上列表显示 qq:group:<群号> 和 qq:private:<号>:<号>。
func TestQQConversationTitles(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	ctx := context.Background()
	for _, g := range [][2]string{
		{"91001", "编造的群"},
		{"91002", ""},
		{"91004", "群​名\n换行"},
		{"91005", strings.Repeat("长", 60)},
	} {
		if _, err := qq.Events.Exec(`INSERT INTO qq_groups(gid,name,updated_at) VALUES (?,?,1)`, g[0], g[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range [][2]string{
		{"80001", "她自己的昵称"},
		{"92001", "编造的朋友"},
		{"92002", ""},
		{"92004", "朋​友\n乙"},
	} {
		if _, err := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname) VALUES (?,?)`, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{
		"qq:group:91001":         "编造的群",
		"qq:group:91002":         "", // 群名是空串：留空，界面退回会话号
		"qq:group:91003":         "", // 没有这个群
		"qq:group:91004":         "群名 换行",
		"qq:group:91005":         strings.Repeat("长", 40) + "…",
		"qq:private:80001:92001": "编造的朋友", // 用对方的昵称，不用她自己（80001）的
		"qq:private:80001:92002": "",
		"qq:private:80001:92003": "",
		"qq:private:80001:92004": "朋友 乙",
		"qq:private:80001:80001": "她自己的昵称", // 对方就是自己号（自己给自己发）：按对方号查，不特殊处理
	}
	for conv := range want {
		if _, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:92001", Kind: "message", Text: "编造正文", CreatedAt: 100}, []string{"a"}); err != nil {
			t.Fatal(err)
		}
	}
	titles := func() map[string]string {
		t.Helper()
		d, err := set.ReadDisplayConversations(ctx, []string{"a"}, "", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, v := range d.Items {
			if strings.HasPrefix(v.Conversation, "qq:") {
				out[v.Conversation] = v.Title
			}
		}
		return out
	}
	got := titles()
	if len(got) != len(want) {
		t.Fatalf("会话数 %d，要 %d：%v", len(got), len(want), got)
	}
	for conv, w := range want {
		if got[conv] != w {
			t.Errorf("%s 的标题 %q，要 %q", conv, got[conv], w)
		}
	}

	// 群改名、对方改昵称：下一次读就是新的，不缓存
	if _, err := qq.Events.Exec(`UPDATE qq_groups SET name='改过的群名',updated_at=2 WHERE gid='91001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := qq.Events.Exec(`UPDATE qq_people SET nickname='改过的昵称' WHERE uid='92001'`); err != nil {
		t.Fatal(err)
	}
	got = titles()
	if got["qq:group:91001"] != "改过的群名" || got["qq:private:80001:92001"] != "改过的昵称" {
		t.Errorf("改名以后：%q、%q", got["qq:group:91001"], got["qq:private:80001:92001"])
	}

	// 分页：第二页的标题也要补
	var seen []string
	cursor := ""
	for {
		d, err := set.ReadDisplayConversations(ctx, []string{"a"}, "", cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range d.Items {
			if !strings.HasPrefix(v.Conversation, "qq:") {
				continue
			}
			if v.Title != want[v.Conversation] && v.Conversation != "qq:group:91001" && v.Conversation != "qq:private:80001:92001" {
				t.Errorf("分页里 %s 的标题 %q，要 %q", v.Conversation, v.Title, want[v.Conversation])
			}
			seen = append(seen, v.Conversation)
		}
		if !d.HasMore {
			break
		}
		cursor = d.NextCursor
	}
	if len(seen) != len(want) {
		t.Errorf("分页共 %d 个，要 %d", len(seen), len(want))
	}
}

// 本地话题自己有标题，不受 QQ 的名册影响；没有标题的本地日常照旧留空（界面用角色名）
func TestLocalTitlesUntouchedByQQNames(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	if _, err := local.Events.Exec(`INSERT INTO local_topics(id,character_id,title,created_at) VALUES ('t1','a','本地话题',1)`); err != nil {
		t.Fatal(err)
	}
	d, err := set.ReadDisplayConversations(context.Background(), []string{"a"}, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, v := range d.Items {
		titles[v.Conversation] = v.Title
	}
	if titles["console:a:topic:t1"] != "本地话题" || titles["console:a"] != "" {
		t.Fatalf("本地标题：%v", titles)
	}
}
