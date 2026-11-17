package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/memory"
)

// stubEvents 是按会话给定事件的来源（只有 Events 有内容），用来给自检认来源场合
type stubEvents struct{ byConv map[string][]memory.Event }

func (s stubEvents) Events(_ context.Context, conv string, after int64, limit int) ([]memory.Event, error) {
	var out []memory.Event
	for _, e := range s.byConv[conv] {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
func (stubEvents) Revoked(context.Context, string, []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}

// LastSaid 和真实来源一样：这个会话里最近一句人话（origin 是 owner 或 peer），事件带上会话 id
func (s stubEvents) LastSaid(_ context.Context, conv string) (memory.Event, bool, error) {
	evs := s.byConv[conv]
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Origin == "owner" || evs[i].Origin == "peer" {
			ev := evs[i]
			ev.Conversation = conv
			return ev, true, nil
		}
	}
	return memory.Event{}, false, nil
}
func (stubEvents) Find(context.Context, string, []string, int) ([]memory.Event, error) {
	return nil, nil
}
func (stubEvents) Conversations(context.Context) ([]memory.ConvHead, error) { return nil, nil }

func evt(seq int64, who string) memory.Event {
	switch who {
	case "owner":
		return memory.Event{Seq: seq, ActorKind: "owner", ActorID: "owner", Origin: "owner"}
	case "her":
		return memory.Event{Seq: seq, ActorKind: "character", ActorID: "suzune", Origin: "character"}
	}
	return memory.Event{Seq: seq, ActorKind: "human", ActorID: who, Origin: "peer"}
}

var checkSource = stubEvents{byConv: map[string][]memory.Event{
	convGroupA:   {evt(1, "qq:5"), evt(2, "owner"), evt(3, "qq:6")},
	"qq:group:8": {evt(1, "qq:5"), evt(2, "qq:6")}, // 主人没说过话的群
	convPeer:     {evt(1, "her"), evt(2, "qq:5")},
	convOwner:    {evt(1, "owner"), evt(2, "her")},
}}

// 越权矩阵：形状（来源场合 × 所属的列 × 存下的范围）× 听众。
// 每行的数字是手算的：over = 实算能看而手写预期不能看的听众数，missed 反之，wider = 范围里表里没有的值的个数。
// 听众一共 6 个固定的（本地私聊、本地群聊、QQ 主人私聊、QQ 别人私聊、别的群、别的群主人问起）加来源自己的 1～2 个
func TestSelfCheckMatrix(t *testing.T) {
	t.Parallel()
	g := convGroupA
	cases := []struct {
		name            string
		conv, subject   string
		scopes          []string
		cells           int
		over, missed, w int
		unjudged        bool
		ownerFromPeer   int
	}{
		{name: "群：主人自己的事 owner+群", conv: g, subject: "owner", scopes: []string{"owner", "conv:" + g}, cells: 8},
		{name: "群：群友自己的事 只有这个群", conv: g, subject: "qq:5", scopes: []string{"conv:" + g}, cells: 8},
		{name: "群：她自己的事 只有这个群", conv: g, subject: "self", scopes: []string{"conv:" + g}, cells: 8},
		{name: "群：范围多了 public", conv: g, subject: "qq:5", scopes: []string{"conv:" + g, "public"}, cells: 8, over: 5, w: 1},
		{name: "群：她自己的事多了 owner", conv: g, subject: "self", scopes: []string{"conv:" + g, "owner"}, cells: 8, over: 3, w: 1},
		{name: "群：模型选 private 收紧掉了 conv", conv: g, subject: "owner", scopes: []string{"owner"}, cells: 8, missed: 1},
		{name: "群：范围多了别的群", conv: g, subject: "qq:5", scopes: []string{"conv:" + g, "conv:" + shadowOtherGroup}, cells: 8, over: 2, w: 1},
		{name: "别人的私聊：他自己的事 person:他", conv: convPeer, subject: "qq:5", scopes: []string{"person:qq:5"}, cells: 7},
		{name: "别人的私聊：讲主人的事 owner（Q8 现行）", conv: convPeer, subject: "owner", scopes: []string{"owner"}, cells: 7, ownerFromPeer: 1},
		{name: "别人的私聊：他的事却进了 owner", conv: convPeer, subject: "qq:5", scopes: []string{"owner"}, cells: 7, over: 3, missed: 1, w: 1},
		{name: "别人的私聊：范围是别人的 person", conv: convPeer, subject: "qq:5", scopes: []string{"person:qq:99"}, cells: 7, missed: 1, w: 1},
		{name: "主人的私聊：owner", conv: convOwner, subject: "owner", scopes: []string{"owner"}, cells: 7},
		{name: "主人的私聊：多了 public", conv: convOwner, subject: "owner", scopes: []string{"owner", "public"}, cells: 7, over: 2, w: 1},
		{name: "本地私聊：owner", conv: convLocal, subject: "owner", scopes: []string{"owner"}, cells: 6},
		{name: "本地群聊：owner", conv: convLocalGp, subject: "owner", scopes: []string{"owner"}, cells: 7},
		{name: "来源会话认不出（渠道不认识）", conv: "telegram:private:1:2", subject: "owner", scopes: []string{"owner"}, unjudged: true},
		{name: "来源会话是空的", conv: "", subject: "owner", scopes: []string{"owner"}, unjudged: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ex := &extracted{Facts: []factRow{{ID: "x1", Ref: "f:x1", Conversation: c.conv, Subject: c.subject, Scopes: c.scopes, New: true}}}
			res, err := runSelfCheck(context.Background(), ex, checkSource)
			if err != nil {
				t.Fatal(err)
			}
			if c.unjudged {
				if len(res.Unjudged) != 1 || res.Cells != 0 || res.Overreach != 0 {
					t.Fatalf("认不出的来源应该只记 Unjudged、不进矩阵：%+v", res)
				}
				return
			}
			if res.Cells != c.cells || res.Overreach != c.over || res.Missed != c.missed || res.Wider != c.w || res.OwnerFromPeer != c.ownerFromPeer {
				t.Fatalf("格 %d 越权 %d 漏看 %d 宽 %d Q8 %d；想要 %d %d %d %d %d\n%+v", res.Cells, res.Overreach, res.Missed, res.Wider, res.OwnerFromPeer,
					c.cells, c.over, c.missed, c.w, c.ownerFromPeer, res.Findings)
			}
		})
	}
}

// 片段：群里主人说过话的批可以带 owner，没说过的不行
func TestSelfCheckEpisodes(t *testing.T) {
	t.Parallel()
	run := func(conv string, from, to int64, scopes []string) *checkResult {
		ex := &extracted{Episodes: []epRow{{ID: "e1", Ref: "e:e1", Conv: conv, From: from, To: to, Scopes: scopes, New: true}}}
		res, err := runSelfCheck(context.Background(), ex, checkSource)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if r := run(convGroupA, 1, 3, []string{"owner", "conv:" + convGroupA}); r.Overreach != 0 || r.Wider != 0 {
		t.Errorf("主人在这一批说过话：owner 加群是对的：%+v", r)
	}
	if r := run("qq:group:8", 1, 2, []string{"owner", "conv:qq:group:8"}); r.Overreach != 3 || r.Wider != 1 {
		t.Errorf("主人没说过话却带了 owner：应该越权 3、宽 1：%+v", r)
	}
	if r := run("qq:group:8", 1, 2, []string{"conv:qq:group:8"}); r.Overreach != 0 || r.Missed != 0 {
		t.Errorf("主人没说过话：只有这个群：%+v", r)
	}
	if r := run(convPeer, 1, 2, []string{"person:qq:5"}); r.Overreach != 0 || r.Wider != 0 {
		t.Errorf("别人的私聊：person:他：%+v", r)
	}
	if r := run(convPeer, 1, 2, []string{"public"}); r.Overreach == 0 {
		t.Errorf("别人的私聊的片段不能是 public：%+v", r)
	}
}

// 手写的听众表和被测的 AllowedScopes 要在"该一致的地方"一致：这一格是对表的表（不然预期表写错了，自检就是在拿错的尺子量）。
// 用一组各种范围的"事实"，在正确范围的情形下 6 个固定听众都不该有任何差别
func TestHandTableAgreesWithAllowedScopes(t *testing.T) {
	t.Parallel()
	scopeSets := [][]string{{"public"}, {"owner"}, {"conv:qq:group:7"}, {"person:qq:5"}, {"owner", "conv:qq:group:7"}, {"conv:console:main"}}
	for _, scopes := range scopeSets {
		for _, a := range auditAudiences(convGroupA, kindQQGroup, "") {
			if got, want := memory.CanSee(scopes, memory.DisclosureScopes(a.Real)), a.Hand.seesAny(scopes); got != want {
				t.Errorf("听众 %q 看 %v：被测 %v，手写 %v", a.Name, scopes, got, want)
			}
		}
	}
}

// 端到端：假生成器正常跑 → 越权 0；往副本里塞一条范围宽了的事实（来源是别人的私聊、范围 public）→ 自检红，报告和 summary.json 里有这条的编号
// 这是规格 W6 测试 ⑥ 的改坏检查：整理写出一条范围宽了的事实，矩阵必须红
func TestSelfCheckEndToEndAndBroadenedFact(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	cfg := r.cfg(&recGen{}, modeFake)
	cfg.Places = []string{placePlatformPrivate, placePlatformGroup}
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Check == nil || res.Check.Cells == 0 || res.Check.Overreach != 0 || res.Check.Wider != 0 {
		t.Fatalf("正常整理的结果不该越权：%+v", res.Check)
	}
	if len(res.Extract.newFacts()) < 3 {
		t.Fatalf("三个放行的会话都应该有事实：%d", len(res.Extract.newFacts()))
	}

	r2 := richFixture(t)
	cfg2 := r2.cfg(&recGen{}, modeFake)
	cfg2.hookBeforeExtract = func(memFile string) {
		db := r2.openMem(memFile)
		defer db.Close()
		r2.exec(db, `INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, conversation, scope, origin)
			VALUES ('badfact1', 'fact', '宽了的事实', 'qq:5', 'public', '[]', 'candidate', 1730000000000, ?, 'public', 'peer')`, convPeer)
		r2.exec(db, `INSERT INTO item_scopes (ref, scope) VALUES ('f:badfact1', 'public')`)
	}
	res2, err := runShadow(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Check.Overreach == 0 || res2.Check.Wider == 0 {
		t.Fatalf("范围宽了的事实必须被自检抓到：%+v", res2.Check)
	}
	found := slices.ContainsFunc(res2.Check.Findings, func(f finding) bool { return f.Ref == "f:badfact1" && f.Kind == "overreach" })
	if !found {
		t.Fatalf("发现里应该有这条的编号：%+v", res2.Check.Findings)
	}
	files := buildReport(res2)
	if !strings.Contains(files.Report, "f:badfact1 · ") || !strings.Contains(files.Report, "**越权（实算能看、预期不能看）：") {
		t.Errorf("报告 F 节要列出这一条：\n%s", files.Report)
	}
	if !strings.Contains(string(files.Summary), `"overreach"`) || strings.Contains(string(files.Summary), "宽了的事实") {
		t.Errorf("summary.json 要有越权数、不能有正文")
	}
}

// 自检的数据来源：来源场合取聊天库里的真实记录，不是整理时的推断。
// 一个 QQ 私聊的会话里从来没有主人的行：就算整理时给它写了 owner 范围，自检也按"别人的私聊"算 → owner 范围是越权
func TestSelfCheckUsesChatRecordsNotInference(t *testing.T) {
	t.Parallel()
	ex := &extracted{Facts: []factRow{{ID: "x1", Ref: "f:x1", Conversation: convPeer, Subject: "qq:5", Scopes: []string{"owner"}, New: true}}}
	res, err := runSelfCheck(context.Background(), ex, checkSource)
	if err != nil {
		t.Fatal(err)
	}
	if res.Overreach == 0 {
		t.Fatalf("convPeer 里没有主人的行，owner 范围应该被抓：%+v", res)
	}
}
