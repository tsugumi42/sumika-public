package memory

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"sumika-native/internal/embed"
)

// 召回（规格-记忆-v2 第七节）。参数是起步值，按考题量了再调（第十四节末尾）
const (
	rrfK            = 60
	lexicalTop      = 50
	vectorTop       = 50
	SimilarityFloor = 0.6 // 向量命中的余弦低于这个不要（第七节第 4 步）
	pinnedMaxChars  = 600 // 固定要点（约定、边界）单算，照现在
	maxCandidates   = 1   // 待确认每轮最多给 1 条，标"听说"（ADR-159 第 5 条）
	maxNarrative    = 2   // 片段（以后的近期要点、长期经历）每轮最多 2 条
)

// Mode 是这一次为什么想
type Mode int

const (
	ModeNormal   Mode = iota // 每轮自动想一下
	ModePast                 // 在问以前（"上次""还记得吗"）：不受门控，给满预算
	ModeExplicit             // memory_search 这类明确的回忆：不受门控
	modeSummary              // 内部整理上下文：相关事实，不重排、不标注原话来源。
)

// Query 是这一次要想起什么
type Query struct {
	Text      string   // 这一句原文
	Recent    []string // 同会话最近几条人说的话（不含她的回复、轮次总结、工具结果）
	Addressed bool     // 被 @、回复她、引用她：不受门控（回她一个"？"也要接得上）
	Mode      Mode
	Given     map[string]bool // 这一段主会话里已经给过的
	Now       time.Time
	TopK      int // 明确搜索的结果上限；0 保持注入筛选
}

// Hit 是想起来的一条
type Hit struct {
	Item
	Score   float64
	Lexical int     // 词法那一路的名次（0 = 没中）
	Vector  int     // 向量那一路的名次（0 = 没中）
	Sim     float64 // 向量的余弦（没中是 0）
	Alias   bool    // 是不是别名命中带进来的
	Hearsay bool    // 待确认：给她时标"听说、不确定"
	// Rerank：重排给的分数（越大越相关；没重排、没被重排看到的是 0）
	Rerank float64
	// OwnerOnly：平台群里因为 OwnerAsked 才看得见的（范围里只有 owner 这条路能看）：给她时标"主人私下说的，他问才答"（ADR-174 已知的坑）
	OwnerOnly bool
	Source    string // 经原始事件核对的来源；缺失时明说未核实。
}

// Recalled 是一次召回的结果，带诊断（召回日志用）
type Recalled struct {
	Pinned     []Hit
	Hits       []Hit
	Gated      bool   // 门控拦了：只看固定要点
	GateReason string // 拦的原因 / 放行的原因
	Candidates []Hit  // 合并排序后的全部候选（前 40，召回日志用）
	// Reranked：这一轮真的交给了重排并换了顺序（没配重排、候选不够两条时为假）
	Reranked bool
	// RerankSkipped 是没排成的原因码（"超时" / "出错" / "顺序不对" / "未就绪"）；没配重排、排成了都是空
	RerankSkipped string
}

var (
	markerRe = regexp.MustCompile(`\[[^\]]*\]`) // [图片]、[表情] 这类标记
	pastRe   = regexp.MustCompile(`上次|之前|以前|那天|那次|还记得|记得吗|记不记得|你忘了`)
)

// strip 去掉 [图片] 这类标记和空白标点，剩下的字数用来判断"值不值得查"
func strip(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, markerRe.ReplaceAllString(text, ""))
}

// Gate 判断这一句值不值得查（第七节第 1 步，纯函数，不调模型）：去掉标记后少于 3 个字、没被叫到、不是在问以前、也没对上谁的名字的，不查
func Gate(q Query, aliasHit bool) (pass bool, why string) {
	switch {
	case q.Mode != ModeNormal:
		return true, "明确回忆"
	case q.Addressed:
		return true, "被叫到"
	case pastRe.MatchString(q.Text):
		return true, "在问以前"
	case aliasHit:
		return true, "提到了人"
	}
	if n := len([]rune(strip(q.Text))); n < 3 {
		return false, fmt.Sprintf("只有 %d 个字", n)
	}
	return true, ""
}

// Budget 是这个场合每轮最多给多少字（第七节第 7 步，起步值）；固定要点另算
func Budget(a Audience) int {
	switch PlaceOf(a.Channel, a.Conversation, a.Scene) {
	case LocalPrivate:
		return 1200
	case LocalGroup:
		return 800
	case PlatformPrivate:
		if a.PrivateOK {
			return 1200
		}
		return 600
	default:
		return 400
	}
}

// Memory 是一个角色的记忆：Bun 版的 memory.db 读进内存，旁边文件里放 Go 自己的索引。
// Options.Writable 为真时还带着写入那一侧（write.go）：库以可写方式开着，写完在锁内刷新下面的 items
type Memory struct {
	// mu 管 items 和 byRef。放出去的这两样不再改（刷新时整个换新的），读的人拿一次就能遍历、不用持锁，也不在持锁时等网络
	mu           sync.RWMutex
	items        []Item
	byRef        map[string]*Item
	ver          uint64 // items 换过几次（reload 一次加一）；补向量的人拿它判断"我手里这份快照还是不是最新的"
	index        *Index
	embed        embed.Embedder
	people       map[string]bool // 别名表里有的人
	opt          Options
	summaryState atomic.Uint32 // 位 0 整理，位 1 自动提升；仅在前提成功后发布
	w            *writer       // 写入那一侧；Writable 为假时是 nil
	// vecState：补向量这一侧（后台协程、降级标志），只读、可写都有；补的只是旁边的派生索引（vectors.go）
	vecState
	// rememberWait：remember 等写锁最多等多久，0 用 DefaultWriteWait（5 秒，13.2.3）。测试里调短
	rememberWait time.Duration
	// 隔离后的条目快照，按 m.ver 缓存（quarantine.go）；没设 QuarantineSince 时不用
	qMu                                sync.Mutex
	qVer                               uint64
	qItems                             []Item
	qByRef                             map[string]*Item
	guardRoute                         atomic.Uint32 // 正常 / 嵌入退路 / 模型退路；渠道用 GuardRoute 观察切换
	rerankNoted                        atomic.Bool   // 旧桥没有重排这件事的日志只写一次，不在每次 memory_search 刷屏（rerank.go 的 reorder）
	shareMu                            sync.Mutex    // 同角色交接和关闭串行，不在两个数据库之间持写事务。
	viewMu                             sync.Mutex
	viewLocal, viewShared, viewVersion uint64
	viewItems                          []Item
	viewByRef                          map[string]*Item
}

// Open 打开角色的记忆库，同步旁边文件里的索引（全文、向量、别名）。e 为空就只用全文；opt 见 Options。
// Writable 为假（默认）：库只读打开，Bun 还在写它。Writable 为真：库以可写方式打开（不新建），写入侧的表必须已经迁移过
// 本地嵌入未 ready 时跳过启动时的向量同步（不等它起来）：宿主在嵌入就绪时调 EmbeddingReady 补一次（只读的也补）；
// 守卫仍持有同一实例，句向量就绪后按需补。
func Open(ctx context.Context, memoryDB string, e embed.Embedder, people []Person, opt Options) (*Memory, error) {
	if opt.Character != "" || opt.Writable {
		if err := ValidCharacterID(opt.Character); err != nil {
			return nil, err
		}
	}
	tracks, err := memoryTracks()
	if err != nil {
		return nil, err
	}
	// 启动时不迁移角色库，只做护栏的只读探查：太新（或认不出、探查失败）就整个不启动；
	// 有待跑迁移不算错，只读挂载照旧读，可写打开仍由 schemaReady 返回 ErrNeedsMigration。
	if _, err := inspectMemoryGuard(ctx, memoryDB, tracks); err != nil {
		return nil, err
	}
	var db *sql.DB
	if opt.Writable {
		if err = mustBeFile(memoryDB); err != nil {
			return nil, err
		}
		db, err = openWritable(memoryDB)
		if err == nil {
			if err = schemaReady(ctx, db); err != nil {
				db.Close()
				return nil, err
			}
		}
	} else {
		db, err = openReadOnly(memoryDB)
	}
	if err != nil {
		return nil, err
	}
	items, err := loadItems(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	x, err := OpenIndex(ctx, SidecarPath(memoryDB))
	if err != nil {
		db.Close()
		return nil, err
	}
	m := &Memory{items: items, byRef: refIndex(items), index: x, embed: e, people: map[string]bool{}, opt: opt}
	m.SetSummaryEnabled(opt.SummarizeEnabled, opt.AutoConfirm)
	m.vecState.init()
	if opt.Writable {
		m.w = newWriter(m, db)
	} else {
		db.Close()
	}
	fail := func(err error) (*Memory, error) {
		m.Close()
		return nil, err
	}
	if err := x.SyncText(items); err != nil {
		return fail(err)
	}
	syncVectors := e != nil
	if local, ok := e.(interface{ Status() (embed.Status, string) }); ok {
		status, _ := local.Status()
		syncVectors = status == embed.StatusReady
	}
	if syncVectors {
		if _, err := x.SyncVectors(ctx, items, e, VectorBatch); err != nil {
			return fail(err)
		}
		m.noteVectors(items)
	}
	if people != nil {
		if err := x.SyncAliases(people); err != nil {
			return fail(err)
		}
	}
	if opt.Shared != nil && opt.Writable {
		if err := m.FlushShared(ctx); err != nil {
			return fail(err)
		}
	}
	return m, nil
}

// refIndex 编号 → 条目的指针表（指向 items 里的元素，所以 items 放出去以后不能再改）
func refIndex(items []Item) map[string]*Item {
	byRef := make(map[string]*Item, len(items))
	for i := range items {
		byRef[items[i].Ref] = &items[i]
	}
	return byRef
}

// snapshot 是此刻的条目表。拿到的两样只读（刷新是整个换掉，不改这一份）
func (m *Memory) snapshot() ([]Item, map[string]*Item) {
	items, by, _ := m.combinedSnapshot()
	return items, by
}

// snapshotVer 同 snapshot 的条目表，外加它的版本号：版本号变了就说明条目表被换过
func (m *Memory) snapshotVer() ([]Item, uint64) {
	items, _, ver := m.combinedSnapshot()
	return items, ver
}

// Close 停掉补向量和写入那一侧（取消后台向量协程、等它们退出、等在途的写完），再关库。可以重复调。
// 只读的也要先停补向量再关旁边的索引，不然后台协程会往关了的库里写
func (m *Memory) Close() error {
	m.shareMu.Lock()
	defer m.shareMu.Unlock()
	var werr error
	if m.w != nil {
		werr = m.w.close()
	} else {
		m.stopVectors()
	}
	if err := m.index.Close(); err != nil {
		return err
	}
	return werr
}

// Items 给考题、管理页看（只读，不要改）
func (m *Memory) Items() []Item {
	items, _ := m.snapshot()
	return items
}

// aliasPeople 这一句里提到了谁（别名精确命中，按包含算；别名至少 2 个字）
func (m *Memory) aliasPeople(text string) []string {
	f := strings.Map(fold, text)
	rows, err := m.index.db.Query(`SELECT alias, person FROM aliases WHERE instr(?, alias) > 0`, f)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	seen := map[string]bool{}
	for rows.Next() {
		var a, p string
		rows.Scan(&a, &p)
		if wholeWord(f, a) && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// AliasesIn 这一句里撞上了哪些别名。中文的按包含算；英文、数字开头或结尾的要整词对上，前后不能紧挨着别的字母数字
// （在实测样本上量过：按包含算时，"ro""te""seek" 这类英文昵称会被 pro、test、deepseek 这样的长单词撞上）
func (m *Memory) AliasesIn(text string) []string {
	f := strings.Map(fold, text)
	rows, err := m.index.db.Query(`SELECT DISTINCT alias FROM aliases WHERE instr(?, alias) > 0`, f)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		rows.Scan(&a)
		if wholeWord(f, a) {
			out = append(out, a)
		}
	}
	return out
}

func alnum(r rune) bool { return r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) }

// wholeWord：别名在 text 里至少有一处，它开头是英文数字时前一个字不是英文数字、结尾是英文数字时后一个字也不是
func wholeWord(text, alias string) bool {
	ar := []rune(alias)
	tr := []rune(text)
	for i := 0; i+len(ar) <= len(tr); i++ {
		if string(tr[i:i+len(ar)]) != alias {
			continue
		}
		if alnum(ar[0]) && i > 0 && alnum(tr[i-1]) {
			continue
		}
		if end := i + len(ar); alnum(ar[len(ar)-1]) && end < len(tr) && alnum(tr[end]) {
			continue
		}
		return true
	}
	return false
}

// AliasStats 别名表按字数分各有几个
func (m *Memory) AliasStats() map[string]int {
	out := map[string]int{}
	rows, err := m.index.db.Query(`SELECT alias FROM aliases`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		rows.Scan(&a)
		k := fmt.Sprintf("%d字", min(len([]rune(a)), 5))
		out[k]++
	}
	return out
}

// lexical 在一张全文表里查，范围在 SQL 里限定，按编号去重，返回名次表
func (m *Memory) lexical(table, match string, allowed []string) []string {
	if match == "" {
		return nil
	}
	q := `SELECT ref FROM ` + table + ` WHERE ` + table + ` MATCH ?`
	args := []any{match}
	if allowed != nil {
		q += ` AND scope IN (` + strings.TrimSuffix(strings.Repeat("?,", len(allowed)), ",") + `)`
		for _, s := range allowed {
			args = append(args, s)
		}
	}
	q += ` ORDER BY bm25(` + table + `) LIMIT ?`
	args = append(args, lexicalTop*3)
	rows, err := m.index.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return distinct(rows, lexicalTop)
}

func distinct(rows *sql.Rows, n int) []string {
	var out []string
	seen := map[string]bool{}
	for rows.Next() && len(out) < n {
		var r string
		rows.Scan(&r)
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Recall 想一下（第七节第 1～8 步）
func (m *Memory) Recall(ctx context.Context, a Audience, q Query) (out Recalled) {
	defer func() {
		if q.Mode != modeSummary {
			m.labelSources(ctx, out.Pinned)
			m.labelSources(ctx, out.Hits)
		}
	}()
	if q.Now.IsZero() {
		q.Now = time.Now()
	}
	allowed := AllowedScopes(a)
	// 这一次召回用同一份条目和向量：写入那一侧可能随时刷新它们，但换上去的是新的一整份，这一份不会在中途变。
	// 止血的隔离（QuarantineSince）在这里生效：被隔离的条目不在这份快照里，词法、向量、别名命中的引用对不上就被丢掉
	items, byRef := m.snapshotForRead()
	vectors := m.index.Vectors()
	usable := func(it *Item) bool {
		return it != nil && CanSee(it.Scopes, allowed) && !it.Expired(q.Now) && !q.Given[it.Ref]
	}
	if q.Mode != modeSummary {
		out.Pinned = pinnedOf(items, allowed, q)
	}
	ownerOnly := ownerOnlyFn(a, DisclosureScopes(a))
	for i := range out.Pinned {
		out.Pinned[i].OwnerOnly = ownerOnly(out.Pinned[i].Scopes)
	}

	people := m.aliasPeople(q.Text)
	pass, why := Gate(q, len(people) > 0)
	out.Gated, out.GateReason = !pass, why
	if !pass {
		return out
	}

	queries := []string{q.Text}
	if len(q.Recent) > 0 {
		queries = append(queries, strings.Join(append(append([]string{}, q.Recent...), q.Text), "\n"))
	}

	// 词法：别名带进来的人的事实排最前，两字、三字两路先合成一路（两级融合：词法几路先合，再和向量合，不然词法三票、向量一票）
	lexScore := map[string]float64{}
	floor := m.newLexFloor(q.Text) // 全文命中的相关度下限只对当前这句话判，上下文只扩查询、不降线
	for _, qq := range queries {
		lists := [][]string{m.lexical("memory_bigram", bigramQuery(qq), allowed), m.lexical("memory_trigram", trigramQuery(qq), allowed)}
		if pool := m.opt.Shared; pool != nil {
			lists = append(lists, pool.lexical("memory_bigram", bigramQuery(qq), allowed), pool.lexical("memory_trigram", trigramQuery(qq), allowed))
		}
		for _, list := range lists {
			i := 0
			for _, r := range list {
				if it := byRef[r]; it != nil && !floor.relevant(it.Text) {
					continue
				}
				lexScore[r] += 1 / float64(rrfK+i+1)
				i++
			}
		}
	}
	aliasRefs := map[string]bool{}
	for _, p := range people {
		for i := range items {
			if it := &items[i]; it.Type == "fact" && it.Subject == p {
				aliasRefs[it.Ref] = true
			}
		}
	}
	lexRank := rankOf(lexScore)

	// 向量：每个查询算一个，和能看的条目逐条点积，过底线的取前 50
	vecScore, sims := map[string]float64{}, map[string]float64{}
	var poolVectors map[string][]float32
	if m.opt.Shared != nil {
		poolVectors = m.opt.Shared.index.Vectors()
	}
	if m.embed != nil && len(vectors)+len(poolVectors) > 0 {
		if vs, err := m.embed.Embed(ctx, queries); err == nil {
			for _, v := range vs {
				u := embed.Unit(v)
				type sv struct {
					ref string
					sim float64
				}
				var hits []sv
				for n, set := range []map[string][]float32{poolVectors, vectors} {
					for ref, iv := range set {
						if n == 1 && poolVectors[ref] != nil {
							continue
						}
						if it := byRef[ref]; it == nil || !CanSee(it.Scopes, allowed) || len(iv) != len(u) || (q.Mode == modeSummary && (it.Type != "fact" || it.Expired(q.Now))) {
							continue
						}
						if s := embed.Dot(u, iv); RecallReaches(s) {
							hits = append(hits, sv{ref, float64(s)})
						}
					}
				}
				// 同分按编号定序；否则 map 的遍历顺序会改变 RRF 名次，近重复去重也会保留不同条目。
				sort.Slice(hits, func(i, j int) bool {
					if hits[i].sim != hits[j].sim {
						return hits[i].sim > hits[j].sim
					}
					return hits[i].ref < hits[j].ref
				})
				for i, h := range hits[:min(len(hits), vectorTop)] {
					vecScore[h.ref] += 1 / float64(rrfK+i+1)
					sims[h.ref] = math.Max(sims[h.ref], h.sim)
				}
			}
		}
	}
	vecRank := rankOf(vecScore)

	// 合并：两路各按名次给 RRF 分，乘新近度；别名命中的直接排前面（精确的字面命中不能被向量挤掉）
	refs := map[string]bool{}
	for r := range lexRank {
		refs[r] = true
	}
	for r := range vecRank {
		refs[r] = true
	}
	for r := range aliasRefs {
		refs[r] = true
	}
	var cands []Hit
	for r := range refs {
		it := byRef[r]
		if it == nil || !CanSee(it.Scopes, allowed) || (q.Mode == modeSummary && (it.Type != "fact" || it.Expired(q.Now))) {
			continue
		}
		h := Hit{Item: *it, Lexical: lexRank[r], Vector: vecRank[r], Sim: sims[r], Alias: aliasRefs[r], Hearsay: it.Status == "candidate"}
		if h.Lexical > 0 {
			h.Score += 1 / float64(rrfK+h.Lexical)
		}
		if h.Vector > 0 {
			h.Score += 1 / float64(rrfK+h.Vector)
		}
		if h.Alias && q.Mode != modeSummary {
			h.Score += 1
		}
		days := q.Now.Sub(time.UnixMilli(it.CreatedAt)).Hours() / 24
		h.Score *= 1 + 0.2*math.Exp(-days/30)
		cands = append(cands, h)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Ref < cands[j].Ref
	})
	// 重排（可选）：合并出候选以后、按字数装进这一轮之前换一次顺序（规格-记忆-v2 第七节，2026-10-28 加）。
	// 只改顺序，后面的挑选、预算、可见集合都不变；没排成就用 RRF 原来的顺序。
	// 只在明确回忆（memory_search）上用：bge-reranker 40 对在 CPU 上可能超过普通召回的 3 秒时限（RecallTimeout），
	// 每轮自动想起的、问以前的都不重排（规格-记忆写入上线 W1a）
	if q.Mode == ModeExplicit {
		cands, out.Reranked, out.RerankSkipped = m.reorder(ctx, q.Text, cands)
	}
	out.Candidates = cands[:min(len(cands), 40)]

	if (q.Mode == ModeExplicit || q.Mode == modeSummary) && q.TopK > 0 {
		for _, h := range cands {
			if it := byRef[h.Ref]; it != nil && CanSee(it.Scopes, allowed) && !it.Expired(q.Now) {
				h.OwnerOnly = ownerOnly(h.Scopes)
				out.Hits = append(out.Hits, h)
				if len(out.Hits) == q.TopK {
					break
				}
			}
		}
		return out
	}
	// 挑：能看、没过期、这一段没给过；待确认最多 1 条、片段最多 2 条；和已经挑的意思几乎一样的跳过；按场合和意图限字数
	budget := Budget(a)
	if q.Mode == ModeNormal && !pastRe.MatchString(q.Text) {
		budget /= 2
	}
	pinned := map[string]bool{}
	for _, p := range out.Pinned {
		pinned[p.Ref] = true
	}
	chars, nCand, nNarr := 0, 0, 0
	for _, h := range cands {
		if !usable(byRef[h.Ref]) || pinned[h.Ref] {
			continue
		}
		if h.Hearsay && nCand >= maxCandidates || h.Type == "episode" && nNarr >= maxNarrative {
			continue
		}
		if nearDuplicate(vectors, poolVectors, h.Ref, out.Hits) {
			continue
		}
		n := len([]rune(h.Text)) + priorChars(h)
		if chars+n > budget {
			break
		}
		chars += n
		if h.Hearsay {
			nCand++
		}
		if h.Type == "episode" {
			nNarr++
		}
		h.OwnerOnly = ownerOnly(h.Scopes)
		out.Hits = append(out.Hits, h)
	}
	return out
}

// ownerOnlyFn 提醒 QQ 群里主人问到的私事不能接着转告群友；召回资格始终不分场合。
// 只标 owner 范围且不在本群或公开范围的事实，不能把别的群的记忆误标成主人私事。
func ownerOnlyFn(a Audience, allowed []string) func(scopes []string) bool {
	if allowed == nil || a.PrivateOK || !a.OwnerAsked || PlaceOf(a.Channel, a.Conversation, a.Scene) != PlatformGroup {
		return func([]string) bool { return false }
	}
	rest := slices.DeleteFunc(slices.Clone(allowed), func(s string) bool { return s == "owner" })
	return func(scopes []string) bool { return slices.Contains(scopes, "owner") && !CanSee(scopes, rest) }
}

// pinnedOf 是要一直遵守的约定和边界：有效的、能看的、没过期的，新的在前，合计不超过 600 字（照 memory.ts 的 pinned）
func pinnedOf(items []Item, allowed []string, q Query) []Hit {
	var ps []*Item
	for i := range items {
		it := &items[i]
		if it.Type == "fact" && it.Status == "active" && (it.Kind == "promise" || it.Kind == "boundary") && CanSee(it.Scopes, allowed) && !it.Expired(q.Now) {
			ps = append(ps, it)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].CreatedAt > ps[j].CreatedAt })
	var out []Hit
	chars := 0
	for _, it := range ps[:min(len(ps), 20)] {
		n := len([]rune(it.Text))
		if chars+n > pinnedMaxChars {
			break
		}
		chars += n
		out = append(out, Hit{Item: *it, Score: 1})
	}
	return out
}

func nearDuplicate(vectors, shared map[string][]float32, ref string, chosen []Hit) bool {
	v := shared[ref]
	if v == nil {
		v = vectors[ref]
	}
	if v == nil {
		return false
	}
	for _, c := range chosen {
		w := shared[c.Ref]
		if w == nil {
			w = vectors[c.Ref]
		}
		if w != nil && len(w) == len(v) && NearDuplicateReaches(embed.Dot(v, w)) {
			return true
		}
	}
	return false
}

// rankOf 把分数表排成名次（1 起）
func rankOf(score map[string]float64) map[string]int {
	refs := make([]string, 0, len(score))
	for r := range score {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool {
		if score[refs[i]] != score[refs[j]] {
			return score[refs[i]] > score[refs[j]]
		}
		return refs[i] < refs[j]
	})
	out := map[string]int{}
	for i, r := range refs {
		out[r] = i + 1
	}
	return out
}
