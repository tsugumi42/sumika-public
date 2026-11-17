package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSearchTool(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"ABCDEF", "fact", "active", "共同关键词公开事实", "owner", "public"},
		{"SECRET", "fact", "active", "共同关键词秘密事实", "owner", "owner"},
	}, [][]string{{"episode1", "共同关键词片段正文", "public"}})
	now := time.Now()
	m, err := Open(context.Background(), file, nil, nil, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	empty, err := m.Search(ctx, ownerHere, "  ")
	if err != nil || !empty.IsError || empty.Text != "要找什么，用一句话写。" {
		t.Fatalf("空查询: %+v %v", empty, err)
	}
	missing, err := m.Search(ctx, ownerHere, "完全不存在的词")
	if err != nil || missing.Text != "没有找到相关的记忆。可以换个说法、用更短的关键词再查一次；要确认对方当时的原话，用 recall_raw 找聊天记录。" {
		t.Fatalf("未找到: %+v %v", missing, err)
	}
	got, err := m.Search(ctx, inGroup(GA), "共同关键词")
	if err != nil {
		t.Fatal(err)
	}
	// 有结果时第一行是 DataNotice（规格-记忆-v2 7.10），没命中和空查询的回话不带
	if !strings.HasPrefix(got.Text, DataNotice+"\n- ") {
		t.Errorf("结果第一行该是 DataNotice：%q", got.Text)
	}
	if strings.Contains(missing.Text, DataNotice) || strings.Contains(empty.Text, DataNotice) {
		t.Errorf("没命中和空查询不带 DataNotice")
	}
	for _, s := range []string{"#ABCDEF", "共同关键词公开事实", "（片段）", "共同关键词片段正文", "秘密事实"} {
		if !strings.Contains(got.Text, s) {
			t.Errorf("缺少 %q: %s", s, got.Text)
		}
	}
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{{59 * time.Minute, "59 分钟前"}, {60 * time.Minute, "1 小时前"}, {24 * time.Hour, "1 天前"}} {
		if got := toolAgo(now.Add(-tc.d).UnixMilli(), now); got != tc.want {
			t.Errorf("%v: %s", tc.d, got)
		}
	}
	var many [][]string
	for i := range 14 {
		many = append(many, []string{fmt.Sprintf("ID%06d", i), "fact", "candidate", fmt.Sprintf("共同检索词第%d条%s", i, strings.Repeat("长", 500)), "owner", "public"})
	}
	manyDB := bunDb(t, many, nil)
	m2, err := Open(ctx, manyDB, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	result, err := m2.Search(ctx, inGroup(GA), "共同检索词")
	if err != nil || strings.Count(result.Text, "\n") != 12 { // DataNotice 一行加 12 条
		t.Fatalf("应返回 12 条且不受字数预算限制: %v %q", err, result.Text)
	}
}

// 四种场合都召回同角色条目，撤销的始终排除；主人在 QQ 群问时仍有披露提醒。
func TestSearchToolWhoSees(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"PUBLIC", "fact", "active", "通用词公开的事", "owner", "public"},
		{"OWNER1", "fact", "active", "通用词主人私下说的事", "owner", "owner"},
		{"CONVAA", "fact", "active", "通用词甲群里的事", "owner", "conv:" + GA},
		{"CONVBB", "fact", "active", "通用词乙群里的事", "owner", "conv:" + GB},
		{"REVOKE", "fact", "revoked", "通用词已撤销的事", "owner", "public"},
	}, nil)
	m, err := Open(context.Background(), file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	const pub, own, ga, gb, gone = "公开的事", "主人私下说的事", "甲群里的事", "乙群里的事", "已撤销的事"
	for _, tc := range []struct {
		name string
		a    Audience
		see  []string
	}{
		{"本地私聊主人", ownerHere, []string{pub, own, ga, gb}},
		{"本地群聊主人在场且场景空", Audience{Channel: "console", Conversation: "console:group:x", PrivateOK: true}, []string{pub, own, ga, gb}},
		{"QQ私聊群友", Audience{Channel: "qq", Conversation: "qq:private:9"}, []string{pub, own, ga, gb}},
		{"QQ群群友", inGroup(GA), []string{pub, own, ga, gb}},
		{"QQ群群友场景空", Audience{Channel: "qq", Conversation: GA}, []string{pub, own, ga, gb}},
		{"QQ群主人问", qqGroup(GA, true), []string{pub, own, ga, gb}},
		{"QQ群主人问场景空", Audience{Channel: "qq", Conversation: GA, OwnerAsked: true}, []string{pub, own, ga, gb}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Search(context.Background(), tc.a, "通用词")
			if err != nil || got.IsError {
				t.Fatalf("%+v %v", got, err)
			}
			for _, s := range []string{pub, own, ga, gb, gone} {
				if has, want := strings.Contains(got.Text, s), slices.Contains(tc.see, s); has != want {
					t.Errorf("%q 该不该看到 %v，实际 %v:\n%s", s, want, has, got.Text)
				}
			}
			for _, l := range strings.Split(got.Text, "\n") {
				if marked, wantMark := strings.Contains(l, ownerMarkText), strings.Contains(l, own) && tc.a.OwnerAsked && tc.a.Channel == "qq"; marked != wantMark {
					t.Errorf("「他问才答」标记该有 %v，实际 %v: %s", wantMark, marked, l)
				}
			}
		})
	}
}

// 工具版的时间：分钟、小时、天，四舍五入照 JS（59.5 分钟进到 1 小时，23.5 小时进到 24 小时，不是 23）
func TestToolAgoBoundaries(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0 分钟前"}, {-5 * time.Minute, "0 分钟前"}, {29 * time.Second, "0 分钟前"}, {30 * time.Second, "1 分钟前"},
		{59 * time.Minute, "59 分钟前"}, {59*time.Minute + 30*time.Second, "1 小时前"}, {60 * time.Minute, "1 小时前"},
		{90 * time.Minute, "2 小时前"}, {23 * time.Hour, "23 小时前"}, {23*time.Hour + 29*time.Minute, "23 小时前"},
		{23*time.Hour + 30*time.Minute, "24 小时前"}, {24 * time.Hour, "1 天前"}, {36 * time.Hour, "2 天前"}, {30 * 24 * time.Hour, "30 天前"},
	} {
		if got := toolAgo(now.Add(-tc.d).UnixMilli(), now); got != tc.want {
			t.Errorf("%v: 得到 %s，该是 %s", tc.d, got, tc.want)
		}
	}
}

// memory_search 给出的待确认事实标"听说："，确认过的不标（ADR-196 第 1 条）；主人问才给的私事，"他问才答"在"听说"前面，和每轮注入一样
func TestSearchToolMarksHearsay(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"ACTIVE", "fact", "active", "共同关键词确认过的事", "owner", "public"},
		{"CANDID", "fact", "candidate", "共同关键词还没确认的事", "owner", "public"},
		{"OWNCAN", "fact", "candidate", "共同关键词主人私下没确认的事", "owner", "owner"},
	}, nil)
	now := time.Now()
	m, err := Open(context.Background(), file, nil, nil, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got, err := m.Search(context.Background(), ownerHere, "共同关键词")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"#ACTIVE", "共同关键词确认过的事", "#CANDID", "听说：共同关键词还没确认的事"} {
		if !strings.Contains(got.Text, s) {
			t.Errorf("缺少 %q: %s", s, got.Text)
		}
	}
	if strings.Contains(got.Text, "听说：共同关键词确认过的事") {
		t.Errorf("确认过的不该标听说: %s", got.Text)
	}
	asked := inGroup(GA)
	asked.OwnerAsked = true
	inGroupAsk, err := m.Search(context.Background(), asked, "共同关键词")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inGroupAsk.Text, ownerMarkText+"听说：共同关键词主人私下没确认的事") {
		t.Errorf("主人在群里问到的待确认私事要先标他问才答、再标听说: %s", inGroupAsk.Text)
	}
}
