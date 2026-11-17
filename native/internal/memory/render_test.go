package memory

import (
	"reflect"
	"testing"
	"time"
)

func TestRender(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(10 * 86_400_000)
	day := int64(86_400_000)
	r := Recalled{
		Pinned: []Hit{{Item: Item{Ref: "f:a", Text: "不叫她全名"}}, {Item: Item{Ref: "f:b", Text: "周五不聊工作"}}},
		Hits: []Hit{
			{Item: Item{Ref: "f:c", Text: "在学吉他", CreatedAt: now.UnixMilli() - day/3}},
			{Item: Item{Ref: "e:d", Text: "一起看了烟花", CreatedAt: now.UnixMilli() - day}},
			{Item: Item{Ref: "f:e", Text: "要搬家", CreatedAt: now.UnixMilli() - 5*day - day/3}, Hearsay: true},
		},
	}
	b := Render(r, now)
	if b.PinnedKey != "f:a,f:b" {
		t.Fatalf("PinnedKey = %q", b.PinnedKey)
	}
	if !reflect.DeepEqual(b.Refs, []string{"f:c", "e:d", "f:e"}) {
		t.Fatalf("Refs = %v", b.Refs)
	}
	if got := Render(Recalled{Gated: true}, now).Lines(true); got != nil {
		t.Fatalf("空的召回应该整段不出，得到 %q", got)
	}
}

// 命中那一段的标题下面带 DataNotice；只有固定要点时不带（那几行就是要她照着做的）
func TestRenderDataNotice(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(10 * 86_400_000)
	hits := Render(Recalled{Hits: []Hit{{Item: Item{Ref: "f:c", Text: "在学吉他", CreatedAt: now.UnixMilli()}}}}, now).Lines(true)
	if len(hits) < 3 || hits[1] != "可能相关的记忆：" || hits[2] != DataNotice {
		t.Fatalf("命中那一段的标题下面要紧跟 DataNotice：%q", hits)
	}
	for _, l := range Render(Recalled{Pinned: []Hit{{Item: Item{Ref: "f:a", Text: "不叫她全名"}}}}, now).Lines(true) {
		if l == DataNotice {
			t.Fatalf("只有固定要点时不该带 DataNotice")
		}
	}
}
