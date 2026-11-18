package memory

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// TempDays：temporal = temporary 的事实没写 valid_to 时，记下几天后不再给她（照 memory.ts 的 TEMP_DAYS，ADR-075 第 2 条）
const TempDays = 7

// Item 是一条能被想起的记忆：事实（有效的、待确认的）或片段
type Item struct {
	Ref          string // f:<id> / e:<id>
	Type         string // fact / episode
	Kind         string // fact / promise / preference / boundary；片段写 episode
	Status       string // active / candidate（AI 没通过的、撤销的、被取代的不读进来）
	Text         string
	Subject      string
	Scopes       []string // 一组范围（ADR-159 第 4 条）；Bun 版的库里每条只有一个
	Conversation string
	CreatedAt    int64 // 毫秒
	LearnedAt    int64
	Temporal     string
	ValidTo      *int64
	// ConfirmedBy：谁放行的（ai / owner / 空）。Bun 版的库没有这一列（迁移前），读进来是空；只有止血的隔离（quarantine.go）用
	ConfirmedBy string
	// ConfirmedAt：confirmed_by = ai 的事实被提升的时间（fact_revisions 里最晚一条 confirm 的 at，毫秒）；别的、或查不到是 0。也只有止血的隔离用
	ConfirmedAt    int64
	SourceSeqs     []int64 // 来源引用只按（会话、seq）回查真实事件。
	FromSeq, ToSeq int64
	LearnedBy      string // 共用事实最初从哪个角色那里得知，原始事件仍不扩观察权限。
	// Whisper：悄悄话处理那一轮里 remember 写的事实带的来源（批号、来源会话、被引事件）；平时是 nil。只加说明，不改可见性。
	Whisper *WhisperSource
	// Previous：被这条取代的旧版本，新的在前，最多 maxPrevious 条（只有有效或待确认的事实带；Status 是 superseded）。
	// 召回把它连同各自的时间一起给她，问"以前怎样、什么时候变的"才答得出；不进全文和向量，不单独被想起
	Previous []Item
}

// maxPrevious：一条事实最多带几个被它取代的旧版本（沿取代链往前数）
const maxPrevious = 3

// Expired：过了有效期（写了 valid_to 的过了那天；暂时的事记下 TempDays 天以后），不再给她，页面上照样看得到（照 memory.ts 的 expired）
func (it Item) Expired(now time.Time) bool {
	ms := now.UnixMilli()
	if it.ValidTo != nil {
		return *it.ValidTo < ms
	}
	learned := it.LearnedAt
	if learned == 0 {
		learned = it.CreatedAt
	}
	return it.Temporal == "temporary" && learned+TempDays*86_400_000 < ms
}

// openReadOnly 只读打开一个 SQLite 文件。URI 转义、mode=ro、query_only 照 internal/store 的 dsn 抄的一份（那边没导出）；
// Go 后台以后导出公共的打开函数时换过去
func openReadOnly(file string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(file))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 %s：%w", file, err)
	}
	return db, nil
}

func readOnlyDSN(file string) string {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
	return "file:" + p + "?" + q.Encode()
}

// ValidCharacterID：角色 id 会拼进路径，空的、带路径分隔符、冒号或 ".." 的不认（B17。2026-10-19 体检：带 ../ 的 id 能在任意已有目录下建出 memory.db）。
// 规则同 daemon/plugins/memory.ts 的 db()
func ValidCharacterID(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\:") || strings.ContainsRune(id, 0) || strings.Contains(id, "..") {
		return fmt.Errorf("角色 id 不对：%q", id)
	}
	return nil
}

// scopeGroups 读范围组的真源 item_scopes（规格-记忆-v2 13.2.12 的 W2）。表不在（还没迁移的 Bun 库）返回 nil，条目退回单值的 scope 列。
// 一条的几个值按 scope 排好
func scopeGroups(db *sql.DB) (map[string][]string, error) {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'item_scopes'`).Scan(&n); err != nil || n == 0 {
		return nil, err
	}
	rows, err := db.Query(`SELECT ref, scope FROM item_scopes ORDER BY ref, scope`)
	if err != nil {
		return nil, fmt.Errorf("读范围组：%w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var ref, scope string
		if err := rows.Scan(&ref, &scope); err != nil {
			return nil, err
		}
		out[ref] = append(out[ref], scope)
	}
	return out, rows.Err()
}

// scopesOf：这一条的范围组。组里没有它（没迁移，或迁移以后 Bun 又写了新的）就退回单值的 scope 列；组里有，第一个值（facts.scope）排在最前
func scopesOf(groups map[string][]string, ref, primary string) []string {
	g := groups[ref]
	if len(g) == 0 {
		return scopeList(primary)
	}
	out := make([]string, 0, len(g))
	if slices.Contains(g, primary) {
		out = append(out, primary)
	}
	for _, s := range g {
		if s != primary {
			out = append(out, s)
		}
	}
	return out
}

// loadItems 读这个角色库里能被想起的全部条目：有效的、待确认的事实（ADR-159 第 5 条：待确认也参与召回），有效的片段。
// 过期的照样读出来，由召回那一步跳过（Expired）
func loadItems(db *sql.DB) ([]Item, error) {
	groups, err := scopeGroups(db)
	if err != nil {
		return nil, err
	}
	var out []Item
	// confirmed_by 是迁移加的列：没迁移的 Bun 库没有，按空读
	confirmed := `''`
	var have int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('facts') WHERE name = 'confirmed_by'`).Scan(&have); err == nil && have > 0 {
		confirmed = `coalesce(confirmed_by, '')`
	}
	rows, err := db.Query(`SELECT id, kind, status, text, coalesce(subject, ''), coalesce(scope, ''), coalesce(conversation, ''), created_at,
		coalesce(learned_at, 0), coalesce(temporal, ''), valid_to, ` + confirmed + ` FROM facts WHERE status IN ('active', 'candidate')`)
	if err != nil {
		return nil, fmt.Errorf("读事实：%w", err)
	}
	for rows.Next() {
		var it Item
		var id, scope string
		var validTo sql.NullInt64
		if err := rows.Scan(&id, &it.Kind, &it.Status, &it.Text, &it.Subject, &scope, &it.Conversation, &it.CreatedAt, &it.LearnedAt, &it.Temporal, &validTo, &it.ConfirmedBy); err != nil {
			rows.Close()
			return nil, err
		}
		it.Ref, it.Type = "f:"+id, "fact"
		it.Scopes = scopesOf(groups, it.Ref, scope)
		if validTo.Valid {
			v := validTo.Int64
			it.ValidTo = &v
		}
		out = append(out, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := fillConfirmedAt(db, out); err != nil {
		return nil, err
	}
	if err := fillPrevious(db, groups, confirmed, out); err != nil {
		return nil, err
	}
	rows, err = db.Query(`SELECT id, text, coalesce(scope, ''), conversation, created_at, coalesce(learned_at, 0) FROM episodes WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("读片段：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		it := Item{Type: "episode", Kind: "episode", Status: "active"}
		var id, scope string
		if err := rows.Scan(&id, &it.Text, &scope, &it.Conversation, &it.CreatedAt, &it.LearnedAt); err != nil {
			return nil, err
		}
		it.Ref = "e:" + id
		it.Scopes = scopesOf(groups, it.Ref, scope)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := fillSourceRefs(db, out); err != nil {
		return nil, err
	}
	return out, nil
}

// fillPrevious 给事实挂上被它取代的旧版本（沿 superseded_by 往前，最多 maxPrevious 条，新的在前）。
// 旧版本只在"谁看得见新的，谁也看得见旧的"时才挂（新的每个范围都在旧的范围组里）：旧的是私事、新的放宽了范围，不能借新的把旧值带出去。
// 没有 superseded_by 列的库（只造了读取要用的列的老库）没有取代关系可读，原样跳过
func fillPrevious(db *sql.DB, groups map[string][]string, confirmed string, items []Item) error {
	var have int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('facts') WHERE name = 'superseded_by'`).Scan(&have); err != nil || have == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT id, kind, text, coalesce(subject, ''), coalesce(scope, ''), coalesce(conversation, ''), created_at,
		coalesce(learned_at, 0), coalesce(temporal, ''), valid_to, ` + confirmed + `, superseded_by FROM facts WHERE status = 'superseded' AND superseded_by IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("读被取代的事实：%w", err)
	}
	var olds []Item
	next := map[string]string{} // 旧的 ref → 取代它的 id
	for rows.Next() {
		var it Item
		var id, scope, by string
		var validTo sql.NullInt64
		if err := rows.Scan(&id, &it.Kind, &it.Text, &it.Subject, &scope, &it.Conversation, &it.CreatedAt, &it.LearnedAt, &it.Temporal, &validTo, &it.ConfirmedBy, &by); err != nil {
			rows.Close()
			return err
		}
		it.Ref, it.Type, it.Status = "f:"+id, "fact", "superseded"
		it.Scopes = scopesOf(groups, it.Ref, scope)
		if validTo.Valid {
			v := validTo.Int64
			it.ValidTo = &v
		}
		olds = append(olds, it)
		next[it.Ref] = by
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(olds) == 0 {
		return nil
	}
	if err := fillConfirmedAt(db, olds); err != nil {
		return err
	}
	before := map[string][]*Item{} // 新的 id → 直接被它取代的旧版本
	for i := range olds {
		before[next[olds[i].Ref]] = append(before[next[olds[i].Ref]], &olds[i])
	}
	for i := range items {
		if items[i].Type != "fact" {
			continue
		}
		var chain []Item
		seen := map[string]bool{items[i].Ref: true}
		frontier := []string{strings.TrimPrefix(items[i].Ref, "f:")}
		for len(frontier) > 0 && len(chain) < 4*maxPrevious {
			var nextFrontier []string
			for _, id := range frontier {
				for _, o := range before[id] {
					if seen[o.Ref] {
						continue
					}
					seen[o.Ref] = true
					nextFrontier = append(nextFrontier, strings.TrimPrefix(o.Ref, "f:"))
					if scopesCover(o.Scopes, items[i].Scopes) {
						chain = append(chain, *o)
					}
				}
			}
			frontier = nextFrontier
		}
		sort.SliceStable(chain, func(a, b int) bool { return chain[a].CreatedAt > chain[b].CreatedAt })
		items[i].Previous = chain[:min(len(chain), maxPrevious)]
	}
	return nil
}

// scopesCover：want 里的每个范围 have 里都有（能看 want 的人一定能看 have）
func scopesCover(have, want []string) bool {
	for _, s := range want {
		if !slices.Contains(have, s) {
			return false
		}
	}
	return true
}

// fillConfirmedAt 给 confirmed_by = ai 的事实补上提升的时间。只有有 ai 的库才多查一次 fact_revisions（confirm 修订只有 Go 写，行数很少）
func fillConfirmedAt(db *sql.DB, items []Item) error {
	need := false
	for i := range items {
		if items[i].ConfirmedBy == "ai" {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	rows, err := db.Query(`SELECT fact_id, max(at) FROM fact_revisions WHERE op = 'confirm' GROUP BY fact_id`)
	if err != nil {
		return fmt.Errorf("读提升修订：%w", err)
	}
	defer rows.Close()
	at := map[string]int64{}
	for rows.Next() {
		var id string
		var v int64
		if err := rows.Scan(&id, &v); err != nil {
			return err
		}
		at["f:"+id] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		if items[i].ConfirmedBy == "ai" {
			items[i].ConfirmedAt = at[items[i].Ref]
		}
	}
	return nil
}

// scopeList：Bun 版的库里 scope 是一个值；没写的（极老的数据）按最严的 owner 算（照 memory.ts 的 scopeOfItem 没来源时）
func scopeList(s string) []string {
	if s == "" {
		return []string{"owner"}
	}
	return []string{s}
}
