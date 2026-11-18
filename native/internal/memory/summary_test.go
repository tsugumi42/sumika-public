package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/prompts"
)

// 整理（SummarizeBatch）的主要流程：一批读到写、每条底线（B1、B2、B6、B7、B8、B9）

func TestSummarizeBatchHappyPath(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	e1 := r.say(ownerA, "我家养了一只猫，叫团子。")
	r.say(herA, "团子是什么颜色的？")
	r.reply("yes", "主人说他家养了一只叫团子的猫。", cand("主人养了一只叫团子的猫", "owner", e1.Seq))

	res := r.run()
	if res.Skipped != "" || res.Cursor != 2 || res.Counts[CountRead] != 2 || res.Counts[CountKept] != 2 || res.Counts[CountModelCalls] != 1 {
		t.Fatalf("结果 %+v", res)
	}
	if got := r.factTexts(); len(got) != 1 || got[0] != "主人养了一只叫团子的猫" {
		t.Errorf("事实 %v", got)
	}
	if got := r.episodes(); len(got) != 1 || got[0] != "主人说他家养了一只叫团子的猫。" {
		t.Errorf("片段 %v", got)
	}
	if r.cursor() != 2 || r.jobs() != 1 || r.gen.count() != 1 {
		t.Errorf("游标 %d，job %d，模型调用 %d", r.cursor(), r.jobs(), r.gen.count())
	}
	call := r.gen.last()
	for _, want := range []string{"[1] 用户：我家养了一只猫", "[2] 铃音（你）：团子是什么颜色的", "- 用户：owner", "- 铃音（你）：self", "2026年10月26日星期一 12:00", "你替铃音选择值得保留的记忆"} {
		if !strings.Contains(call.prompt, want) {
			t.Errorf("提示词里该有 %q\n%s", want, call.prompt)
		}
	}
	if call.model != "bg-model" {
		t.Errorf("模型 %q", call.model)
	}
	// 数下来的东西跟着这一批存进 memory_jobs，W6 回放、记忆页都能看
	if raw := str(t, r.m, `SELECT counts_json FROM memory_jobs`); !strings.Contains(raw, `"added_candidate":1`) || !strings.Contains(raw, `"model_calls":1`) {
		t.Errorf("counts_json %s", raw)
	}
}

// B2：同一批（会话 + 起止 seq + 版本）整理过，不调模型、不写，只挪游标
func TestB2SameBatchIsNotSummarizedTwice(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天去看牙医。")
	r.reply("no", "", cand("主人明天去看牙医", "owner", 1))
	r.run()
	before := r.factTexts()

	// 游标被拨回去（比如迁移时重建过）：同一批又出现
	if _, err := r.m.w.db.Exec(`UPDATE summary_state SET last_seq = 0`); err != nil {
		t.Fatal(err)
	}
	res := r.run()
	if res.Skipped != SkipDone || res.Cursor != 1 || r.cursor() != 1 {
		t.Fatalf("该只挪游标：%+v 游标 %d", res, r.cursor())
	}
	if r.gen.count() != 1 || len(r.factTexts()) != len(before) || r.jobs() != 1 {
		t.Errorf("模型 %d 次、事实 %v、job %d：不该再写", r.gen.count(), r.factTexts(), r.jobs())
	}
	// 提示词版本换了就是另一批（边界 ⑲）：缓存键里带版本，不复用
	if summaryCacheKey("suzune", "console:suzune", 1) == summaryCacheKey("suzune", "console:suzune", 2) {
		t.Error("版本不同缓存键该不同")
	}
}

func TestSummaryCacheKey(t *testing.T) {
	t.Parallel()
	a := summaryCacheKey("suzune", "qq:private:20001:7001", 1)
	if a != summaryCacheKey("suzune", "qq:private:20001:7001", 1) {
		t.Error("内容不变该复用同一个键")
	}
	for name, other := range map[string]string{
		"换会话": summaryCacheKey("suzune", "qq:private:20001:7002", 1),
		"换角色": summaryCacheKey("kaede", "qq:private:20001:7001", 1),
		"换版本": summaryCacheKey("suzune", "qq:private:20001:7001", 2),
	} {
		if a == other {
			t.Errorf("%s键该不同", name)
		}
	}
	if strings.Contains(a, "7001") || strings.Contains(a, "qq") {
		t.Errorf("键里不该带会话 id（里面有号码）：%s", a)
	}
	r := newRig(t, "console:suzune")
	r.say(ownerA, "今天下雨。")
	r.run()
	if got := r.gen.last().key; got != summaryCacheKey("suzune", "console:suzune", SummaryVersion) {
		t.Errorf("传给模型的缓存键 %q", got)
	}
}

// 新会话（没有游标行）不回填历史：不读事件、不调模型；调用方用 InitCursor 立起点以后，只整理起点之后的（13.2.4 第 1 步）
func TestNewConversationIsNotBackfilled(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-new-conversation-not-backfilled")
	r := newRig(t, "console:suzune")
	other := "console:other"
	for i := 1; i <= 5; i++ {
		r.ev.add(other, ownerA, fmt.Sprintf("旧事第%d件", i))
	}
	res, err := r.m.SummarizeBatch(context.Background(), other, false)
	if err != nil || res.Skipped != SkipNoCursor || res.Counts[CountStartupNoBackfill] != 1 {
		t.Fatalf("新会话 %+v %v（%s）", res, err, it.Go)
	}
	if r.ev.readCount() != 0 || r.gen.count() != 0 || r.jobs() != 0 {
		t.Errorf("不该读事件（%d 次）、调模型（%d 次）、写 job（%d 条）", r.ev.readCount(), r.gen.count(), r.jobs())
	}
	if _, err := r.m.InitCursor(context.Background(), other, 3); err != nil {
		t.Fatal(err)
	}
	r.ev.add(other, ownerA, "新事第6件")
	r.gen.set(`{"episode_record":"no","ops":[]}`)
	res, err = r.m.SummarizeBatch(context.Background(), other, false)
	if err != nil || res.Cursor != 6 || res.Counts[CountRead] != 3 {
		t.Fatalf("立了起点以后 %+v %v", res, err)
	}
	p := r.gen.last().prompt
	if strings.Contains(p, "旧事第1件") || strings.Contains(p, "旧事第3件") || !strings.Contains(p, "旧事第4件") || !strings.Contains(p, "新事第6件") {
		t.Errorf("只该整理起点（seq 3）之后的：\n%s", p)
	}
	// 邻居：有游标的会话照常整理，不看别的会话的事件
	r.say(ownerA, "这个会话自己的事。")
	if res := r.run(); res.Skipped != "" || strings.Contains(r.gen.last().prompt, "旧事第") {
		t.Errorf("别的会话的事件不该混进来：%+v", res)
	}
}

// B6：没选后台模型，不整理、游标不动、不算失败；选好以后下次照常整理
func TestB6NoBackgroundModel(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我在学吉他。")
	r.model = ""
	res := r.run()
	if res.Skipped != SkipNoModel || res.Cursor != 0 || r.gen.count() != 0 || r.cursor() != 0 || r.jobs() != 0 {
		t.Fatalf("没选模型：%+v 模型 %d 次 游标 %d", res, r.gen.count(), r.cursor())
	}
	r.model = "  "
	if res := r.run(); res.Skipped != SkipNoModel {
		t.Errorf("只有空白也算没选：%+v", res)
	}
	r.model = "bg-model"
	r.reply("no", "", cand("主人在学吉他", "owner", 1))
	if res := r.run(); res.Skipped != "" || r.cursor() != 1 || len(r.factTexts()) != 1 {
		t.Errorf("选好以后该整理：%+v", res)
	}
}

// B7：episode_record 是 no 时不存片段，事实照记；旧格式（没有这一栏）有文字照存
func TestB7EpisodeRecordNoStillRecordsFacts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, reply string
		episodes    int
	}{
		{"no：不存片段，事实照记", `{"episode_record":"no","episode_reason":"闲聊","episode":"大家聊了天。","ops":[` + cand("主人在学吉他", "owner", 1) + `]}`, 0},
		{"yes：片段和事实都存", `{"episode_record":"yes","episode":"主人说在学吉他。","ops":[` + cand("主人在学吉他", "owner", 1) + `]}`, 1},
		{"没有表态不存经历", `{"episode":"主人说在学吉他。","ops":[` + cand("主人在学吉他", "owner", 1) + `]}`, 0},
		{"旧格式没有文字：不存", `{"ops":[` + cand("主人在学吉他", "owner", 1) + `]}`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "console:suzune")
			r.say(ownerA, "我在学吉他。")
			r.gen.set(c.reply)
			r.run()
			if len(r.episodes()) != c.episodes || len(r.factTexts()) != 1 {
				t.Errorf("片段 %v，事实 %v", r.episodes(), r.factTexts())
			}
		})
	}
}

// 一条坏动作混在好的里面，端到端：坏的丢掉、计数、好的照记，游标照挪（Bun 整批失败）
func TestOneBadOpDoesNotLoseTheBatchEndToEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.say(ownerA, "后天去旅行。")
	r.reply("yes", "主人明天考试，后天去旅行。",
		cand("主人明天考试", "owner", 1),
		`{"op":"dispute","id":"x"}`,
		`{"op":"candidate","kind":"gossip","text":"坏的"}`,
		cand("主人后天去旅行", "owner", 2))
	res := r.run()
	if got := r.factTexts(); len(got) != 2 || res.Counts[CountOpUnknown] != 1 || res.Counts[CountKindInvalid] != 1 || r.cursor() != 2 {
		t.Fatalf("事实 %v 计数 %v 游标 %d", got, res.Counts, r.cursor())
	}
}

// B8：这些消息不进聊天记录、不当材料。单独一条被丢的 → 不调模型，游标挪过它，不计失败
func TestB8DroppedEventsNeverReachTheModel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		a     actor
		mod   func(*Event)
		count string
	}{
		{"automation：复读的那句", ownerA, func(e *Event) { e.Origin = "automation:repeater" }, CountDroppedAutomation},
		{"automation：试运行", herA, func(e *Event) { e.Origin = "automation:dry-run" }, CountDroppedAutomation},
		{"补录的", ownerA, func(e *Event) { e.Backfill = true }, CountDroppedBackfill},
		{"自检的", ownerA, func(e *Event) { e.Selftest = true }, CountDroppedSelftest},
		{"她没看见（陌生人私聊）", pingA, func(e *Event) { e.Seen = false }, CountDroppedUnseen},
		{"撤回了", ownerA, func(e *Event) { e.Revoked = true }, CountDroppedRevoked},
		{"她的回复没发出去", herA, func(e *Event) { e.Unsent = true }, CountDroppedUnsent},
		{"kind 不是 message：撤回通知", ownerA, func(e *Event) { e.Kind = "notice" }, CountDroppedKind},
		{"kind 没填", ownerA, func(e *Event) { e.Kind = "" }, CountDroppedKind},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "qq:group:5001001")
			ev := r.say(c.a, "这句话不该让模型看到", c.mod)
			res := r.run()
			if res.Skipped != SkipNothing || res.Cursor != ev.Seq || r.cursor() != ev.Seq || res.Counts[c.count] != 1 {
				t.Fatalf("结果 %+v 游标 %d", res, r.cursor())
			}
			if r.gen.count() != 0 {
				t.Errorf("不该调模型")
			}
			// 再来一次：游标已经越过它，不会反复读到
			if res := r.run(); res.Counts[CountRead] != 0 {
				t.Errorf("被丢的行不该反复读到：%+v", res)
			}
		})
	}
}

// 被丢的夹在留下的中间：聊天记录里只有留下的；补录行夹在实时消息中间（be786e20）游标照常越过它们
func TestB8DroppedEventsBetweenLiveOnes(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-skips-revoked-and-unsent")
	r := newRig(t, "qq:group:5001001")
	r.say(pingA, "第一句实时的话")
	r.say(pingA, "补录的旧话", func(e *Event) { e.Backfill = true })
	r.say(boA, "第二句实时的话")
	r.say(herA, "没发出去的回复", func(e *Event) { e.Unsent = true })
	r.say(ownerA, "撤回掉的话", func(e *Event) { e.Revoked = true })
	r.say(pingA, "复读", func(e *Event) { e.Origin = "automation:repeater" })
	r.say(ownerA, "第三句实时的话")
	r.say(ownerA, "最后一条也被丢", func(e *Event) { e.Selftest = true })
	r.reply("no", "")
	res := r.run()
	p := r.gen.last().prompt
	for _, want := range []string{"第一句实时的话", "第二句实时的话", "第三句实时的话"} {
		if !strings.Contains(p, want) {
			t.Errorf("该看到 %q", want)
		}
	}
	for _, bad := range []string{"补录的旧话", "没发出去的回复", "撤回掉的话", "复读", "最后一条也被丢"} {
		if strings.Contains(p, bad) {
			t.Errorf("不该看到 %q（%s）", bad, it.Go)
		}
	}
	if res.Cursor != 8 || r.cursor() != 8 || res.Counts[CountRead] != 8 || res.Counts[CountKept] != 3 {
		t.Errorf("游标该越过结尾被丢的：%+v", res)
	}
}

// 只有占位（[图片]、表情、"嗯"）的批不调模型，游标挪过去；有图片描述的、有话的不算占位
func TestOnlyPlaceholderBatchSkipsTheModel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		lines  []string
		images []Image
		model  bool
	}{
		{"一张图", []string{"[图片]"}, nil, false},
		{"表情包和商城表情", []string{"[表情包]", "[商城表情：笑哭]"}, nil, false},
		{"语音和视频", []string{"[语音]", "[视频]"}, nil, false},
		{"语气词和标点", []string{"嗯", "哈哈哈", "。。。", "草", "？？"}, nil, false},
		{"图加语气词", []string{"嗯 [图片]"}, nil, false},
		{"有话：不是占位", []string{"[图片]", "我明天去考试"}, nil, true},
		{"一个字的话：好", []string{"好"}, nil, true},
		{"文件：有名字，有内容", []string{"[文件：期末复习.pdf]"}, nil, true},
		{"图识完了有描述：有内容", []string{"[图片]"}, []Image{{Ready: true, Brief: "一只橘猫趴在键盘上"}}, true},
		{"图识完了但没有描述：还是占位", []string{"[图片]"}, []Image{{Ready: true}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "qq:group:5001001")
			for _, l := range c.lines {
				r.say(pingA, l, func(e *Event) { e.Images = c.images })
			}
			res := r.run()
			if (r.gen.count() == 1) != c.model {
				t.Fatalf("调模型 %d 次，应该 %v：%+v", r.gen.count(), c.model, res)
			}
			if !c.model && (res.Skipped != SkipNothing || res.Cursor != int64(len(c.lines)) || res.Counts[CountOnlyPlaceholder] != len(c.lines)) {
				t.Errorf("占位的批：%+v", res)
			}
		})
	}
}

// B9：四种场合都给同角色的已知事实；撤销、替换仍只认实际展示过的编号。
func TestB9KnownFactsInEveryPlace(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T) func(db *sql.DB) {
		return func(db *sql.DB) {
			seedFact(t, db, "FPUBLIC", "公开的事", "public", 1)
			seedFact(t, db, "FGROUP", "本群的事", "conv:qq:group:5001001", 2)
			seedFact(t, db, "FOTHERGROUP", "别的群的事", "conv:qq:group:5002002", 3)
			seedFact(t, db, "FOWNER", "主人的私事", "owner", 4)
			seedFact(t, db, "FPERSON", "阿萍的事", "person:qq:60001", 5)
		}
	}
	for _, c := range []struct {
		name, conv string
		a          actor
	}{
		{"本地私聊", "console:suzune", ownerA},
		{"本地群聊", "console:group:g1", ownerA},
		{"QQ 群", "qq:group:5001001", ownerA},
		{"QQ 私聊·主人", "qq:private:20001:7001", ownerA},
		{"QQ 私聊·某人", "qq:private:20001:7002", pingA},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, c.conv, rigOpts{seed: seed(t)})
			r.say(c.a, "今天天气不错，出去走走。")
			r.run()
			known := section(r.gen.last().prompt, "已知事实：\n", "\n\n听说过、还没确认的")
			for _, id := range []string{"FPUBLIC", "FGROUP", "FOTHERGROUP", "FOWNER", "FPERSON"} {
				if !strings.Contains(known, id+"｜fact｜") {
					t.Errorf("同角色事实 %s 未进入已知事实：%s", id, known)
				}
			}
		})
	}
	// 没有任何有效事实：写（暂无）
	r := newRig(t, "console:suzune")
	r.say(ownerA, "你好。")
	r.run()
	if known := section(r.gen.last().prompt, "已知事实：\n", "\n\n听说过、还没确认的"); known != "（暂无）" {
		t.Errorf("没有事实该写（暂无）：%q", known)
	}
}

// 最近 40 条：更早的撤不掉、换不了（Bun 的硬顶），超出的记进计数
func TestB9KnownFactsAreTheNewest40(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune", rigOpts{seed: func(db *sql.DB) {
		for i := 1; i <= 41; i++ {
			seedFact(t, db, fmt.Sprintf("F%02d", i), fmt.Sprintf("第%d件事", i), "owner", int64(i))
		}
	}})
	// 撤销要成，那条得是 AI 自己提升的（confirmed_by = ai）；导入的（NULL）整理撤不掉（W2 白名单，TestRevokeNeedsTheOwnersWords 里测）。
	// 这个测试测的是"最近 40 条"的上限，所以把 F02 种成 ai
	if _, err := r.m.w.db.Exec(`UPDATE facts SET confirmed_by = 'ai' WHERE id = 'F02'`); err != nil {
		t.Fatal(err)
	}
	r.say(ownerA, "那条不对，别记那个了。")
	r.reply("no", "",
		`{"op":"revoke","id":"F01","src":[1]}`, // 第 41 条之外最早的一条：没给它看，撤不掉
		`{"op":"revoke","id":"F02","src":[1]}`, // 第 40 条：给了
		upsertSup("换掉最早的", "F01", 1))
	res := r.run()
	known := section(r.gen.last().prompt, "已知事实：\n", "\n\n听说过、还没确认的")
	if strings.Contains(known, "F01｜") || !strings.Contains(known, "F02｜") || !strings.Contains(known, "F41｜") || strings.Count(known, "\n") != 39 {
		t.Errorf("该给最新的 40 条（F02～F41）：\n%s", known)
	}
	if res.Counts[CountKnownOverLimit] != 1 || res.Counts[CountRevokeUnknown] != 1 || res.Counts[CountSupersedesUnknown] != 1 || res.Counts[CountRevoked] != 1 {
		t.Errorf("计数 %v", res.Counts)
	}
	if st := str(t, r.m, `SELECT status FROM facts WHERE id = 'F01'`); st != "active" {
		t.Errorf("F01 没给模型看过，撤不掉也换不了，状态 %s", st)
	}
	if st := str(t, r.m, `SELECT status FROM facts WHERE id = 'F02'`); st != "revoked" {
		t.Errorf("F02 给了，该撤掉，状态 %s", st)
	}
	if got := r.fact("换掉最早的"); got.status != "candidate" {
		t.Errorf("替换的旧编号不在范围里：新事实照记，状态 %s", got.status)
	}
}

// 撤销只认 src 恰好一个序号、那一行是主人自己说的话（13.2.6，我定的，默认收紧；"恰好一个"是 W3a 收紧的）。不带 src 的撤销会被拒
func TestRevokeNeedsTheOwnersWords(t *testing.T) {
	t.Parallel()
	it := intended(t, "summary-revoke-needs-owner-words")
	// confirmedBy 是 FTARGET 的 confirmed_by：撤销要成得是 "ai"（AI 自己提升的）；nil 是导入的（NULL），"owner" 是主人放行过的，整理撤不掉（W2 白名单）
	run := func(t *testing.T, conv string, a actor, scope string, ops func(ownerSeq, herSeq int64) string, confirmedBy any) (BatchResult, *rig) {
		r := newRig(t, conv, rigOpts{seed: func(db *sql.DB) { seedFact(t, db, "FTARGET", "要被撤销的事", scope, 1) }})
		if _, err := r.m.w.db.Exec(`UPDATE facts SET confirmed_by = ? WHERE id = 'FTARGET'`, confirmedBy); err != nil { // confirmed_by 这一列是迁移加的，放进库要在打开以后
			t.Fatal(err)
		}
		o := r.say(a, "那条不对，别记了。")
		h := r.say(herA, "好的。")
		r.gen.set(`{"episode_record":"no","ops":[` + ops(o.Seq, h.Seq) + `]}`)
		return r.run(), r
	}
	status := func(r *rig) string { return str(t, r.m, `SELECT status FROM facts WHERE id = 'FTARGET'`) }
	for _, c := range []struct {
		name  string
		conv  string
		scope string
		ops   func(o, h int64) string
		count string
		want  string
	}{
		{"src 里有主人的话：撤销", "console:suzune", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, o) }, CountRevoked, "revoked"},
		{"src 里有主人和她的话：不撤销（只认恰好一个序号，W3a）", "console:suzune", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d,%d]}`, o, h) }, CountRevokeRefused, "active"},
		{"src 重复写了同一个主人的序号：算一个，撤销", "console:suzune", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d,%d]}`, o, o) }, CountRevoked, "revoked"},
		{"没带 src：不撤销", "console:suzune", "owner", func(o, h int64) string { return `{"op":"revoke","id":"FTARGET"}` }, CountRevokeRefused, "active"},
		{"src 只有她自己的话：不撤销", "console:suzune", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, h) }, CountRevokeRefused, "active"},
		{"src 里有一个不是这一批的编号：整个当没有", "console:suzune", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d,999]}`, o) }, CountRevokeRefused, "active"},
		{"src 写坏了", "console:suzune", "owner", func(o, h int64) string { return `{"op":"revoke","id":"FTARGET","src":"那句"}` }, CountRevokeRefused, "active"},
		{"编号编出来的", "console:suzune", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"NOSUCH","src":[%d]}`, o) }, CountRevokeUnknown, "active"},
		{"平台群聊：群里的事，主人在群里说了撤销", "qq:group:5001001", "conv:qq:group:5001001", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, o) }, CountRevoked, "revoked"},
		{"QQ 群：主人单句可撤销同角色的 AI 私事事实", "qq:group:5001001", "owner", func(o, h int64) string { return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, o) }, CountRevoked, "revoked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, r := run(t, c.conv, ownerA, c.scope, c.ops, "ai")
			if res.Counts[c.count] != 1 || status(r) != c.want {
				t.Errorf("计数 %v，状态 %s，应该 %s 数到 %s（%s）", res.Counts, status(r), c.want, c.count, it.Go)
			}
		})
	}
	// 群友说"那条不对"不撤销（群友能不能撤自己的事归 17.1，现在不开口子）
	t.Run("群友说不对：不撤销", func(t *testing.T) {
		res, r := run(t, "qq:group:5001001", pingA, "conv:qq:group:5001001", func(o, h int64) string {
			return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, o)
		}, "ai")
		if res.Counts[CountRevokeRefused] != 1 || status(r) != "active" {
			t.Errorf("计数 %v，状态 %s", res.Counts, status(r))
		}
	})
	// 群友把名片改成"主人"说"不对"：身份看 ActorKind 和 Origin，名字没用
	t.Run("名片叫主人的群友说不对：不撤销", func(t *testing.T) {
		spoof := actor{"human", "qq:60009", "主人", "peer"}
		res, r := run(t, "qq:group:5001001", spoof, "conv:qq:group:5001001", func(o, h int64) string {
			return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, o)
		}, "ai")
		if res.Counts[CountRevokeRefused] != 1 || res.Counts[CountRevoked] != 0 || status(r) != "active" {
			t.Errorf("计数 %v，状态 %s", res.Counts, status(r))
		}
	})
	// 主人放行过的、导入的（NULL）、空串、不认识的值：主人的话到了写入那一层，整理也撤不掉，计 revoke_protected，不记修订（W2 白名单）
	for name, by := range map[string]any{"主人放行过的": "owner", "导入的（confirmed_by 是 NULL）": nil, "空串": "", "不认识的值": "import"} {
		t.Run(name+"：写入那一层挡下", func(t *testing.T) {
			res, r := run(t, "console:suzune", ownerA, "owner", func(o, h int64) string {
				return fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":[%d]}`, o)
			}, by)
			if res.Counts[CountRevokeProtected] != 1 || res.Counts[CountRevoked] != 0 || res.Counts[CountRevokeNoop] != 0 || status(r) != "active" ||
				count(t, r.m, `SELECT count(*) FROM fact_revisions WHERE fact_id = 'FTARGET'`) != 0 {
				t.Errorf("计数 %v，状态 %s", res.Counts, status(r))
			}
		})
	}
}

// 替换：旧编号要在给模型看过的范围里；模型写的新事实一律待确认，写入时不取代任何有效事实（W2），旧的原样有效，想取代谁记在 add 修订的 wants_supersede 里
func TestSupersedeKnownFacts(t *testing.T) {
	t.Parallel()
	intended(t, "summary-supersede-deferred-whitelist")
	r := newRig(t, "console:suzune", rigOpts{seed: func(db *sql.DB) {
		seedFact(t, db, "FOLD", "主人住在北京", "owner", 1)
		seedFact(t, db, "FPINNED", "主人不吃香菜", "owner", 2)
	}})
	if _, err := r.m.w.db.Exec(`UPDATE facts SET confirmed_by = 'owner' WHERE id = 'FPINNED'`); err != nil {
		t.Fatal(err)
	}
	r.say(ownerA, "我搬到上海了。我现在也吃香菜了。")
	r.reply("no", "", upsertSup("主人搬到了上海", "FOLD", 1), upsertSup("主人现在吃香菜", "FPINNED", 1), upsertSup("编号编出来的", "NOSUCH", 1))
	res := r.run()
	for _, id := range []string{"FOLD", "FPINNED"} {
		if st := str(t, r.m, `SELECT status FROM facts WHERE id = ?`, id); st != "active" {
			t.Errorf("%s 写入时不该被整理取代（想取代的记在新候选上，提升时才定）：%s", id, st)
		}
		if n := count(t, r.m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ?`, id); n != 0 {
			t.Errorf("%s 不该多出修订：%d 条", id, n)
		}
	}
	for _, text := range []string{"主人搬到了上海", "主人现在吃香菜", "编号编出来的"} {
		if got := r.fact(text); got.status != "candidate" {
			t.Errorf("%q 一律待确认：%s", text, got.status)
		}
	}
	if res.Counts[CountSuperseded] != 0 || res.Counts[CountSupersedeLost] != 0 || res.Counts[CountSupersedeDeferred] != 2 || res.Counts[CountSupersedesUnknown] != 1 {
		t.Errorf("计数 %v", res.Counts)
	}
	wants := func(text string) any {
		return jsonMap(t, str(t, r.m, `SELECT after_json FROM fact_revisions WHERE op = 'add' AND fact_id = (SELECT id FROM facts WHERE text = ?)`, text))["wants_supersede"]
	}
	if wants("主人搬到了上海") != "FOLD" || wants("主人现在吃香菜") != "FPINNED" || wants("编号编出来的") != nil {
		t.Errorf("add 修订里记想取代谁：%v %v %v（给模型看过的才记，编出来的编号去掉）", wants("主人搬到了上海"), wants("主人现在吃香菜"), wants("编号编出来的"))
	}
}

// 导入的有效事实不被待确认替换；新旧事实在四种场合都想起，新事实仍标听说。
func TestImportedFactSurvivesSummarySupersede(t *testing.T) {
	t.Parallel()
	intended(t, "summary-supersede-deferred-whitelist")
	const g = "qq:group:5001001"
	outsider := Audience{Channel: "qq", Conversation: "qq:group:99999", Scene: "group"}
	for _, c := range []struct {
		name, conv string
		seedScope  string   // 导入的旧事实的范围
		reader     Audience // 整理之前就想得起旧事实的听众
		wantNewFor bool     // 同角色新候选在各场合都能召回。
		newScopes  string   // 手写：新候选的范围（4.2 的表；visibility 写 private 去掉 conv:*）
	}{
		{"本地私聊", "console:suzune", "owner", Audience{Channel: "console", Conversation: "console:suzune", Scene: "private", PrivateOK: true, PersonIDs: []string{"owner"}}, true, "owner"},
		{"本地群聊", "console:group:g1", "owner", Audience{Channel: "console", Conversation: "console:group:g1", Scene: "group", PrivateOK: true}, true, "owner"},
		{"QQ 私聊（主人）", "qq:private:20001:7001", "owner", Audience{Channel: "qq", Conversation: "qq:private:20001:7001", Scene: "private", PrivateOK: true, PersonIDs: []string{"owner"}}, true, "owner"},
		{"QQ 群", g, "conv:" + g, Audience{Channel: "qq", Conversation: g, Scene: "group"}, true, "owner"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.conv, rigOpts{seed: func(db *sql.DB) { seedFact(t, db, "FOLD", "主人住在北京", c.seedScope, 1) }})
			ctx := context.Background()
			hits := func(a Audience, q string) []Hit {
				return r.m.Recall(ctx, a, Query{Now: r.clock, Text: q, Mode: ModeExplicit}).Hits
			}
			// 找某一条（全文检索会把共用字的别的条目也带出来，所以按编号找，不数总数）
			find := func(hs []Hit, ref string) (Hit, bool) {
				for _, h := range hs {
					if h.Ref == ref {
						return h, true
					}
				}
				return Hit{}, false
			}
			if h, ok := find(hits(c.reader, "主人住在北京"), "f:FOLD"); !ok || h.Hearsay {
				t.Fatalf("整理之前该想得起旧事实：%+v %v", h, ok)
			}
			row0 := rowOf(t, r.m, "FOLD")
			r.say(ownerA, "我搬到上海了。")
			r.reply("no", "", upsertSup("主人搬到了上海", "FOLD", 1))
			res := r.run()
			if res.Counts[CountSupersedeDeferred] != 1 || res.Counts[CountSuperseded] != 0 {
				t.Fatalf("计数 %v", res.Counts)
			}
			if got := rowOf(t, r.m, "FOLD"); got != row0 {
				t.Fatalf("旧事实的状态、放行、范围、修订一个字节都不该变：\n前 %s\n后 %s", row0, got)
			}
			if h, ok := find(hits(c.reader, "主人住在北京"), "f:FOLD"); !ok || h.Hearsay {
				t.Fatalf("整理之后原来想得起的人照样想得起，而且不是听说：%+v %v", h, ok)
			}
			nf := r.fact("主人搬到了上海")
			if nf.status != "candidate" || nf.scopes != c.newScopes {
				t.Fatalf("新写的该是待确认、范围 %q：%+v", c.newScopes, nf)
			}
			h, ok := find(hits(c.reader, "主人搬到了上海"), "f:"+nf.id)
			if c.wantNewFor && (!ok || !h.Hearsay) {
				t.Fatalf("新的待确认该被标听说：%+v %v", h, ok)
			}
			// 另一场合也应召回旧事实和待确认新事实，后者仍标听说。
			if h, ok := find(hits(outsider, "主人住在北京"), "f:FOLD"); !ok || h.Hearsay {
				t.Fatalf("跨场合旧事实丢失：%+v", h)
			}
			if h, ok := find(hits(outsider, "主人搬到了上海"), "f:"+nf.id); !ok || !h.Hearsay {
				t.Fatalf("跨场合候选丢失或未经确认：%+v", h)
			}
		})
	}
}

// 同一句话在不同的批里又说一遍：合并成一条，证据按（哪一天，哪个会话）记；撤销过的同文不复活（写入路径上的调用点，13.2.13）
func TestSameFactInLaterBatchesMerges(t *testing.T) {
	t.Parallel()
	r := newRig(t, "qq:group:5001001")
	r.say(pingA, "我下周搬家。")
	r.reply("no", "", cand("阿萍下周搬家", "qq:60001", 1))
	r.run()
	// 同一天又说一遍：还是一次证据
	r.say(pingA, "对了，我下周搬家。")
	r.reply("no", "", cand("阿萍下周搬家", "qq:60001", 2))
	res := r.run()
	f := r.fact("阿萍下周搬家")
	if len(r.facts()) != 1 || f.evidence != 1 || res.Counts[CountMerged] != 1 || res.Counts[CountEvidenceRepeat] != 1 {
		t.Fatalf("同一天又说一遍：事实 %d 条，证据 %d，计数 %v", len(r.facts()), f.evidence, res.Counts)
	}
	// 隔了一天再说：证据 2
	r.say(pingA, "我还是下周搬家。", func(e *Event) { e.At += 26 * 3600 * 1000 })
	r.reply("no", "", cand("阿萍下周搬家", "qq:60001", 3))
	r.run()
	if f := r.fact("阿萍下周搬家"); len(r.facts()) != 1 || f.evidence != 2 || f.byself != 2 {
		t.Errorf("隔天：事实 %d 条，证据 %d（本人 %d）", len(r.facts()), f.evidence, f.byself)
	}
	// 主人撤销以后，AI 再提取到同一句：不新建，不复活
	if _, err := r.m.w.db.Exec(`UPDATE facts SET status = 'revoked'`); err != nil {
		t.Fatal(err)
	}
	r.say(pingA, "我真的下周搬家！")
	r.reply("no", "", cand("阿萍下周搬家", "qq:60001", 4))
	res = r.run()
	if f := r.fact("阿萍下周搬家"); len(r.facts()) != 1 || f.status != "revoked" || res.Counts[CountDupRevoked] != 1 {
		t.Errorf("撤销过的同文不复活：%d 条，状态 %s，计数 %v", len(r.facts()), f.status, res.Counts)
	}
}

// 格式级失败：游标不动、什么都不写、不记 job；调用方退避（W5）。条目级的好处是不走到这里
func TestBadOutputWritesNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, reply string }{
		{"不是 JSON", `这批没什么好记的。`},
		{"两栏都没有", `{"episode":"x"}`},
		{"ops 不是数组", `{"ops":"无"}`},
		{"截在 episode 中间", `{"episode_record":"yes","episode":"主人说`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "console:suzune")
			r.say(ownerA, "我明天考试。")
			r.gen.set(c.reply)
			res, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
			if !errors.Is(err, ErrBadOutput) || res.Counts[CountBadOutput] != 1 || res.Cursor != 0 {
				t.Fatalf("结果 %+v 错误 %v", res, err)
			}
			if r.cursor() != 0 || r.jobs() != 0 || len(r.factTexts()) != 0 || len(r.episodes()) != 0 {
				t.Errorf("什么都不该写：游标 %d job %d", r.cursor(), r.jobs())
			}
			// 下次模型回对了，同一批照常整理（游标没动）
			r.reply("no", "", cand("主人明天考试", "owner", 1))
			if res := r.run(); res.Skipped != "" || len(r.factTexts()) != 1 {
				t.Errorf("再来一次该整理成：%+v", res)
			}
		})
	}
}

func TestModelErrorWritesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	boom := errors.New("网关 502：上游挂了 key=sk-secret")
	r.gen.err = boom
	res, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
	if !errors.Is(err, boom) || res.Cursor != 0 || r.cursor() != 0 || r.jobs() != 0 {
		t.Fatalf("结果 %+v 错误 %v", res, err)
	}
	if strings.Contains(r.sink.String(), "sk-secret") || strings.Contains(r.sink.String(), "502") {
		t.Errorf("日志里不该有模型调用的错误原文：%s", r.sink.String())
	}
}

// 没配好的东西不静默当没事：没有事件来源、没有后台模型端口、没有提示词、写入没打开
func TestSummarizeBatchNeedsItsPorts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		name string
		o    rigOpts
		want error
	}{
		{"没配后台模型端口", rigOpts{noGen: true}, ErrNoGenerator},
		{"没配事件来源", rigOpts{mod: func(o *Options) { o.Events = nil }}, ErrNoEvents},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "console:suzune", c.o)
			if _, err := r.m.SummarizeBatch(ctx, r.conv, false); !errors.Is(err, c.want) {
				t.Errorf("错误 %v，应该是 %v", err, c.want)
			}
		})
	}
	t.Run("没配提示词：不调模型，不写", func(t *testing.T) {
		r := newRig(t, "console:suzune", rigOpts{mod: func(o *Options) { o.Prompts = prompts.Store{} }})
		r.say(ownerA, "你好")
		if _, err := r.m.SummarizeBatch(ctx, r.conv, false); err == nil || !strings.Contains(err.Error(), "prompt_error") || r.gen.count() != 0 || r.cursor() != 0 {
			t.Errorf("错误 %v 模型 %d 次", err, r.gen.count())
		}
	})
	t.Run("写入没打开", func(t *testing.T) {
		m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Gen: &scriptGen{}, Events: &memEvents{}})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if _, err := m.SummarizeBatch(ctx, "console:suzune", false); !errors.Is(err, ErrNotWritable) {
			t.Errorf("错误 %v", err)
		}
	})
}

// section 取 s 里 from 之后、to 之前的一段（找不到 from 返回空；to 为空取到结尾）
func section(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	if j := strings.Index(s, to); to != "" && j >= 0 {
		s = s[:j]
	}
	return s
}
