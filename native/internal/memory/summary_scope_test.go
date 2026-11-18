package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// 整理写进去的事实：一律待确认、范围由程序按场合定、证据只用人说的话（W4，13.2.6）

const groupConv = "qq:group:5001001"

// 一律先写待确认：src 里有主人的一句也不直接有效（Bun 的 B11 是有效）；没有 src、src 写坏、越界、只指向她自己的话，也是待确认，证据和归属各按能认出的记
func TestEveryFactTheModelWritesIsACandidate(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-ai-facts-are-candidates")
	r := newRig(t, groupConv)
	e1 := r.say(pingA, "我下周要搬家。")        // 1 群友
	e2 := r.say(ownerA, "我最近在学吉他。")      // 2 主人
	e3 := r.say(herA, "那挺好的呀。")          // 3 她
	e4 := r.say(herAcct, "（主人用她的号手滑发的）") // 4 她的号上别人发的
	e5 := r.say(boA, "听说阿萍要搬家。")         // 5 另一个群友，转述
	r.reply("no", "",
		cand("阿萍下周搬家", "qq:60001", e1.Seq),                                                  // 本人说的
		cand("主人在学吉他", "owner", e2.Seq),                                                     // 主人说的
		upsert("主人在学吉他的进度", "owner", "public", e2.Seq),                                      // upsert_fact，src 里有主人的话：Bun 里是有效，这里也是待确认
		cand("主人没写来源的事", "owner"),                                                           // 没有 src
		`{"op":"candidate","kind":"fact","text":"主人src写坏的事","subject":"owner","src":"第二句"}`, // src 写坏
		cand("主人越界编号的事", "owner", 99),                                                       // 越界，但批里有主人的话
		cand("主人一半越界的事", "owner", e2.Seq, 99),                                               // 一个对的一个越界：整个当没有
		cand("主人只指向她的话", "owner", e3.Seq),                                                   // 只指向她自己的话
		cand("主人只指向她号上别人的话", "owner", e4.Seq),                                               // 只指向她的号上别人发的
		cand("阿萍搬家是转述的", "qq:60001", e5.Seq),                                                // 别人转述的
		cand("铃音喜欢吉他", "self", e1.Seq),                                                      // 她自己的事，群友说的
		cand("群友替主人说的事", "owner", e1.Seq),                                                   // 主人的事，群友说的
		cand("src 里有群友也有主人", "owner", e1.Seq, e2.Seq))                                       // 两行：不是本人说的（ADR-231），证据取第一句人话
	res := r.run()
	if active := count(t, r.m, `SELECT count(*) FROM facts WHERE status <> 'candidate'`); active != 0 || len(r.facts()) != 13 {
		t.Fatalf("一律待确认（%s）：%d 条不是，共 %d 条", it.Go, active, len(r.facts()))
	}
	type want struct {
		text                string
		evidence, byself    int
		attributed, sources string
		audience            string
	}
	for _, w := range []want{
		{"阿萍下周搬家", 1, 1, "", "[1]", "members"},
		{"主人在学吉他", 1, 1, "", "[2]", "owner"},
		{"主人在学吉他的进度", 1, 1, "", "[2]", "owner"},
		{"主人没写来源的事", 0, 0, "", "[]", "members"}, // 没有 src：不退回整批原话；批里有群友，在场的算 members
		{"主人src写坏的事", 0, 0, "", "[]", "members"},
		{"主人越界编号的事", 0, 0, "", "[]", "members"},
		{"主人一半越界的事", 0, 0, "", "[]", "members"},
		{"主人只指向她的话", 0, 0, "", "[3]", "owner"}, // 有 src 但没有证据
		{"主人只指向她号上别人的话", 0, 0, "", "[4]", "owner"},
		{"阿萍搬家是转述的", 1, 0, "qq:60002", "[5]", "members"}, // 证据算一次，不是本人说的，记下是谁说的
		{"铃音喜欢吉他", 1, 0, "qq:60001", "[1]", "members"},
		{"群友替主人说的事", 1, 0, "qq:60001", "[1]", "members"},
		{"src 里有群友也有主人", 1, 0, "qq:60001", "[1,2]", "members"}, // 旧：本人说的优先，算本人说的；ADR-231 起两行一律是转述，证据是第一句人话（阿萍）
	} {
		f := r.fact(w.text)
		if f.evidence != w.evidence || f.byself != w.byself || f.attributed != w.attributed || f.sources != w.sources || f.audience != w.audience {
			t.Errorf("%s：证据 %d（本人 %d）归属 %q 来源 %s 在场 %s，应该是 %d（%d）%q %s %s", w.text, f.evidence, f.byself, f.attributed, f.sources, f.audience,
				w.evidence, w.byself, w.attributed, w.sources, w.audience)
		}
	}
	// 来源的 origin：证据那一句是谁的
	for text, origin := range map[string]string{"阿萍下周搬家": "peer", "主人在学吉他": "owner", "主人只指向她的话": ""} {
		if got := r.fact(text).origin; got != origin {
			t.Errorf("%s 的 origin %q，应该是 %q", text, got, origin)
		}
	}
	for k, n := range map[string]int{CountSrcMissing: 1, CountSrcUnparsable: 1, CountSrcOutOfBatch: 2, CountSrcNoEvidence: 2, CountOpUpsert: 1, CountOpCandidate: 12} {
		if res.Counts[k] != n {
			t.Errorf("计数 %s=%d，应该 %d：%v", k, res.Counts[k], n, res.Counts)
		}
	}
}

// 不退回整批原话：越界的 src 当没有，批里有谁的话都不借给它（退回会让批里任一句的归属扩大，13.2.6）
func TestOutOfBatchSrcDoesNotFallBackToTheWholeBatch(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-src-out-of-batch-is-none")
	r := newRig(t, groupConv)
	r.say(ownerA, "我在学吉他。")
	r.say(pingA, "我下周搬家。")
	r.reply("no", "", cand("主人在学吉他", "owner", 99), cand("阿萍下周搬家", "qq:60001"))
	r.run()
	for _, f := range r.facts() {
		if f.sources != "[]" || f.evidence != 0 || f.origin != "" || f.attributed != "" {
			t.Errorf("%s：没有 src 就没有来源、证据、归属（%s）：%+v", f.text, it.Go, f)
		}
	}
}

// 说话人对不上号的丢这一条：编出来的编号、名字、她自己的编号、不在这一批的人
func TestSubjectMustBeInTheSpeakerTable(t *testing.T) {
	t.Parallel()
	r := newRig(t, groupConv)
	r.say(pingA, "我下周搬家。")
	r.say(herAcct, "手滑了")
	r.reply("no", "",
		cand("对得上的", "qq:60001", 1),
		cand("写了名字不是编号", "阿萍", 1),
		cand("编号不在这一批", "qq:99999", 1),
		cand("写成她的角色编号", "suzune", 1),
		cand("带了前缀", "person:qq:60001", 1),
		cand("OWNER 大小写不拘", "OWNER", 1),
		`{"op":"candidate","kind":"fact","text":"没写主体的事","src":[1]}`)
	res := r.run()
	got := strings.Join(r.factTexts(), "|")
	if got != "对得上的|OWNER 大小写不拘|没写主体的事" || res.Counts[CountSubjectUnknown] != 4 {
		t.Errorf("事实 %s 计数 %v", got, res.Counts)
	}
	if f := r.fact("OWNER 大小写不拘"); f.subject != "owner" {
		t.Errorf("主体 %q", f.subject)
	}
	if f := r.fact("对得上的"); f.subject != "qq:60001" {
		t.Errorf("群友的事主体存编号本身（对得上人名表的 Person.ID）：%q", f.subject)
	}
}

// 范围由程序按来源场合定（4.2 那张表），模型只能用 visibility 收紧。五种场合 × 三列，邻居不变都在表里
func TestB13ScopeIsSetByTheProgram(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-scope-set-by-program")
	g := groupConv
	type place struct {
		name, conv string
		lines      []actor
		other      string // "别人的事"这一列用的 subject
	}
	places := []place{
		{"本地私聊", "console:suzune", []actor{ownerA}, ""},
		{"本地群聊", "console:group:g1", []actor{ownerA, otherCh}, "kaede"},
		{"平台私聊·主人", "qq:private:20001:7001", []actor{ownerA}, ""},
		{"平台私聊·某人", "qq:private:20001:7002", []actor{pingA}, "qq:60001"},
		{"平台群聊", g, []actor{ownerA, pingA}, "qq:60001"},
	}
	// 三列：主人自己的事、她自己的事、别人自己的事
	want := map[string][3]string{
		"本地私聊":    {"owner", "owner", "owner"},
		"本地群聊":    {"owner", "owner", "owner"},
		"平台私聊·主人": {"owner", "owner", "owner"},
		"平台私聊·某人": {"owner", "person:qq:60001", "person:qq:60001"}, // 某人的私聊里提到主人的事：待确认，范围 owner
		"平台群聊":    {"conv:" + g + ",owner", "conv:" + g, "conv:" + g},
	}
	for _, p := range places {
		t.Run(p.name, func(t *testing.T) {
			r := newRig(t, p.conv)
			for _, a := range p.lines {
				r.say(a, "说了一句话")
			}
			r.reply("no", "", cand("主人的事", "owner", 1), cand("她的事", "self", 1), cand("别人的事", p.other, 1))
			r.run()
			w := want[p.name]
			for i, text := range []string{"主人的事", "她的事", "别人的事"} {
				if got := r.fact(text).scopes; got != w[i] {
					t.Errorf("%s 的范围 %q，应该是 %q（%s）", text, got, w[i], it.Go)
				}
			}
		})
	}

	// 片段的范围：整段对话的摘要。主人在群里说过话就再加 owner
	for _, c := range []struct {
		name, conv string
		lines      []actor
		want       string
	}{
		{"本地私聊", "console:suzune", []actor{ownerA}, "owner"},
		{"本地群聊", "console:group:g1", []actor{ownerA, otherCh}, "owner"},
		{"平台私聊·主人", "qq:private:20001:7001", []actor{ownerA}, "owner"},
		{"平台私聊·某人", "qq:private:20001:7002", []actor{pingA}, "person:qq:60001"},
		{"平台群聊·主人说过话", g, []actor{ownerA, pingA}, "conv:" + g + ",owner"},
		{"平台群聊·只有群友", g, []actor{pingA, boA}, "conv:" + g},
	} {
		t.Run("片段·"+c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, c.conv)
			for _, a := range c.lines {
				r.say(a, "聊了一件正事")
			}
			r.reply("yes", "聊了一件正事。")
			r.run()
			if got := str(t, r.m, `SELECT group_concat(scope) FROM (SELECT scope FROM item_scopes WHERE ref LIKE 'e:%' ORDER BY scope)`); got != c.want {
				t.Errorf("片段的范围 %q，应该是 %q", got, c.want)
			}
		})
	}
}

// visibility 只许收紧、不决定范围：private（没写也按 private，Bun 的默认值）去掉 conv:*，去光了就保持表里的范围不动（不退到 owner 或 person:，那是放宽）；person、public 不改
func TestVisibilityOnlyTightens(t *testing.T) {
	t.Parallel()
	g := groupConv
	for _, c := range []struct {
		vis, owner, self, other string
	}{
		{"", "owner", "conv:" + g, "conv:" + g},
		{"private", "owner", "conv:" + g, "conv:" + g},
		{"person", "conv:" + g + ",owner", "conv:" + g, "conv:" + g},
		{"public", "conv:" + g + ",owner", "conv:" + g, "conv:" + g},
	} {
		t.Run("平台群聊·visibility="+c.vis, func(t *testing.T) {
			r := newRig(t, g)
			r.say(ownerA, "说了一句话")
			r.say(pingA, "也说了一句话")
			r.reply("no", "", upsert("主人的事", "owner", c.vis, 1), upsert("她的事", "self", c.vis, 1), upsert("别人的事", "qq:60001", c.vis, 2))
			r.run()
			for text, want := range map[string]string{"主人的事": c.owner, "她的事": c.self, "别人的事": c.other} {
				if got := r.fact(text).scopes; got != want {
					t.Errorf("%s 的范围 %q，应该是 %q", text, got, want)
				}
			}
		})
	}
	// 邻居不变：candidate 没有 visibility，范围照表；本地和主人的私聊范围本来就是 owner，不因 visibility 变宽
	r := newRig(t, g)
	r.say(pingA, "说了一句话")
	r.reply("no", "", cand("candidate 的别人的事", "qq:60001", 1))
	r.run()
	if got := r.fact("candidate 的别人的事").scopes; got != "conv:"+g {
		t.Errorf("candidate 范围照表：%q", got)
	}
	for _, conv := range []string{"console:suzune", "qq:private:20001:7001"} {
		r := newRig(t, conv)
		r.say(ownerA, "说了一句话")
		r.reply("no", "", upsert("公开写法也不放宽", "owner", "public", 1))
		r.run()
		if got := r.fact("公开写法也不放宽").scopes; got != "owner" {
			t.Errorf("%s：public 不放宽，范围 %q", conv, got)
		}
	}
}

// 收紧只许取子集，不许换一个别的范围：private 去掉 conv:* 以后一个不剩，就保持表里的范围（conv:这个群），不退到 owner 或 person:。
// 写入范围仍保留来源关联：不能把群内事实误标成 owner 或 person，让披露规则改变。
// 四种场合的表达资格由 DisclosureScopes 判断；能否想起另测 ADR-242。
func TestPrivateVisibilityNeverWidensTheScope(t *testing.T) {
	t.Parallel()
	g, other := groupConv, "qq:group:5001002"
	audiences := []struct {
		name string
		a    Audience
	}{
		{"本群·群友", Audience{Channel: "qq", Conversation: g, Scene: "group", PersonIDs: []string{"qq:60001"}}},
		{"本群·主人问起", Audience{Channel: "qq", Conversation: g, Scene: "group", PersonIDs: []string{"owner"}, OwnerAsked: true}},
		{"别的群·群友", Audience{Channel: "qq", Conversation: other, Scene: "group", PersonIDs: []string{"qq:60001"}}},
		{"别的群·主人问起", Audience{Channel: "qq", Conversation: other, Scene: "group", PersonIDs: []string{"owner"}, OwnerAsked: true}},
		{"群友自己的私聊", Audience{Channel: "qq", Conversation: "qq:private:20001:60001", Scene: "private", PersonIDs: []string{"qq:60001"}}},
		{"主人的 QQ 私聊", Audience{Channel: "qq", Conversation: "qq:private:20001:7001", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}},
		{"本地私聊", Audience{Channel: "console", Conversation: "console:suzune", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true, OwnerAsked: true}},
	}
	seen := func(scopes string, a Audience) bool { return CanSee(strings.Split(scopes, ","), DisclosureScopes(a)) }
	// 写入范围仍约束证据和表达，召回资格另按 ADR-242 测。
	rows := []struct {
		text, op string
		want     [7]bool
	}{
		{"她的事", `{"op":"upsert_fact","kind":"fact","text":"她的事","subject":"self","src":[1]}`, [7]bool{true, true, false, false, false, false, true}},
		{"没写主体的事", `{"op":"upsert_fact","kind":"fact","text":"没写主体的事","src":[1]}`, [7]bool{true, true, false, false, false, false, true}},
		{"群友的事", upsert("群友的事", "qq:60001", "private", 2), [7]bool{true, true, false, false, false, false, true}},
		// 主人自己的事仍收成 owner，表达时需主人本人问起；不把群友在场当授权。
		{"主人的事", upsert("主人的事", "owner", "", 1), [7]bool{false, true, false, true, false, true, true}},
		// candidate 没有 visibility，范围照表：和 upsert 写 private 的她的事、群友的事一样
		{"candidate 她的事", cand("candidate 她的事", "self", 1), [7]bool{true, true, false, false, false, false, true}},
	}
	r := newRig(t, g)
	r.say(ownerA, "说了一句话")
	r.say(pingA, "也说了一句话")
	ops := make([]string, len(rows))
	for i, row := range rows {
		ops[i] = row.op
	}
	r.reply("no", "", ops...)
	res := r.run()
	for _, row := range rows {
		f := r.fact(row.text)
		for i, a := range audiences {
			if got := seen(f.scopes, a.a); got != row.want[i] {
				t.Errorf("%s（范围 %s）在「%s」的披露资格 %v，应该是 %v", row.text, f.scopes, a.name, got, row.want[i])
			}
		}
	}
	// 去不掉任何 conv:* 的三条（她的事、没写主体的事、群友的事）各记一次，别的不记
	if res.Counts[CountScopeNotNarrowed] != 3 {
		t.Errorf("visibility 没能收得更窄的该记 3 次，计数 %v", res.Counts)
	}

	// 私聊身份未知时保留会话来源，不退到 owner，也不把别的私聊纳入直接表达集合。
	p := newRig(t, "qq:private:20001:7003")
	p.ev.said = func(string) (Event, bool, error) { return Event{}, false, nil }
	p.say(herA, "在吗？")
	p.reply("no", "", upsert("她的事", "self", "private", 1))
	p.run()
	f := p.fact("她的事")
	for name, c := range map[string]struct {
		a    Audience
		want bool
	}{
		"这个私聊里的人": {Audience{Channel: "qq", Conversation: "qq:private:20001:7003", Scene: "private", PersonIDs: []string{"qq:60009"}}, true},
		"别人的私聊":   {Audience{Channel: "qq", Conversation: "qq:private:20001:7004", Scene: "private", PersonIDs: []string{"qq:60001"}}, false},
		"主人的私聊":   {audienceOf("qq:private:20001:7001", "owner", true), false},
	} {
		if got := seen(f.scopes, c.a); got != c.want {
			t.Errorf("平台私聊身份未知：她的事（范围 %s）在「%s」的披露资格 %v，应该是 %v", f.scopes, name, got, c.want)
		}
	}
}

func audienceOf(conv, person string, privateOK bool) Audience {
	return Audience{Channel: "qq", Conversation: conv, Scene: "private", PersonIDs: []string{person}, PrivateOK: privateOK}
}

// 私聊身份按会话判，写入范围不能冒认主人；已知事实按 ADR-242 不再依赖这层身份过滤。
func TestPlatformPrivateWhoseChatIsJudgedByTheConversation(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		said      func(conv string) (Event, bool, error)
		wantScope string
	}{
		{"这个会话里最近的人话是主人说的", func(conv string) (Event, bool, error) {
			return Event{Conversation: conv, ActorKind: "owner", Origin: "owner"}, true, nil
		}, "owner"},
		{"这个会话里最近的人话是对方说的", func(conv string) (Event, bool, error) {
			return Event{Conversation: conv, ActorKind: "human", ActorID: "qq:60001", Origin: "peer"}, true, nil
		}, "person:qq:60001"},
		{"查不到：按某人算，对方是谁也不知道", func(string) (Event, bool, error) { return Event{}, false, nil }, "conv:qq:private:20001:7003"},
		{"查出错：按某人算", func(string) (Event, bool, error) { return Event{}, false, fmt.Errorf("库忙") }, "conv:qq:private:20001:7003"},
		{"查到的是别的会话：当没查到", func(string) (Event, bool, error) {
			return Event{Conversation: "qq:private:other", ActorKind: "owner", Origin: "owner"}, true, nil
		}, "conv:qq:private:20001:7003"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "qq:private:20001:7003", rigOpts{seed: func(db *sql.DB) { seedFact(t, db, "FOWNER", "主人的私事", "owner", 1) }})
			r.ev.said = c.said
			r.say(herA, "在吗？") // 这一批只有她自己的话：主人还没回
			r.reply("no", "", cand("她的事", "self", 1))
			r.run()
			known := section(r.gen.last().prompt, "已知事实：\n", "\n\n听说过、还没确认的")
			if !strings.Contains(known, "FOWNER｜") {
				t.Errorf("会话身份不应筛掉同角色已知事实：%s", known)
			}
			if got := r.fact("她的事").scopes; got != c.wantScope {
				t.Errorf("她的事的范围 %q，应该是 %q", got, c.wantScope)
			}
		})
	}
	// 这一批里就有主人的行：不用再查
	r := newRig(t, "qq:private:20001:7004")
	r.say(ownerA, "在吗？")
	r.reply("no", "", cand("她的事", "self", 1))
	r.run()
	if r.ev.saidCall != 0 || r.fact("她的事").scopes != "owner" {
		t.Errorf("批里有主人的行就够了，不该去查（查了 %d 次），范围 %q", r.ev.saidCall, r.fact("她的事").scopes)
	}
}

// 她的号上别人发的（human、external）：留在聊天记录里让模型看懂上下文，但不是说话的人、不是证据、不决定这是谁的私聊
func TestHerAccountIsNotASpeakerNorEvidence(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-her-account-not-a-speaker")
	r := newRig(t, "qq:private:20001:7005")
	r.ev.said = func(string) (Event, bool, error) { return Event{}, false, nil }
	r.say(herAcct, "（主人手动用她的号发的一句话）")
	r.say(actor{"character", "suzune", "铃音", "external"}, "（补录时她的号上别人发的）")
	r.reply("no", "", cand("铃音的事", "suzune", 1), cand("主人的事", "owner", 1, 2), cand("她的事", "self", 1))
	r.run()
	p := r.gen.last().prompt
	if !strings.Contains(p, "铃音（你的号上别人发的）：（主人手动用她的号发的一句话）") || !strings.Contains(p, "铃音（你的号上别人发的）：（补录时她的号上别人发的）") {
		t.Errorf("该标明不是她说的：\n%s", p)
	}
	if table := section(p, "说话的人（subject 照这里写）：\n", "\n\n对话"); strings.Contains(table, "suzune") || strings.Count(table, "\n") != 1 {
		t.Errorf("她的号上别人发的不是说话的人（%s）：\n%s", it.Go, table)
	}
	if got := strings.Join(r.factTexts(), "|"); got != "主人的事|她的事" {
		t.Errorf("事实 %s", got)
	}
	for _, f := range r.facts() {
		// 主人的事范围本来就是 owner；她的事退到这个会话本身，因为她的号上别人发的话判不出这是谁的私聊
		want := map[string]string{"主人的事": "owner", "她的事": "conv:qq:private:20001:7005"}[f.text]
		if f.evidence != 0 || f.scopes != want {
			t.Errorf("%s：不是证据（证据 %d），也判不出是谁的私聊（范围 %s，应该 %s）", f.text, f.evidence, f.scopes, want)
		}
	}
}
