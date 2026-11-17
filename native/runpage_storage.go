package main

// 存储标签（规格-运行页 第五节）：概览（总占用、上次统计、重新统计），按类别的表（行可展开到聊天库按平台、记忆库按角色）。
// 数据来自 api.StatsReader.Storage：小目录是现量的，大目录是后台统计的快照，媒体是快照加增量，数字旁边的"约"由读服务的 Approx 说了算。
// 还没统计过的大目录写"未统计"，不写 0。这一步没有任何清理按钮（第 6 步才做）；"打开所在文件夹"需要读服务给出可打开的位置，
// 现在的 StorageReport 里没有路径（报告里不放路径是第 2 步定的），所以这一步也没有，登记在规格第十二节第 3 步段落。

import (
	"fmt"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/loadui"
)

const storageTableMaxRows = 14

// storageRow 是表里的一行：一个类别，或类别下的一个分行
type storageRow struct {
	label  string
	note   string
	rule   string
	auto   bool
	bytes  int64
	files  int64
	absent bool // 位置不存在：不是没统计，也不是出错
	known  bool // 有数字：现量的或有快照的；没统计过的大目录是 false，写"未统计"，不写 0
	apx    bool
	kids   []string
}

func storageRows(rep ctlproto.StorageReport, sorter ui.SortOrder) (map[string]*storageRow, []string) {
	rows := map[string]*storageRow{}
	roots := make([]string, 0, len(rep.Categories))
	for _, c := range rep.Categories {
		r := &storageRow{label: c.Label, rule: c.Rule, auto: c.Auto, bytes: c.Bytes, files: c.Files, apx: c.Approx,
			known: c.Source != ctlproto.SourceNone && !c.Absent, absent: c.Absent}
		var notes []string
		if c.Absent {
			notes = append(notes, "位置不存在")
		}
		if c.Links > 0 {
			notes = append(notes, fmt.Sprintf("%d 个联接没跟进去", c.Links))
		}
		if c.Errors > 0 {
			notes = append(notes, fmt.Sprintf("%d 处读不了", c.Errors))
		}
		for i, n := range notes {
			if i > 0 {
				r.note += "，"
			}
			r.note += n
		}
		for _, it := range c.Items {
			k := c.Key + "/" + it.Name
			rows[k] = &storageRow{label: it.Name, bytes: it.Bytes, files: it.Files, known: r.known}
			r.kids = append(r.kids, k)
		}
		sort.SliceStable(r.kids, func(i, j int) bool { return rows[r.kids[i]].bytes > rows[r.kids[j]].bytes })
		rows[c.Key] = r
		roots = append(roots, c.Key)
	}
	desc := sorter.Descending
	less := func(a, b *storageRow) bool {
		switch sorter.Column {
		case "name":
			return a.label < b.label
		case "files":
			return a.files < b.files
		}
		return a.bytes < b.bytes
	}
	sort.SliceStable(roots, func(i, j int) bool {
		a, b := rows[roots[i]], rows[roots[j]]
		if a.known != b.known { // 没统计过的沉底
			return a.known
		}
		if desc {
			return less(b, a)
		}
		return less(a, b)
	})
	return rows, roots
}

// storageModel 同 usageModel：存储的行按数据版本和排序算一次
type storageModel struct {
	ok     bool
	ver    uint64
	sort   ui.SortOrder
	rows   map[string]*storageRow
	roots  []string
	builds int
}

func (p *runPage) storageModelOf(rep ctlproto.StorageReport) *storageModel {
	m := &p.storeTbl.cache
	if m.ok && m.ver == p.store.ver && m.sort == p.storeTbl.sort {
		return m
	}
	builds := m.builds + 1
	*m = storageModel{ok: true, ver: p.store.ver, sort: p.storeTbl.sort, builds: builds}
	m.rows, m.roots = storageRows(rep, p.storeTbl.sort)
	return m
}

func (a *app) storageView(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	b := &p.store
	if p.storeTbl.sort.Column == "" {
		p.storeTbl.sort = ui.SortOrder{Column: "bytes", Descending: true}
	}
	v := b.load.View(c.Now())
	loadui.Wake(c, v)
	switch {
	case p.src == nil:
		a.runUnavailable(c, "后台还没起来", "存储要等后台打开以后才能看。状态标签里写着原因。")
		return
	case !b.has && b.unavailable():
		a.runUnavailable(c, "存储暂时看不了", b.err.Error())
		return
	case !b.has && b.err != nil:
		retry := a.errorState(c, "没取到存储统计", b.err.Error(), "重试")
		if retry {
			a.runFetchStorage()
		}
		return
	case !b.has || v.Skeleton: // 慢了才出骨架；骨架出了以后数据早到也留够时间再换（loadstate）
		a.runSkeleton(c, v.Skeleton, false, 3)
		return
	}
	rep := b.data
	model := p.storageModelOf(rep)
	rows, roots := model.rows, model.roots
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(t.Space(4), 0).Gap(t.Space(4)).MaxWidth(1280).Children(func() {
			if b.stale() {
				a.staleNote(c, b.err.Error(), b.at)
			}
			a.storageSummary(c, rep)
			a.storageTable(c, rep, rows, roots)
		})
	})
}

func (a *app) storageSummary(c *ui.Context, rep ctlproto.StorageReport) {
	t := c.Theme()
	p := &a.run
	ui.Row(c).Wrap().Gap(t.Space(8)).GapY(t.Space(3)).AlignItems(ui.End).Children(func() {
		total := fmtBytes(rep.Total)
		if rep.NeverScanned || rep.DeltaBytes > 0 {
			total = "约 " + total
		}
		statCell(c, "数据目录总占用", total, false)
		switch {
		case rep.NeverScanned:
			statCell(c, "上次统计", "还没有统计过", false)
		default:
			statCell(c, "上次统计", time.UnixMilli(rep.ScannedAtMS).Format("01-02 15:04")+"，用时 "+fmtMS(rep.ScanMs), false)
		}
		if rep.DeltaBytes > 0 {
			statCell(c, "统计之后媒体新增", fmtBytes(rep.DeltaBytes), false)
		}
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			switch {
			case rep.Scan.Running:
				mutedText(c, "正在统计，已处理 "+fmtCount(rep.Scan.Entries)+" 项")
				if p.scan != nil && a.ghostButton(c, "取消统计") {
					a.runCancelScan()
				}
			case p.scan != nil:
				label := "重新统计"
				if rep.NeverScanned {
					label = "开始统计"
				}
				if a.ghostButton(c, label) {
					a.runRescan()
				}
			}
			if !rep.Scan.Running && a.ghostButton(c, "刷新") {
				a.runFetchStorage()
			}
			if p.store.load.View(c.Now()).Refreshing {
				mutedText(c, "刷新中…")
			} else if !p.store.at.IsZero() {
				mutedText(c, "更新于 "+p.store.at.Format("15:04:05"))
			}
		})
	})
}

func (a *app) storageTable(c *ui.Context, rep ctlproto.StorageReport, rows map[string]*storageRow, roots []string) {
	t := c.Theme()
	tbl := &a.run.storeTbl
	cols := []ui.TableColumn{
		{Title: "类别", ID: "name", MinWidth: 150, Sortable: true},
		{Title: "占用", ID: "bytes", Width: 110, Align: ui.End, Sortable: true},
		{Title: "文件数", ID: "files", Width: 90, Align: ui.End, Sortable: true},
		{Title: "占比", ID: "share", Width: 150},
		{Title: "清理规则", ID: "rule", MinWidth: 260},
	}
	tbl.out.List.Sort = &tbl.sort
	n := tbl.out.Rows()
	if n == 0 {
		n = len(roots)
	}
	h := float32(min(n, storageTableMaxRows)+1)*t.Space(8) + 2
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(t.Radius).Clip().Children(func() {
		ui.OutlineTable(c, &tbl.out, cols, roots, func(k string) []string { return rows[k].kids }, func(k string, col int) {
			r := rows[k]
			switch col {
			case 0:
				ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, r.label).SingleLine().Tooltip(r.label)
					if r.note != "" {
						ui.Text(c, r.note).TextColor(t.TextMuted).FontSize(t.Rem(0.85)).SingleLine()
					}
				})
			case 1:
				switch {
				case r.absent:
					mutedText(c, "—")
				case !r.known:
					mutedText(c, "未统计")
				case r.apx:
					numText(c, "约 "+fmtBytes(r.bytes))
				default:
					numText(c, fmtBytes(r.bytes))
				}
			case 2:
				if r.known {
					numText(c, fmtCount(r.files))
				} else {
					mutedText(c, "—")
				}
			case 3:
				if !r.known || rep.Total <= 0 {
					return
				}
				frac := float64(r.bytes) / float64(rep.Total)
				ui.Row(c).FillWidth().Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Grow(1).MinWidth(0).Children(func() { barOf(c, frac, 8) })
					ui.Text(c, fmt.Sprintf("%.1f%%", frac*100)).FontFeatures("tnum").Width(t.Space(12)).Shrink(0).TextAlign(ui.End).SingleLine()
				})
			case 4:
				if r.rule == "" {
					return
				}
				e := ui.Text(c, r.rule).SingleLine().Tooltip(r.rule)
				if !r.auto {
					e.TextColor(t.TextMuted)
				}
			}
		}).Height(h)
	})
}
