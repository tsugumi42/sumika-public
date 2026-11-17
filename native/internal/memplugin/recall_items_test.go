package memplugin

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/memory"
	"sumika-native/internal/turn"
)

// 界面看的召回明细和注入给模型的行是同一次召回：条数、顺序、正文都对得上，给过的不重复，固定要点没变不重给。
func TestContributionRecallMatchesLines(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), &fakeEmbed{})
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}
	ask := func(text string) ([]string, []string) {
		c, err := lf.context(context.Background(), in("suzu", consoleAud, text, st))
		if err != nil {
			t.Fatal(err)
		}
		if c == nil {
			return nil, nil
		}
		c.OnAccepted()
		var texts []string
		for _, it := range c.Recall {
			texts = append(texts, it.Text)
		}
		return c.Lines, texts
	}

	lines, texts := ask("吉他练得怎么样了")
	body := joined(lines)
	if len(texts) == 0 {
		t.Fatal("注入了记忆却没有结构化明细")
	}
	last := -1
	for _, tx := range texts {
		i := strings.Index(body, tx)
		if i < 0 {
			t.Fatalf("明细里的「%s」没出现在注入给模型的行里：\n%s", tx, body)
		}
		if i < last {
			t.Fatalf("明细的顺序和注入的顺序不一致：%v\n%s", texts, body)
		}
		last = i
	}
	// 条数对得上：固定要点 + 命中，行数里的每一条（以"- "开头）都有明细
	if n := strings.Count(body, "\n- ") + strings.Count(body, "\r\n- "); n > 0 && n != len(texts) {
		t.Fatalf("注入 %d 条，明细 %d 条", n, len(texts))
	}
	c, _ := lf.context(context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", map[string]any{}))
	var pinned, hit int
	for _, it := range c.Recall {
		if it.Pinned {
			pinned++
			if it.Score != 0 || it.Rerank != 0 {
				t.Errorf("固定要点不是按这句话召回的，不该带分数：%+v", it)
			}
		} else {
			hit++
			if it.Score <= 0 || it.CreatedAt <= 0 {
				t.Errorf("命中应带综合分和记录时间：%+v", it)
			}
			if it.Rerank != 0 {
				t.Errorf("没配重排却有重排分（不许编）：%+v", it)
			}
		}
	}
	if pinned == 0 || hit == 0 {
		t.Fatalf("应有固定要点和命中各至少一条：pinned=%d hit=%d", pinned, hit)
	}

	// 原样再问：什么都没有新的，没有贡献，也就没有召回明细
	if lines, texts := ask("吉他练得怎么样了"); len(lines) != 0 || len(texts) != 0 {
		t.Fatalf("重复问不该再给：%v %v", lines, texts)
	}
	// 问到新的一条：只有它，固定要点不重复
	_, texts = ask("搬家的事怎么样了")
	got := strings.Join(texts, " | ")
	if !strings.Contains(got, textMove) || strings.Contains(got, pinnedPublic) || strings.Contains(got, textGuitar) {
		t.Fatalf("只该多出新的命中，固定要点和给过的不重复：%v", texts)
	}
}

// 注入的行后面拼着“取代了旧记录”，界面明细要带同一份旧版本（正文、时间、新的在前），不然看不到模型读到的旧值。
func TestRecallItemCarriesPrevious(t *testing.T) {
	t.Parallel()
	h := memory.Hit{Item: memory.Item{Text: "新住址", CreatedAt: 9, Previous: []memory.Item{{Text: "旧住址甲", CreatedAt: 5}, {Text: "旧住址乙", CreatedAt: 2}}}}
	got := recallItem(h, false).Previous
	want := []turn.RecallPrior{{Text: "旧住址甲", CreatedAt: 5}, {Text: "旧住址乙", CreatedAt: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("旧版本没带到明细：%+v", got)
	}
	if p := recallItem(memory.Hit{Item: memory.Item{Text: "无旧版"}}, false).Previous; p != nil {
		t.Fatalf("没有旧版本不该有：%+v", p)
	}
}
