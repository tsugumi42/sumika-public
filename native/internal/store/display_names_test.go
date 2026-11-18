package store

import (
	"context"
	"strings"
	"testing"
)

func TestCleanDisplayName(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("长", 60)
	for _, c := range []struct{ name, in, want string }{
		{"普通", "编造昵称", "编造昵称"},
		{"空串", "", ""},
		{"换行并成空格", "第一行\n第二行\r\n第三行", "第一行 第二行 第三行"},
		{"制表和全角空格", "甲\t\u3000乙", "甲 乙"},
		{"零宽去掉", "群\u200b名\ufeff片\u2060", "群名片"},
		{"方向控制去掉", "\u202eevil\u202c", "evil"},
		{"其他控制字符去掉", "名\x00字\x07", "名字"},
		{"只有零宽等于没有", "\u200b\u200c\u200d\ufeff", ""},
		{"只有空白等于没有", " \n\t ", ""},
		{"首尾空白去掉", "  名字  ", "名字"},
		{"emoji 拼合的连接符留着", "家\U0001F468\u200d\U0001F469\u200d\U0001F467", "家\U0001F468\u200d\U0001F469\u200d\U0001F467"},
		{"夹在字之间的连接符去掉", "甲\u200d乙", "甲乙"},
		{"结尾的连接符去掉", "名字\U0001F468\u200d", "名字\U0001F468"},
		{"过长截断加省略号", long, strings.Repeat("长", 40) + "…"},
		{"恰好够长不截", strings.Repeat("长", 40), strings.Repeat("长", 40)},
	} {
		if got := CleanDisplayName(c.in); got != c.want {
			t.Errorf("%s：%q 清成 %q，要 %q", c.name, c.in, got, c.want)
		}
		if again := CleanDisplayName(CleanDisplayName(c.in)); again != CleanDisplayName(c.in) {
			t.Errorf("%s：清两遍和清一遍不一样：%q → %q", c.name, CleanDisplayName(c.in), again)
		}
	}
}

// 发言人名字：人的 ActorID 是 "qq:<号>"，qq_people.uid 是裸号（channel.Roster.Resolve 这样写）。
// 旧代码拿 "qq:<号>" 直接查，一个也查不到，界面上整页显示 qq:<号>。
func TestSpeakerNamesFromRoster(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	ctx := context.Background()
	people := []struct{ uid, nick, cards string }{
		{"90011", "编造昵称甲", `{"90002":"编造群名片甲","90003":"别的群的名片"}`},
		{"90012", "编造昵称乙", `{}`},
		{"90013", "", `{}`},
		{"90015", "昵称丙", "{\"90002\":\"群\\u200b名\\n片\"}"},
		{"90016", "零宽名片的昵称", "{\"90002\":\"\\u200b\\u200c\"}"},
		{"90017", "昵称丁", `{"90002":"` + strings.Repeat("长", 60) + `"}`},
		{"90018", "坏名片的昵称", `{坏掉`},
		{"90019", "名片是数字的昵称", `{"90002":12345}`},
		{"90020", "只在别的群有名片", `{"90003":"别群名片"}`},
		{"90030", "导入时冻住的昵称", `{"90002":"导入时冻住的名片"}`},
	}
	for _, p := range people {
		if _, err := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname,cards_json) VALUES (?,?,?)`, p.uid, p.nick, p.cards); err != nil {
			t.Fatal(err)
		}
	}
	type spec struct {
		id, kind string
		from     map[string]any // 入库时存下的发言当时的名字
	}
	say := func(conv string, s spec) string {
		t.Helper()
		var seg any
		if s.from != nil {
			seg = map[string]any{"from": s.from}
		}
		seq, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: s.kind, ActorID: s.id, Kind: "message", Text: "编造正文", Segments: seg, CreatedAt: 100}, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	read := func(conv string) map[string]DisplayRow {
		t.Helper()
		page, err := qq.ReadDisplay(ctx, DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a"}}, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]DisplayRow{}
		for _, r := range page.Items {
			out[r.ID] = r
		}
		return out
	}

	group := "qq:group:90002"
	cases := []struct {
		name string
		s    spec
		want string
	}{
		{"本群群名片优先于昵称", spec{id: "qq:90011", kind: "human"}, "编造群名片甲"},
		{"没有群名片用昵称", spec{id: "qq:90012", kind: "human"}, "编造昵称乙"},
		{"昵称为空也没有名片：什么都查不到留空", spec{id: "qq:90013", kind: "human"}, ""},
		{"名册里没有这个人：用入库时存下的群名片", spec{id: "qq:90014", kind: "human", from: map[string]any{"card": "存下的名片", "nick": "存下的昵称"}}, "存下的名片"},
		{"名册里没有这个人：没有名片用存下的昵称", spec{id: "qq:90021", kind: "human", from: map[string]any{"nick": "存下的昵称"}}, "存下的昵称"},
		{"名册里没有、存下的也没有：留空", spec{id: "qq:90022", kind: "human"}, ""},
		{"名片带换行和零宽", spec{id: "qq:90015", kind: "human"}, "群名 片"},
		{"名片清完是空的退到昵称", spec{id: "qq:90016", kind: "human"}, "零宽名片的昵称"},
		{"名片过长截断", spec{id: "qq:90017", kind: "human"}, strings.Repeat("长", 40) + "…"},
		{"cards_json 坏掉退到昵称", spec{id: "qq:90018", kind: "human"}, "坏名片的昵称"},
		{"这个群的名片不是字符串退到昵称", spec{id: "qq:90019", kind: "human"}, "名片是数字的昵称"},
		{"别的群的名片不能当本群的", spec{id: "qq:90020", kind: "human"}, "只在别的群有名片"},
		{"没有 qq: 前缀的号也认", spec{id: "90011", kind: "human"}, "编造群名片甲"},
		{"qq: 后面是空的（协议端没说是谁）", spec{id: "qq:", kind: "human"}, ""},
		// Go 接管以后没有程序写 qq_people，导入的人改了名，新消息里存下的才是新名字
		{"名册和存下的都有且不同：存下的群名片优先", spec{id: "qq:90030", kind: "human", from: map[string]any{"card": "改过的新名片", "nick": "改过的新昵称"}}, "改过的新名片"},
		{"存下的没有名片：用存下的昵称，不退回名册里冻住的旧名片", spec{id: "qq:90030", kind: "human", from: map[string]any{"card": "", "nick": "改过的新昵称"}}, "改过的新昵称"},
		{"存下的名片清完是空的：退到存下的昵称", spec{id: "qq:90030", kind: "human", from: map[string]any{"card": "​", "nick": "改过的新昵称"}}, "改过的新昵称"},
		{"存下的名字全是空的：退到名册", spec{id: "qq:90030", kind: "human", from: map[string]any{"card": "", "nick": ""}}, "导入时冻住的名片"},
		{"导入来的旧事件没存名字：用名册", spec{id: "qq:90030", kind: "human"}, "导入时冻住的名片"},
		{"主人也用原本的群名片（认主人靠 ActorKind，不靠名字）", spec{id: "owner", kind: "owner", from: map[string]any{"id": "90040", "card": "主人的群名片"}}, "主人的群名片"},
		{"主人没存名字：留空，读取层再写主人", spec{id: "owner", kind: "owner"}, ""},
		{"角色自己的号这里不填", spec{id: "a", kind: "character", from: map[string]any{"card": "不该出现"}}, ""},
	}
	ids := map[string]string{}
	for _, c := range cases {
		ids[c.name] = say(group, c.s)
	}
	rows := read(group)
	for _, c := range cases {
		if got := rows[ids[c.name]].DisplayName; got != c.want {
			t.Errorf("群里 %s：显示名 %q，要 %q", c.name, got, c.want)
		}
	}

	// 私聊：只看昵称，不拿任何群的名片
	private := "qq:private:80001:90011"
	pid := say(private, spec{id: "qq:90011", kind: "human"})
	pid2 := say(private, spec{id: "qq:90014", kind: "human", from: map[string]any{"card": "私聊里不认名片", "nick": "私聊存下的昵称"}})
	pid3 := say(private, spec{id: "qq:90030", kind: "human", from: map[string]any{"card": "私聊不认名片", "nick": "改过的新昵称"}})
	prows := read(private)
	if prows[pid3].DisplayName != "改过的新昵称" {
		t.Errorf("私聊里名册和存下的都有：%q，要存下的昵称", prows[pid3].DisplayName)
	}
	if prows[pid].DisplayName != "编造昵称甲" {
		t.Errorf("私聊发言人：%q，要昵称而不是群名片", prows[pid].DisplayName)
	}
	if prows[pid2].DisplayName != "私聊存下的昵称" {
		t.Errorf("私聊里名册没有的人：%q，要存下的昵称，不用群名片", prows[pid2].DisplayName)
	}

	// 引用：被引用那条的作者也走同一个查询
	quoted := say(group, spec{id: "qq:90012", kind: "human"})
	replySeq, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: group, ActorKind: "human", ActorID: "qq:90011", Kind: "message", Text: "回复", ReplyTo: &quoted, CreatedAt: 200}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	var replyID string
	if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, replySeq).Scan(&replyID); err != nil {
		t.Fatal(err)
	}
	r := read(group)[replyID]
	if r.Reply == nil || r.Reply.DisplayName != "编造昵称乙" || r.DisplayName != "编造群名片甲" {
		t.Errorf("引用：%+v", r)
	}

	// 名字改了，下一次读就是新的，不缓存
	if _, err := qq.Events.Exec(`UPDATE qq_people SET cards_json='{"90002":"改过的名片"}' WHERE uid='90011'`); err != nil {
		t.Fatal(err)
	}
	if got := read(group)[ids[cases[0].name]].DisplayName; got != "改过的名片" {
		t.Errorf("名片改了以后还是 %q", got)
	}
}
