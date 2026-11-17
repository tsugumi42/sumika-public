package main

// 记忆页（第一步，只看不改；规格-记忆页 第四节）：数据流在这个文件，画页面在 memoryview.go。
// 只认 api.MemoryReader：查库、取聊天库原话都在读服务的后台线程，界面线程只并结果。
// 每次连发（换角色、改筛选、连敲搜索词）请求号加一，晚到的旧结果对不上就丢；换了读服务（宿主切 archive / receive）整页清回空。
// 页面上没有任何会改库的按钮；出处原话只留在内存里（最多 20 条，不能选中、不放进剪贴板），换角色、关详情、离开记忆页就丢，不写日志。
// 订阅和在途查询只在"记忆页在前台"时活着（规格 4.5、4.6）：切到别的页、窗口隐藏或最小化就全部取消，回来重新订、重新取；
// 角色、筛选、选中项、详情的标签留在内存里，回来接着看。

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/loadstate"
)

// 面向界面的"种类"和"状态组"选项（键是接口的取值，标签是整词）
type memOption struct{ Key, Label string }

var memKinds = []memOption{{"", "全部"}, {"fact", "事实"}, {"promise", "约定"}, {"preference", "喜好"}, {"boundary", "边界"}, {"episode", "片段"}}
var memFactGroups = []memOption{{"", "全部"}, {"owner", "你确认过"}, {"ai", "她提升的"}, {"said", "你说过、她记的"}, {"imported", "导入的"}, {"candidate", "听说，待确认"}, {"revoked", "已撤销"}, {"superseded", "被取代"}, {"other", "其他"}}
var memEpisodeGroups = []memOption{{"", "全部"}, {"active", "有效"}, {"revoked", "已撤销"}, {"other", "其他"}}
var memScopeKinds = []memOption{{"", "全部来源"}, {"public", "公开来源"}, {"owner", "主人场合来源"}, {"conv", "会话来源"}, {"person", "私聊来源"}}

// groupLabel 是一行/详情里状态组的整词
func groupLabel(g string) string {
	for _, o := range memFactGroups {
		if o.Key == g && g != "" {
			return o.Label
		}
	}
	if g == "active" {
		return "有效"
	}
	return "其他"
}

type memQuote struct {
	phase string // "" 没取 / loading / ok / failed
	q     api.MemoryQuote
	err   string
	load  loadstate.Loader // "取数据中…"这一行晚点才出现（见 loadstate）；结果到了原地改这个结构，不换新的，计时才连得上
}

type memDetail struct {
	ref     string
	req     uint64
	phase   string // loading / ready / failed
	err     string
	d       api.MemoryDetail
	changed bool // 刷新时发现这一条的状态组变了

	evidence          []api.MemoryEvidenceRow
	evNext            api.Cursor
	evMore, evLoading bool
	evTotal           int
	evMissing, evDone bool
	evErr             string
	evLoad            loadstate.Loader // 证据那一行"取数据中…"晚点才出现

	revs             []api.MemoryRevision
	revNext          api.Cursor
	revMore, revBusy bool
	revDone          bool
	revErr           string
	revLoad          loadstate.Loader
	quotes           map[int]*memQuote
	quoteOrder       []int  // quotes 里各条的取用顺序（最早的在前），超过 memQuoteKeep 条时丢最早的
	wasGroup         string // 离开记忆页时这一条的状态组；回来重读后对不上就标"刚刚变了"
}

// memQuoteKeep 出处原话在内存里最多留几条（规格 3.3）
const memQuoteKeep = 20

type memPage struct {
	rd     api.MemoryReader
	gen    uint64             // 代次：换读服务、离开记忆页都加一，在途的旧结果对不上就丢
	cancel context.CancelFunc // 订阅和所有在途查询的总开关；nil = 现在没有订阅（页面不在前台）
	life   context.Context
	resume bool // 这次订阅是"回来"：第一份快照到了就整页重取

	ov     api.MemoryOverview
	haveOv bool
	ovErr  string

	char                           string
	kind, group, scope, conv, subj string // 键；空 = 全部
	query, reported, applied       string

	rows      []api.MemoryRow
	next      api.Cursor
	more      bool
	truncated bool
	phase     string // "" 还没选 / nodb / loading（还没有任何行）/ ready（有行，刷新时旧行留着）/ failed（读失败而且没有行）
	err       string // failed 的原因；ready 时非空 = 刷新失败、显示的是上一次的
	moreBusy  bool
	moreErr   string
	req       uint64 // 请求号：连发时旧结果对不上就丢
	reqCancel context.CancelFunc
	listRev   uint64 // 列表取到时这个角色库的 Rev
	list      ui.ListState
	rowsChar  string // rows 是哪个角色的：换角色后新的没到时旧行还在，不能点（引用是旧角色的）
	toTop     bool   // 新的第一页到了要回到顶（换筛选、换角色）；回到页面时的重取不回顶

	// 加载占位的时机（loadstate）：快就全程不出骨架，旧数据留到新数据到了原地换。详情的放在页上、不放进 memDetail，
	// 换一条时 memDetail 整个重建，占位"已经出现、还没留够"的计时不能跟着丢
	ovLoad, listLoad, moreLoad, recentLoad, detLoad loadstate.Loader

	selRef  string
	tab     int
	prevTab int
	det     memDetail

	showRecent  bool
	recent      []api.MemoryRecentChange
	recentPhase string // "" / loading（还没有行）/ ready（有行，刷新时旧行留着）/ failed
	recentErr   string
}

// attachMemory 换读服务（界面线程）：旧订阅、旧请求全取消，整页清回空；nil = 后台没起来
func (a *app) attachMemory(m api.MemoryReader) {
	p := &a.mem
	if p.rd == m {
		return
	}
	a.memStop()
	*p = memPage{gen: p.gen + 1, rd: m}
	a.memSync()
}

// memSync 让订阅跟着"记忆页在前台"走（界面线程；view 每帧、窗口显示 / 隐藏 / 最小化 / 还原、换读服务时调）：
// 在前台就订，不在就取消订阅和在途查询。订阅一开才会去打开库、才会自动选角色
func (a *app) memSync() {
	p := &a.mem
	want := p.rd != nil && a.rail == 2 && !a.away.Load()
	if want == (p.cancel != nil) {
		return
	}
	if want {
		a.memStart()
	} else {
		a.memStop()
	}
}

func (a *app) memStart() {
	p := &a.mem
	ctx, cancel := context.WithCancel(a.base())
	p.life, p.cancel = ctx, cancel
	p.resume = p.char != ""
	if !p.haveOv {
		p.ovLoad.Begin()
	}
	go a.memWatch(ctx, p.rd, p.gen)
}

// memStop 离开记忆页：订阅和在途查询全取消（订阅那边 Close 会等 worker 退出），代次加一让晚到的结果作废；
// 出处原话马上丢（规格 3.3），其余留着，回来重新取
func (a *app) memStop() {
	p := &a.mem
	if p.cancel == nil {
		return
	}
	p.cancel()
	p.cancel, p.life, p.reqCancel, p.resume = nil, nil, nil, false
	p.gen++
	p.moreBusy = false
	if p.phase == "loading" {
		p.phase = ""
	}
	if p.recentPhase == "loading" {
		p.recentPhase = ""
	}
	p.ovLoad.Cancel()
	p.listLoad.Cancel()
	p.moreLoad.Cancel()
	p.recentLoad.Cancel()
	p.detLoad.Cancel()
	d := &p.det
	if d.phase == "ready" {
		d.wasGroup = d.d.Row.Group
	}
	d.evLoading, d.revBusy = false, false
	d.evLoad.Cancel()
	d.revLoad.Cancel()
	d.quotes, d.quoteOrder = nil, nil
}

// memLife 页面在前台这段时间的 ctx：查询都挂在它下面，离开页面时一起取消
func (a *app) memLife() context.Context {
	if a.mem.life != nil {
		return a.mem.life
	}
	return a.base()
}

// memResume 回到记忆页后第一份快照到了：库可能在你走开时变过（Rev 是每次订阅从头数的，对不上，不能比），
// 所以不比版本，直接重取列表、详情（带"刚刚变了"的比较）和最近变动。旧的行、旧的详情先留着，新的到了原地换掉，不先清空
func (a *app) memResume() {
	p := &a.mem
	a.memRefetchKeep(true)
	if p.selRef != "" {
		if p.det.phase == "ready" {
			p.det.wasGroup = ""
			a.memLoadDetail(true)
		} else {
			p.det = memDetail{ref: p.selRef, req: p.det.req + 1, wasGroup: p.det.wasGroup}
			a.memLoadDetail(false)
		}
	}
	if p.showRecent {
		a.memOpenRecent()
	}
}

func (a *app) memWatch(ctx context.Context, m api.MemoryReader, gen uint64) {
	sub, err := m.WatchMemory(ctx)
	if err != nil {
		a.post(func() {
			if a.mem.gen == gen {
				a.mem.ovErr = "读不到记忆：" + err.Error()
			}
		})
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		a.post(func() { a.memApplyOverview(gen, snap) })
	}
}

func (p *memPage) character(id string) *api.MemoryCharacter {
	for i := range p.ov.Characters {
		if p.ov.Characters[i].ID == id {
			return &p.ov.Characters[i]
		}
	}
	return nil
}

func (a *app) memApplyOverview(gen uint64, snap api.Snapshot[api.MemoryOverview]) {
	p := &a.mem
	if p.gen != gen {
		return
	}
	if snap.Err != nil && !snap.Stale {
		p.ovErr = "读不到记忆：" + snap.Err.Error()
		p.ovLoad.Fail()
		return
	}
	p.ovLoad.Succeed()
	p.ovErr = ""
	if snap.Err != nil {
		p.ovErr = "刷新失败，显示的是上一次的：" + snap.Err.Error()
	}
	p.ov, p.haveOv = snap.Value, true
	resume := p.resume
	p.resume = false
	cur := p.character(p.char)
	if cur == nil {
		pick := ""
		for _, ch := range p.ov.Characters {
			if ch.HasDB {
				pick = ch.ID
				break
			}
		}
		if pick == "" && len(p.ov.Characters) > 0 {
			pick = p.ov.Characters[0].ID
		}
		if pick != "" {
			a.memSelectChar(pick)
		}
		return
	}
	if resume {
		a.memResume()
		return
	}
	// 库在页面开着时变了：列表不自己乱跳。你什么都没点、列表在最顶端、没有选中项时才自动刷新；选中项变了就重读详情
	if p.phase == "ready" && cur.Rev != p.listRev {
		first, _ := p.list.Visible()
		if p.selRef == "" && p.reported == "" && first <= 0 {
			a.memRefetch()
		} else if p.selRef != "" && p.det.phase == "ready" {
			a.memLoadDetail(true)
		}
	}
}

// memSelectChar 换角色：筛选、搜索、选中项全部清回默认
func (a *app) memSelectChar(id string) {
	p := &a.mem
	p.char = id
	p.kind, p.group, p.scope, p.conv, p.subj = "", "", "", "", ""
	p.query, p.reported, p.applied = "", "", ""
	p.selRef, p.det, p.tab, p.prevTab = "", memDetail{}, 0, 0
	p.detLoad.Cancel()
	p.detLoad.Reset()
	p.showRecent, p.recent, p.recentPhase = false, nil, ""
	p.recentLoad.Cancel()
	p.recentLoad.Reset()
	a.memRefetch()
}

func (p *memPage) listReq(cursor api.Cursor) api.MemoryListRequest {
	rq := api.MemoryListRequest{Character: p.char, Kind: p.kind, ScopeKind: p.scope, Conversation: p.conv, Subject: p.subj, Query: strings.TrimSpace(p.reported), Cursor: cursor}
	if p.group != "" {
		rq.Groups = []string{p.group}
	}
	return rq
}

// memRefetch 按当前筛选重取第一页（请求号加一，在途的旧请求作废）
func (a *app) memRefetch() { a.memRefetchKeep(false) }

// memRefetchKeep 重取第一页。已经有行时，旧的行一直留着，新的第一页到了原地整份换掉，不先清空再填；
// 只有真的没有行（第一次进、上一次读失败、这个角色没库）才走"加载中"。resume 为真是回到记忆页时的重取：换上新行后不回到顶
func (a *app) memRefetchKeep(resume bool) {
	p := &a.mem
	if p.reqCancel != nil {
		p.reqCancel()
		p.reqCancel = nil
	}
	p.req++
	req, gen := p.req, p.gen
	p.moreBusy, p.moreErr = false, ""
	p.moreLoad.Cancel()
	p.toTop = !resume
	p.applied = p.reported
	cv := p.character(p.char)
	if cv == nil || !cv.HasDB || p.rd == nil {
		p.rows, p.next, p.more, p.truncated, p.err = nil, "", false, false, ""
		p.list = ui.ListState{}
		p.phase = "nodb"
		p.listLoad.Cancel()
		p.listLoad.Reset()
		return
	}
	p.listRev = cv.Rev
	if p.phase != "ready" {
		p.phase, p.err = "loading", ""
	}
	p.listLoad.Begin()
	ctx, cancel := context.WithCancel(a.memLife())
	p.reqCancel = cancel
	rd, rq := p.rd, p.listReq("")
	go func() {
		res, err := rd.ListMemory(ctx, rq)
		a.post(func() { a.memApplyList(gen, req, res, err, false) })
	}()
}

// memMore 取下一页（滚到离底 10 行时）。同一个请求号：筛选变了，这一页的结果就作废
func (a *app) memMore() {
	p := &a.mem
	if p.phase != "ready" || !p.more || p.moreBusy || p.rd == nil {
		return
	}
	p.moreBusy, p.moreErr = true, ""
	p.moreLoad.Begin()
	req, gen := p.req, p.gen
	ctx := a.memLife()
	if p.reqCancel != nil {
		// 沿用这一次筛选的取消：筛选一变，在途的下一页跟着取消
		ctx2, cancel := context.WithCancel(ctx)
		old := p.reqCancel
		p.reqCancel = func() { old(); cancel() }
		ctx = ctx2
	}
	rd, rq := p.rd, p.listReq(p.next)
	go func() {
		res, err := rd.ListMemory(ctx, rq)
		a.post(func() { a.memApplyList(gen, req, res, err, true) })
	}()
}

func (a *app) memApplyList(gen, req uint64, res api.Result[api.MemoryPage], err error, more bool) {
	p := &a.mem
	if p.gen != gen || p.req != req {
		return // 旧请求晚到：丢掉，不盖新的
	}
	if err != nil || res.State != api.Ready {
		if err == nil {
			err = res.Err
		}
		msg := "没取到"
		if err != nil {
			msg = err.Error()
		}
		if more {
			p.moreBusy, p.moreErr = false, msg
			p.moreLoad.Fail()
			return
		}
		p.err = msg
		if p.phase != "ready" { // 有旧行就留着，页面标"已过期"；没有才是出错
			p.phase = "failed"
		}
		p.listLoad.Fail()
		return
	}
	if more {
		p.moreBusy = false
		p.moreLoad.Succeed()
		p.rows = append(p.rows, res.Value.Items...)
	} else {
		p.phase, p.err = "ready", ""
		p.rows, p.rowsChar = res.Value.Items, p.char
		if p.toTop {
			p.list, p.toTop = ui.ListState{}, false
		}
		p.listLoad.Succeed()
	}
	p.next, p.more, p.truncated = res.Value.NextCursor, res.Value.HasMore, res.Value.Truncated
}

// ───────────── 详情 ─────────────

func (a *app) memSelect(ref string) {
	p := &a.mem
	if p.selRef == ref {
		return
	}
	p.selRef, p.tab, p.prevTab = ref, 0, 0
	p.det = memDetail{ref: ref, req: p.det.req}
	p.detLoad.Reset() // 换成另一条：旧内容不能当这一条显示，晚点才出骨架，之前只留空位
	a.memLoadDetail(false)
}

func (a *app) memCloseDetail() {
	p := &a.mem
	p.selRef, p.det, p.tab, p.prevTab = "", memDetail{req: p.det.req + 1}, 0, 0
	p.detLoad.Cancel()
	p.detLoad.Reset()
}

// memLoadDetail 读详情；refresh 为真是库变了以后的重读：保留已取的证据和修订，状态组变了就标"这条刚刚变了"
func (a *app) memLoadDetail(refresh bool) {
	p := &a.mem
	p.det.req++
	req, gen, ref, char, rd := p.det.req, p.gen, p.selRef, p.char, p.rd
	if !refresh {
		p.det.phase = "loading"
	}
	p.detLoad.Begin()
	go func() {
		res, err := rd.ReadMemoryDetail(a.memLife(), char, ref)
		a.post(func() {
			p := &a.mem
			if p.gen != gen || p.det.req != req || p.selRef != ref {
				return
			}
			if err != nil || res.State != api.Ready {
				if err == nil {
					err = res.Err
				}
				if !refresh {
					p.det.phase, p.det.err = "failed", err.Error()
				}
				p.detLoad.Fail()
				return
			}
			if refresh && p.det.phase == "ready" && p.det.d.Row.Group != res.Value.Row.Group {
				p.det.changed = true
			}
			first := p.det.phase != "ready"
			if first && p.det.wasGroup != "" && p.det.wasGroup != res.Value.Row.Group {
				p.det.changed = true // 走开的时候变了
			}
			p.det.wasGroup = ""
			p.det.phase, p.det.d = "ready", res.Value
			p.detLoad.Succeed()
			if first && strings.HasPrefix(ref, "f:") {
				a.memLoadEvidence(false)
			}
			if (first || p.det.quotes == nil) && p.tab == 1 { // 离开页面时原话丢了，回来重读详情后补取
				a.memLoadQuotes()
			}
		})
	}()
}

func (a *app) memLoadEvidence(more bool) {
	p := &a.mem
	if p.det.evLoading {
		return
	}
	p.det.evLoading, p.det.evErr = true, ""
	p.det.evLoad.Begin()
	cursor := api.Cursor("")
	if more {
		cursor = p.det.evNext
	}
	req, gen, ref, char, rd := p.det.req, p.gen, p.selRef, p.char, p.rd
	go func() {
		res, err := rd.ReadMemoryEvidence(a.memLife(), char, ref, cursor)
		a.post(func() {
			p := &a.mem
			if p.gen != gen || p.selRef != ref || p.det.req < req {
				return
			}
			p.det.evLoading = false
			if err != nil || res.State != api.Ready {
				if err == nil {
					err = res.Err
				}
				p.det.evErr = err.Error()
				p.det.evLoad.Fail()
				return
			}
			p.det.evLoad.Succeed()
			p.det.evDone, p.det.evMissing, p.det.evTotal = true, res.Value.Missing, res.Value.Total
			if more {
				p.det.evidence = append(p.det.evidence, res.Value.Items...)
			} else {
				p.det.evidence = res.Value.Items
			}
			p.det.evNext, p.det.evMore = res.Value.NextCursor, res.Value.HasMore
		})
	}()
}

func (a *app) memLoadRevisions(more bool) {
	p := &a.mem
	if p.det.revBusy {
		return
	}
	p.det.revBusy, p.det.revErr = true, ""
	p.det.revLoad.Begin()
	cursor := api.Cursor("")
	if more {
		cursor = p.det.revNext
	}
	req, gen, ref, char, rd := p.det.req, p.gen, p.selRef, p.char, p.rd
	go func() {
		res, err := rd.ReadMemoryRevisions(a.memLife(), char, ref, cursor)
		a.post(func() {
			p := &a.mem
			if p.gen != gen || p.selRef != ref || p.det.req < req {
				return
			}
			p.det.revBusy = false
			if err != nil || res.State != api.Ready {
				if err == nil {
					err = res.Err
				}
				p.det.revErr = err.Error()
				p.det.revLoad.Fail()
				return
			}
			p.det.revLoad.Succeed()
			p.det.revDone = true
			if more {
				p.det.revs = append(p.det.revs, res.Value.Items...)
			} else {
				p.det.revs = res.Value.Items
			}
			p.det.revNext, p.det.revMore = res.Value.NextCursor, res.Value.HasMore
		})
	}()
}

// memOnTab 切到出处 / 变化页时才取（慢的块不挡快的）
func (a *app) memOnTab() {
	p := &a.mem
	if p.det.phase != "ready" {
		return
	}
	switch p.tab {
	case 1:
		a.memLoadQuotes()
	case 2:
		if !p.det.revDone && strings.HasPrefix(p.selRef, "f:") {
			a.memLoadRevisions(false)
		}
	}
}

// memLoadQuotes 出处原话：详情打开出处页时取前 3 条，其余点开才取（规格-记忆页 3.3、4.4）
func (a *app) memLoadQuotes() {
	p := &a.mem
	n := min(len(p.det.d.Sources), 3)
	for i := 0; i < n; i++ {
		a.memLoadQuote(i)
	}
}

func (a *app) memLoadQuote(i int) {
	p := &a.mem
	if i < 0 || i >= len(p.det.d.Sources) {
		return
	}
	if p.det.quotes == nil {
		p.det.quotes = map[int]*memQuote{}
	}
	if q := p.det.quotes[i]; q != nil && q.phase != "" && q.phase != "failed" {
		return
	}
	q := &memQuote{phase: "loading"}
	q.load.Begin()
	p.det.quotes[i] = q
	// 内存里最多留 20 条原话（规格 3.3）：超出的从最早取的开始丢，丢掉的那条回到"看这一句"
	d := &p.det
	d.quoteOrder = append(slices.DeleteFunc(d.quoteOrder, func(k int) bool { return k == i }), i)
	for len(d.quoteOrder) > memQuoteKeep {
		delete(d.quotes, d.quoteOrder[0])
		d.quoteOrder = d.quoteOrder[1:]
	}
	src, req, gen, ref, char, rd := p.det.d.Sources[i], p.det.req, p.gen, p.selRef, p.char, p.rd
	go func() {
		res, err := rd.ReadMemoryQuote(a.memLife(), char, src)
		a.post(func() {
			p := &a.mem
			if p.gen != gen || p.selRef != ref || p.det.req < req {
				return
			}
			q := p.det.quotes[i]
			if q == nil {
				return // 等的时候被丢了（超过 20 条、关了详情、离开了页面）：结果也不留
			}
			if err != nil || res.State != api.Ready {
				if err == nil {
					err = res.Err
				}
				q.phase, q.err = "failed", err.Error()
				q.load.Fail()
				return
			}
			q.phase, q.q = "ok", res.Value // 原地改，不换新的结构：计时连得上
			q.load.Succeed()
		})
	}()
}

// ───────────── 最近变动 ─────────────

func (a *app) memOpenRecent() {
	p := &a.mem
	p.showRecent, p.recentErr = true, ""
	if p.recentPhase != "ready" { // 已经有这个角色的旧列表就留着，新的到了原地换
		p.recentPhase = "loading"
	}
	p.recentLoad.Begin()
	gen, char, rd := p.gen, p.char, p.rd
	go func() {
		res, err := rd.ReadMemoryRecent(a.memLife(), char)
		a.post(func() {
			p := &a.mem
			if p.gen != gen || p.char != char || !p.showRecent {
				return
			}
			if err != nil || res.State != api.Ready {
				if err == nil {
					err = res.Err
				}
				p.recentErr = err.Error()
				if p.recentPhase != "ready" {
					p.recentPhase = "failed"
				}
				p.recentLoad.Fail()
				return
			}
			p.recentPhase, p.recent, p.recentErr = "ready", res.Value, ""
			p.recentLoad.Succeed()
		})
	}()
}

// memTime 毫秒时间 → 看得懂的一行；0 = 不详
func memTime(ms int64) string {
	if ms <= 0 {
		return "时间不详"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}
