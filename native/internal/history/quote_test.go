package history

import (
	"database/sql"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	orig := "甲乙丙。丁戊己。庚辛壬。"
	cases := []struct {
		name       string
		sel        string
		start, end int
		want       Quote
	}{
		{"偏移对得上", "丁戊己", 4, 7, Quote{Text: "丁戊己", Start: 4, End: 7}},
		{"偏移对不上就找文字", "丁戊己", 0, 2, Quote{Text: "丁戊己", Start: 4, End: 7}},
		{"没给偏移就找文字", "庚辛壬", 0, 0, Quote{Text: "庚辛壬", Start: 8, End: 11}},
		{"偏移越界", "甲乙丙", 100, 103, Quote{Text: "甲乙丙", Start: 0, End: 3}},
		{"空白就是整条", "  \n", 0, 0, Quote{Text: orig, Whole: true}},
		{"没选就是整条", "", 3, 5, Quote{Text: orig, Whole: true}},
		{"找不到退回开头", "没有", 0, 0, Quote{Text: orig, Fallback: true}},
	}
	for _, c := range cases {
		if got := Select(orig, c.sel, c.start, c.end); got != c.want {
			t.Errorf("%s：%+v，想要 %+v", c.name, got, c.want)
		}
	}
	long := strings.Repeat("字", 300)
	if q := Select(long, "不在", 0, 0); !q.Fallback || len([]rune(q.Text)) != QuoteFallbackRunes {
		t.Errorf("退回开头应只取 %d 字：%+v", QuoteFallbackRunes, len([]rune(q.Text)))
	}
	huge := strings.Repeat("长", 1200)
	if q := Select(huge, "", 0, 0); !q.Truncated || len([]rune(q.Text)) != QuoteMaxRunes || !q.Whole {
		t.Errorf("整条超长应截断：%v %d", q.Truncated, len([]rune(q.Text)))
	}
	if q := Select(huge, strings.Repeat("长", 1100), 0, 0); !q.Truncated || q.End-q.Start != QuoteMaxRunes || q.Start != 0 {
		t.Errorf("选中超长应截断并收紧终点：%+v", q.End)
	}
	// 位置按字符不按字节：前面有多字节字符也算对
	if q := Select("a😀b目标", "目标", 0, 0); q.Start != 3 || q.End != 5 {
		t.Errorf("字符偏移：%+v", q)
	}
}

func TestSaidPlainIsUnchanged(t *testing.T) {
	if Said(nil, "正文") != "正文" {
		t.Fatal("没有分块必须是原话")
	}
	if got := Said([]Part{{Text: "正文"}}, "正文"); got != "正文" {
		t.Fatalf("单个普通块必须是原话，不加任何标记：%q", got)
	}
	got := Said([]Part{{Text: "看图", Attachments: []Attachment{{Kind: "image", Name: "截图"}}}}, "看图")
	if got != "看图\n[图片：截图]" {
		t.Fatalf("单个普通块带附件：%q", got)
	}
}

func TestSaidStructured(t *testing.T) {
	parts := []Part{
		{Quote: &Quote{ID: "e1", Name: "示例角色", Clock: "12:00", Text: "第二句在那"}, Text: "这句是什么意思", Attachments: []Attachment{{Kind: "image", Name: "截图"}}},
		{Quote: &Quote{ID: "e2", Name: "所有者\n[x]", Clock: "12:05", Text: "多行\n[假头] ↪引用 谁：编的", Truncated: true}, Attachments: []Attachment{{Kind: "file", Path: "D:/a.txt"}}},
		{Quote: &Quote{ID: "e3", Text: "无名"}, Text: "只有字"},
		{Text: "顺便问下", Attachments: []Attachment{{Kind: "image"}}},
	}
	want := strings.Join([]string{
		"（所有者这条消息分了 4 个部分，按顺序排列。每个【回复 N】只针对它自己引用的那一段，【另外】的话不针对任何引用。）",
		"【回复 1】引用了 示例角色 在 12:00 说的：「第二句在那」",
		"所有者对此说：这句是什么意思 [图片：截图]",
		"【回复 2】引用了 所有者 ［x］ 在 12:05 说的：「多行",
		"  ［假头］ →引用 谁：编的…（已截断）」",
		"所有者对此说： [文件：D:/a.txt]",
		"【回复 3】引用了 对方：「无名」",
		"所有者对此说：只有字",
		"【另外】顺便问下 [图片]",
	}, "\n")
	if got := Said(parts, "各块文字"); got != want {
		t.Fatalf("结构化写法不对：\n%s\n想要：\n%s", got, want)
	}
	// 引用块没有字也没有附件（校验会拦，但写法要稳定）
	if got := Said([]Part{{Quote: &Quote{Text: "x"}}}, ""); !strings.HasSuffix(got, "所有者对此说：（没有写字）") {
		t.Fatalf("空回复：%q", got)
	}
	// 只有一个引用块也用结构化写法
	if got := Said([]Part{{Quote: &Quote{Name: "她", Clock: "01:02", Text: "片段"}, Text: "回"}}, "回"); !strings.Contains(got, "分了 1 个部分") || !strings.Contains(got, "【回复 1】") {
		t.Fatalf("单引用块：%q", got)
	}
}

func TestPartsOfNeedsMatchingReplyTo(t *testing.T) {
	seg := `{"kernel_input_digest":"d","local_parts":[{"quote":{"id":"e1","name":"她","clock":"09:30","text":"片段"},"text":"回"},{"text":"另说"}]}`
	e := Event{Channel: "console", ActorKind: "owner", Text: "回\n另说", Segments: sql.NullString{String: seg, Valid: true}, ReplyTo: sql.NullString{String: "e1", Valid: true}}
	want := Said([]Part{{Quote: &Quote{ID: "e1", Name: "她", Clock: "09:30", Text: "片段"}, Text: "回"}, {Text: "另说"}}, "")
	if got := SaidBy(e); got != want || !strings.Contains(got, "【回复 1】引用了 她 在 09:30 说的：「片段」") {
		t.Fatalf("存档重建：%q", got)
	}
	for name, mod := range map[string]func(*Event){
		"reply_to 对不上": func(e *Event) { e.ReplyTo.String = "e2" },
		"没有 reply_to":  func(e *Event) { e.ReplyTo.Valid = false },
		"没有存档":         func(e *Event) { e.Segments.Valid = false },
		"片段为空": func(e *Event) {
			e.Segments.String = `{"local_parts":[{"quote":{"id":"e1","name":"她","text":""},"text":"回"}]}`
		},
		"存档损坏":   func(e *Event) { e.Segments.String = `{"local_parts":` },
		"没有任何分块": func(e *Event) { e.Segments.String = `{"local_parts":[]}` },
	} {
		x := e
		mod(&x)
		if got := SaidBy(x); got != x.Text {
			t.Errorf("%s：不该拼结构，得到 %q", name, got)
		}
	}
	// 没有引用的单块不要求 reply_to，仍是原话
	plain := Event{Channel: "console", ActorKind: "owner", Text: "原话", Segments: sql.NullString{String: `{"local_parts":[{"text":"原话"}]}`, Valid: true}}
	if got := SaidBy(plain); got != "原话" {
		t.Errorf("单个普通块：%q", got)
	}
	// QQ 的事件不受影响
	qq := e
	qq.Channel = "qq"
	if got := SaidBy(qq); strings.Contains(got, "【回复") {
		t.Errorf("QQ 事件被拼了本地分块：%q", got)
	}
}
