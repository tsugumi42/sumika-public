package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memory_forget（Forget）：门槛和 remember 一样，只会更窄。名字、编号、会话都是编的。

const forgetQuote = "请忘掉那条记忆"

type forgetRig struct {
	t    *testing.T
	m    *Memory
	ev   *sqlEvents
	seen map[string]bool
}

func newForgetRig(t *testing.T) *forgetRig {
	t.Helper()
	ev := newSQLEvents(t)
	m, _ := openTools(t, ev)
	return &forgetRig{t: t, m: m, ev: ev, seen: map[string]bool{}}
}

// call 在 a 这个场合里调 Forget；该场合的会话里先放一句主人的话当来源（只放一次）
func (r *forgetRig) call(a Audience, id, reason string) ToolResult {
	r.t.Helper()
	if !r.seen[a.Conversation] {
		r.seen[a.Conversation] = true
		r.ev.add(ownerSays(a.Conversation, forgetQuote, time.Minute))
	}
	return r.callWith(a, ForgetArgs{ID: id, Reason: reason, SourceQuote: forgetQuote})
}

func (r *forgetRig) callWith(a Audience, in ForgetArgs) ToolResult {
	r.t.Helper()
	if in.InputSeqs == nil {
		if e, ok, err := r.ev.LastSaid(context.Background(), a.Conversation); err == nil && ok {
			in.InputSeqs = []int64{e.Seq}
		}
	}
	res, err := r.m.Forget(context.Background(), a, in)
	if err != nil {
		r.t.Fatalf("Forget：%v", err)
	}
	return res
}

// fact 用整理的身份写一条有效事实：confirmed 是 ai、owner、"" 之一（"" 就是导入的 NULL）
func (r *forgetRig) fact(text, subject, confirmed string) string {
	r.t.Helper()
	mustApply(r.t, r.m, Batch{By: BySummary, Ops: []Op{FactOp{Kind: "fact", Text: text, Subject: subject, Status: "active", ConfirmedBy: confirmed, Scopes: []string{"owner"}}}})
	id := str(r.t, r.m, `SELECT id FROM facts WHERE text = ?`, text)
	if confirmed == "" {
		if _, err := r.m.w.db.Exec(`UPDATE facts SET confirmed_by = NULL WHERE id = ?`, id); err != nil {
			r.t.Fatal(err)
		}
	}
	return "#" + id[len(id)-6:]
}

func (r *forgetRig) status(text string) string { return factStatus(r.t, r.m, text) }

// 四种场合 × 结果：只有主人的私聊（本地私聊、QQ 私聊）忘得掉，群里、陌生人私聊一律不行，而且什么都没写
func TestForgetPlaces(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		a    Audience
		ok   bool
	}{
		{"本地私聊", ownerHere, true},
		{"QQ 私聊（主人）", tlOwnerQQ, true},
		{"本地群聊（主人在场）", Audience{Channel: "console", Conversation: "console:group:home", Scene: "group", PrivateOK: true, PersonIDs: []string{"owner"}, OwnerAsked: true}, false},
		{"QQ 群（主人问了）", qqGroup(tlGroup, true, "u9"), false},
		{"QQ 群（群友）", qqGroup(tlGroup, false, "u9"), false},
		{"QQ 别人的私聊", tlFriendFull, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newForgetRig(t)
			id := r.fact("主人喜欢银杏茶", "owner", "ai")
			before := writes(t, r.m)
			res := r.call(c.a, id, "主人说这条不对")
			if c.ok {
				if res.IsError || !strings.Contains(res.Text, "已经忘掉") {
					t.Fatalf("应该忘掉：%+v", res)
				}
				if s := r.status("主人喜欢银杏茶"); s != "revoked" {
					t.Fatalf("状态：%s", s)
				}
				return
			}
			if !res.IsError {
				t.Fatalf("不该忘掉：%+v", res)
			}
			if after := writes(t, r.m); after != before || r.status("主人喜欢银杏茶") != "active" {
				t.Fatalf("拒绝时什么都不该写：%v → %v", before, after)
			}
		})
	}
}

// 软删除：行还在、状态 revoked、修订里有原因和写入身份；不再被想起、memory_search 也找不到；同文不能被 remember 复活
func TestForgetIsSoftAndSticks(t *testing.T) {
	t.Parallel()
	r := newForgetRig(t)
	id := r.fact("主人讨厌香菜", "owner", "ai")
	res := r.call(ownerHere, strings.ToLower(id), "  主人说改口了  ")
	if res.IsError || !strings.Contains(res.Text, "没有真删") {
		t.Fatalf("结果：%+v", res)
	}
	if n := count(t, r.m, `SELECT count(*) FROM facts WHERE text = '主人讨厌香菜' AND status = 'revoked'`); n != 1 {
		t.Fatalf("行应该还在、状态 revoked：%d", n)
	}
	var by, reason string
	if err := r.m.w.db.QueryRow(`SELECT by, coalesce(reason, '') FROM fact_revisions WHERE op = 'revoke'`).Scan(&by, &reason); err != nil {
		t.Fatal(err)
	}
	if by != ByRememberAI || reason != "主人说改口了" {
		t.Fatalf("修订：%q %q", by, reason)
	}
	if hits := r.m.Recall(context.Background(), ownerHere, Query{Text: "香菜", Mode: ModeExplicit, TopK: 12}).Hits; len(hits) != 0 {
		t.Fatalf("忘掉的不该再被想起：%+v", hits)
	}
	if s, err := r.m.Search(context.Background(), ownerHere, "香菜"); err != nil || strings.Contains(s.Text, "主人讨厌香菜") {
		t.Fatalf("memory_search 不该再找到：%+v %v", s, err)
	}
	// remember 同文不复活
	r.ev.add(ownerSays(tlMain, "主人讨厌香菜", 0))
	rem := remembered(t, r.m, ownerHere, RememberArgs{Kind: "fact", Text: "主人讨厌香菜"})
	if !rem.IsError || r.status("主人讨厌香菜") != "revoked" {
		t.Fatalf("remember 不能把忘掉的复活：%+v %s", rem, r.status("主人讨厌香菜"))
	}
}

// 敌对输入：别人的条目、受保护的、不存在的、空的、格式错的、已经忘掉的、待确认的；都不动，并说清楚为什么
func TestForgetHostileInputs(t *testing.T) {
	t.Parallel()
	r := newForgetRig(t)
	other := r.fact("某群友喜欢熏鱼", "qq:u2", "ai")
	owned := r.fact("主人亲口放行的一条", "owner", "owner")
	imported := r.fact("导入来的一条", "owner", "")
	selfFact := r.fact("她自己喜欢雨天", "self", "ai")
	mustApply(t, r.m, Batch{By: BySummary, Ops: []Op{candidate("主人好像想养狗", "owner", "owner")}})
	cand := "#" + codeOf(t, r.m, "主人好像想养狗")
	gone := r.fact("以后会被忘掉的一条", "owner", "ai")
	if res := r.call(ownerHere, gone, "第一次"); res.IsError {
		t.Fatalf("第一次应成功：%+v", res)
	}
	before := writes(t, r.m)

	for _, c := range []struct {
		name, id, want string
	}{
		{"别人的条目", other, "只能忘掉关于所有者或你自己"},
		{"主人放行过的（受保护）", owned, "受所有者确认或历史导入保护"},
		{"导入的（受保护）", imported, "受所有者确认或历史导入保护"},
		{"待确认的", cand, "没找到编号"},
		{"已经忘掉的", gone, "没找到编号"},
		{"不存在的编号", "#ZZZZ99", "没找到编号"},
		{"编号格式不对", "#abc", "没找到编号"},
		{"空编号", "", "没写要忘掉哪一条"},
		{"只有空白", "   ", "没写要忘掉哪一条"},
		{"片段式的编号冒充", "e:01HXYZ", "没找到编号"},
	} {
		res := r.call(ownerHere, c.id, "测试")
		if !res.IsError || !strings.Contains(res.Text, c.want) {
			t.Errorf("%s：%+v，应含 %q", c.name, res, c.want)
		}
	}
	if after := writes(t, r.m); after != before {
		t.Fatalf("这些都不该写任何东西：%v → %v", before, after)
	}
	for text, want := range map[string]string{"某群友喜欢熏鱼": "active", "主人亲口放行的一条": "active", "导入来的一条": "active", "主人好像想养狗": "candidate", "以后会被忘掉的一条": "revoked"} {
		if got := r.status(text); got != want {
			t.Errorf("%s 状态 %s，应 %s", text, got, want)
		}
	}
	// 她自己的事实（self）和 remember 一样动得了
	if res := r.call(ownerHere, selfFact, "主人说她改口味了"); res.IsError {
		t.Fatalf("self 的事实应该能忘：%+v", res)
	}
}

// 参数缺漏、来源对不上：不忘，不写
func TestForgetNeedsReasonAndOwnerQuote(t *testing.T) {
	t.Parallel()
	r := newForgetRig(t)
	id := r.fact("主人喜欢夜跑", "owner", "ai")
	r.ev.add(ownerSays(tlMain, forgetQuote, time.Minute))
	r.seen[tlMain] = true
	before := writes(t, r.m)
	for _, c := range []struct {
		name string
		in   ForgetArgs
		want string
	}{
		{"没写原因", ForgetArgs{ID: id, SourceQuote: forgetQuote}, "没写原因"},
		{"原因太长", ForgetArgs{ID: id, Reason: strings.Repeat("长", forgetMaxReason+1), SourceQuote: forgetQuote}, "原因太长"},
		{"没有来源原话", ForgetArgs{ID: id, Reason: "测试"}, "没有找到对应的所有者原话"},
		{"引用不在主人话里", ForgetArgs{ID: id, Reason: "测试", SourceQuote: "主人从没说过这句"}, "没有找到对应的所有者原话"},
	} {
		if res := r.callWith(ownerHere, c.in); !res.IsError || !strings.Contains(res.Text, c.want) {
			t.Errorf("%s：%+v，应含 %q", c.name, res, c.want)
		}
	}
	// 宿主没绑输入：就算引用对得上主人的话也不忘
	res, err := r.m.Forget(context.Background(), ownerHere, ForgetArgs{ID: id, Reason: "测试", SourceQuote: forgetQuote, InputSeqs: []int64{}})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "没有找到对应的所有者原话") {
		t.Errorf("没绑输入：%+v %v", res, err)
	}
	if after := writes(t, r.m); after != before || r.status("主人喜欢夜跑") != "active" {
		t.Fatalf("什么都不该写：%v → %v", before, after)
	}
}

// 只读的记忆：不忘，告诉她如实说没忘成
func TestForgetReadOnly(t *testing.T) {
	t.Parallel()
	ev := newSQLEvents(t)
	m, err := Open(context.Background(), migratedDB(t, nil), nil, nil, Options{Character: "suzune", Events: ev, Now: func() time.Time { return toolsNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	ev.add(ownerSays(tlMain, forgetQuote, time.Minute))
	res, err := m.Forget(context.Background(), ownerHere, ForgetArgs{ID: "#ABCDEF", Reason: "测试", SourceQuote: forgetQuote, InputSeqs: []int64{1}})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "只读") {
		t.Fatalf("只读时：%+v %v", res, err)
	}
}

// 共用池里的主人事实：别的角色 memory_search 看到的编号，在这个角色里忘掉，所有角色都不再想起
func TestForgetSharedPoolFact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := newSQLEvents(t)
	pool, err := OpenOwnerPool(ctx, filepath.Join(t.TempDir(), "owner-memory.db"), nil, nil, Options{Writable: true, SourceEvents: func(string) (EventSource, error) { return events, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	open := func(name string) *Memory {
		m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Character: name, Writable: true, Shared: pool, Events: events})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a, b := open("aria"), open("sora")
	defer a.Close()
	defer b.Close()
	no := false
	seedRaw(t, events, ownerSays(tlMain, "我喜欢冷杉茶", 0))
	res, err := a.Remember(ctx, ownerHere, RememberArgs{InputSeqs: inputSeqs(t, events, tlMain), Kind: "preference", Text: "主人喜欢冷杉茶", OnlyHer: &no, SourceQuote: "我喜欢冷杉茶", Temporal: "stable"})
	if err != nil || res.IsError {
		t.Fatalf("Remember=%+v %v", res, err)
	}
	var code string
	for _, it := range b.Items() {
		if it.Text == "主人喜欢冷杉茶" {
			code = it.Ref[len(it.Ref)-6:]
		}
	}
	if code == "" {
		t.Fatal("sora 应该看得到共用的这条")
	}
	seedRaw(t, events, ownerSays(tlMain, forgetQuote, 0))
	out, err := b.Forget(ctx, ownerHere, ForgetArgs{InputSeqs: inputSeqs(t, events, tlMain), ID: "#" + code, Reason: "主人改口了", SourceQuote: forgetQuote})
	if err != nil || out.IsError {
		t.Fatalf("Forget=%+v %v", out, err)
	}
	for name, m := range map[string]*Memory{"aria": a, "sora": b} {
		for _, h := range m.Recall(ctx, ownerHere, Query{Text: "冷杉茶", Mode: ModeExplicit, TopK: 12}).Hits {
			if h.Text == "主人喜欢冷杉茶" {
				t.Fatalf("%s 还想得起忘掉的共用事实", name)
			}
		}
	}
}
