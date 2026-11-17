package memsource

import (
	"context"
	"reflect"
	"testing"

	"sumika-native/internal/memory"
)

// oldFind preserves the pre-index search as a behavioral oracle, including fill and paging.
func oldFind(ctx context.Context, s *Source, conv string, like []string, limit int) ([]memory.Event, error) {
	return oldSearch(ctx, s, findScope{conversation: conv}, like, limit)
}

func TestSearchOracle(t *testing.T) {
	f := newFixture(t)
	for i, text := range []string{"周", "周三", "周三牙医", "ABC abc AbC", "Ää Éé", "中English混合", `50% a_b c\d`, "aXb", "", "前缀中间后缀", "😀🍀中文", "nul\x00tail", "prefix\x00周三"} {
		conv := grp
		if i%2 == 1 {
			conv = priv
		}
		seq, _ := f.msg(conv, "qq:1", text, nil, me)
		f.exec(`UPDATE events SET created_at = ? WHERE seq = ?`, 1000+(i%3)*100, seq)
	}
	f.msg(grp, "qq:1", "周三牙医 仅凛", nil, other)
	f.add(grp, "notice", "human", "qq:1", "peer", "周三牙医 通知", nil, "", me)
	f.msg(cons, "owner", "周三牙医 主窗口", nil, me)
	f.msg("qq:group:another", "qq:1", "周三牙医 另一个群", nil, me)
	f.msg("console:group:test", "owner", "周三牙医 本地群", nil, me)
	for _, text := range []string{"a\"b", "a'b", "a\nb", "e\u0301x", "中A!", "%_", `\%`} {
		f.msg(grp, "qq:1", text, nil, me)
	}
	first, _ := f.msg(grp, "qq:1", "分页词 老消息", nil, me)
	for range 60 {
		_, id := f.msg(grp, "qq:1", "分页词 撤回", nil, me)
		f.add(grp, "notice", "human", "qq:1", "peer", "撤回", map[string]any{"notice": "recall"}, id, me)
	}
	for range 60 {
		f.msg(grp, me, "分页词 未发", map[string]any{"unsent": "失败"}, me)
	}
	patterns := [][]string{nil, {}, {""}, {"%"}, {"___"}, {"%周%"}, {"%周三%"}, {"%周三牙医%"}, {"%a%"}, {"%Ab%"}, {"%ABC%"}, {"%Ä%"}, {"%ä%"}, {"%Éé%"}, {"%中English%"}, {`%50\%%`}, {`%a\_b%`}, {`%c\\d%`}, {"a_b"}, {"%前%后%"}, {"%周%", "%牙医%"}, {"%不存在%"}, {"%分页词%"}, {`%\`}, {"%nul\x00tail%"}, {"%\x00周%"}, {`%a"b%`}, {"%a'b%"}, {"%a\nb%"}, {"%e\u0301x%"}, {"%中A!%"}, {`%\%%`}, {`%\%\_%`}, {`%\\\%%`}}
	patterns = append(patterns,
		[]string{"%周三%", "%牙医%"},
		[]string{"%周三%", "%缺词%"},
		[]string{"%周三%", "%周三%"},
		[]string{"%中E%", "%gl%"},
		[]string{"%gl%", "%中E%"},
	)
	count := 0
	for _, character := range []string{me, other} {
		s := f.source(character)
		for _, conv := range []string{"", grp, priv, cons, "console:group:test", "qq:group:another", "missing"} {
			for _, p := range patterns {
				for _, limit := range []int{0, 1, 3, 200} {
					got, err := s.Find(ctx, conv, p, limit)
					want, oldErr := oldFind(ctx, s, conv, p, limit)
					count++
					if (err == nil) != (oldErr == nil) || err == nil && !reflect.DeepEqual(got, want) {
						t.Fatalf("character=%q conv=%q like=%q limit=%d\ngot=%+v err=%v\nwant=%+v err=%v", character, conv, p, limit, got, err, want, oldErr)
					}
				}
			}
		}
	}
	t.Logf("oracle comparisons=%d", count)
	got, err := f.source(me).Find(ctx, grp, []string{"%分页词%"}, 3)
	if err != nil || len(got) != 1 || got[0].Seq != first {
		t.Fatalf("paging: %+v %v", got, err)
	}
}

func TestSearchWindow(t *testing.T) {
	f := newFixture(t)
	for _, v := range []struct {
		conv string
		at   int64
		text string
	}{{grp, 999, "外"}, {grp, 1000, "下界"}, {priv, 1500, "排除"}, {grp, 1999, "上界内"}, {grp, 2000, "上界外"}, {grp, 1500, "同刻一"}, {grp, 1500, "同刻二"}} {
		seq, _ := f.msg(v.conv, "qq:1", "窗口 "+v.text, nil, me)
		f.exec(`UPDATE events SET created_at = ? WHERE seq = ?`, v.at, seq)
	}
	s := f.source(me)
	got, err := s.FindWindow(ctx, priv, []string{"%窗口%"}, 1000, 2000, 10)
	if err != nil || !equalTexts(got, "窗口 上界内", "窗口 同刻二", "窗口 同刻一", "窗口 下界") {
		t.Fatalf("window: %+v %v", got, err)
	}
	all, err := oldFind(ctx, s, "", []string{"%窗口%"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var want []memory.Event
	for _, e := range all {
		if e.Conversation != priv && e.At >= 1000 && e.At < 2000 {
			want = append(want, e)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("window mismatch: %+v %+v", got, want)
	}
	seq, _ := f.msg(grp, "qq:1", "窗口 ABC 模型机器人", nil, me)
	f.exec(`UPDATE events SET created_at = ? WHERE seq = ?`, 1500, seq)
	for _, patterns := range [][]string{
		{"%窗%"}, {"%窗口%"}, {"%口 上界内%"}, {"%abc%"}, {"%模型机器人%"},
		{"%窗%", "%模型机器人%"}, {"%ABC%", "%模型%"}, {"%不存在%"},
	} {
		for _, bounds := range [][2]int64{{1000, 2000}, {1500, 1501}, {1501, 2000}} {
			scope := findScope{excludeConversation: priv, window: true, sinceAt: bounds[0], untilAt: bounds[1]}
			got, err := s.FindWindow(ctx, priv, patterns, bounds[0], bounds[1], 10)
			want, oldErr := oldSearch(ctx, s, scope, patterns, 10)
			if (err == nil) != (oldErr == nil) || err == nil && !reflect.DeepEqual(got, want) {
				t.Fatalf("window like=%q bounds=%v got=%+v err=%v want=%+v err=%v", patterns, bounds, got, err, want, oldErr)
			}
		}
	}
	for _, v := range []struct {
		like         []string
		since, until int64
		limit        int
	}{{nil, 0, 3000, 10}, {[]string{"%窗口%"}, 2000, 2000, 10}, {[]string{"%窗口%"}, 2001, 2000, 10}, {[]string{"%窗口%"}, 0, 3000, 0}} {
		got, err := s.FindWindow(ctx, "", v.like, v.since, v.until, v.limit)
		if err != nil || got != nil {
			t.Fatalf("empty window: %+v %v", got, err)
		}
	}
}

func TestSearchWindowPagingAndRole(t *testing.T) {
	f := newFixture(t)
	first, _ := f.msg(grp, "qq:1", "窗口分页 保留", nil, me)
	f.msg(grp, "qq:1", "窗口分页 凛", nil, other)
	for range 60 {
		_, id := f.msg(grp, "qq:1", "窗口分页 撤回", nil, me)
		f.add(grp, "notice", "human", "qq:1", "peer", "撤回", map[string]any{"notice": "recall"}, id, me)
	}
	for range 60 {
		f.msg(grp, me, "窗口分页 未发", map[string]any{"unsent": "失败"}, me)
	}
	for _, character := range []string{me, other} {
		s := f.source(character)
		got, err := s.FindWindow(ctx, priv, []string{"%窗口分页%"}, 1000, 1001, 3)
		if err != nil {
			t.Fatal(err)
		}
		want, err := oldSearch(ctx, s, findScope{excludeConversation: priv, window: true, sinceAt: 1000, untilAt: 1001}, []string{"%窗口分页%"}, 3)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("window role=%s got=%+v want=%+v err=%v", character, got, want, err)
		}
		if character == me && (len(got) != 1 || got[0].Seq != first) {
			t.Fatalf("window paging: %+v", got)
		}
	}
}

func TestMultiSearchWindow(t *testing.T) {
	f := newMultiFixture(t)
	f.msg(f.local, cons, "qq:1", "关键词 本地一", 3000, me)
	f.msg(f.local, cons, "qq:1", "关键词 本地二", 3000, me)
	f.msg(f.qq, grp, "qq:1", "关键词 QQ", 3000, me)
	f.msg(f.qq, priv, "qq:1", "关键词 排除", 4000, me)
	f.msg(f.qq, grp, "qq:1", "关键词 下界", 1000, me)
	f.msg(f.qq, grp, "qq:1", "关键词 上界", 5000, me)
	got, err := f.multi(me).FindWindow(ctx, priv, []string{"%关键词%"}, 1000, 5000, 3)
	if err != nil || !equalTexts(got, "关键词 本地二", "关键词 本地一", "关键词 QQ") {
		t.Fatalf("multi window: %+v %v", got, err)
	}
}
