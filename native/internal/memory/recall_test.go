package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 照 Bun 版 memory.db 的表（只建 loadItems 读到的列）造一个临时库
func bunDb(t *testing.T, facts [][]string, episodes [][]string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建召回要读的几列，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	// facts：id, kind, status, text, subject, scope
	for _, f := range facts {
		db.Exec(`INSERT INTO facts VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, NULL, NULL)`, f[0], f[1], f[2], f[3], f[4], f[5], now, now)
	}
	// episodes：id, text, scope
	for _, e := range episodes {
		db.Exec(`INSERT INTO episodes VALUES (?, ?, ?, '', ?, ?, 'active')`, e[0], e[1], e[2], now, now)
	}
	return file
}

// textEmbed：按"关键词"给向量，同一个关键词的句子方向一样；没有关键词的给一个和谁都不太像的方向
type textEmbed struct{}

func (textEmbed) Model() string { return "fake" }
func (textEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	keys := []string{"吉他", "搬家", "考试", "猫"}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(keys)+1)
		v[len(keys)] = 0.3
		for k, key := range keys {
			if strings.Contains(t, key) {
				v[k] = 1
			}
		}
		out[i] = v
	}
	return out, nil
}

const GA, GB = "qq:group:30001", "qq:group:30002"

var ownerHere = Audience{Channel: "console", Conversation: "console:a", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
var inGroup = func(g string) Audience { return Audience{Channel: "qq", Conversation: g, Scene: "group"} }

func refs(hs []Hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Ref)
	}
	return out
}

func TestGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		q     Query
		alias bool
		want  bool
	}{
		{Query{Text: "草"}, false, false},
		{Query{Text: "[图片]？"}, false, false},
		{Query{Text: "？", Addressed: true}, false, true}, // 回她一个"？"要接得上
		{Query{Text: "上次那个"}, false, true},
		{Query{Text: "水哥"}, true, true}, // 只有两个字，但对上了人
		{Query{Text: "吉他", Mode: ModeExplicit}, false, true},
		{Query{Text: "最近怎么样"}, false, true},
	}
	for _, c := range cases {
		if got, why := Gate(c.q, c.alias); got != c.want {
			t.Errorf("%+v：得到 %v（%s）", c.q, got, why)
		}
	}
}

func TestRecall(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "主人最近在学吉他，每天练半小时", "owner", "owner"},
		{"2", "fact", "candidate", "群友小A说下周搬家", "qq:60001", "conv:" + GA},
		{"3", "fact", "candidate", "群友小A说搬家后想养猫", "qq:60001", "conv:" + GA},
		{"4", "fact", "active", "群友在群B说他家的猫很胖", "qq:60002", "conv:" + GB},
		{"5", "promise", "active", "主人考试前提醒他早睡", "owner", "owner"},
		{"6", "fact", "revoked", "撤销的吉他事实", "owner", "owner"},
	}, [][]string{
		{"7", "群里大家在发表情包，只是闲聊", "conv:" + GA},
	})
	people := []Person{{ID: "qq:60001", Names: []string{"小A同学"}}}
	m, err := Open(context.Background(), file, textEmbed{}, people, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()

	// 一两个字的寒暄：不查，固定要点照给
	r := m.Recall(ctx, inGroup(GA), Query{Text: "草"})
	if !r.Gated || len(r.Hits) != 0 {
		t.Fatalf("寒暄不该查：%+v", r.Hits)
	}
	// 待确认参与召回、标"听说"、每轮最多 1 条（Bun 版完全不给待确认）
	r = m.Recall(ctx, inGroup(GA), Query{Text: "小A搬家的事怎么样了", Mode: ModeExplicit})
	n := 0
	for _, h := range r.Hits {
		if h.Status == "candidate" {
			n++
			if !h.Hearsay {
				t.Fatal("待确认没标听说")
			}
		}
	}
	if n != 1 {
		t.Fatalf("待确认应该正好 1 条：%v", refs(r.Hits))
	}
	// 群 A 的待确认也能在群 B 想起；仍只注入一条并带听说。
	if hs := m.Recall(ctx, inGroup(GB), Query{Text: "搬家的事", Mode: ModeExplicit}).Hits; len(hs) == 0 || !hs[0].Hearsay {
		t.Fatalf("跨群候选缺失或未标听说：%v", hs)
	}
	for _, h := range m.Recall(ctx, ownerHere, Query{Text: "吉他练得怎么样", Mode: ModeExplicit}).Candidates {
		if h.Ref == "f:6" {
			t.Fatal("撤销的出现了")
		}
	}
	// 两个字的"吉他"：trigram 对不上，两字切分对得上
	if hs := m.Recall(ctx, ownerHere, Query{Text: "吉他", Mode: ModeExplicit}).Hits; len(hs) == 0 || hs[0].Ref != "f:1" {
		t.Fatalf("两个字查不到：%v", refs(hs))
	}
	// 向量过底线：只和"猫"有关的问吉他，猫那条不能靠向量凑进来
	for _, h := range m.Recall(ctx, ownerHere, Query{Text: "吉他练得怎么样", Mode: ModeExplicit}).Candidates {
		if h.Ref == "f:4" {
			t.Fatalf("不够像的向量命中被收了：sim=%.2f", h.Sim)
		}
	}
	// 这一段给过的不再给
	if hs := m.Recall(ctx, ownerHere, Query{Text: "吉他", Mode: ModeExplicit, Given: map[string]bool{"f:1": true}}).Hits; len(hs) > 0 && hs[0].Ref == "f:1" {
		t.Fatal("给过的又给了")
	}
	// 别名：只说了名字（两个字以上）也放行，带出他的事
	if hs := m.Recall(ctx, inGroup(GA), Query{Text: "小A同学"}); hs.Gated || len(hs.Hits) == 0 || !hs.Hits[0].Alias {
		t.Fatalf("别名命中没带出他的事：%+v", refs(hs.Hits))
	}
	// 固定要点不再按场合过滤。
	if p := m.Recall(ctx, ownerHere, Query{Text: "草"}).Pinned; len(p) != 1 || p[0].Ref != "f:5" {
		t.Fatalf("固定要点不对：%v", refs(p))
	}
	if p := m.Recall(ctx, inGroup(GA), Query{Text: "草"}).Pinned; len(p) != 1 || p[0].Ref != "f:5" {
		t.Fatalf("群里缺固定要点：%v", refs(p))
	}
}

func TestBudgetByPlace(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		a    Audience
		want int
	}{
		"本地私聊":      {ownerHere, 1200},
		"本地群聊":      {Audience{Channel: "console", Conversation: "console:group:1", Scene: "group", PrivateOK: true}, 800},
		"主人的 QQ 私聊": {Audience{Channel: "qq", Conversation: "qq:private:1:2", Scene: "private", PrivateOK: true}, 1200},
		"别人的 QQ 私聊": {Audience{Channel: "qq", Conversation: "qq:private:1:3", Scene: "private"}, 600},
		"QQ 群":      {inGroup(GA), 400},
	}
	for name, c := range cases {
		if got := Budget(c.a); got != c.want {
			t.Errorf("%s：%d", name, got)
		}
	}
}

// 英文、数字的别名要整词对上（真实群消息里 "ro""te""seek" 被 pro、test、deepseek 撞上过）；中文的按包含算
func TestAliasWholeWord(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{{"1", "fact", "active", "群友 ro 最近在学吉他", "qq:1", "conv:" + GA}}, nil)
	m, err := Open(context.Background(), file, nil, []Person{{ID: "qq:1", Names: []string{"ro", "seek", "小可乐"}}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	cases := map[string]bool{
		"deepseek 真强": false,
		"pro max 好用吗": false,
		"test 一下":     false,
		"@ro 在吗":      true,
		"ro，你来":       true,
		"问问seek":      true, // 前面是汉字，不算紧挨着字母
		"小可乐今天来了吗":    true,
		"可乐":          false, // 中文别名本身两个字以上，"可乐"不是"小可乐"
	}
	for text, want := range cases {
		if got := len(m.aliasPeople(text)) > 0; got != want {
			t.Errorf("%q：得到 %v，要 %v", text, got, want)
		}
	}
}

// 同分向量不能因 map 遍历次序换名次，否则近重复去重会让主人和群友两问保留不同条目。
func TestVectorTiesKeepRecallOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "群里那只猫", "qq:60002", "public"},
		{"2", "fact", "active", "主人养的猫", "owner", "owner"},
	}, nil)
	m, err := Open(ctx, file, textEmbed{}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	q := Query{Text: "猫", Mode: ModeExplicit, TopK: 2, Now: time.Now()}
	for i := range 32 {
		for _, a := range []Audience{qqGroup(GA, true, "owner"), qqGroup(GA, false, peerA)} {
			got := m.Recall(ctx, a, q)
			if order := strings.Join(refs(got.Hits), ","); order != "f:1,f:2" {
				t.Fatalf("第 %d 次、owner=%v，同分名次漂移：%s", i, a.OwnerAsked, order)
			}
		}
	}
}
