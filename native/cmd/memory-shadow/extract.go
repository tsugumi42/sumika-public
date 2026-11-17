package main

// 从副本里读这次整理新写了什么（报告 B～G 节的原料）。只读：用只读连接开副本的记忆库，原话回查走副本的聊天库。
//
// 读的是 W2、W3 的存法：
//   - W2：整理想取代谁，记在新候选那条 add 修订的 after_json 里（wants_supersede: <旧编号>，提升时才定，write.go 的 insertFact）；
//     取代、撤销只动 confirmed_by = 'ai' 的有效事实，别的一律受保护（protectedFromAI）；
//   - W3：被 AI 提升的事实 confirmed_by = 'ai'，提升那一刻有一条 op = 'confirm' 的修订（write_confirm.go）；
//     would_confirm、confirm_blocked_protected、supersede_deferred 这些只有计数，在 runResult.Counts 里，报告 D、C 节直接按键名取。
//     "这次运行里提升的"靠修订编号认：跑之前记下 fact_revisions 的最大编号，之后更大的 confirm 修订才算（启动补提升提升的是导入的旧候选，不是新写的）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/memory"
)

// existingIDs 记下跑之前副本里已经有的事实和片段的编号：跑完以后"新写的"就是不在这里的
type existingSet struct {
	facts, episodes map[string]bool
	maxRev          int64 // 跑之前 fact_revisions 的最大编号：更大的修订是这次运行写的
	// protected：跑之前有效、confirmed_by 不是 ai 的事实（W2 说整理动不了它们），编号 → 当时的 confirmed_by。跑完逐条对：状态或 confirmed_by 变了就是被动了
	protected map[string]string
}

func existingIDs(memFile string) (existingSet, error) {
	db, err := sql.Open("sqlite", sqliteDSN(memFile, true))
	if err != nil {
		return existingSet{}, err
	}
	defer db.Close()
	out := existingSet{facts: map[string]bool{}, episodes: map[string]bool{}, protected: map[string]string{}}
	for table, set := range map[string]map[string]bool{"facts": out.facts, "episodes": out.episodes} {
		rows, err := db.Query(`SELECT id FROM ` + table)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return out, err
			}
			set[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	if err := db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM fact_revisions`).Scan(&out.maxRev); err != nil {
		return out, err
	}
	rows, err := db.Query(`SELECT id, coalesce(confirmed_by, '') FROM facts WHERE status = 'active' AND coalesce(confirmed_by, '') <> 'ai'`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, by string
		if err := rows.Scan(&id, &by); err != nil {
			return out, err
		}
		out.protected[id] = by
	}
	return out, rows.Err()
}

// evRow 是一次证据
type evRow struct {
	Day, Conv string
	Seq       int64
	Speaker   string
	BySubject bool
	Ord       int    // 说话人是这条事实的第几个不同的说话人，1 起
	Quote     string // 回查到的原话，最多 120 字；回查不到是空
	Missing   bool   // 回查不到（消息不在了）
}

type factRow struct {
	ID, Ref                                              string
	Kind, Text, Subject, Status, ConfirmedBy             string
	AttributedTo, Conversation, Origin, AudienceAtOrigin string
	Scopes                                               []string
	SourceSeqs                                           []int64
	CreatedAt                                            int64
	Evidence                                             []evRow
	SupersededBy                                         string // 被哪条取代了（status 是 superseded 时）
	AddBy, AddReason                                     string
	WantsSupersede                                       string // W2：想取代的旧事实编号（add 修订 after_json 里的 wants_supersede）
	WantsTarget                                          string // 想取代的那条现在是什么情况：wantsAI / wantsProtected / wantsGone / wantsMissing，没有想取代的是空
	Confirmed                                            bool   // W3：这次运行里提升成有效的（confirm 修订比跑之前的最大编号大），不论是新写的还是导入的旧候选
	Revisions                                            []string
	New                                                  bool // 这次整理新写的
}

// 想取代的旧事实现在是什么情况（报告 D 节写在"旧 → 新"那一行后面）
const (
	wantsAI        = "ai"        // 旧事实是 AI 自己提升的（confirmed_by = ai）：提升时可以被换
	wantsProtected = "protected" // 旧事实有效但 AI 动不了（导入的、你放行过的）：提升这条候选会被挡下
	wantsReplaced  = "replaced"  // 旧事实是 AI 自己提升的，已经被这条换掉了
	wantsGone      = "gone"      // 旧事实已经不是有效的了：提升时照常提升，不取代任何东西
	wantsMissing   = "missing"   // 库里找不到这个编号（模型编的，或已被清掉）
)

type epRow struct {
	ID, Ref, Conv            string
	From, To                 int64
	Text                     string
	Scopes                   []string
	Origin, AudienceAtOrigin string
	CreatedAt                int64
	New                      bool
}

// protectedStat 是"受保护的有效事实"的一格：来源 × 主体（W2 之后 AI 动不了它们）
type protectedStat struct{ Source, Subject string }

type extracted struct {
	Facts    []factRow // 副本里全部事实（New 标出这次新写的）
	Episodes []epRow
	// 受保护的有效事实：status = active 且 confirmed_by 不是 ai（规格 W2 的白名单），按来源 × 主体计数
	Protected map[protectedStat]int
	// ProtectedChanged：跑之前有效、受保护的事实，跑完状态或 confirmed_by 变了的编号。W2 的保证是整理动不了它们，所以必须是空的
	ProtectedChanged []string
	// 导入的待确认：共几条、其中几条在"最近 20 条、主人私聊看得见的"那一块里（W3a 的候选块 memory.candidateFacts 的口径：看得见的、新的在前、取前 20 条）
	ImportedCandidates, ImportedInTop20 int
	Duplicates                          [][2]string // 同一主体同时有效的相似事实对（两个编号）
	SimilarCandidates                   int         // 待确认里的相似对数（现在整理写的全是待确认，有效的相似对是 0）
}

func (e *extracted) newFacts() (out []factRow) {
	for _, f := range e.Facts {
		if f.New {
			out = append(out, f)
		}
	}
	return out
}

func (e *extracted) newEpisodes() (out []epRow) {
	for _, p := range e.Episodes {
		if p.New {
			out = append(out, p)
		}
	}
	return out
}

const quoteMax = 120

// extract 读副本。events 是副本的聊天库视图（回查证据原话）；before 是跑之前已有的编号
func extract(ctx context.Context, memFile string, events memory.EventSource, before existingSet) (*extracted, error) {
	db, err := sql.Open("sqlite", sqliteDSN(memFile, true))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := &extracted{Protected: map[protectedStat]int{}}

	scopes := map[string][]string{}
	rows, err := db.QueryContext(ctx, `SELECT ref, scope FROM item_scopes ORDER BY ref, scope`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ref, sc string
		if err := rows.Scan(&ref, &sc); err != nil {
			rows.Close()
			return nil, err
		}
		scopes[ref] = append(scopes[ref], sc)
	}
	rows.Close()

	type rev struct {
		n                     int64
		op, by, reason, after string
	}
	revs := map[string][]rev{}
	rows, err = db.QueryContext(ctx, `SELECT id, fact_id, op, by, coalesce(reason, ''), coalesce(after_json, '') FROM fact_revisions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var r rev
		if err := rows.Scan(&r.n, &id, &r.op, &r.by, &r.reason, &r.after); err != nil {
			rows.Close()
			return nil, err
		}
		revs[id] = append(revs[id], r)
	}
	rows.Close()

	evidence := map[string][]evRow{}
	rows, err = db.QueryContext(ctx, `SELECT fact_id, day, conversation, coalesce(seq, 0), coalesce(speaker, ''), by_subject FROM fact_evidence ORDER BY fact_id, at, day, conversation`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var e evRow
		var by int
		if err := rows.Scan(&id, &e.Day, &e.Conv, &e.Seq, &e.Speaker, &by); err != nil {
			rows.Close()
			return nil, err
		}
		e.BySubject = by != 0
		evidence[id] = append(evidence[id], e)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT id, kind, text, coalesce(subject, ''), status, coalesce(confirmed_by, ''), coalesce(attributed_to, ''),
		coalesce(conversation, ''), coalesce(origin, ''), coalesce(audience_at_origin, ''), source_seqs, created_at, coalesce(superseded_by, '') FROM facts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f factRow
		var seqs string
		if err := rows.Scan(&f.ID, &f.Kind, &f.Text, &f.Subject, &f.Status, &f.ConfirmedBy, &f.AttributedTo, &f.Conversation, &f.Origin, &f.AudienceAtOrigin, &seqs, &f.CreatedAt, &f.SupersededBy); err != nil {
			rows.Close()
			return nil, err
		}
		f.Ref = "f:" + f.ID
		f.New = !before.facts[f.ID]
		f.Scopes = scopes[f.Ref]
		_ = json.Unmarshal([]byte(seqs), &f.SourceSeqs)
		for _, r := range revs[f.ID] {
			f.Revisions = append(f.Revisions, r.op)
			switch r.op {
			case "add":
				if f.AddBy == "" {
					f.AddBy, f.AddReason = r.by, r.reason
					f.WantsSupersede = wantsSupersede(r.after)
				}
			case "confirm":
				if r.n > before.maxRev {
					f.Confirmed = true
				}
			}
		}
		f.Evidence = evidence[f.ID]
		out.Facts = append(out.Facts, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.QueryContext(ctx, `SELECT id, conversation, text, from_seq, to_seq, created_at, coalesce(origin, ''), coalesce(audience_at_origin, '') FROM episodes WHERE status = 'active' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p epRow
		if err := rows.Scan(&p.ID, &p.Conv, &p.Text, &p.From, &p.To, &p.CreatedAt, &p.Origin, &p.AudienceAtOrigin); err != nil {
			rows.Close()
			return nil, err
		}
		p.Ref = "e:" + p.ID
		p.New = !before.episodes[p.ID]
		p.Scopes = scopes[p.Ref]
		out.Episodes = append(out.Episodes, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 想取代的旧事实现在是什么情况
	byID := map[string]*factRow{}
	for i := range out.Facts {
		byID[out.Facts[i].ID] = &out.Facts[i]
	}
	for i := range out.Facts {
		f := &out.Facts[i]
		if f.WantsSupersede == "" {
			continue
		}
		switch old := byID[strings.TrimPrefix(f.WantsSupersede, "f:")]; {
		case old == nil:
			f.WantsTarget = wantsMissing
		case old.Status == "superseded" && old.SupersededBy == f.ID:
			f.WantsTarget = wantsReplaced // 受保护的被换掉了会在 ProtectedChanged 里单独报出来，这里只说这条候选取代成了
		case old.Status != "active":
			f.WantsTarget = wantsGone
		case old.ConfirmedBy == "ai":
			f.WantsTarget = wantsAI
		default:
			f.WantsTarget = wantsProtected
		}
	}

	// 证据原话：只回查这次新写的、这次提升的事实的证据（别的旧事实不是这次整理的产物，也不该多读）
	for i := range out.Facts {
		f := &out.Facts[i]
		if !f.New && !f.Confirmed {
			continue
		}
		order := map[string]int{}
		for j := range f.Evidence {
			e := &f.Evidence[j]
			if _, ok := order[e.Speaker]; !ok {
				order[e.Speaker] = len(order) + 1
			}
			e.Ord = order[e.Speaker]
			e.Quote, e.Missing = quoteOf(ctx, events, e.Conv, e.Seq)
		}
	}
	for _, f := range out.Facts {
		if by, was := before.protected[f.ID]; was && (f.Status != "active" || f.ConfirmedBy != by) {
			out.ProtectedChanged = append(out.ProtectedChanged, f.Ref)
		}
	}
	classifyProtected(out)
	return out, nil
}

// wantsSupersede 读 add 修订 after_json 里的 wants_supersede（W2 的存法）；没有就空
func wantsSupersede(after string) string {
	if after == "" {
		return ""
	}
	var o struct {
		Wants string `json:"wants_supersede"`
	}
	if json.Unmarshal([]byte(after), &o) != nil {
		return ""
	}
	return o.Wants
}

// quoteOf 回查一条消息的原话：Events(conv, seq-1, 1)，不给 EventSource 加 BySeq（规格 W6 报告 B 节）。取不到 missing 为真
func quoteOf(ctx context.Context, events memory.EventSource, conv string, seq int64) (string, bool) {
	if seq <= 0 {
		return "", true
	}
	evs, err := events.Events(ctx, conv, seq-1, 1)
	if err != nil || len(evs) == 0 || evs[0].Seq != seq {
		return "", true
	}
	t := strings.Join(strings.Fields(evs[0].Text), " ")
	if utf8.RuneCountInString(t) > quoteMax {
		t = string([]rune(t)[:quoteMax]) + "…"
	}
	return t, false
}

// auditFacts 是自检要过一遍的事实：这次新写的，和这次提升成有效的（导入的旧候选提升以后才会被召回，范围也要核）
func (e *extracted) auditFacts() (out []factRow) {
	for _, f := range e.Facts {
		if f.New || f.Confirmed {
			out = append(out, f)
		}
	}
	return out
}

// confirmedNow 返回这次运行里提升成有效的事实（新写的和导入的旧候选都算），按创建时间排
func (e *extracted) confirmedNow() (out []factRow) {
	for _, f := range e.Facts {
		if f.Confirmed {
			out = append(out, f)
		}
	}
	return out
}

// 受保护的有效事实的来源三类
const (
	srcBunSummary = "Bun 整理写的"
	srcOwnerOK    = "你放行的"
	srcOther      = "其余（导入时就有效的）"
)

// 主体三类
const (
	subjOwner = "主人"
	subjSelf  = "她自己"
	subjOther = "别人"
)

func subjectClass(s string) string {
	switch s {
	case "owner":
		return subjOwner
	case "self":
		return subjSelf
	}
	return subjOther
}

// classifyProtected 按规格 W2 的白名单（status = active 且 confirmed_by 不是 ai 就受保护）数受保护的有效事实，
// 并算"导入的待确认"和"同一主体同时有效的相似对"。数的是副本跑完以后的全部事实，不只这次新写的
func classifyProtected(e *extracted) {
	for _, f := range e.Facts {
		if f.Status != "active" || f.ConfirmedBy == "ai" {
			continue
		}
		src := srcOther
		switch {
		case f.ConfirmedBy == "owner":
			src = srcOwnerOK
		case f.AddBy == "summary":
			src = srcBunSummary
		}
		e.Protected[protectedStat{src, subjectClass(f.Subject)}]++
	}
	// 导入的待确认：在主人私聊（QQ 里主人自己的私聊）看得见的候选里，最近 20 条里有几条是跑之前就有的。
	// 排序和 memory 给模型看的候选块同一个口径（visibleFacts：创建时间新的在前，一样新的编号大的在前）；
	// 跑之前就有的候选里被这次启动补提升提走的（Confirmed 且不是新写的）也算，因为问的是"一开始模型看得到几条"
	aud := memory.Audience{Channel: "qq", Conversation: "qq:private:0:0", Scene: "private", PrivateOK: true, PersonIDs: []string{"owner"}}
	allowed := memory.AllowedScopes(aud)
	var cands []factRow
	for _, f := range e.Facts {
		wasCandidate := f.Status == "candidate" || (f.Confirmed && !f.New)
		if wasCandidate && memory.CanSee(f.Scopes, allowed) {
			cands = append(cands, f)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].CreatedAt != cands[j].CreatedAt {
			return cands[i].CreatedAt > cands[j].CreatedAt
		}
		return cands[i].Ref > cands[j].Ref
	})
	for i, f := range cands {
		if !f.New {
			e.ImportedCandidates++
			if i < 20 {
				e.ImportedInTop20++
			}
		}
	}
	// 相似对：同一主体、文本的字二元组 Jaccard ≥ 0.6。有效的相似对要看（同一件事多条独立有效），待确认的只数个数
	bySubject := map[string][]factRow{}
	for _, f := range e.Facts {
		if f.Status == "active" || f.Status == "candidate" {
			bySubject[f.Subject] = append(bySubject[f.Subject], f)
		}
	}
	subjects := make([]string, 0, len(bySubject))
	for s := range bySubject {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects)
	for _, s := range subjects {
		fs := bySubject[s]
		for i := range fs {
			for j := i + 1; j < len(fs); j++ {
				if jaccard2(fs[i].Text, fs[j].Text) < 0.6 {
					continue
				}
				if fs[i].Status == "active" && fs[j].Status == "active" {
					e.Duplicates = append(e.Duplicates, [2]string{fs[i].Ref, fs[j].Ref})
				} else {
					e.SimilarCandidates++
				}
			}
		}
	}
}

func jaccard2(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	inter := 0
	for g := range ga {
		if gb[g] {
			inter++
		}
	}
	return float64(inter) / float64(len(ga)+len(gb)-inter)
}

func bigrams(s string) map[string]bool {
	r := []rune(strings.Join(strings.Fields(s), ""))
	out := map[string]bool{}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = true
	}
	return out
}

func shaOfText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
