package main

// 设置 → 工具与审批 → 联网：搜索服务和网页读取各家的先后，拖动排序（规格-网络搜索和抓网页 8.2）。
// 每行：拖动把手、序号、服务名、状态（没填 key 的灰显，auto 会跳过；查得到余额的写“剩余 N”）。
// 拖完整张表写回设置注册表（web.searchOrder / web.fetchOrder），后台落盘后当场改运行中的顺序，不用重启。
// 做法同 settings_notify.go：读一次拿版本号；写成功才算数，失败写明没改成并重读；迟到的结果按数据源代次丢弃。
// 这个文件和 workbench_*.go 一样不许直接 import 任何组件库（archcheck/newui_test.go）。

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/config"
	"sumika-native/internal/winshell"
)

type webOrderKind struct{ key, title, hint string }

var webOrderKinds = []webOrderKind{
	{"web.searchOrder", "搜索服务的先后", "自动挑选时从上往下取第一个能用的：填了 key 且没用完。"},
	{"web.fetchOrder", "网页读取的先后", "从上往下试，前一家读不到（报错、没内容、人机校验）就换下一家。"},
}

// 各家的叫法、key 在设置注册表里的键、不用 key 的家。
var (
	webNames = map[string]string{"deepseek": "DeepSeek", "tavily": "Tavily", "exa": "Exa", "brave": "Brave", "firecrawl": "Firecrawl", "searxng": "SearXNG", "duckduckgo": "DuckDuckGo", "http": "本机直接读取"}
	webKeys  = map[string]string{"deepseek": "web.deepseekKeyRef", "tavily": "web.keyRef", "exa": "web.exaKeyRef", "brave": "web.braveKeyRef", "firecrawl": "web.firecrawlKeyRef"}
	webFree  = map[string]bool{"duckduckgo": true, "http": true}
)

const webRefresh = 10 * time.Second

type webOrderUI struct {
	gen     uint64
	loading bool
	busy    bool // 一次写入在路上
	loaded  time.Time
	known   bool
	loadErr string
	note    string
	version api.SettingsVersion
	enabled bool
	order   map[string][]string // 配置里存的顺序（键是 web.searchOrder / web.fetchOrder）
	hasKey  map[string]bool     // 服务名 -> 有 key（searxng 是配了地址）
	balance map[string]string   // 服务名 -> “剩余 N”，查不到的不在里面
	lists   map[string]*ui.ListState
}

func (st *webOrderUI) reset(gen uint64) {
	*st = webOrderUI{gen: gen, order: map[string][]string{}, hasKey: map[string]bool{}, balance: map[string]string{}, lists: map[string]*ui.ListState{}}
}

func webKnownIDs(key string) []string {
	if key == "web.fetchOrder" {
		return config.DefaultFetchOrder()
	}
	return config.DefaultSearchOrder()
}

// shown 是页面上显示的顺序：配置里的（去掉不认识的和重复的），再把配置里没写的补在最后。
func (st *webOrderUI) shown(key string) []string {
	known := webKnownIDs(key)
	var out []string
	for _, id := range st.order[key] {
		if slices.Contains(known, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	for _, id := range known {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (st *webOrderUI) available(id string) bool { return webFree[id] || st.hasKey[id] }

// wbWebOrder 画“联网”一节；后台没有设置注册表（演示、旧数据源）就不画。
func (a *app) wbWebOrder(c *ui.Context, kit *winshell.Kit) {
	reg, ok := a.reader.(api.SettingsRegistry)
	if !ok {
		return
	}
	st := &a.webUI
	if st.gen != a.srcGen || st.order == nil {
		st.reset(a.srcGen)
	}
	if !st.loading && !st.busy && (st.loaded.IsZero() || c.Now().Sub(st.loaded) >= webRefresh) {
		a.loadWebOrder(reg, c.Now())
	}
	c.After(webRefresh)
	g, p := kit.G, kit.P
	kit.SectionHeading(c, "联网")
	if st.loadErr != "" {
		kit.Note(c, "读不到："+st.loadErr, true)
	}
	if !st.known {
		return
	}
	if !st.enabled {
		kit.Note(c, "联网还没开（配置里的 web.enabled）。顺序可以先排好。", false)
	}
	if st.note != "" {
		kit.Note(c, st.note, true)
	}
	for _, kind := range webOrderKinds {
		order := st.shown(kind.key)
		ui.Text(c.Key("web-title-"+kind.key), kind.title).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).Padding(g.SpaceSmall, 0, 0, 0)
		ui.Text(c.Key("web-hint-"+kind.key), kind.hint).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
		ls := st.lists[kind.key]
		if ls == nil {
			key := kind.key
			ls = &ui.ListState{
				Key:     func(i int) any { return st.shown(key)[i] },
				Label:   func(i int) string { return webNames[st.shown(key)[i]] },
				Reorder: func(rows []int, to int) { a.reorderWeb(reg, key, rows, to) },
			}
			st.lists[kind.key] = ls
		}
		ui.List(c.Key("web-list-"+kind.key), ls, len(order), func(i int) { a.webOrderRow(c, kit, kind.key, order, i) }).FillWidth().Height(float32(len(order)) * g.RowHeight).Label(kind.title)
	}
}

func (a *app) webOrderRow(c *ui.Context, kit *winshell.Kit, key string, order []string, i int) {
	st := &a.webUI
	g, p := kit.G, kit.P
	id := order[i]
	ok := st.available(id)
	ink, status := p.Ink, ""
	switch {
	case !ok && id == "searxng":
		ink, status = p.Muted, "没配地址"
	case !ok:
		ink, status = p.Muted, "没填 key"
	case st.balance[id] != "":
		status = st.balance[id]
	case webFree[id]:
		status = "免费"
	default:
		status = "已填 key"
	}
	ui.Row(c.Key("web-row-"+key+"-"+id)).FillWidth().Height(g.RowHeight).AlignItems(ui.Center).Gap(g.SpaceSmall).Padding(0, g.SpaceSmall).Label(webNames[id]).Children(func() {
		ui.Icon(c.Key("grip"), chatui.Icon("grip", false)).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Meta)
		ui.Text(c.Key("no"), strconv.Itoa(i+1)).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).Width(g.IconSmall).Shrink(0)
		ui.Text(c.Key("name"), webNames[id]).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(ink).Grow(1).MinWidth(0)
		ui.Text(c.Key("status"), status).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine().Shrink(0)
	})
}

func (a *app) loadWebOrder(reg api.SettingsRegistry, now time.Time) {
	st := &a.webUI
	st.loading, st.loaded = true, now
	ctx, generation := a.base(), a.srcGen
	var bals func() map[string]string
	if b, ok := a.reader.(interface{ WebBalanceTexts() map[string]string }); ok {
		bals = b.WebBalanceTexts
	}
	go func() {
		v, err := reg.ReadSettingsRegistry(ctx)
		var texts map[string]string
		if bals != nil {
			texts = bals()
		}
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.webUI
			st.loading = false
			if err != nil {
				st.loadErr = err.Error()
				return
			}
			st.loadErr = ""
			st.balance = texts
			if st.busy {
				return // 写入在路上：别拿读到的旧顺序盖掉刚拖好的
			}
			st.apply(v)
		})
	}()
}

func (st *webOrderUI) apply(v api.SettingsView) {
	st.version, st.known = v.Version, true
	byKey := map[string]any{}
	for _, item := range v.Items {
		byKey[item.Key] = item.Value
	}
	st.enabled, _ = byKey["web.enabled"].(bool)
	for _, kind := range webOrderKinds {
		st.order[kind.key] = stringList(byKey[kind.key])
	}
	for id, k := range webKeys {
		st.hasKey[id] = byKey[k] == "已存"
	}
	url, _ := byKey["web.searxng.url"].(string)
	st.hasKey["searxng"] = url != "" && url != "地址无效"
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return slices.Clone(x)
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// moveRows 把 rows（升序的下标）这几项挪到原来第 to 项的前面（to == len 是挪到最后）。
func moveRows(list []string, rows []int, to int) []string {
	moved := make([]string, 0, len(rows))
	for _, r := range rows {
		moved = append(moved, list[r])
	}
	var rest []string
	at := len(list)
	for i, it := range list {
		if i == to {
			at = len(rest)
		}
		if !slices.Contains(rows, i) {
			rest = append(rest, it)
		}
	}
	if to >= len(list) {
		at = len(rest)
	}
	return slices.Insert(rest, at, moved...)
}

func (a *app) reorderWeb(reg api.SettingsRegistry, key string, rows []int, to int) {
	st := &a.webUI
	if st.busy {
		st.note = "上一次的顺序还在保存，稍等一下再拖"
		return
	}
	cur := st.shown(key)
	next := moveRows(cur, rows, to)
	if slices.Equal(cur, next) {
		return
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return
	}
	st.order[key], st.busy, st.note = next, true, ""
	ctx, generation, version := a.base(), a.srcGen, st.version
	go func() {
		v, err := reg.WriteSetting(ctx, api.SettingChange{Key: key, Value: raw}, version)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.webUI
			st.busy = false
			var saved *api.SavedSettingsError
			switch {
			case err == nil:
				st.apply(v)
			case errors.As(err, &saved):
				st.note, st.loaded = "顺序已保存，但重新读取设置失败："+saved.Cause.Error(), time.Time{}
			default:
				st.note = "没有改成：" + strings.TrimSpace(err.Error()) // 下一帧重读，显示回真实的顺序
				st.loaded = time.Time{}
			}
		})
	}()
}
