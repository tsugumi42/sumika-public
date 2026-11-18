package main

// 记忆页怎么画（规格-记忆页 4.6）。控件走 legacy_choice.go 的入口（新界面是 winshell 的标准控件）：筛选片、防抖搜索框、键值下拉、
// 详情的分段和“名字 + 值”、空态和错误态。变化过程用 CrychicUI data.AuditTrail；加载占位按 loadstate 的规则（延迟出现、旧数据先留着），骨架在 memskeleton.go。
// 列表本身用 mygo 的 ui.List（虚拟的，行数多了也只建看得见的几行）：CrychicUI 的 InfiniteList 把每一行都建出来，不适合上千条；
// 行的排法照 chat.ConversationList（按钮底、悬停和选中色）。
// 页面没有任何会改库的按钮，也不写"敬请期待"。窄窗口（内容宽 < 1020 逻辑像素）时详情盖在列表上。

import (
	"fmt"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/data"
	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/ELY-Open-Source/CrychicUI/layout"
	"github.com/ELY-Open-Source/CrychicUI/typography"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/loadui"
	"sumika-native/internal/winshell"
)

const (
	memNarrow   = 1020 // 内容区比这窄，详情盖在列表上
	memFilterW  = 300
	memDetailW  = 420
	memMoreRows = 10 // 滚到离底这么多行就取下一页
)

func (a *app) memoryView(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	root := ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch)
	narrow := false
	if w := root.LayoutBounds().W; w > 0 && w < memNarrow {
		narrow = true
	}
	root.Children(func() {
		if p.rd == nil {
			ui.Column(c).Grow(1).Padding(t.Space(8)).Children(func() {
				a.pageTitle(c, "记忆")
				a.emptyState(c, "brain", "后台还没起来", "记忆页要等后台打开以后才能看。状态区会写原因。", "")
			})
			return
		}
		a.memFilters(c)
		switch {
		case narrow && p.selRef != "":
			a.memDetailCol(c, true)
		case narrow:
			a.memListCol(c)
		default:
			a.memListCol(c)
			a.memDetailCol(c, false)
		}
	})
	_ = t
}

// ───────────── 左栏：角色、搜索、筛选 ─────────────

// uniqLabels 保证 ChipChoice / Dropdown 的标签两两不同（组件遇到重复会 panic）
func uniqLabels(labels []string) []string {
	seen := map[string]int{}
	out := make([]string, len(labels))
	for i, l := range labels {
		seen[l]++
		if n := seen[l]; n > 1 {
			l = fmt.Sprintf("%s（%d）", l, n)
		}
		out[i] = l
	}
	return out
}

// pickChip：把键翻成标签、交给 ChipChoice、再把选中的标签翻回键。标签带数字会变，所以每次现翻（组件要求选中项必须在选项里）
func (a *app) pickChip(c *ui.Context, label string, opts []memOption, labels []string, key *string) bool {
	idx := 0
	for i, o := range opts {
		if o.Key == *key {
			idx = i
		}
	}
	sel := labels[idx]
	a.chipChoice(c, "chips-"+label, label, labels, &sel)
	if sel != labels[idx] {
		for i, l := range labels {
			if l == sel {
				*key = opts[i].Key
				return true
			}
		}
	}
	return false
}

func memCount(cv *api.MemoryCharacter, kind, group string) int {
	n := 0
	for _, k := range cv.Counts {
		if (kind == "" && k.Kind != "episode" || kind != "" && k.Kind == kind) && (group == "" || k.Group == group) {
			n += k.N
		}
	}
	return n
}

func (a *app) memFilters(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	ui.Column(c).Width(memFilterW).Shrink(0).Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Padding(0, t.Space(4)).Children(func() { a.pageTitle(c, "记忆") })
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, t.Space(4), t.Space(6), t.Space(4)).Gap(t.Space(4)).Children(func() {
				if p.ovErr != "" {
					typography.Caption(c, p.ovErr).TextColor(t.Danger).Wrap()
				}
				if !p.haveOv {
					if p.ovErr == "" {
						ov := p.ovLoad.View(c.Now())
						loadui.Wake(c, ov)
						loadui.Line(ov, "取数据中…", func(s string) ui.Element { return typography.Caption(c, s) })
					}
					return
				}
				if len(p.ov.Characters) == 0 {
					typography.Caption(c, "还没有角色")
					return
				}
				a.memCharChips(c)
				cv := p.character(p.char)
				if cv == nil || !cv.HasDB {
					return
				}
				c.Key("mem-search")
				a.debouncedSearch(c, "mem-search-field", "搜索", &p.query, &p.reported, "她记的话、人的名字", "稍等一下…", 250*time.Millisecond)
				if p.reported != p.applied {
					a.memRefetch()
				}
				a.memKindGroupChips(c, cv)
				a.memDrops(c, cv)
				if a.ghostButton(c, "最近变动") {
					a.memOpenRecent()
				}
				a.memNotices(c, cv)
			})
		})
	})
}

func (a *app) memCharChips(c *ui.Context) {
	p := &a.mem
	opts := make([]memOption, len(p.ov.Characters))
	labels := make([]string, len(p.ov.Characters))
	for i, ch := range p.ov.Characters {
		opts[i].Key = ch.ID
		switch {
		case !ch.HasDB:
			labels[i] = ch.Name + " · 还没有记忆库"
		case ch.Err != "":
			labels[i] = ch.Name + " · 读不了"
		default:
			labels[i] = fmt.Sprintf("%s · %d", ch.Name, ch.Total)
		}
	}
	labels = uniqLabels(labels)
	c.Key("mem-char")
	key := p.char
	if a.pickChip(c, "角色", opts, labels, &key) {
		a.memSelectChar(key)
	}
}

func (a *app) memKindGroupChips(c *ui.Context, cv *api.MemoryCharacter) {
	p := &a.mem
	kl := make([]string, len(memKinds))
	for i, o := range memKinds {
		kl[i] = fmt.Sprintf("%s %d", o.Label, memCount(cv, o.Key, ""))
	}
	c.Key("mem-kind")
	key := p.kind
	if a.pickChip(c, "种类", memKinds, uniqLabels(kl), &key) {
		p.kind, p.group = key, "" // 片段和事实的状态组不一样
		a.memRefetch()
	}
	groups := memFactGroups
	if p.kind == "episode" {
		groups = memEpisodeGroups
	}
	gl := make([]string, len(groups))
	for i, o := range groups {
		gl[i] = fmt.Sprintf("%s %d", o.Label, memCount(cv, p.kind, o.Key))
	}
	c.Key("mem-group-" + p.kind)
	gkey := p.group
	if a.pickChip(c, "状态", groups, uniqLabels(gl), &gkey) {
		p.group = gkey
		a.memRefetch()
	}
}

func (a *app) memDrops(c *ui.Context, cv *api.MemoryCharacter) {
	p := &a.mem
	sl := make([]string, len(memScopeKinds))
	sv := make([]string, len(memScopeKinds))
	for i, o := range memScopeKinds {
		sl[i], sv[i] = o.Label, o.Key
	}
	c.Key("mem-scope")
	key := p.scope
	if a.pickDrop(c, "来源范围", sv, sl, &key, true) {
		p.scope = key
		a.memRefetch()
	}
	if p.kind != "episode" {
		if len(cv.Subjects) > 0 {
			vals, labs := []string{""}, []string{"关于谁：全部"}
			for _, s := range cv.Subjects {
				vals, labs = append(vals, s.Value), append(labs, fmt.Sprintf("%s · %d", s.Label, s.N))
			}
			c.Key("mem-subj")
			k := p.subj
			if a.pickDrop(c, "关于谁", vals, uniqLabels(labs), &k, true) {
				p.subj = k
				a.memRefetch()
			}
		}
	}
	if len(cv.Conversations) > 0 {
		vals, labs := []string{""}, []string{"会话：全部"}
		for _, s := range cv.Conversations {
			vals, labs = append(vals, s.Value), append(labs, fmt.Sprintf("%s · %d", s.Label, s.N))
		}
		c.Key("mem-conv")
		k := p.conv
		if a.pickDrop(c, "来自会话", vals, uniqLabels(labs), &k, true) {
			p.conv = k
			a.memRefetch()
		}
	}
}

func (a *app) memNotices(c *ui.Context, cv *api.MemoryCharacter) {
	t := c.Theme()
	if !cv.Migrated {
		typography.Caption(c, "这个库还没迁移：放行者、证据和修订记录要迁移以后才有，下面的状态按「导入的」显示。").TextColor(t.Danger).Wrap()
	}
	if q := cv.Quarantine; q.Active {
		typography.Caption(c, fmt.Sprintf("止血线开着：她想不起 %d 条事实、%d 条待确认、%d 个片段（库里照样留着）。", q.Facts, q.Candidates, q.Episodes)).Wrap()
	}
	if cv.Err != "" {
		typography.Caption(c, "这个库现在读不了："+cv.Err).TextColor(t.Danger).Wrap()
	}
}

// ───────────── 中栏：列表 ─────────────

func (a *app) memListCol(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	// 这一帧的加载占位该怎么画：还没选角色时看概览有没有到，选了以后看列表
	lv := p.listLoad.View(c.Now())
	if p.phase == "" && !p.haveOv && p.ovErr == "" {
		lv = p.ovLoad.View(c.Now())
	}
	loadui.Wake(c, lv)
	ui.Column(c).Grow(1).MinWidth(0).Children(func() {
		sub := "\u3000" // 一个全角空格：没有话要说时也占着这一行，副标题出现前后标题栏高度不变
		switch p.phase {
		case "ready":
			sub = fmt.Sprintf("已列出 %d 条", len(p.rows))
			if p.more {
				sub += "，往下滚还有"
			}
			if p.truncated {
				sub = "只显示前 200 条，换个更具体的词"
			}
			if lv.Refreshing {
				sub += " · 刷新中…"
			}
		case "loading":
			if lv.Skeleton {
				sub = "取数据中…"
			}
		}
		ui.Column(c).Padding(0, t.Space(5)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			title := "她记了什么"
			if p.showRecent {
				title = "最近变动"
			}
			layout.PageHeader(c, title, layout.PageHeaderOptions{Subtitle: sub, Actions: func() {
				if p.showRecent && a.ghostButton(c, "回到列表") {
					p.showRecent = false
				}
			}})
		})
		if cv := p.character(p.char); cv != nil && p.phase == "ready" && cv.Rev > p.listRev && !p.showRecent {
			if a.ghostButton(c, fmt.Sprintf("有 %d 处变化，点这里刷新", cv.Rev-p.listRev)) {
				a.memRefetch()
			}
		}
		if lv.Stale && p.err != "" && !p.showRecent {
			a.memStaleNote(c, p.err)
		}
		switch {
		case p.showRecent:
			a.memRecent(c)
		case p.phase == "nodb":
			ui.Column(c).Grow(1).Center().Children(func() {
				a.emptyState(c, "database", "这个角色还没有记忆库", "她有了记忆库以后，这里才会有东西。", "")
			})
		case lv.Skeleton || p.phase == "loading" || (p.phase == "" && !p.haveOv && p.ovErr == ""):
			memListSkeleton(c, lv.Skeleton) // 没到出现的时候也排好位置、不画，到点只是画出来
		case p.phase == "failed":
			ui.Column(c).Grow(1).Center().Children(func() {
				retry := a.errorState(c, "没取到记忆", p.err, "重试")
				if retry {
					a.memRefetch()
				}
			})
		case len(p.rows) == 0:
			ui.Column(c).Grow(1).Center().Children(func() {
				switch {
				case strings.TrimSpace(p.reported) != "":
					a.emptyState(c, "search-x", "搜不到", "换个更短或更具体的词试试；搜索会连已撤销的一起找。", "")
				case p.kind != "" || p.group != "" || p.scope != "" || p.conv != "" || p.subj != "":
					a.emptyState(c, "filter", "筛选后没有", "放宽一点筛选条件试试。", "")
				default:
					a.emptyState(c, "inbox", "她还没有记忆", "这个角色的记忆库是空的。", "")
				}
			})
		default:
			a.memRows(c)
		}
	})
}

func (a *app) memRows(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	p.list.Key = func(i int) any {
		if i < 0 || i >= len(p.rows) {
			return i
		}
		return p.rows[i].Ref
	}
	ui.List(c, &p.list, len(p.rows), func(i int) { a.memRow(c, i) }).Grow(1).Padding(t.Space(2), t.Space(4))
	if _, last := p.list.Visible(); p.more && !p.moreBusy && p.moreErr == "" && last >= len(p.rows)-memMoreRows {
		a.memMore() // 离底 10 行：取下一页（moreBusy 守着一次只取一页；失败了不自己重试，等点"点这里重试"）
	}
	if p.more || p.moreBusy { // 还有下一页就一直占着这一行，"取下一页…"出现前后列表高度不变
		mv := p.moreLoad.View(c.Now())
		loadui.Wake(c, mv)
		loadui.Line(mv, "取下一页…", func(s string) ui.Element { return typography.Caption(c, s) }).Padding(t.Space(2), t.Space(5))
	}
	if p.moreErr != "" {
		ui.Row(c).Gap(t.Space(2)).Padding(t.Space(1), t.Space(5)).AlignItems(ui.Center).Children(func() {
			typography.Caption(c, "没取到下一页："+p.moreErr).TextColor(t.Danger)
			if a.ghostButton(c, "点这里重试") {
				a.memMore()
			}
		})
	}
}

// scopeBrief：一行里的范围简写——第一个范围的标签，多于一个加"等 N 个"
func scopeBrief(ss []api.MemoryScope) string {
	if len(ss) == 0 {
		return "来源没记"
	}
	if len(ss) == 1 {
		return ss[0].Label
	}
	return fmt.Sprintf("%s 等 %d 个", ss[0].Label, len(ss))
}

func (a *app) memRow(c *ui.Context, i int) {
	t := c.Theme()
	p := &a.mem
	if i >= len(p.rows) {
		return
	}
	r := p.rows[i]
	on := r.Ref == p.selRef
	b := kit.Press(ui.ButtonBase(c).Label(r.Text).Row().FillWidth().Gap(t.Space(3)).AlignItems(ui.Center).Padding(t.Space(2), t.Space(3)).Radius(t.Radius).Checked(on))
	hover := float32(0)
	if b.Hovered() {
		hover = 1
	}
	bg := t.SurfaceHover.Alpha(hover)
	if on {
		bg = t.Accent.Alpha(0.1)
	}
	meta := groupLabel(r.Group)
	if r.Group == "other" {
		meta = "其他：" + r.Status
	}
	meta += " · " + scopeBrief(r.Scopes) + " · " + memTime(r.CreatedAt)
	if r.Expired {
		meta += " · 暂时的，已过期"
	}
	b.Background(b.AnimateColor("bg", bg, ui.Snappy)).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(t.Space(0.5)).Children(func() {
			ui.Text(c, r.Text).SingleLine()
			kit.Muted(c, meta).SingleLine().FontSize(t.Rem(0.8))
		})
	})
	if b.Clicked() && !on && p.rowsChar == p.char { // 刚换了角色、新的行还没到时，眼前是上一个角色的旧行，引用对不上，不响应
		a.memSelect(r.Ref)
	}
}

// memStaleNote 刷新失败、显示的还是上一次的行：标"已过期"，给个重试（和运行页的 staleNote 同一种写法）
func (a *app) memStaleNote(c *ui.Context, reason string) {
	t := c.Theme()
	ui.Row(c).Gap(t.Space(2)).Padding(t.Space(1), t.Space(5)).AlignItems(ui.Center).Children(func() {
		a.badge(c, "已过期", winshell.BadgeWarn)
		kit.Muted(c, "刷新失败："+reason+"；下面是上一次的").FontSize(t.Rem(0.8)).SingleLine()
		if a.ghostButton(c, "重试") {
			a.memRefetchKeep(true)
		}
	})
}

func (a *app) memRecent(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	rv := p.recentLoad.View(c.Now())
	loadui.Wake(c, rv)
	switch {
	case rv.Skeleton || p.recentPhase == "loading":
		memRecentSkeleton(c, rv.Skeleton)
		return
	}
	switch p.recentPhase {
	case "failed":
		ui.Column(c).Grow(1).Center().Children(func() {
			retry := a.errorState(c, "没取到最近变动", p.recentErr, "重试")
			if retry {
				a.memOpenRecent()
			}
		})
	default:
		if len(p.recent) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				a.emptyState(c, "history", "还没有变动记录", "这个库里没有修订记录。", "")
			})
			return
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(t.Space(2), t.Space(4)).Gap(t.Space(1)).Children(func() {
				for i, ch := range p.recent {
					c.Key(i)
					b := kit.Press(ui.ButtonBase(c).Label(ch.Text).Row().FillWidth().Padding(t.Space(2), t.Space(3)).Radius(t.Radius).Disabled(!ch.Exists))
					b.Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Gap(t.Space(0.5)).Children(func() {
							text := ch.Text
							if !ch.Exists {
								text = "（这条已经不在库里了）"
							}
							ui.Text(c, text).SingleLine()
							line := memTime(ch.At) + " · " + ch.Who + " · " + ch.Did
							if ch.Change != "" {
								line += " · " + ch.Change
							}
							kit.Muted(c, line).SingleLine().FontSize(t.Rem(0.8))
						})
					})
					if b.Clicked() && ch.Exists {
						a.memSelect(ch.Ref)
					}
				}
			})
		})
	}
}

// ───────────── 右栏：详情 ─────────────

func (a *app) memDetailCol(c *ui.Context, overlay bool) {
	t := c.Theme()
	p := &a.mem
	col := ui.Column(c).Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border)
	if overlay {
		col.Grow(1).MinWidth(0)
	} else {
		col.Width(memDetailW).Shrink(0)
	}
	col.Children(func() {
		if p.selRef == "" {
			ui.Column(c).Grow(1).Center().Children(func() {
				typography.Caption(c, "点左边的一条，看她凭什么记下它")
			})
			return
		}
		ui.Column(c).Padding(t.Space(3), t.Space(4), 0, t.Space(4)).Gap(t.Space(2)).Children(func() {
			if overlay && a.ghostButton(c, "← 回到列表") {
				a.memCloseDetail()
				return
			}
			c.Key("mem-tabs")
			ui.Row(c.Key("mem-detail-tabs-row")).MaxWidth(420).Children(func() {
				a.segmentedChoice(c, "mem-detail-tabs", "记忆详情", []string{"详情", "出处", "变化"}, &p.tab)
			})
			if p.tab != p.prevTab {
				p.prevTab = p.tab
				a.memOnTab()
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(t.Space(4)).Gap(t.Space(3)).Children(func() {
				if p.det.changed {
					typography.Caption(c, "这条刚刚变了（状态组和刚才不一样），下面已经是最新的。").TextColor(t.Danger).Wrap()
				}
				dv := p.detLoad.View(c.Now())
				loadui.Wake(c, dv)
				switch {
				case p.det.phase == "failed":
					typography.Caption(c, "没取到这一条："+p.det.err).TextColor(t.Danger).Wrap()
				case dv.Skeleton || p.det.phase != "ready":
					memDetailSkeleton(c, dv.Skeleton)
				default:
					switch p.tab {
					case 0:
						a.memDetailFields(c)
					case 1:
						a.memSources(c)
					case 2:
						a.memRevisions(c)
					}
				}
			})
		})
	})
}

func recallText(r api.MemoryRecall) string {
	switch r.Reason {
	case "ok":
		return "她现在想得起"
	case "expired":
		return "暂时的事，已经过期，不再给她"
	case "quarantined":
		return "止血线藏着它，她现在想不起"
	case "revoked":
		return "已撤销，她想不起"
	case "superseded":
		return "被新的取代了，她想不起"
	}
	return "她想不起（状态不认识）"
}

var memPlaceNames = map[string]string{"local-private": "本地私聊", "local-group": "本地群聊", "platform-private": "QQ 私聊", "platform-group": "QQ 群"}

func whoText(w api.MemoryWho) string {
	switch w.Kind {
	case "owner", "self":
		return w.Name
	case "person":
		if w.Name != "" {
			return w.Name + "（" + w.ID + "）"
		}
		return w.ID
	case "other":
		return w.Raw
	}
	return "没写"
}

func (a *app) memDetailFields(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	d := p.det.d
	r := d.Row
	ui.Text(c, r.Text).Selectable().Wrap()
	if r.TextCut > 0 {
		typography.Caption(c, fmt.Sprintf("还有 %d 个字没显示", r.TextCut))
	}
	a.statusBadge(c, recallText(d.Recall), d.Recall.Can)
	status := groupLabel(r.Group)
	if r.Group == "other" {
		status = "其他：" + r.Status
		if r.ConfirmedBy != "" {
			status += "，放行者 " + r.ConfirmedBy
		}
	}
	if r.Kind == "episode" {
		status = groupLabel(r.Group)
	}
	scopes := make([]string, len(r.Scopes))
	for i, s := range r.Scopes {
		scopes[i] = s.Label
	}
	kind := map[string]string{"fact": "事实", "promise": "约定", "preference": "喜好", "boundary": "边界", "episode": "片段"}[r.Kind]
	if kind == "" {
		kind = r.Kind
	}
	items := []display.KeyValuePair{{Key: "状态", Value: status}, {Key: "种类", Value: kind}, {Key: "来源范围", Value: strings.Join(scopes, "、")}}
	if r.Kind != "episode" {
		items = append(items, display.KeyValuePair{Key: "关于谁", Value: whoText(r.Subject)})
		if r.AttributedTo.Kind != "none" {
			items = append(items, display.KeyValuePair{Key: "谁说的", Value: whoText(r.AttributedTo)})
		}
	}
	if r.Conversation != "" {
		items = append(items, display.KeyValuePair{Key: "来自会话", Value: r.Conversation})
	}
	items = append(items, display.KeyValuePair{Key: "记下的时间", Value: memTime(r.CreatedAt)})
	if r.LearnedAt > 0 && r.LearnedAt != r.CreatedAt {
		items = append(items, display.KeyValuePair{Key: "学到的时间", Value: memTime(r.LearnedAt)})
	}
	if r.Kind == "episode" {
		items = append(items, display.KeyValuePair{Key: "覆盖的句子", Value: fmt.Sprintf("第 %d 到 %d 句", r.FromSeq, r.ToSeq)})
	}
	if d.Origin != "" {
		items = append(items, display.KeyValuePair{Key: "出处是谁的话", Value: d.Origin})
	}
	if d.Whisper != "" {
		items = append(items, display.KeyValuePair{Key: "来自悄悄话", Value: d.Whisper})
	}
	a.keyValues(c, "mem-detail-fields", items)
	if d.SupersededBy != "" {
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			typography.Caption(c, "被另一条取代了")
			if a.ghostButton(c, "看取代它的那条") {
				a.memSelect(d.SupersededBy)
			}
		})
	}
	if o := d.Occasions; o != nil {
		line := fmt.Sprintf("已攒 %d / %d 次，其中本人说的 %d 次", o.Have, o.Need, o.Self)
		if o.Auto && o.Self >= 1 {
			line += "；攒够就会自己提升成有效"
		} else if !o.Auto {
			line += "；没开自动提升，要你亲口放行"
		}
		typography.Caption(c, line).Wrap()
	}
	typography.Caption(c, "她在哪些场合想得起")
	for _, pl := range d.Places {
		c.Key("place-" + pl.Place)
		txt := memPlaceNames[pl.Place] + "：" + map[string]string{"yes": "想得起", "no": "想不起", "when": "要看情况"}[pl.Visible]
		if pl.Visible == "when" {
			txt += "（" + strings.Join(pl.When, "、") + "）"
		}
		kit.Muted(c, txt).Wrap()
	}
	if !d.Migrated {
		typography.Caption(c, "这个库还没迁移：没有放行者、证据和修订。").Wrap()
	}
	if strings.HasPrefix(r.Ref, "f:") {
		a.memEvidence(c)
	}
}

func (a *app) memEvidence(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	ui.Text(c, "证据").FontWeight(500)
	switch {
	case p.det.evMissing:
		typography.Caption(c, "旧库没有证据记录")
	case p.det.evErr != "":
		typography.Caption(c, "没取到证据："+p.det.evErr).TextColor(t.Danger).Wrap()
	case !p.det.evDone:
		ev := p.det.evLoad.View(c.Now())
		loadui.Wake(c, ev)
		loadui.Line(ev, "取数据中…", func(s string) ui.Element { return typography.Caption(c, s) })
	case len(p.det.evidence) == 0:
		typography.Caption(c, "没有证据记录")
	default:
		for i, e := range p.det.evidence {
			c.Key(i)
			who := whoText(e.Speaker)
			if e.BySubject {
				who += "（本人说的）"
			}
			kit.Muted(c, fmt.Sprintf("%s · %s · %s", e.Day, e.Conversation.Label, who)).Wrap().FontSize(t.Rem(0.85))
		}
		if p.det.evMore && a.ghostButton(c, fmt.Sprintf("再显示 20 行（共 %d 行）", p.det.evTotal)) {
			a.memLoadEvidence(true)
		}
	}
}

func (a *app) memSources(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	d := p.det.d
	if len(d.Sources) == 0 {
		typography.Caption(c, "没有记下出处")
		return
	}
	if d.SourcesTotal > 3 && d.Row.Kind != "episode" {
		typography.Caption(c, fmt.Sprintf("这条是整批总结的，共 %d 句，下面按顺序列出", d.SourcesTotal)).Wrap()
	}
	if d.Row.Kind == "episode" {
		typography.Caption(c, fmt.Sprintf("片段覆盖第 %d 到 %d 句，只列头尾几句", d.Row.FromSeq, d.Row.ToSeq)).Wrap()
	}
	for i, s := range d.Sources {
		c.Key(i)
		ui.Column(c).FillWidth().Gap(t.Space(1)).Padding(t.Space(2), t.Space(3)).Border(1, t.Border).Radius(t.Radius).Children(func() {
			kit.Muted(c, fmt.Sprintf("%s · 第 %d 句", s.Label, s.Seq)).FontSize(t.Rem(0.8)).Wrap()
			q := p.det.quotes[i]
			switch {
			case q == nil || q.phase == "":
				if a.ghostButton(c, "看这一句") {
					a.memLoadQuote(i)
				}
			case q.phase == "loading":
				qv := q.load.View(c.Now())
				loadui.Wake(c, qv)
				loadui.Line(qv, "取数据中…", func(s string) ui.Element { return typography.Caption(c, s) })
			case q.phase == "failed":
				typography.Caption(c, "没取到："+q.err).TextColor(t.Danger).Wrap()
			default:
				a.memQuoteBody(c, q.q)
			}
		})
	}
}

func (a *app) memQuoteBody(c *ui.Context, q api.MemoryQuote) {
	switch q.Status {
	case "ok":
		ui.Text(c, q.Text).Wrap() // 不做 Selectable：原话不进剪贴板（规格 3.3）
		kit.Muted(c, whoText(q.Who)+" · "+memTime(q.At)).FontSize(c.Theme().Rem(0.8))
	case "recalled":
		typography.Caption(c, "这句话后来被撤回了")
	case "unreadable":
		typography.Caption(c, "现在读不到 "+q.Platform+" 的聊天记录")
	default:
		typography.Caption(c, "原话找不到了")
	}
}

func (a *app) memRevisions(c *ui.Context) {
	t := c.Theme()
	p := &a.mem
	if !strings.HasPrefix(p.selRef, "f:") {
		typography.Caption(c, "片段没有修订记录，只有状态。")
		return
	}
	switch {
	case p.det.revErr != "":
		typography.Caption(c, "没取到修订："+p.det.revErr).TextColor(t.Danger).Wrap()
	case !p.det.revDone:
		rv := p.det.revLoad.View(c.Now())
		loadui.Wake(c, rv)
		loadui.Line(rv, "取数据中…", func(s string) ui.Element { return typography.Caption(c, s) })
	case len(p.det.revs) == 0:
		typography.Caption(c, "没有修订记录")
	default:
		audits := make([]data.Audit, len(p.det.revs))
		for i, r := range p.det.revs {
			what := r.Did
			if r.Change != "" {
				what += "：" + r.Change
			}
			if r.Reason != "" {
				what += "（" + r.Reason + "）"
			}
			audits[i] = data.Audit{Who: r.Who, What: what, When: memTime(r.At)}
		}
		data.AuditTrail(c, "变化过程", audits)
		if p.det.revMore && a.ghostButton(c, "再显示 20 行") {
			a.memLoadRevisions(true)
		}
	}
}
