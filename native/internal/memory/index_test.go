package memory

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"sumika-native/internal/embed"
)

func TestBigrams(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"主人最近在学吉他":     {"主人", "人最", "最近", "近在", "在学", "学吉", "吉他"},
		"草":            {"草"},
		"我用GPT5写的，ＯＫ！": {"我用", "gpt5", "写的", "ok"},   // 不跨文字、不跨标点；全角转半角、英文小写
		"きょうは晴れ":       {"きょ", "ょう", "うは", "晴", "れ"}, // 假名一段、汉字一段（"晴"后面接"れ"是两种文字，各自成段）
		"？？":           nil,
	}
	for in, want := range cases {
		got := Bigrams(in)
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q：得到 %q，要 %q", in, got, want)
		}
	}
	if q := trigramQuery("吉他"); q != "" {
		t.Errorf("两个字 trigram 应该切不出来：%q", q)
	}
}

type fakeEmbed struct {
	calls int
	down  bool
}

func (f *fakeEmbed) Model() string { return "fake" }
func (f *fakeEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.down {
		return nil, embed.ErrUnavailable
	}
	f.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len([]rune(t))), 1}
	}
	return out, nil
}

func TestSidecarSync(t *testing.T) {
	t.Parallel()
	x, err := OpenIndex(context.Background(), filepath.Join(t.TempDir(), "memory-v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	items := []Item{
		{Ref: "f:1", Text: "主人最近在学吉他", Scopes: []string{"owner"}},
		{Ref: "f:2", Text: "群友下周搬家", Scopes: []string{"owner", "conv:qq:group:1"}},
	}
	if err := x.SyncText(items); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) (n int) { x.db.QueryRow(q, args...).Scan(&n); return }
	// 一组范围：每个范围一行
	if n := count(`SELECT count(*) FROM memory_bigram WHERE ref = 'f:2'`); n != 2 {
		t.Fatalf("两个范围应该两行：%d", n)
	}
	// 两字一组查得到"吉他"（trigram 查不到）
	if n := count(`SELECT count(*) FROM memory_bigram WHERE memory_bigram MATCH ?`, bigramQuery("吉他")); n != 1 {
		t.Fatalf("两字表查不到吉他：%d", n)
	}
	e := &fakeEmbed{}
	if n, err := x.SyncVectors(context.Background(), items, e, 64); err != nil || n != 2 {
		t.Fatalf("第一次该算 2 条：%d %v", n, err)
	}
	if n, _ := x.SyncVectors(context.Background(), items, e, 64); n != 0 {
		t.Fatalf("没变的不该重算：%d", n)
	}
	items[0].Text = "主人最近在学尤克里里"
	if n, _ := x.SyncVectors(context.Background(), items[:1], e, 64); n != 1 || count(`SELECT count(*) FROM memory_vectors`) != 1 {
		t.Fatalf("改了正文的重算、不在了的删掉：算了 %d，还剩 %d 行", n, count(`SELECT count(*) FROM memory_vectors`))
	}
	if v := x.vectors["f:1"]; len(v) != 2 || v[0] < 0.99 {
		t.Fatalf("存进去的要归一化：%v", v)
	}
	// 嵌入不可用：不报错，照用已有的
	if n, err := x.SyncVectors(context.Background(), append(items[:1], Item{Ref: "f:3", Text: "新的"}), &fakeEmbed{down: true}, 64); err != nil || n != 0 || len(x.vectors) != 1 {
		t.Fatalf("嵌入不可用时：%d %v %d", n, err, len(x.vectors))
	}
	if err := x.SyncAliases([]Person{{ID: "qq:1", Names: []string{"水哥", "草", " Nuke16 "}}}); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM aliases`); n != 2 {
		t.Fatalf("一个字的名字不收，别的收：%d", n)
	}
}

func TestSyncVectorsUnavailableKeepsOldRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, model, text string }{
		{"换模型", "编的旧模型", "主人最近在学吉他"},
		{"改正文", "fake", "主人以前在学口琴"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x, err := OpenIndex(context.Background(), filepath.Join(t.TempDir(), "memory-v2.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			old := []Item{{Ref: "f:1", Text: tc.text}}
			seed := &outboundFakeEmbed{model: tc.model, vector: func(string) []float32 { return []float32{0, 1} }}
			if n, err := x.SyncVectors(context.Background(), old, seed, 64); err != nil || n != 1 {
				t.Fatalf("准备旧向量：%d %v", n, err)
			}
			type row struct {
				model, prompt, hash string
				dim                 int
				vec                 []byte
			}
			read := func() row {
				t.Helper()
				var r row
				if err := x.db.QueryRow(`SELECT model, prompt_version, content_hash, dim, vec FROM memory_vectors WHERE ref = 'f:1'`).Scan(&r.model, &r.prompt, &r.hash, &r.dim, &r.vec); err != nil {
					t.Fatalf("旧向量丢了：%v", err)
				}
				return r
			}
			before := read()
			items := []Item{{Ref: "f:1", Text: "主人最近在学吉他"}}
			if n, err := x.SyncVectors(context.Background(), items, &fakeEmbed{down: true}, 64); err != nil || n != 0 {
				t.Fatalf("嵌入不可用应该保留待补齐：%d %v", n, err)
			}
			if after := read(); !reflect.DeepEqual(before, after) {
				t.Fatalf("重算失败改了旧向量：之前 %+v，之后 %+v", before, after)
			}
			if n, err := x.SyncVectors(context.Background(), items, &fakeEmbed{}, 64); err != nil || n != 1 {
				t.Fatalf("嵌入恢复应补齐一条：%d %v", n, err)
			}
			if after := read(); after.model != "fake" || after.hash != contentHash(items[0].Text) || reflect.DeepEqual(after.vec, before.vec) {
				t.Fatalf("嵌入恢复后没有替换旧向量：%+v", after)
			}
		})
	}
}

// 索引有表、却没有 memory-index 的版本行（没有 sumika_schema 或缺这一行）：打开时补写，版本是程序的 1。
func TestOpenIndexWritesMissingVersionRow(t *testing.T) {
	cases := []struct {
		name string
		drop string
	}{
		{"没有版本表", `DROP TABLE sumika_schema`},
		{"版本表缺 memory-index 行", `DELETE FROM sumika_schema`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "memory-v2.db")
			x, err := OpenIndex(context.Background(), file)
			if err != nil {
				t.Fatal(err)
			}
			x.Close()
			mustExec(t, file, tc.drop)

			x, err = OpenIndex(context.Background(), file)
			if err != nil {
				t.Fatal(err)
			}
			x.Close()
			version, minReader, by := schemaRow(t, file, "memory-index")
			if version != 1 || minReader != 1 || by == "" {
				t.Fatalf("版本行是 version=%d min_reader=%d migrated_by=%q，要 1/1/非空", version, minReader, by)
			}
		})
	}
}

// 版本行齐全且已是最新：再打开一次不重写版本表，主文件一个字节不变（不改 migrated_at）。
func TestOpenIndexKeepsCompleteVersionRow(t *testing.T) {
	file := filepath.Join(t.TempDir(), "memory-v2.db")
	x, err := OpenIndex(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	x.Close()
	version, minReader, by := schemaRow(t, file, "memory-index")
	if version != 1 || minReader != 1 || by == "" {
		t.Fatalf("版本行是 version=%d min_reader=%d migrated_by=%q，要 1/1/非空", version, minReader, by)
	}
	before := fileHash(t, file)
	x, err = OpenIndex(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	x.Close()
	if after := fileHash(t, file); after != before {
		t.Fatalf("版本行齐全时重写了版本表：主文件 %s → %s", before, after)
	}
}
