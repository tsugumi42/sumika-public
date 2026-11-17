package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// floorFacts 是编造的一批记忆：不少条都带"晚上""周五""什么"这类常用字，只有两条各自有罕见的词
func floorFacts(n int) [][]string {
	var facts [][]string
	add := func(text string) {
		facts = append(facts, []string{fmt.Sprint(len(facts) + 1), "fact", "active", text, "owner", "owner"})
	}
	add("主人的妹妹叫编造甲，在外地读编造专业，下个月放暑假回家。")
	add("主人喝咖啡只喝不加糖的冷萃。")
	for i := 0; len(facts) < n; i++ {
		switch i % 4 {
		case 0:
			add(fmt.Sprintf("主人第%d次说周五晚上要加班，不知道晚饭吃什么。", i))
		case 1:
			add(fmt.Sprintf("主人说第%d个晚上睡得不好，起来看了什么也没看进去。", i))
		case 2:
			add(fmt.Sprintf("主人记了第%d条备忘：这周要整理书架和桌面。", i))
		default:
			add(fmt.Sprintf("主人在第%d周去了公园，回来说什么都没带。", i))
		}
	}
	return facts
}

// 全文那一路要有相关度下限：库里没有相关内容时，问句只和记忆共用"周五""晚上""什么"这类常用字，不能返回结果。
// 修之前这几句各会拿到好几条（只要有一个两字词撞上就进名次）
func TestLexicalFloorUnrelatedQueries(t *testing.T) {
	t.Parallel()
	file := bunDb(t, floorFacts(40), nil)
	m, err := Open(context.Background(), file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, q := range []string{
		"我的车是什么牌子的？",
		"我周五要去哪里面试？",
		"我平时晚上写日记用什么墨水？",
	} {
		if hs := m.Recall(context.Background(), ownerHere, Query{Text: q, Mode: ModeExplicit, TopK: 12}).Hits; len(hs) != 0 {
			t.Errorf("无关问句 %q 不该有结果：%v", q, refs(hs))
		}
	}
}

// 下限不能误伤：有罕见词撞上的、几个常用词一起撞上的都要留
func TestLexicalFloorKeepsRelevant(t *testing.T) {
	t.Parallel()
	file := bunDb(t, floorFacts(40), nil)
	m, err := Open(context.Background(), file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	cases := map[string]string{
		"我妹妹学的是什么专业，什么时候回来？": "f:1",
		"冷萃是不是我常喝的":          "f:2",
	}
	for q, want := range cases {
		hs := m.Recall(context.Background(), ownerHere, Query{Text: q, Mode: ModeExplicit, TopK: 12}).Hits
		if len(hs) == 0 || hs[0].Ref != want {
			t.Errorf("%q 应该排第一的是 %s：%v", q, want, refs(hs))
		}
	}
	// 周五、晚上、加班三个常用词一起撞上同一条，合起来够
	if hs := m.Recall(context.Background(), ownerHere, Query{Text: "周五晚上加班", Mode: ModeExplicit, TopK: 12}).Hits; len(hs) == 0 {
		t.Error("几个常用词同时撞上同一条，不该被下限拦掉")
	}
}

// 库里不到 minDocsForFloor 条时词频没有意义，不设下限（保持原来的行为）
func TestLexicalFloorSkippedOnSmallCorpus(t *testing.T) {
	t.Parallel()
	file := bunDb(t, floorFacts(10), nil)
	m, err := Open(context.Background(), file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if hs := m.Recall(context.Background(), ownerHere, Query{Text: "我周五要去哪里面试？", Mode: ModeExplicit, TopK: 12}).Hits; len(hs) == 0 {
		t.Error("库太小不该设下限")
	}
}

func TestQueryTerms(t *testing.T) {
	t.Parallel()
	got := strings.Join(queryTerms("我的车是什么牌子的？gpt5 呢"), "|")
	if got != "牌子|gpt5" {
		t.Fatalf("查询词不对：%s", got)
	}
}
