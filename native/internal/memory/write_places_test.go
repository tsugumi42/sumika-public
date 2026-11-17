package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 后台整理的场合过滤（规格-记忆写入上线 W5）。会话 id、号码、名字都是编的；预期表手写，不调被测的 convPlace / PlaceMayWrite

func TestConvPlaceShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		conv  string
		place Place
		ok    bool
	}{
		// 真实的格式
		{"console:suzune", LocalPrivate, true},
		{"console:suzune:topic:t1", LocalPrivate, true},
		{"console:group:g1", LocalGroup, true},
		{"console:suzune:group:demo", LocalGroup, true},
		{"qq:group:5001001", PlatformGroup, true},
		{"qq:private:20001:60001", PlatformPrivate, true},
		// 陌生渠道：一律认不出，哪怕形状像
		{"telegram:private:1:2", "", false},
		{"telegram:group:9", "", false},
		{"discord:suzune", "", false},
		{"QQ:group:5001001", "", false},
		{"qqq:group:5001001", "", false},
		// 形状错：没渠道、空段、多段、缺段、号不是数字串、带空白
		{"", "", false},
		{"qq", "", false},
		{"qq:", "", false},
		{":group:1", "", false},
		{"console:", "", false},
		{"console::x", "", false},
		{"console:a:topic", "", false},
		{"console:a:topic:b:c", "", false},
		{"console:a:b:c", "", false},
		{"qq:group:", "", false},
		{"qq:group:abc", "", false},
		{"qq:group:12:34", "", false},
		{"qq:group:12 34", "", false},
		{"qq:group:5001001 ", "", false},
		{"qq:private:7001", "", false},
		{"qq:private:20001:", "", false},
		{"qq:private::60001", "", false},
		{"qq:private:20001:60001:9", "", false},
		{"qq:private:20001:60x01", "", false},
		{"qq:private:x:60001", "", false},
		{"qq:private:1:group:2", "", false},
		{"qq:60001", "", false},
		{"qq:other:20001:60001", "", false},
		{"qq:group:５００１", "", false}, // 全角数字不是号
		{"qq:group:5001001\n", "", false},
	}
	for _, c := range cases {
		place, ok := convPlace(c.conv)
		if ok != c.ok || place != c.place {
			t.Errorf("%q：得到 (%q, %v)，应该是 (%q, %v)", c.conv, place, ok, c.place, c.ok)
		}
	}
}

// 五个会话 × 几份放行名单：放不放行，手写
func TestPlaceMayWriteMatrix(t *testing.T) {
	t.Parallel()
	convs := []struct{ name, conv string }{
		{"本地私聊", "console:suzune"},
		{"本地话题", "console:suzune:topic:t1"},
		{"本地群聊", "console:group:g1"},
		{"QQ 私聊", "qq:private:20001:60001"},
		{"QQ 群", "qq:group:5001001"},
		{"陌生渠道", "telegram:private:1:2"},
		{"形状错", "qq:private:7001"},
	}
	// 名单 → 各会话放不放行（顺序同 convs）。QQ 私聊在 owner-private 下是"先放行"：是不是主人的要读了事件才知道
	lists := []struct {
		name   string
		places []string
		want   [7]bool
	}{
		{"默认（nil）", nil, [7]bool{false, false, false, true, false, false, false}},
		{"空名单", []string{}, [7]bool{}},
		{"owner-private", []string{WritePlaceOwnerPrivate}, [7]bool{false, false, false, true, false, false, false}},
		{"local-private", []string{WritePlaceLocalPrivate}, [7]bool{true, true, false, false, false, false, false}},
		{"local-group", []string{WritePlaceLocalGroup}, [7]bool{false, false, true, false, false, false, false}},
		{"platform-private", []string{WritePlacePlatformPrivate}, [7]bool{false, false, false, true, false, false, false}},
		{"platform-group", []string{WritePlacePlatformGroup}, [7]bool{false, false, false, false, true, false, false}},
		{"五个全开", AllWritePlaces(), [7]bool{true, true, true, true, true, false, false}},
		{"不认识的取值", []string{"everything", "qq", ""}, [7]bool{}},
	}
	for _, l := range lists {
		r := newRig(t, "console:suzune", rigOpts{mod: func(o *Options) { o.WritePlaces = l.places }})
		for i, c := range convs {
			if got := r.m.PlaceMayWrite(c.conv); got != l.want[i] {
				t.Errorf("名单 %s，%s（%s）：放行 %v，应该是 %v", l.name, c.name, c.conv, got, l.want[i])
			}
		}
	}
}

// PlaceAllows：名单里只有 owner-private 时，QQ 私聊还要看最近一句人话是不是主人说的（身份按 ActorKind，不按名字）
func TestPlaceAllowsOwnerSide(t *testing.T) {
	t.Parallel()
	const conv = "qq:private:20001:60001"
	cases := []struct {
		name   string
		places []string
		say    func(r *rig)
		said   func(string) (Event, bool, error)
		want   bool
	}{
		{"主人说的", nil, func(r *rig) { r.say(ownerA, "在吗") }, nil, true},
		{"对方说的", nil, func(r *rig) { r.say(pingA, "在吗") }, nil, false},
		{"群名片叫主人的对方", nil, func(r *rig) {
			r.say(actor{"human", "qq:60001", "主人", "peer"}, "我是主人")
		}, nil, false},
		{"没人说过话", nil, func(r *rig) {}, nil, false},
		{"只有她自己说过", nil, func(r *rig) { r.say(herA, "晚安") }, nil, false},
		{"查出错", nil, func(r *rig) {}, func(string) (Event, bool, error) { return Event{}, false, errors.New("库忙") }, false},
		{"查到的是别的会话", nil, func(r *rig) {}, func(string) (Event, bool, error) {
			return Event{Conversation: "qq:private:20001:60002", ActorKind: "owner", Origin: "owner", Kind: "message"}, true, nil
		}, false},
		{"名单有 platform-private：对方说的也放行", []string{WritePlacePlatformPrivate}, func(r *rig) { r.say(pingA, "在吗") }, nil, true},
		{"名单有两个：对方说的也放行", []string{WritePlaceOwnerPrivate, WritePlacePlatformPrivate}, func(r *rig) { r.say(pingA, "在吗") }, nil, true},
		{"空名单：主人说的也不放行", []string{}, func(r *rig) { r.say(ownerA, "在吗") }, nil, false},
		{"名单只有 platform-group：不放行", []string{WritePlacePlatformGroup}, func(r *rig) { r.say(ownerA, "在吗") }, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, conv, rigOpts{mod: func(o *Options) { o.WritePlaces = c.places }})
			c.say(r)
			r.ev.said = c.said
			got, err := r.m.PlaceAllows(context.Background(), conv)
			if got != c.want {
				t.Fatalf("放行 %v（错误 %v），应该是 %v", got, err, c.want)
			}
		})
	}
	// 群、本地：不读事件
	r := newRig(t, "qq:group:5001001", rigOpts{mod: func(o *Options) { o.WritePlaces = nil }})
	if ok, err := r.m.PlaceAllows(context.Background(), "qq:group:5001001"); ok || err != nil || r.ev.saidCall != 0 {
		t.Fatalf("默认名单里群不放行，也不用读事件：%v %v 读了 %d 次", ok, err, r.ev.saidCall)
	}
}

// dbState 把一次整理可能动的东西都收进一个字符串：事实、片段、修订、证据、任务、游标行
func dbState(t *testing.T, r *rig) string {
	t.Helper()
	var parts []string
	for _, q := range []string{
		`SELECT count(*) FROM facts`, `SELECT count(*) FROM episodes`, `SELECT count(*) FROM fact_revisions`, `SELECT count(*) FROM fact_evidence`,
		`SELECT count(*) FROM memory_jobs`, `SELECT count(*) FROM summary_state`, `SELECT coalesce(sum(last_seq), 0) FROM summary_state`,
		`SELECT coalesce(sum(failures), 0) FROM summary_state`, `SELECT coalesce(sum(updated_at), 0) FROM summary_state`,
	} {
		parts = append(parts, fmt.Sprint(count(t, r.m, q)))
	}
	return strings.Join(parts, ",")
}

func TestSummarizeBatchPlaceGate(t *testing.T) {
	t.Parallel()
	t.Run("整理没开：报错不悄悄返回", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "qq:private:20001:60001", rigOpts{mod: func(o *Options) { o.SummarizeEnabled = false }})
		r.say(ownerA, "今天去了公园")
		_, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
		if !errors.Is(err, ErrSummarizeOff) || r.gen.count() != 0 {
			t.Fatalf("得到 %v，模型调用 %d 次", err, r.gen.count())
		}
		if FailureCode(err) == "other" {
			t.Fatalf("原因码要认得它：%q", FailureCode(err))
		}
	})

	// 不放行的会话：什么都不读不写。每一格：SkipPlace、游标不动、模型 0 次、库一个字节没变、事件来源没被读
	skip := []struct {
		name   string
		conv   string
		places []string
		say    func(r *rig)
		before func(r *rig)
	}{
		{"QQ 群，默认名单", "qq:group:5001001", nil, func(r *rig) { r.say(ownerA, "今天去了公园") }, nil},
		{"QQ 群，空名单", "qq:group:5001001", []string{}, func(r *rig) { r.say(ownerA, "今天去了公园") }, nil},
		{"本地私聊，默认名单", "console:suzune", nil, func(r *rig) { r.say(ownerA, "今天去了公园") }, nil},
		{"本地群聊，只开 platform-group", "console:group:g1", []string{WritePlacePlatformGroup}, func(r *rig) { r.say(ownerA, "今天去了公园") }, nil},
		{"陌生渠道", "telegram:private:1:2", AllWritePlaces(), func(r *rig) { r.say(ownerA, "今天去了公园") }, nil},
		{"形状错的 QQ 会话", "qq:private:7001", AllWritePlaces(), func(r *rig) { r.say(ownerA, "今天去了公园") }, nil},
		{"别人的私聊，默认名单", "qq:private:20001:60001", nil, func(r *rig) { r.say(pingA, "今天去了公园") }, nil},
		// 下面三格是顺序的坑：这几条路径本来都会挪游标（只有占位、已整理过、全被丢掉），别人的私聊也不能动
		{"别人的私聊，只有占位", "qq:private:20001:60001", nil, func(r *rig) { r.say(pingA, "[图片]"); r.say(pingA, "嗯") }, nil},
		{"别人的私聊，这一批已整理过", "qq:private:20001:60001", nil, func(r *rig) {
			r.say(pingA, "今天去了公园")
		}, func(r *rig) {
			if _, err := r.m.w.db.Exec(`INSERT INTO memory_jobs (conversation, from_seq, to_seq, version, done_at) VALUES (?, 1, 1, ?, 1)`, r.conv, SummaryVersion); err != nil {
				t.Fatal(err)
			}
		}},
		{"别人的私聊，全是撤回的", "qq:private:20001:60001", nil, func(r *rig) {
			r.say(pingA, "今天去了公园", func(e *Event) { e.Revoked = true })
		}, nil},
		{"群名片叫主人的对方", "qq:private:20001:60001", nil, func(r *rig) {
			r.say(actor{"human", "qq:60001", "主人", "peer"}, "今天去了公园")
		}, nil},
		{"没人说过话的私聊", "qq:private:20001:60001", nil, func(r *rig) {}, nil},
		{"她自己先开口、对方没回的私聊", "qq:private:20001:60001", nil, func(r *rig) { r.say(herA, "在吗") }, nil},
	}
	for _, c := range skip {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, c.conv, rigOpts{mod: func(o *Options) { o.WritePlaces = c.places }})
			c.say(r)
			if c.before != nil {
				c.before(r)
			}
			before, reads := dbState(t, r), r.ev.readCount()
			res, err := r.m.SummarizeBatch(context.Background(), c.conv, true) // force：不让退避挡住，要证明挡住它的是场合
			if err != nil {
				t.Fatal(err)
			}
			if res.Skipped != SkipPlace || res.Counts[CountPlaceSkipped] != 1 {
				t.Fatalf("结局 %q 计数 %v，应该是 SkipPlace 和 place_skipped=1", res.Skipped, res.Counts)
			}
			if len(res.Counts) != 1 {
				t.Errorf("跳过的这一批不该带别的计数（读了几条之类会让对账以为整理过）：%v", res.Counts)
			}
			if r.gen.count() != 0 {
				t.Errorf("不放行的会话调了 %d 次模型", r.gen.count())
			}
			if after := dbState(t, r); after != before {
				t.Errorf("库变了：%s → %s", before, after)
			}
			if r.cursor() != 0 {
				t.Errorf("游标动了：%d", r.cursor())
			}
			if c.conv == "telegram:private:1:2" || c.conv == "qq:private:7001" || strings.HasPrefix(c.conv, "qq:group:") || strings.HasPrefix(c.conv, "console:") {
				if reads != r.ev.readCount() {
					t.Errorf("会话级就不放行的，连事件都不该读：读了 %d → %d 次", reads, r.ev.readCount())
				}
			}
			// 日志只说一次、不带会话 id 和号码
			log := r.sink.String()
			if strings.Count(log, "place_skipped") != 1 {
				t.Errorf("日志该说一次 place_skipped：%q", log)
			}
			for _, leak := range []string{"20001", "60001", "5001001", "7001", c.conv} {
				if strings.Contains(log, leak) {
					t.Errorf("日志里有会话 id 或号码 %q：%q", leak, log)
				}
			}
			if _, err := r.m.SummarizeBatch(context.Background(), c.conv, true); err != nil {
				t.Fatal(err)
			}
			if strings.Count(r.sink.String(), "place_skipped") != 1 {
				t.Errorf("同一个会话第二次不该再记日志：%q", r.sink.String())
			}
		})
	}

	// 放行的会话照常整理
	ok := []struct {
		name   string
		conv   string
		places []string
		say    func(r *rig)
	}{
		{"主人的私聊，默认名单", "qq:private:20001:60001", nil, func(r *rig) { r.say(ownerA, "今天去了公园") }},
		{"主人的私聊，这一批只有她自己的话，之前主人说过", "qq:private:20001:60001", nil, func(r *rig) {
			r.say(ownerA, "晚上见")
			r.say(herA, "好呀")
			r.ev.said = func(string) (Event, bool, error) {
				return Event{Conversation: "qq:private:20001:60001", ActorKind: "owner", Origin: "owner", Kind: "message"}, true, nil
			}
			r.cursorTo(t, 1)
		}},
		{"别人的私聊，开了 platform-private", "qq:private:20001:60001", []string{WritePlacePlatformPrivate}, func(r *rig) { r.say(pingA, "今天去了公园") }},
		{"群，开了 platform-group", "qq:group:5001001", []string{WritePlacePlatformGroup}, func(r *rig) { r.say(ownerA, "今天去了公园") }},
		{"本地私聊，开了 local-private", "console:suzune", []string{WritePlaceLocalPrivate}, func(r *rig) { r.say(ownerA, "今天去了公园") }},
		{"本地群聊，开了 local-group", "console:group:g1", []string{WritePlaceLocalGroup}, func(r *rig) { r.say(ownerA, "今天去了公园") }},
		{"五个全开的群", "qq:group:5001001", AllWritePlaces(), func(r *rig) { r.say(pingA, "今天去了公园") }},
	}
	for _, c := range ok {
		t.Run("放行："+c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, c.conv, rigOpts{mod: func(o *Options) { o.WritePlaces = c.places }})
			c.say(r)
			res, err := r.m.SummarizeBatch(context.Background(), c.conv, true)
			if err != nil || res.Skipped == SkipPlace || res.Counts[CountPlaceSkipped] != 0 {
				t.Fatalf("放行的会话被挡了：%v %v", res.Skipped, err)
			}
			if r.gen.count() != 1 || r.cursor() == 0 {
				t.Errorf("模型 %d 次，游标 %d：应该整理了", r.gen.count(), r.cursor())
			}
		})
	}
}

// cursorTo 把游标直接放到某个位置（测试里模拟"前一批已经整理过"）
func (r *rig) cursorTo(t *testing.T, seq int64) {
	t.Helper()
	if _, err := r.m.w.db.Exec(`UPDATE summary_state SET last_seq = ? WHERE conversation = ?`, seq, r.conv); err != nil {
		t.Fatal(err)
	}
}
