package memory

// 记忆页的详情、计数、证据、修订、最近变动（规格-记忆页 4.3、4.4）。和 browse.go 同一个只读 Browser。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ───────────── 计数 ─────────────

// BrowseCounts：胶囊和角色按钮上的数字。一条 GROUP BY 查完；不随搜索词变
type BrowseCounts struct {
	Facts    map[string]map[string]int // 种类 → 状态组 → 条数
	Episodes map[string]int            // 状态组 → 条数
	Total    int                       // 事实 + 片段
	Schema   BrowseSchema
	// Quarantine 是止血线藏起来的条数（线没设时全零）：只数有效和待确认的事实、有效的片段，同 QuarantineReport
	Quarantine QuarantineReport
}

func (b *Browser) Counts(ctx context.Context) (BrowseCounts, error) {
	sc, err := b.Schema(ctx)
	if err != nil {
		return BrowseCounts{}, err
	}
	out := BrowseCounts{Facts: map[string]map[string]int{}, Episodes: map[string]int{}, Schema: sc}
	rs, err := b.q.QueryContext(ctx, `SELECT f.kind, `+sc.factGroupSQL()+`, count(*) FROM facts f GROUP BY 1, 2`)
	if err != nil {
		return out, fmt.Errorf("数事实：%w", err)
	}
	for rs.Next() {
		var kind, group string
		var n int
		if err := rs.Scan(&kind, &group, &n); err != nil {
			rs.Close()
			return out, err
		}
		if out.Facts[kind] == nil {
			out.Facts[kind] = map[string]int{}
		}
		out.Facts[kind][group] += n
		out.Total += n
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return out, err
	}
	rs, err = b.q.QueryContext(ctx, `SELECT `+sc.episodeGroupSQL()+`, count(*) FROM episodes e GROUP BY 1`)
	if err != nil {
		return out, fmt.Errorf("数片段：%w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var group string
		var n int
		if err := rs.Scan(&group, &n); err != nil {
			return out, err
		}
		out.Episodes[group] += n
		out.Total += n
	}
	if err := rs.Err(); err != nil {
		return out, err
	}
	rs.Close()
	if b.opt.QuarantineSince > 0 {
		out.Quarantine, err = b.quarantineCounts(ctx, sc)
	}
	return out, err
}

// quarantineCounts 数被止血线藏起来的：读出会被想起的条目（有效、待确认的事实，有效的片段）的几个字段，用 quarantinedSince 判。
// 只在设了线的时候才跑
func (b *Browser) quarantineCounts(ctx context.Context, sc BrowseSchema) (QuarantineReport, error) {
	r := QuarantineReport{Active: true, Since: b.opt.QuarantineSince}
	var items []Item
	rs, err := b.q.QueryContext(ctx, `SELECT f.id, coalesce(f.status, ''), coalesce(f.created_at, 0), `+sc.confirmedExpr()+` FROM facts f WHERE f.status IN ('active', 'candidate')`)
	if err != nil {
		return r, fmt.Errorf("数隔离的事实：%w", err)
	}
	for rs.Next() {
		it := Item{Type: "fact", Kind: "fact"}
		var id string
		if err := rs.Scan(&id, &it.Status, &it.CreatedAt, &it.ConfirmedBy); err != nil {
			rs.Close()
			return r, err
		}
		it.Ref = "f:" + id
		items = append(items, it)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return r, err
	}
	if err := fillConfirmedAt(b.q, items); err != nil {
		return r, err
	}
	epWhere := `1`
	if sc.EpStatus {
		epWhere = `e.status = 'active'`
	}
	rs, err = b.q.QueryContext(ctx, `SELECT coalesce(e.created_at, 0) FROM episodes e WHERE `+epWhere)
	if err != nil {
		return r, fmt.Errorf("数隔离的片段：%w", err)
	}
	defer rs.Close()
	for rs.Next() {
		it := Item{Type: "episode", Kind: "episode", Status: "active"}
		if err := rs.Scan(&it.CreatedAt); err != nil {
			return r, err
		}
		items = append(items, it)
	}
	if err := rs.Err(); err != nil {
		return r, err
	}
	for i := range items {
		if !quarantinedSince(&items[i], r.Since) {
			continue
		}
		switch {
		case items[i].Type == "episode":
			r.Episodes++
		case items[i].Status == "candidate":
			r.Candidates++
		default:
			r.Facts++
		}
	}
	return r, nil
}

// TopValues：下拉里放的会话、关于谁，按条数排，最多 n 个（规格-记忆页 4.3：多的靠搜索）。column 只许 conversation / subject
func (b *Browser) TopValues(ctx context.Context, column string, n int) ([]ValueCount, error) {
	if column != "conversation" && column != "subject" {
		return nil, ErrBrowseParam
	}
	rs, err := b.q.QueryContext(ctx, `SELECT `+column+`, count(*) AS n FROM facts WHERE coalesce(`+column+`, '') <> '' GROUP BY 1 ORDER BY n DESC, 1 LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("数 %s：%w", column, err)
	}
	defer rs.Close()
	var out []ValueCount
	for rs.Next() {
		var v ValueCount
		if err := rs.Scan(&v.Value, &v.N); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rs.Err()
}

type ValueCount struct {
	Value string
	N     int
}

// ───────────── 详情 ─────────────

// Recall：她现在想得起这一条吗。和召回同一套函数：有效或待确认、没过期（Expired）、没被隔离（quarantinedSince）
type Recall struct {
	Can    bool
	Reason string // RecallOK 等
}

const (
	RecallOK          = "ok"
	RecallExpired     = "expired"     // 暂时的事，过期了
	RecallQuarantined = "quarantined" // 止血线藏着
	RecallRevoked     = "revoked"
	RecallSuperseded  = "superseded"
	RecallOther       = "other" // 不认识的状态
)

// Occasions 是待确认的事实"已攒 N / M 次"（共用证据计次函数和配置换算）
type Occasions struct {
	Have, Need, Self, Rows int
	Auto                   bool // 配置里开了自动提升，并且这一种事实能自动提升
}

// Source 是一句出处：会话加序号。原话由调用方（memview）按（会话, seq）去聊天库取，这里不碰聊天库
type Source struct {
	Conversation string
	Seq          int64
}

type Detail struct {
	Row          BrowseRow
	Origin       string
	SpeechAct    string
	SupersededBy string // f:<id>
	Recall       Recall
	Occasions    *Occasions // 只有待确认的事实
	Sources      []Source   // 去重、最多 50 个，按库里的顺序
	SourcesTotal int        // 去重后的总数（超过 50 的也数）
	// Whisper：这条事实是悄悄话处理那一轮里写下的（批号、来源会话、被引事件）；平时是 nil。没迁移的库没有这一列，也是 nil
	Whisper *WhisperSource
	Schema  BrowseSchema
}

var refRE = regexp.MustCompile(`^[fe]:[A-Za-z0-9_.-]{1,80}$`)

// ParseRef 验 f:<id> / e:<id>，畸形的（空 id、别的前缀、超长、带引号空白）都是参数错误
func ParseRef(ref string) (table, id string, err error) {
	if !refRE.MatchString(ref) {
		return "", "", ErrBrowseParam
	}
	if ref[0] == 'f' {
		return "facts", ref[2:], nil
	}
	return "episodes", ref[2:], nil
}

// Detail 取一条的字段（一次查完）。出处原话、证据、修订各有自己的调用，慢的不挡快的
func (b *Browser) Detail(ctx context.Context, ref string) (Detail, error) {
	table, id, err := ParseRef(ref)
	if err != nil {
		return Detail{}, err
	}
	sc, err := b.Schema(ctx)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Schema: sc}
	var text, scope, seqs string
	var validTo sql.NullInt64
	if table == "facts" {
		r := BrowseRow{Ref: ref}
		err = b.q.QueryRowContext(ctx, `SELECT f.kind, coalesce(f.status, ''), f.text, coalesce(f.subject, ''), coalesce(f.attributed_to, ''), coalesce(f.scope, ''),
			coalesce(f.conversation, ''), coalesce(f.created_at, 0), coalesce(f.learned_at, 0), coalesce(f.temporal, ''), f.valid_to, `+sc.confirmedExpr()+`,
			coalesce(f.superseded_by, ''), coalesce(f.origin, ''), coalesce(f.speech_act, ''), coalesce(f.source_seqs, '')
			FROM facts f WHERE f.id = ?`, id).Scan(&r.Kind, &r.Status, &text, &r.Subject, &r.AttributedTo, &scope, &r.Conversation, &r.CreatedAt, &r.LearnedAt,
			&r.Temporal, &validTo, &r.ConfirmedBy, &d.SupersededBy, &d.Origin, &d.SpeechAct, &seqs)
		if errors.Is(err, sql.ErrNoRows) {
			return Detail{}, ErrBrowseNotFound
		}
		if err != nil {
			return Detail{}, fmt.Errorf("读 %s：%w", ref, err)
		}
		r.Group = GroupOf(r.Status, r.ConfirmedBy)
		d.Row = r
		if d.SupersededBy != "" {
			d.SupersededBy = "f:" + d.SupersededBy
		}
		d.Sources, d.SourcesTotal = parseSourceSeqs(seqs, r.Conversation)
		var raw string
		if b.q.QueryRowContext(ctx, `SELECT coalesce(whisper_json, '') FROM facts WHERE id = ?`, id).Scan(&raw) == nil { // 没这一列（没迁移）就查不出来，按没有
			d.Whisper = parseWhisper(raw)
		}
	} else {
		r := BrowseRow{Ref: ref, Kind: "episode"}
		err = b.q.QueryRowContext(ctx, `SELECT `+sc.episodeStatusExpr()+`, e.text, coalesce(e.scope, ''), coalesce(e.conversation, ''), coalesce(e.created_at, 0),
			coalesce(e.learned_at, 0), coalesce(e.from_seq, 0), coalesce(e.to_seq, 0) FROM episodes e WHERE e.id = ?`, id).Scan(&r.Status, &text, &scope, &r.Conversation,
			&r.CreatedAt, &r.LearnedAt, &r.FromSeq, &r.ToSeq)
		if errors.Is(err, sql.ErrNoRows) {
			return Detail{}, ErrBrowseNotFound
		}
		if err != nil {
			return Detail{}, fmt.Errorf("读 %s：%w", ref, err)
		}
		r.Group = EpisodeGroupOf(r.Status)
		d.Row = r
	}
	if validTo.Valid {
		v := validTo.Int64
		d.Row.ValidTo = &v
	}
	d.Row.Text, d.Row.TextCut = CleanText(text, false, detailTextRunes)
	// 范围组：只取这一条的
	d.Row.Scopes = []string{scope}
	one := []BrowseRow{d.Row}
	if err := b.fillScopes(ctx, sc, one); err != nil {
		return Detail{}, err
	}
	d.Row = one[0]
	it := d.Row.item()
	d.Row.Expired = it.Expired(b.now())
	if b.opt.QuarantineSince > 0 && it.ConfirmedBy == "ai" && it.Type == "fact" {
		// 提升的时间：最晚一条 confirm 修订，查不到当它晚（同 fillConfirmedAt）
		_ = b.q.QueryRowContext(ctx, `SELECT coalesce(max(at), 0) FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id).Scan(&it.ConfirmedAt)
	}
	d.Recall = b.recallOf(it)
	if table == "facts" && d.Row.Status == "candidate" && sc.Evidence {
		t, err := TallyEvidence(ctx, b.q, id)
		if err != nil {
			return Detail{}, fmt.Errorf("数 %s 的证据：%w", ref, err)
		}
		d.Occasions = &Occasions{Have: t.Occasions, Need: ConfirmOccasionsFor(b.opt.ConfirmOccasions), Self: t.Self, Rows: t.Rows, Auto: b.autoOn() && b.autoKind(d.Row.Kind)}
	}
	return d, nil
}

// autoKind：这一种事实攒够证据能不能自动提升（同写入侧的 confirmKind）
func (b *Browser) autoKind(kind string) bool {
	kinds := b.opt.ConfirmKinds
	if kinds == nil {
		kinds = defaultConfirmKinds
	}
	return slices.Contains(factKinds, kind) && slices.Contains(kinds, kind)
}

// recallOf：和召回同一套判断。状态不是有效 / 待确认的先说状态；再看过期、隔离
func (b *Browser) recallOf(it *Item) Recall {
	switch it.Status {
	case "active", "candidate":
	case "revoked":
		return Recall{Reason: RecallRevoked}
	case "superseded":
		return Recall{Reason: RecallSuperseded}
	default:
		return Recall{Reason: RecallOther}
	}
	if it.Expired(b.now()) {
		return Recall{Reason: RecallExpired}
	}
	if quarantinedSince(it, b.opt.QuarantineSince) {
		return Recall{Reason: RecallQuarantined}
	}
	return Recall{Can: true, Reason: RecallOK}
}

// parseSourceSeqs 读 source_seqs：必须是整数数组；含非整数、负数、超大数的元素跳过，去重，最多留 maxSourceSeqs 个。
// 不是数组（坏 JSON、对象、字符串）当没有（规格-记忆页 3.4）
func parseSourceSeqs(raw, conversation string) ([]Source, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, 0
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var arr []json.RawMessage
	if err := dec.Decode(&arr); err != nil {
		return nil, 0
	}
	seen := map[int64]bool{}
	var out []Source
	total := 0
	for _, el := range arr {
		s := strings.TrimSpace(string(el))
		if s == "" || strings.ContainsAny(s, ".eE\"[{tfn") { // 小数、科学计数、字符串、嵌套、布尔、null 都不是整数
			continue
		}
		var n int64
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil || fmt.Sprint(n) != s || n < 0 || n > maxSeq || seen[n] {
			continue
		}
		seen[n] = true
		total++
		if len(out) < maxSourceSeqs {
			out = append(out, Source{Conversation: conversation, Seq: n})
		}
	}
	return out, total
}

// ───────────── 证据 ─────────────

type EvidenceRow struct {
	Day          string
	Conversation string
	Seq          int64
	Speaker      string
	BySubject    bool
	Verdict      string
	At           int64
}

type EvidencePage struct {
	Rows    []EvidenceRow
	Next    string
	Total   int
	Missing bool // 库里没有证据表（旧库）
}

// Evidence 取一条事实的证据，新的在前，每页 BrowseSubPage 行；游标是（天，会话）
func (b *Browser) Evidence(ctx context.Context, ref, cursor string) (EvidencePage, error) {
	table, id, err := ParseRef(ref)
	if err != nil || table != "facts" {
		return EvidencePage{}, ErrBrowseParam
	}
	sc, err := b.Schema(ctx)
	if err != nil {
		return EvidencePage{}, err
	}
	if !sc.Evidence {
		return EvidencePage{Missing: true}, nil
	}
	fp := "ev:" + ref
	args := []any{id}
	where := `fact_id = ?`
	if cursor != "" {
		c, err := b.open(cursor, fp)
		if err != nil {
			return EvidencePage{}, err
		}
		day, conv, _ := strings.Cut(c.ID, "\x00")
		where += ` AND (day < ? OR (day = ? AND conversation < ?))`
		args = append(args, day, day, conv)
	}
	var page EvidencePage
	if err := b.q.QueryRowContext(ctx, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("数 %s 的证据：%w", ref, err)
	}
	rs, err := b.q.QueryContext(ctx, `SELECT day, conversation, coalesce(seq, 0), coalesce(speaker, ''), by_subject, coalesce(verdict, ''), at FROM fact_evidence WHERE `+where+
		` ORDER BY day DESC, conversation DESC LIMIT ?`, append(args, BrowseSubPage+1)...)
	if err != nil {
		return page, fmt.Errorf("读 %s 的证据：%w", ref, err)
	}
	defer rs.Close()
	for rs.Next() {
		var e EvidenceRow
		var by int
		if err := rs.Scan(&e.Day, &e.Conversation, &e.Seq, &e.Speaker, &by, &e.Verdict, &e.At); err != nil {
			return page, err
		}
		e.BySubject = by != 0
		page.Rows = append(page.Rows, e)
	}
	if err := rs.Err(); err != nil {
		return page, err
	}
	if len(page.Rows) > BrowseSubPage {
		page.Rows = page.Rows[:BrowseSubPage]
		last := page.Rows[len(page.Rows)-1]
		page.Next = b.seal(browseCursor{FP: fp, ID: last.Day + "\x00" + last.Conversation})
	}
	return page, nil
}

// ───────────── 修订 ─────────────

// RevisionState 是修订前后的小 JSON 里认得的几样；键不在就是没记
type RevisionState struct {
	Status         string
	ConfirmedBy    string
	HasConfirmedBy bool
	Scopes         []string
	HasScopes      bool
	SupersededBy   string
	Occasions      int
}

type RevisionRow struct {
	ID     int64
	FactID string
	Op     string
	At     int64
	By     string
	Reason string
	Before RevisionState
	After  RevisionState
}

type RevisionPage struct {
	Rows []RevisionRow
	Next string
}

// Revisions 取一条事实的修订，新的在上，每页 BrowseSubPage 行；游标是修订 id
func (b *Browser) Revisions(ctx context.Context, ref, cursor string) (RevisionPage, error) {
	table, id, err := ParseRef(ref)
	if err != nil || table != "facts" {
		return RevisionPage{}, ErrBrowseParam
	}
	sc, err := b.Schema(ctx)
	if err != nil {
		return RevisionPage{}, err
	}
	if !sc.Revisions {
		return RevisionPage{}, nil
	}
	fp := "rev:" + ref
	where, args := `r.fact_id = ?`, []any{id}
	if cursor != "" {
		c, err := b.open(cursor, fp)
		if err != nil {
			return RevisionPage{}, err
		}
		if b.shared {
			id, err := strconv.ParseInt(c.ID, 10, 64)
			if err != nil {
				return RevisionPage{}, ErrBrowseParam
			}
			where += ` AND (r.at < ? OR (r.at = ? AND r.id < ?))`
			args = append(args, c.At, c.At, id)
		} else {
			where += ` AND r.id < ?`
			args = append(args, c.At)
		}
	}
	rows, err := b.revisionRows(ctx, sc, where, args, BrowseSubPage+1)
	if err != nil {
		return RevisionPage{}, fmt.Errorf("读 %s 的修订：%w", ref, err)
	}
	var page RevisionPage
	if len(rows) > BrowseSubPage {
		rows = rows[:BrowseSubPage]
		last := rows[len(rows)-1]
		c := browseCursor{FP: fp, At: last.ID}
		if b.shared {
			c.At, c.ID = last.At, strconv.FormatInt(last.ID, 10)
		}
		page.Next = b.seal(c)
	}
	page.Rows = rows
	return page, nil
}

func (b *Browser) revisionRows(ctx context.Context, sc BrowseSchema, where string, args []any, limit int) ([]RevisionRow, error) {
	before, after := `NULL`, `NULL`
	if sc.RevJSON {
		before, after = `r.before_json`, `r.after_json`
	}
	order := `r.id DESC`
	if b.shared {
		order = `r.at DESC, r.id DESC`
	}
	rs, err := b.q.QueryContext(ctx, `SELECT r.id, r.fact_id, r.op, r.at, coalesce(r.by, ''), coalesce(r.reason, ''), `+before+`, `+after+` FROM fact_revisions r WHERE `+where+
		` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []RevisionRow
	for rs.Next() {
		var r RevisionRow
		var bj, aj sql.NullString
		if err := rs.Scan(&r.ID, &r.FactID, &r.Op, &r.At, &r.By, &r.Reason, &bj, &aj); err != nil {
			return nil, err
		}
		r.Before, r.After = parseRevisionState(bj.String), parseRevisionState(aj.String)
		out = append(out, r)
	}
	return out, rs.Err()
}

// parseRevisionState 读修订前后的小 JSON；读不懂（坏 JSON、类型不对）当没记
func parseRevisionState(raw string) RevisionState {
	var s RevisionState
	if strings.TrimSpace(raw) == "" {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil {
		return s
	}
	_ = json.Unmarshal(m["status"], &s.Status)
	if v, ok := m["confirmed_by"]; ok {
		var cb *string
		if json.Unmarshal(v, &cb) == nil {
			s.HasConfirmedBy = true
			if cb != nil {
				s.ConfirmedBy = *cb
			}
		}
	}
	for _, key := range []string{"scopes", "scope"} {
		if v, ok := m[key]; ok {
			var list []string
			if json.Unmarshal(v, &list) == nil {
				s.Scopes, s.HasScopes = list, true
				break
			}
		}
	}
	_ = json.Unmarshal(m["superseded_by"], &s.SupersededBy)
	_ = json.Unmarshal(m["occasions"], &s.Occasions)
	return s
}

// ───────────── 最近变动 ─────────────

type RecentRow struct {
	RevisionRow
	Text   string // 对应事实的开头；事实已经不在（真删后只剩 purged 行）是空
	Exists bool
}

// Recent 取最近的 BrowseRecentMax 行修订，按修订 id 倒序（主键倒序，不扫表），每行带对应事实的正文开头
func (b *Browser) Recent(ctx context.Context) ([]RecentRow, error) {
	sc, err := b.Schema(ctx)
	if err != nil {
		return nil, err
	}
	if !sc.Revisions {
		return nil, nil
	}
	rows, err := b.revisionRows(ctx, sc, `1`, nil, BrowseRecentMax)
	if err != nil {
		return nil, fmt.Errorf("读最近变动：%w", err)
	}
	out := make([]RecentRow, len(rows))
	for i, r := range rows {
		out[i].RevisionRow = r
		var text sql.NullString
		err := b.q.QueryRowContext(ctx, `SELECT text FROM facts WHERE id = ?`, r.FactID).Scan(&text)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("读最近变动的正文：%w", err)
		}
		if err == nil {
			out[i].Exists = true
			out[i].Text = CleanLine(text.String, 60)
		}
	}
	return out, nil
}
