package memory

// 记忆页用的只读查询（规格-记忆页 4.1～4.4）：一个角色库一个 Browser，自己开两条只读连接——
// 一条跑查询（一次一个），一条只做变化轮询（PRAGMA data_version），免得慢查询把变化通知也堵住。
// 不借用 *Memory 的连接，不开 memory-v2.db（那是写方的派生文件）；列表不走 Items()（它不含撤销的、被取代的，还要全读进内存）。
// 状态组、范围、过期、隔离、证据计次的口径都用本包已有的函数（scopesOf、Item.Expired、quarantinedSince、tallyEvidence），不另写一套（边界清单 ⑪）。
// 日志不写：这里没有日志出口，错误只带 ref 和原因，不带正文、搜索词、原话。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var (
	// ErrBrowseParam：参数不对（畸形 ref、被篡改或别的筛选条件下发的游标、数字越界）。界面当成程序错误，不当成"没有数据"
	ErrBrowseParam = errors.New("记忆页查询参数不对")
	// ErrBrowseNotFound：库里没有这一条
	ErrBrowseNotFound = errors.New("记忆页：库里没有这一条")
)

const (
	BrowsePageSize  = 50  // 列表每页
	BrowseSearchMax = 200 // 搜索结果上限
	BrowseSubPage   = 20  // 证据、修订每页
	BrowseRecentMax = 50  // 最近变动
	listTextRunes   = 120 // 列表一行正文
	detailTextRunes = 2000
	maxSourceSeqs   = 50
	maxSeq          = int64(1) << 53 // 再大的序号当不是整数（JS 之类读不准的范围，也不可能是真序号）
)

// 状态组：由（status, confirmed_by）算，计数、筛选、每一行共用这一处（GroupOf 和 factGroupSQL 必须一致，测试逐格对）
const (
	GroupOwner      = "owner"      // 有效，你确认过
	GroupAI         = "ai"         // 有效，她攒够证据自己提升的
	GroupSaid       = "said"       // 有效，主人亲口说、她用 remember 记下的（来源绑了原话，但不是主人放行；整理动不了）
	GroupImported   = "imported"   // 有效，导入的（放行者空）
	GroupCandidate  = "candidate"  // 听说，待确认
	GroupRevoked    = "revoked"    // 已撤销
	GroupSuperseded = "superseded" // 被新的取代
	GroupActive     = "active"     // 片段：有效
	GroupOther      = "other"      // 不认识的状态，或有效但放行者不认识
)

// GroupOf：一条事实的状态组
func GroupOf(status, confirmedBy string) string {
	switch status {
	case "active":
		switch confirmedBy {
		case "owner":
			return GroupOwner
		case "ai":
			return GroupAI
		case ConfirmedOwnerSaid:
			return GroupSaid
		case "":
			return GroupImported
		}
	case "candidate":
		return GroupCandidate
	case "revoked":
		return GroupRevoked
	case "superseded":
		return GroupSuperseded
	}
	return GroupOther
}

// EpisodeGroupOf：一个片段的状态组（片段没有放行者）
func EpisodeGroupOf(status string) string {
	switch status {
	case "active":
		return GroupActive
	case "revoked":
		return GroupRevoked
	}
	return GroupOther
}

// BrowserOptions 是装配带来的：确认三项配置照写入侧的取值，页面不读配置文件
type BrowserOptions struct {
	ConfirmOccasions int // 配置里的 confirmOccasions；实际次数经 ConfirmOccasionsFor
	AutoConfirm      bool
	AutoNow          func() bool // 非 nil 时每次问一遍现在写入侧真的在不在自动提升，盖过 AutoConfirm（页面不能只看配置：整理没开成时配置写着开也不会提升）
	ConfirmKinds     []string    // nil 用默认
	QuarantineSince  int64
	SharedDB         string // 宿主明确提供主人共用库；不枚举其他角色库。
	Now              func() time.Time
}

// autoOn：写入侧现在会不会把攒够证据的待确认事实自动提升
func (b *Browser) autoOn() bool {
	if b.opt.AutoNow != nil {
		return b.opt.AutoNow()
	}
	return b.opt.AutoConfirm
}

// Browser 是一个角色库的只读视图
type Browser struct {
	q, poll       *sql.DB
	shared        bool
	localMigrated bool
	opt           BrowserOptions
	key           [32]byte // 游标的签名钥匙：每个 Browser 一把，换了实例旧游标就作废
}

// OpenBrowser 只读打开一个角色库（文件必须已经存在）
func OpenBrowser(file string, opt BrowserOptions) (*Browser, error) {
	q, err := openReadOnly(file)
	if err != nil {
		return nil, err
	}
	poll, err := openReadOnly(file)
	if err != nil {
		q.Close()
		return nil, err
	}
	b := &Browser{q: q, poll: poll, opt: opt}
	if err := b.attachOwnerPool(opt.SharedDB); err != nil {
		b.Close()
		return nil, err
	}
	if _, err := rand.Read(b.key[:]); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// Close 关两条连接；调用方负责先等在途查询退出（memview 的 Close 做这件事）
func (b *Browser) Close() error {
	return errors.Join(b.q.Close(), b.poll.Close())
}

func (b *Browser) now() time.Time {
	if b.opt.Now != nil {
		return b.opt.Now()
	}
	return time.Now()
}

// DataVersion 是变化轮询：任何别的连接提交过，这个数就变（规格-记忆页 4.5 选 B）。只在专用的连接上问，这条连接绝不写
func (b *Browser) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	if err := b.poll.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
		return 0, err
	}
	if b.shared {
		var shared int64
		if err := b.poll.QueryRowContext(ctx, `PRAGMA owner_shared.data_version`).Scan(&shared); err != nil {
			return 0, err
		}
		v += shared
	}
	return v, nil
}

// BrowseSchema 是这个库现在有哪些后来才加的列和表（没迁移的 Bun 库缺，规格-记忆页 3.4 第一组）
type BrowseSchema struct {
	ConfirmedBy bool // facts.confirmed_by
	ItemScopes  bool // 范围组表
	Evidence    bool // fact_evidence
	Revisions   bool // fact_revisions
	RevJSON     bool // fact_revisions.before_json / after_json
	EpStatus    bool // episodes.status
	Unmigrated  bool // 合并视图不能掩盖当前角色库尚未迁移。
}

// Migrated：写入侧的迁移跑过了（页面顶上"这个库还没迁移"的依据）
func (s BrowseSchema) Migrated() bool {
	return !s.Unmigrated && s.ConfirmedBy && s.ItemScopes && s.Evidence && s.RevJSON
}

func (b *Browser) Schema(ctx context.Context) (BrowseSchema, error) {
	var s BrowseSchema
	if b.shared {
		return BrowseSchema{ConfirmedBy: true, ItemScopes: true, Evidence: true, Revisions: true, RevJSON: true, EpStatus: true, Unmigrated: !b.localMigrated}, nil
	}
	rows, err := b.q.QueryContext(ctx, `
		SELECT 'confirmed_by' FROM pragma_table_info('facts') WHERE name = 'confirmed_by'
		UNION ALL SELECT 'rev_json' FROM pragma_table_info('fact_revisions') WHERE name = 'before_json'
		UNION ALL SELECT 'ep_status' FROM pragma_table_info('episodes') WHERE name = 'status'
		UNION ALL SELECT 't:' || name FROM sqlite_master WHERE type = 'table' AND name IN ('item_scopes', 'fact_evidence', 'fact_revisions')`)
	if err != nil {
		return s, fmt.Errorf("看库的结构：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return s, err
		}
		switch k {
		case "confirmed_by":
			s.ConfirmedBy = true
		case "rev_json":
			s.RevJSON = true
		case "ep_status":
			s.EpStatus = true
		case "t:item_scopes":
			s.ItemScopes = true
		case "t:fact_evidence":
			s.Evidence = true
		case "t:fact_revisions":
			s.Revisions = true
		}
	}
	return s, rows.Err()
}

// ───────────── 状态组、范围的 SQL ─────────────

func (s BrowseSchema) confirmedExpr() string {
	if s.ConfirmedBy {
		return `coalesce(f.confirmed_by, '')`
	}
	return `''`
}

// factGroupSQL 是 GroupOf 的 SQL 写法：计数（GROUP BY）和筛选（IN）用的是同一段
func (s BrowseSchema) factGroupSQL() string {
	return `CASE f.status WHEN 'active' THEN CASE ` + s.confirmedExpr() + ` WHEN 'owner' THEN 'owner' WHEN 'ai' THEN 'ai' WHEN '` + ConfirmedOwnerSaid + `' THEN 'said' WHEN '' THEN 'imported' ELSE 'other' END` +
		` WHEN 'candidate' THEN 'candidate' WHEN 'revoked' THEN 'revoked' WHEN 'superseded' THEN 'superseded' ELSE 'other' END`
}

func (s BrowseSchema) episodeStatusExpr() string {
	if s.EpStatus {
		return `coalesce(e.status, '')`
	}
	return `'active'`
}

func (s BrowseSchema) episodeGroupSQL() string {
	return `CASE ` + s.episodeStatusExpr() + ` WHEN 'active' THEN 'active' WHEN 'revoked' THEN 'revoked' ELSE 'other' END`
}

// scopePredicate：范围类型筛选。一条的范围组以 item_scopes 为真源；组里没有它（没迁移，或迁移后 Bun 又写了新的）就退回 scope 列（同 scopesOf）
func (s BrowseSchema) scopePredicate(alias, refPrefix, kind string) (string, bool) {
	pred := func(x string) string {
		switch kind {
		case "public":
			return x + ` = 'public'`
		case "owner":
			return x + ` = 'owner'`
		case "conv":
			return `substr(` + x + `, 1, 5) = 'conv:'`
		case "person":
			return `substr(` + x + `, 1, 7) = 'person:'`
		}
		return ""
	}
	if pred("x") == "" {
		return "", false
	}
	primary := pred(`coalesce(nullif(` + alias + `.scope, ''), 'owner')`)
	if !s.ItemScopes {
		return `(` + primary + `)`, true
	}
	ref := `'` + refPrefix + `' || ` + alias + `.id`
	return `(EXISTS (SELECT 1 FROM item_scopes s WHERE s.ref = ` + ref + ` AND ` + pred("s.scope") + `)` +
		` OR (NOT EXISTS (SELECT 1 FROM item_scopes s WHERE s.ref = ` + ref + `) AND ` + primary + `))`, true
}

// likeEscape：搜索词里的 % _ \ 按字面找（LIKE … ESCAPE '\'）
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ───────────── 列表 ─────────────

// BrowseQuery 是列表的筛选条件（都是 AND）。Kind：空 = 全部事实；fact / promise / preference / boundary；episode = 片段（另一张表）
type BrowseQuery struct {
	Kind         string
	Groups       []string // 状态组，空 = 全部
	ScopeKind    string   // public / owner / conv / person，空 = 全部
	Conversation string
	Subject      string
	SubjectIDs   []string // 搜索词能解析成的人（名字或别名经认人那一处查出来，由调用方给）
	Query        string   // 搜索词；非空时最多 BrowseSearchMax 条、没有下一页
	Cursor       string
	Limit        int // 0 = BrowsePageSize
}

// BrowseRow 是列表里的一行，也是详情的头
type BrowseRow struct {
	Ref          string // f:<id> / e:<id>
	Kind         string // fact / promise / preference / boundary；片段写 episode
	Status       string
	ConfirmedBy  string
	Group        string
	Text         string // 列表里并成一行、截 120 字；详情里是前 2000 字，保留换行
	TextCut      int    // 被截掉的字数
	Subject      string
	AttributedTo string
	Conversation string
	LearnedBy    string
	Scopes       []string
	CreatedAt    int64 // 毫秒；0 = 不详
	LearnedAt    int64
	Temporal     string
	ValidTo      *int64
	Expired      bool // 暂时的、已经过期
	FromSeq      int64
	ToSeq        int64
}

type BrowsePage struct {
	Rows      []BrowseRow
	Next      string // 空 = 没有下一页
	Truncated bool   // 搜索结果多于上限，只给了前 BrowseSearchMax 条
}

func (q BrowseQuery) fingerprint() string {
	groups := slices.Clone(q.Groups)
	slices.Sort(groups)
	ids := slices.Clone(q.SubjectIDs)
	slices.Sort(ids)
	h := sha256.Sum256([]byte(strings.Join([]string{q.Kind, strings.Join(groups, ","), q.ScopeKind, q.Conversation, q.Subject, strings.Join(ids, ","), q.Query}, "\x00")))
	return hex.EncodeToString(h[:8])
}

type browseCursor struct {
	FP string `json:"f"`
	At int64  `json:"a"`
	ID string `json:"i"`
}

func (b *Browser) seal(c browseCursor) string {
	data, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, b.key[:])
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:12])
}

// open 验签并解开；签不对、不是这次筛选下发的，一律参数错误
func (b *Browser) open(s, fp string) (browseCursor, error) {
	var c browseCursor
	body, sig, ok := strings.Cut(s, ".")
	if !ok {
		return c, ErrBrowseParam
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	got, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || err2 != nil {
		return c, ErrBrowseParam
	}
	mac := hmac.New(sha256.New, b.key[:])
	mac.Write(data)
	if !hmac.Equal(got, mac.Sum(nil)[:12]) || json.Unmarshal(data, &c) != nil || c.FP != fp {
		return browseCursor{}, ErrBrowseParam
	}
	return c, nil
}

var (
	factKindsAll = []string{"fact", "promise", "preference", "boundary"}
	allGroups    = []string{GroupOwner, GroupAI, GroupSaid, GroupImported, GroupCandidate, GroupRevoked, GroupSuperseded, GroupActive, GroupOther}
)

func (q BrowseQuery) validate() error {
	if q.Kind != "" && q.Kind != "episode" && !slices.Contains(factKindsAll, q.Kind) {
		return ErrBrowseParam
	}
	for _, g := range q.Groups {
		if !slices.Contains(allGroups, g) {
			return ErrBrowseParam
		}
	}
	switch q.ScopeKind {
	case "", "public", "owner", "conv", "person":
	default:
		return ErrBrowseParam
	}
	if q.Limit < 0 || q.Limit > BrowseSearchMax {
		return ErrBrowseParam
	}
	return nil
}

// List 取一页。分页用 (created_at, id) 游标，不用 offset；游标带筛选条件的指纹。搜索时最多 BrowseSearchMax 条、没有下一页
func (b *Browser) List(ctx context.Context, q BrowseQuery) (BrowsePage, error) {
	if err := q.validate(); err != nil {
		return BrowsePage{}, err
	}
	sc, err := b.Schema(ctx)
	if err != nil {
		return BrowsePage{}, err
	}
	fp := q.fingerprint()
	var cur *browseCursor
	if q.Cursor != "" {
		c, err := b.open(q.Cursor, fp)
		if err != nil {
			return BrowsePage{}, err
		}
		cur = &c
	}
	limit := q.Limit
	searching := strings.TrimSpace(q.Query) != ""
	switch {
	case searching:
		limit = BrowseSearchMax
	case limit == 0:
		limit = BrowsePageSize
	}
	episodes := q.Kind == "episode"
	var rows []BrowseRow
	if episodes {
		rows, err = b.listEpisodes(ctx, sc, q, cur, limit+1)
	} else {
		rows, err = b.listFacts(ctx, sc, q, cur, limit+1)
	}
	if err != nil {
		return BrowsePage{}, err
	}
	var page BrowsePage
	if len(rows) > limit {
		rows = rows[:limit]
		if searching {
			page.Truncated = true
		} else {
			last := rows[len(rows)-1]
			page.Next = b.seal(browseCursor{FP: fp, At: last.CreatedAt, ID: refID(last.Ref)})
		}
	}
	page.Rows = rows
	if err := b.fillScopes(ctx, sc, page.Rows); err != nil {
		return BrowsePage{}, err
	}
	return page, nil
}

// searchClause 是搜索词的 WHERE 片段：正文、关于谁里找（按字面），加上调用方给的"这个词解析成的人"
func searchClause(q BrowseQuery, alias string, withSubject bool) (string, []any) {
	term := strings.TrimSpace(q.Query)
	if term == "" {
		return "", nil
	}
	pat := "%" + likeEscape(term) + "%"
	if !withSubject {
		return alias + `.text LIKE ? ESCAPE '\'`, []any{pat}
	}
	sql := `(` + alias + `.text LIKE ? ESCAPE '\' OR ` + alias + `.subject LIKE ? ESCAPE '\'`
	args := []any{pat, pat}
	if len(q.SubjectIDs) > 0 {
		sql += ` OR ` + alias + `.subject IN (` + strings.TrimSuffix(strings.Repeat("?,", len(q.SubjectIDs)), ",") + `)`
		for _, id := range q.SubjectIDs {
			args = append(args, id)
		}
	}
	return sql + `)`, args
}

func groupClause(expr string, groups []string) (string, []any) {
	if len(groups) == 0 {
		return "", nil
	}
	args := make([]any, len(groups))
	for i, g := range groups {
		args[i] = g
	}
	return `(` + expr + `) IN (` + strings.TrimSuffix(strings.Repeat("?,", len(groups)), ",") + `)`, args
}

func (b *Browser) listFacts(ctx context.Context, sc BrowseSchema, q BrowseQuery, cur *browseCursor, n int) ([]BrowseRow, error) {
	var where []string
	var args []any
	add := func(sql string, a ...any) {
		if sql != "" {
			where = append(where, sql)
			args = append(args, a...)
		}
	}
	if q.Kind != "" {
		add(`f.kind = ?`, q.Kind)
	}
	g, ga := groupClause(sc.factGroupSQL(), q.Groups)
	add(g, ga...)
	if p, ok := sc.scopePredicate("f", "f:", q.ScopeKind); ok {
		add(p)
	}
	if q.Conversation != "" {
		add(`f.conversation = ?`, q.Conversation)
	}
	if q.Subject != "" {
		add(`f.subject = ?`, q.Subject)
	}
	s, sa := searchClause(q, "f", true)
	add(s, sa...)
	if cur != nil {
		add(`(coalesce(f.created_at, 0) < ? OR (coalesce(f.created_at, 0) = ? AND f.id < ?))`, cur.At, cur.At, cur.ID)
	}
	cond := "1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	query := `SELECT f.id, f.kind, coalesce(f.status, ''), f.text, coalesce(f.subject, ''), coalesce(f.attributed_to, ''), coalesce(f.scope, ''),
		coalesce(f.conversation, ''), coalesce(f.created_at, 0), coalesce(f.learned_at, 0), coalesce(f.temporal, ''), f.valid_to, ` + sc.confirmedExpr() + `
		FROM facts f WHERE ` + cond + ` ORDER BY coalesce(f.created_at, 0) DESC, f.id DESC LIMIT ?`
	rs, err := b.q.QueryContext(ctx, query, append(args, n)...)
	if err != nil {
		return nil, fmt.Errorf("读事实列表：%w", err)
	}
	defer rs.Close()
	var out []BrowseRow
	now := b.now()
	for rs.Next() {
		var r BrowseRow
		var id, text, scope string
		var validTo sql.NullInt64
		if err := rs.Scan(&id, &r.Kind, &r.Status, &text, &r.Subject, &r.AttributedTo, &scope, &r.Conversation, &r.CreatedAt, &r.LearnedAt, &r.Temporal, &validTo, &r.ConfirmedBy); err != nil {
			return nil, err
		}
		r.Ref = "f:" + id
		r.Group = GroupOf(r.Status, r.ConfirmedBy)
		r.Text, r.TextCut = CleanText(text, true, listTextRunes)
		r.Scopes = []string{scope} // 先放单值，fillScopes 再按范围组换成真的
		if validTo.Valid {
			v := validTo.Int64
			r.ValidTo = &v
		}
		r.Expired = r.item().Expired(now)
		out = append(out, r)
	}
	return out, rs.Err()
}

func (b *Browser) listEpisodes(ctx context.Context, sc BrowseSchema, q BrowseQuery, cur *browseCursor, n int) ([]BrowseRow, error) {
	var where []string
	var args []any
	add := func(sql string, a ...any) {
		if sql != "" {
			where = append(where, sql)
			args = append(args, a...)
		}
	}
	g, ga := groupClause(sc.episodeGroupSQL(), q.Groups)
	add(g, ga...)
	if p, ok := sc.scopePredicate("e", "e:", q.ScopeKind); ok {
		add(p)
	}
	if q.Conversation != "" {
		add(`e.conversation = ?`, q.Conversation)
	}
	s, sa := searchClause(q, "e", false)
	add(s, sa...)
	if cur != nil {
		add(`(coalesce(e.created_at, 0) < ? OR (coalesce(e.created_at, 0) = ? AND e.id < ?))`, cur.At, cur.At, cur.ID)
	}
	cond := "1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	query := `SELECT e.id, ` + sc.episodeStatusExpr() + `, e.text, coalesce(e.scope, ''), coalesce(e.conversation, ''), coalesce(e.created_at, 0), coalesce(e.learned_at, 0),
		coalesce(e.from_seq, 0), coalesce(e.to_seq, 0) FROM episodes e WHERE ` + cond + ` ORDER BY coalesce(e.created_at, 0) DESC, e.id DESC LIMIT ?`
	rs, err := b.q.QueryContext(ctx, query, append(args, n)...)
	if err != nil {
		return nil, fmt.Errorf("读片段列表：%w", err)
	}
	defer rs.Close()
	var out []BrowseRow
	for rs.Next() {
		r := BrowseRow{Kind: "episode"}
		var id, text, scope string
		if err := rs.Scan(&id, &r.Status, &text, &scope, &r.Conversation, &r.CreatedAt, &r.LearnedAt, &r.FromSeq, &r.ToSeq); err != nil {
			return nil, err
		}
		r.Ref = "e:" + id
		r.Group = EpisodeGroupOf(r.Status)
		r.Text, r.TextCut = CleanText(text, true, listTextRunes)
		r.Scopes = []string{scope}
		out = append(out, r)
	}
	return out, rs.Err()
}

// item 把一行变回 Item，给 Expired、quarantinedSince 这些本包的判断用
func (r BrowseRow) item() *Item {
	it := &Item{Ref: r.Ref, Kind: r.Kind, Status: r.Status, Subject: r.Subject, Scopes: r.Scopes, Conversation: r.Conversation,
		CreatedAt: r.CreatedAt, LearnedAt: r.LearnedAt, Temporal: r.Temporal, ValidTo: r.ValidTo, ConfirmedBy: r.ConfirmedBy, Type: "fact"}
	if strings.HasPrefix(r.Ref, "e:") {
		it.Type, it.Kind = "episode", "episode"
	}
	return it
}

// fillScopes 按这一页的 ref 取范围组（不整表读），用 scopesOf 合并（组里没有的退回 scope 列）
func (b *Browser) fillScopes(ctx context.Context, sc BrowseSchema, rows []BrowseRow) error {
	if err := b.fillSharedSources(ctx, rows); err != nil {
		return err
	}
	if len(rows) == 0 || !sc.ItemScopes {
		for i := range rows {
			rows[i].Scopes = scopeList(rows[i].Scopes[0])
		}
		return nil
	}
	refs := make([]any, len(rows))
	for i, r := range rows {
		refs[i] = r.Ref
	}
	rs, err := b.q.QueryContext(ctx, `SELECT ref, scope FROM item_scopes WHERE ref IN (`+strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",")+`) ORDER BY ref, scope`, refs...)
	if err != nil {
		return fmt.Errorf("读范围组：%w", err)
	}
	defer rs.Close()
	groups := map[string][]string{}
	for rs.Next() {
		var ref, scope string
		if err := rs.Scan(&ref, &scope); err != nil {
			return err
		}
		groups[ref] = append(groups[ref], scope)
	}
	if err := rs.Err(); err != nil {
		return err
	}
	for i := range rows {
		rows[i].Scopes = scopesOf(groups, rows[i].Ref, rows[i].Scopes[0])
	}
	return nil
}

// refID 去掉 f: / e: 前缀
func refID(ref string) string { return ref[min(2, len(ref)):] }
