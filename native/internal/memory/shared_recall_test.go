package memory

// 共用考题（testdata/shared/memory-recall，格式见该目录 README.md）：全部虚构的记忆和题，Go 和 Bun 后台验收同一份。
// 这是考题不是回归测试：不要求全过，分数记在 baseline.json，退步到约定的幅度才红。
// 两个测试：
//   - TestSharedMemoryRecallData：考题数据自己一致（编号、场合、来源、范围、取代链、过期都对得上，不含长数字）。
//   - TestSharedMemoryRecall：用真实 Memory.Recall / CheckOutbound 跑每一题，输出命中情况和总分，和 baseline.json 比。
// 没有真模型：嵌入用 stubHashEmbed（见下），不连任何服务；守卫的小模型确认没有（Gen 为空，走字面 + 句向量相似度两层）。
// SUMIKA_SHARED_UPDATE=1 把这次的分数写回 baseline.json（提交前人读过 diff）。

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/sharedtest"
)

// ---------- 数据结构（字段名和 JSON 一一对应，sharedtest.Load 遇到不认识的字段会报错） ----------

type mrWorld struct {
	About    string                    `json:"about"`
	BaseTime string                    `json:"base_time"`
	Owner    struct{ ID, Name string } `json:"owner"`
	People   []struct {
		ID    string   `json:"id"`
		Names []string `json:"names"`
	} `json:"people"`
	Characters []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"characters"`
	Conversations []struct {
		ID        string `json:"id"`
		Place     string `json:"place"`
		Character string `json:"character"`
		About     string `json:"about"`
	} `json:"conversations"`
}

type mrSource struct {
	Speaker      string  `json:"speaker"`
	Place        string  `json:"place"`
	Conversation string  `json:"conversation"`
	Session      string  `json:"session"`
	SaidAgoDays  float64 `json:"said_ago_days"`
}

type mrMemory struct {
	ID             string   `json:"id"`
	Character      string   `json:"character"`
	Type           string   `json:"type"` // fact | episode
	Kind           string   `json:"kind"` // fact | preference | promise | boundary | episode
	Text           string   `json:"text"`
	Subject        string   `json:"subject"`
	Scope          string   `json:"scope"`  // owner | public | conv:<会话> | person:<人>
	Status         string   `json:"status"` // active | candidate | superseded
	SupersededBy   string   `json:"superseded_by"`
	Confirmed      string   `json:"confirmed"` // owner | owner_said | none
	Temporal       string   `json:"temporal"`  // stable | temporary
	ValidUntilDays *float64 `json:"valid_until_days"`
	Source         mrSource `json:"source"`
}

type mrRecall struct {
	Expect   []string `json:"expect"`
	Ordered  bool     `json:"ordered"`
	Forbid   []string `json:"forbid"`
	Hearsay  []string `json:"hearsay"`
	Previous []string `json:"previous"`
	Empty    bool     `json:"empty"`
}

type mrDisclosure struct {
	Draft   string   `json:"draft"`
	Blocked []string `json:"blocked"`
}

type mrQuestion struct {
	ID           string        `json:"id"`
	Type         string        `json:"type"`
	Character    string        `json:"character"`
	Q            string        `json:"q"`
	Place        string        `json:"place"`
	Asker        string        `json:"asker"`
	Conversation string        `json:"conversation"`
	Recall       mrRecall      `json:"recall"`
	Disclosure   *mrDisclosure `json:"disclosure"`
}

var mrTypes = []string{"paraphrase", "time", "supersede", "visibility", "hearsay", "expired", "schedule", "no_answer", "isolation"}

// mrTopK：线上 memory_search 带的 TopK（规格-记忆召回不分场合：明确查记忆最多 12 条）
const mrTopK = 12

// mrStubName 是基线里记的嵌入替身名；换了替身（维度、算法）要换名字并重录基线
const mrStubName = "stub-hash-uni-bigram-256"

type mrExam struct {
	world     mrWorld
	base      time.Time
	memories  []mrMemory
	questions []mrQuestion
}

func mrLoad(t testing.TB) *mrExam {
	t.Helper()
	dir := filepath.Join(sharedtest.Root(t), "memory-recall")
	var ex mrExam
	sharedtest.Load(t, filepath.Join(dir, "world.json"), &ex.world)
	var mf struct {
		Memories []mrMemory `json:"memories"`
	}
	sharedtest.Load(t, filepath.Join(dir, "memories.json"), &mf)
	var qf struct {
		Questions []mrQuestion `json:"questions"`
	}
	sharedtest.Load(t, filepath.Join(dir, "questions.json"), &qf)
	ex.memories, ex.questions = mf.Memories, qf.Questions
	base, err := time.Parse(time.RFC3339, ex.world.BaseTime)
	if err != nil {
		t.Fatalf("base_time：%v", err)
	}
	ex.base = base
	return &ex
}

// ---------- 数据一致性 ----------

func TestSharedMemoryRecallData(t *testing.T) {
	t.Parallel()
	ex := mrLoad(t)
	w := ex.world
	convPlace, convChar := map[string]string{}, map[string]string{}
	for _, c := range w.Conversations {
		if _, dup := convPlace[c.ID]; dup {
			t.Errorf("会话 %s 重复", c.ID)
		}
		convPlace[c.ID], convChar[c.ID] = c.Place, c.Character
		if !strings.HasPrefix(c.ID, map[string]string{"local_private": "console:", "local_group": "console:", "qq_private": "qq:private:", "qq_group": "qq:group:"}[c.Place]) || c.Place == "" {
			t.Errorf("会话 %s 的场合 %q 对不上 id 的写法", c.ID, c.Place)
		}
	}
	people := map[string]bool{"owner": true}
	for _, p := range w.People {
		people[p.ID] = true
	}
	chars := map[string]bool{}
	for _, c := range w.Characters {
		chars[c.ID] = true
	}
	digits := regexp.MustCompile(`\d{5,}`)
	mem := map[string]mrMemory{}
	for _, m := range ex.memories {
		if _, dup := mem[m.ID]; dup {
			t.Errorf("记忆 %s 重复", m.ID)
		}
		mem[m.ID] = m
	}
	for _, m := range ex.memories {
		bad := func(f string, a ...any) { t.Errorf("记忆 %s：%s", m.ID, fmt.Sprintf(f, a...)) }
		if !chars[m.Character] {
			bad("角色 %q 不存在", m.Character)
		}
		if strings.TrimSpace(m.Text) == "" || digits.MatchString(m.Text) {
			bad("文字为空或含 5 位以上的数字串")
		}
		if !slices.Contains([]string{"active", "candidate", "superseded"}, m.Status) {
			bad("status %q", m.Status)
		}
		s := m.Source
		if s.Speaker != "" && !people[s.Speaker] {
			bad("说话人 %q 不在人物表", s.Speaker)
		}
		if p, ok := convPlace[s.Conversation]; !ok || p != s.Place {
			bad("来源会话 %q 的场合是 %q，来源写的是 %q", s.Conversation, p, s.Place)
		}
		if strings.HasPrefix(s.Conversation, "console:") && convChar[s.Conversation] != m.Character {
			bad("本地会话 %s 属于 %q，不是 %q", s.Conversation, convChar[s.Conversation], m.Character)
		}
		if (s.Place == "local_private") != (s.Session != "") {
			bad("本地私聊要写 session，其他场合不写")
		}
		if s.SaidAgoDays < 0 {
			bad("said_ago_days 不能是负的")
		}
		switch {
		case m.Scope == "owner" || m.Scope == "public":
		case strings.HasPrefix(m.Scope, "conv:"):
			if _, ok := convPlace[strings.TrimPrefix(m.Scope, "conv:")]; !ok {
				bad("范围里的会话不存在：%s", m.Scope)
			}
		case strings.HasPrefix(m.Scope, "person:"):
			if !people[strings.TrimPrefix(m.Scope, "person:")] {
				bad("范围里的人不存在：%s", m.Scope)
			}
		default:
			bad("范围 %q 写法不对", m.Scope)
		}
		if m.Type == "episode" {
			if m.Kind != "episode" || m.Subject != "" || m.Confirmed != "" || m.Temporal != "" || m.ValidUntilDays != nil || m.Status != "active" || m.SupersededBy != "" {
				bad("片段只写 kind=episode、范围、状态 active、来源")
			}
			continue
		}
		if m.Type != "fact" || !slices.Contains([]string{"fact", "preference", "promise", "boundary"}, m.Kind) {
			bad("type/kind %q/%q", m.Type, m.Kind)
		}
		if m.Subject != "" && !people[m.Subject] {
			bad("主体 %q 不在人物表", m.Subject)
		}
		if !slices.Contains([]string{"owner", "owner_said", "none"}, m.Confirmed) {
			bad("confirmed %q", m.Confirmed)
		}
		if m.Status == "candidate" && m.Confirmed != "none" {
			bad("待确认的不能标已确认")
		}
		if !slices.Contains([]string{"stable", "temporary"}, m.Temporal) {
			bad("temporal %q", m.Temporal)
		}
		if m.ValidUntilDays != nil && m.Temporal != "temporary" {
			bad("valid_until_days 只配 temporary")
		}
		if (m.Status == "superseded") != (m.SupersededBy != "") {
			bad("superseded 状态和 superseded_by 要一起出现")
		}
		if m.SupersededBy != "" {
			n, ok := mem[m.SupersededBy]
			if !ok || n.Character != m.Character || n.Subject != m.Subject || n.Status != "active" || n.Scope != m.Scope {
				bad("取代它的 %s 必须是同角色、同主体、同范围的有效事实", m.SupersededBy)
			}
		}
	}

	seen := map[string]bool{}
	types := map[string]int{}
	for _, q := range ex.questions {
		bad := func(f string, a ...any) { t.Errorf("题 %s：%s", q.ID, fmt.Sprintf(f, a...)) }
		if seen[q.ID] {
			bad("编号重复")
		}
		seen[q.ID] = true
		types[q.Type]++
		if !slices.Contains(mrTypes, q.Type) {
			bad("题型 %q 不认识", q.Type)
		}
		if !chars[q.Character] || strings.TrimSpace(q.Q) == "" {
			bad("角色或问题为空")
		}
		if p, ok := convPlace[q.Conversation]; !ok || p != q.Place {
			bad("会话 %q 的场合是 %q，题里写的是 %q", q.Conversation, p, q.Place)
		}
		if strings.HasPrefix(q.Conversation, "console:") && convChar[q.Conversation] != q.Character {
			bad("本地会话属于 %q，不是 %q", convChar[q.Conversation], q.Character)
		}
		switch {
		case q.Asker == "owner":
		case !people[q.Asker]:
			bad("提问人 %q 不在人物表", q.Asker)
		case strings.HasPrefix(q.Place, "local_"):
			bad("本地场合只有主人提问")
		case q.Place == "qq_private" && q.Conversation != "qq:private:u-"+strings.TrimPrefix(q.Asker, "qq:u-"):
			bad("QQ 私聊的对方就是提问人，会话 %s 对不上 %s", q.Conversation, q.Asker)
		}
		r := q.Recall
		for _, id := range append(append(append(append([]string{}, r.Expect...), r.Hearsay...), r.Previous...), r.Forbid...) {
			if _, ok := mem[id]; !ok {
				bad("引用的记忆 %s 不存在", id)
			}
		}
		for _, id := range append(append(append([]string{}, r.Expect...), r.Hearsay...), r.Previous...) {
			if m, ok := mem[id]; ok && m.Character != q.Character {
				bad("期望里的 %s 属于角色 %s，不是 %s", id, m.Character, q.Character)
			}
		}
		for _, id := range append(append([]string{}, r.Expect...), r.Hearsay...) {
			m := mem[id]
			expired := m.ValidUntilDays != nil && *m.ValidUntilDays < 0 || m.Temporal == "temporary" && m.ValidUntilDays == nil && m.Source.SaidAgoDays > TempDays
			if m.Status == "superseded" || expired {
				bad("期望召回的 %s 已过期或被取代", id)
			}
			if slices.Contains(r.Forbid, id) {
				bad("%s 既期望又禁止", id)
			}
		}
		for _, id := range r.Hearsay {
			if mem[id].Status != "candidate" || slices.Contains(r.Expect, id) {
				bad("hearsay 里的 %s 必须是待确认的，且不再写进 expect", id)
			}
		}
		for _, id := range r.Previous {
			if mem[id].Status != "superseded" {
				bad("previous 里的 %s 必须是被取代的旧版本", id)
			}
		}
		if r.Empty && (len(r.Expect)+len(r.Hearsay)+len(r.Previous) > 0) {
			bad("empty 的题不能有期望")
		}
		if r.Ordered && len(r.Expect) < 2 {
			bad("ordered 至少两条")
		}
		if q.Type == "no_answer" && !r.Empty {
			bad("没有答案的题要写 empty")
		}
		if q.Type == "hearsay" && len(r.Hearsay) == 0 {
			bad("传闻题要写 hearsay")
		}
		if q.Type == "expired" || q.Type == "supersede" {
			for _, id := range r.Forbid {
				m := mem[id]
				gone := m.Status == "superseded" || m.ValidUntilDays != nil && *m.ValidUntilDays < 0 || m.Temporal == "temporary" && m.ValidUntilDays == nil && m.Source.SaidAgoDays > TempDays
				if !gone {
					bad("禁止项 %s 既没过期也没被取代", id)
				}
			}
		}
		if (q.Type == "visibility") != (q.Disclosure != nil) {
			bad("只有场合可见性的题带 disclosure")
		}
		if d := q.Disclosure; d != nil {
			if strings.TrimSpace(d.Draft) == "" || digits.MatchString(d.Draft) {
				bad("draft 为空或含长数字串")
			}
			for _, id := range d.Blocked {
				if _, ok := mem[id]; !ok {
					bad("blocked 里的 %s 不存在", id)
				}
			}
		}
		if digits.MatchString(q.Q) {
			bad("问题里有 5 位以上的数字串")
		}
	}
	for _, ty := range mrTypes {
		if types[ty] == 0 {
			t.Errorf("缺题型 %s", ty)
		}
	}
	if len(ex.questions) < 50 {
		t.Errorf("题数 %d，少于 50", len(ex.questions))
	}
}

// ---------- 嵌入替身 ----------

// stubHashEmbed 是确定性的嵌入替身：每个汉字 / 字母和相邻两字各哈希进 256 维的袋子再归一化。
// 只认字面重叠，不懂同义（"上班"和"工作"毫无关系）；真实模型（gte-multilingual 一类）懂语义，所以换说法、按时间、
// 传闻这几类题在真实模型上应该比这里高，这里的分数是下限。它也让向量那一路在余弦 0.6 的底线上几乎不出力，召回主要靠全文那一路。
type stubHashEmbed struct{}

func (stubHashEmbed) Model() string { return mrStubName }
func (stubHashEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, 256)
		rs := []rune(outboundNormalize(text))
		add := func(s string) {
			h := fnv.New32a()
			h.Write([]byte(s))
			v[h.Sum32()%256]++
		}
		for j, r := range rs {
			add(string(r))
			if j+1 < len(rs) {
				add(string(rs[j : j+2]))
			}
		}
		var n float64
		for _, x := range v {
			n += float64(x) * float64(x)
		}
		if n > 0 {
			for j := range v {
				v[j] = float32(float64(v[j]) / math.Sqrt(n))
			}
		}
		out[i] = v
	}
	return out, nil
}

// ---------- 建库 ----------

// mrBuildDB 照 Bun 版 memory.db 的表（读取要用的列 + 取代链和确认列）把一个角色的虚构记忆写进临时库
func mrBuildDB(t *testing.T, ex *mrExam, character string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建共用池召回要读的几列（多两个确认/取代列），不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER, confirmed_by TEXT, superseded_by TEXT)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	day := float64(24 * time.Hour / time.Millisecond)
	for _, m := range ex.memories {
		if m.Character != character {
			continue
		}
		said := ex.base.UnixMilli() - int64(m.Source.SaidAgoDays*day)
		if m.Type == "episode" {
			_, err = db.Exec(`INSERT INTO episodes VALUES (?, ?, ?, ?, ?, ?, ?)`, m.ID, m.Text, m.Scope, m.Source.Conversation, said, said, m.Status)
		} else {
			var validTo any
			if m.ValidUntilDays != nil {
				validTo = ex.base.UnixMilli() + int64(*m.ValidUntilDays*day)
			}
			confirmed := m.Confirmed
			if confirmed == "none" {
				confirmed = ""
			}
			var by any
			if m.SupersededBy != "" {
				by = m.SupersededBy
			}
			_, err = db.Exec(`INSERT INTO facts VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.Kind, m.Status, m.Text, m.Subject, m.Scope, m.Source.Conversation, said, said, m.Temporal, validTo, confirmed, by)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return file
}

// mrAudience 把（场合、提问人、会话）变成记忆看到的听众，口径同渠道中间件：
// 本地两种场合都是主人在主窗口（PrivateOK、OwnerAsked）；QQ 私聊主人是 PrivateOK；QQ 里只有主人本人问时 OwnerAsked；
// 别人只带自己的 PersonIDs
func mrAudience(q mrQuestion) Audience {
	a := Audience{Conversation: q.Conversation, PersonIDs: []string{q.Asker}}
	switch q.Place {
	case "local_private":
		a.Channel, a.Scene, a.PrivateOK, a.OwnerAsked = "console", "private", true, true
	case "local_group":
		a.Channel, a.Scene, a.PrivateOK, a.OwnerAsked = "console", "group", true, true
	case "qq_private":
		a.Channel, a.Scene = "qq", "private"
		a.PrivateOK, a.OwnerAsked = q.Asker == "owner", q.Asker == "owner"
	case "qq_group":
		a.Channel, a.Scene = "qq", "group"
		a.OwnerAsked = q.Asker == "owner"
	}
	return a
}

// ---------- 跑题 ----------

type mrResult struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	Pass bool     `json:"pass"`
	Hits []string `json:"hits"` // 给出的条目（前 TopK，去掉 f:/e: 前缀）
	Why  []string `json:"why,omitempty"`
}

type mrCount struct {
	Pass int `json:"pass"`
	N    int `json:"n"`
}

type mrBaseline struct {
	Embedder  string             `json:"embedder"`
	TopK      int                `json:"topk"`
	Total     mrCount            `json:"total"`
	ByType    map[string]mrCount `json:"by_type"`
	Questions map[string]bool    `json:"questions"`
}

func mrID(ref string) string { return strings.TrimPrefix(strings.TrimPrefix(ref, "f:"), "e:") }

func mrRun(t *testing.T, ex *mrExam) []mrResult {
	t.Helper()
	var people []Person
	for _, p := range ex.world.People {
		people = append(people, Person{ID: p.ID, Names: p.Names})
	}
	ctx := context.Background()
	by := map[string]*Memory{}
	for _, c := range ex.world.Characters {
		m, err := Open(ctx, mrBuildDB(t, ex, c.ID), stubHashEmbed{}, people, Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		by[c.ID] = m
	}
	var out []mrResult
	for _, q := range ex.questions {
		m := by[q.Character]
		aud := mrAudience(q)
		r := m.Recall(ctx, aud, Query{Text: q.Q, Mode: ModeExplicit, TopK: mrTopK, Now: ex.base})
		res := mrResult{ID: q.ID, Type: q.Type, Hits: []string{}}
		hit := map[string]Hit{}
		pos := map[string]int{}
		for i, h := range r.Hits {
			id := mrID(h.Ref)
			res.Hits = append(res.Hits, id)
			hit[id], pos[id] = h, i
		}
		fail := func(f string, a ...any) { res.Why = append(res.Why, fmt.Sprintf(f, a...)) }
		rc := q.Recall
		for _, id := range rc.Expect {
			h, ok := hit[id]
			if !ok {
				fail("没召回 %s", id)
			} else if h.Hearsay {
				fail("%s 是确认过的事实，却被标成听说", id)
			}
		}
		for _, id := range rc.Hearsay {
			h, ok := hit[id]
			if !ok {
				fail("没召回传闻 %s", id)
			} else if !h.Hearsay {
				fail("传闻 %s 被当成事实给出", id)
			}
		}
		if rc.Ordered {
			last := -1
			for _, id := range rc.Expect {
				if p, ok := pos[id]; ok {
					if p < last {
						fail("顺序不对：%s 应在前面的之后", id)
					}
					last = p
				}
			}
		}
		for _, id := range rc.Previous {
			found := false
			for _, e := range append(append([]string{}, rc.Expect...), rc.Hearsay...) {
				for _, p := range hit[e].Previous {
					found = found || mrID(p.Ref) == id
				}
			}
			if !found {
				fail("新值没带出旧版本 %s", id)
			}
		}
		for _, id := range rc.Forbid {
			if _, ok := hit[id]; ok {
				fail("不该召回 %s", id)
			}
		}
		if rc.Empty && len(r.Hits) > 0 {
			fail("该召回为空，实际给了 %d 条", len(r.Hits))
		}
		if d := q.Disclosure; d != nil {
			blocked, err := m.CheckOutbound(ctx, d.Draft, aud)
			if err != nil {
				t.Fatalf("%s：守卫出错：%v", q.ID, err)
			}
			got := []string{}
			for _, ref := range blocked {
				got = append(got, mrID(ref))
			}
			sort.Strings(got)
			want := append([]string{}, d.Blocked...)
			sort.Strings(want)
			if !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
				fail("守卫拦下 %v，应该是 %v", got, want)
			}
		}
		res.Pass = len(res.Why) == 0
		out = append(out, res)
	}
	return out
}

func mrScore(ex *mrExam, rs []mrResult) mrBaseline {
	b := mrBaseline{Embedder: mrStubName, TopK: mrTopK, ByType: map[string]mrCount{}, Questions: map[string]bool{}}
	for _, r := range rs {
		c := b.ByType[r.Type]
		c.N++
		b.Total.N++
		if r.Pass {
			c.Pass++
			b.Total.Pass++
		}
		b.ByType[r.Type] = c
		b.Questions[r.ID] = r.Pass
	}
	return b
}

// 退步的口径（README 同）：总通过题数比基线少 3 题以上；任何一个题型比基线少 2 题以上；场合可见性（visibility）少 1 题就算
const (
	mrTotalDrop = 3
	mrTypeDrop  = 2
)

func TestSharedMemoryRecall(t *testing.T) {
	t.Parallel()
	ex := mrLoad(t)
	results := mrRun(t, ex)
	got := mrScore(ex, results)

	var sb strings.Builder
	fmt.Fprintf(&sb, "共用考题：嵌入替身 %s，TopK %d，总分 %d/%d\n", got.Embedder, got.TopK, got.Total.Pass, got.Total.N)
	for _, ty := range mrTypes {
		c := got.ByType[ty]
		fmt.Fprintf(&sb, "  %-10s %2d/%2d\n", ty, c.Pass, c.N)
	}
	for _, r := range results {
		if !r.Pass {
			fmt.Fprintf(&sb, "  未过 %s（%s）给了 %v：%s\n", r.ID, r.Type, r.Hits, strings.Join(r.Why, "；"))
		}
	}
	t.Log("\n" + sb.String())
	if p := os.Getenv("SUMIKA_RECALL_REPORT"); p != "" {
		if err := os.WriteFile(p, sharedtest.Marshal(results), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(sharedtest.Root(t), "memory-recall", "baseline.json")
	if sharedtest.Update() {
		sharedtest.Save(t, path, got)
		return
	}
	var want mrBaseline
	sharedtest.Load(t, path, &want)
	if want.Embedder != got.Embedder || want.TopK != got.TopK {
		t.Fatalf("基线是 %s / TopK %d 录的，现在是 %s / TopK %d：换了替身或参数要重录（SUMIKA_SHARED_UPDATE=1）", want.Embedder, want.TopK, got.Embedder, got.TopK)
	}
	if len(want.Questions) != len(got.Questions) {
		t.Fatalf("基线有 %d 题，考题有 %d 题：改了考题要重录基线", len(want.Questions), len(got.Questions))
	}
	for id := range got.Questions {
		if _, ok := want.Questions[id]; !ok {
			t.Fatalf("基线里没有题 %s：改了考题要重录基线", id)
		}
	}
	if want.Total.Pass-got.Total.Pass >= mrTotalDrop {
		t.Errorf("总分退步：基线 %d/%d，现在 %d/%d（少 %d 题，约定少 %d 题就算退步）", want.Total.Pass, want.Total.N, got.Total.Pass, got.Total.N, want.Total.Pass-got.Total.Pass, mrTotalDrop)
	}
	for _, ty := range mrTypes {
		drop := want.ByType[ty].Pass - got.ByType[ty].Pass
		limit := mrTypeDrop
		if ty == "visibility" {
			limit = 1
		}
		if drop >= limit {
			t.Errorf("题型 %s 退步：基线 %d/%d，现在 %d/%d", ty, want.ByType[ty].Pass, want.ByType[ty].N, got.ByType[ty].Pass, got.ByType[ty].N)
		}
	}
	// 变好、变差的题单列出来，方便决定要不要重录基线
	var up, down []string
	for id, pass := range got.Questions {
		switch {
		case pass && !want.Questions[id]:
			up = append(up, id)
		case !pass && want.Questions[id]:
			down = append(down, id)
		}
	}
	sort.Strings(up)
	sort.Strings(down)
	if len(up)+len(down) > 0 {
		t.Logf("和基线比：新过 %v，新挂 %v", up, down)
	}
}
