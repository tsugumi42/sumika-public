package main

import (
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"sumika-native/internal/linktemplate"
)

// 页面：左边目录（平台 → 页面类型 → 变体，带编号），右边每条一个区块，区块里每种样式一栏，
// 每栏画成 QQ 群聊：先是群友发的那条链接，再按真实发送顺序画 Sumika 发出的每条消息。
// 图片一律用 files/ 下的相对路径、懒加载，页面单文件，双击就能看。

var esc = html.EscapeString

const pageCSS = `
:root{--bg:#eef0f3;--panel:#fff;--chat:#f2f3f5;--ink:#1f2329;--mute:#646a73;--faint:#8f959e;--line:#dee0e3;
--bub:#fff;--self:#fff;--acc:#0e7cff;--ok:#2ea043;--bad:#d1242f;--warn:#bf8700;--quote:#f0f1f3}
*{box-sizing:border-box}
html{scroll-padding-top:12px}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.55 "Microsoft YaHei UI","Microsoft YaHei",system-ui,sans-serif;
display:grid;grid-template-columns:300px minmax(0,1fr);min-height:100vh}
nav{position:sticky;top:0;height:100vh;overflow:auto;background:var(--panel);border-right:1px solid var(--line);padding:14px 10px 40px;font-size:14px}
nav h2{font-size:15px;margin:14px 6px 4px;display:flex;justify-content:space-between;gap:8px}
nav h2 small{color:var(--faint);font-weight:400}
nav h3{font-size:13px;color:var(--mute);font-weight:500;margin:6px 6px 2px 10px}
nav a{display:flex;gap:6px;align-items:baseline;padding:2px 6px 2px 16px;border-radius:6px;color:var(--ink);text-decoration:none}
nav a:hover{background:var(--chat)}
nav a .c{font:12px/1.4 Consolas,monospace;color:var(--mute);flex:none;width:50px}
nav a .v{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dot{width:8px;height:8px;border-radius:50%;flex:none;align-self:center;background:var(--ok)}
.dot.bad{background:var(--bad)}.dot.skip{background:var(--warn)}
main{padding:20px 28px 80px;min-width:0}
h1{font-size:22px;margin:0 0 6px}
.run{background:var(--panel);border:1px solid var(--line);border-radius:14px;padding:12px 16px;margin:10px 0 16px}
.run ul{margin:4px 0;padding-left:20px}.run li{margin:2px 0}
table.sum{border-collapse:collapse;font-size:14px;margin-top:8px}
table.sum td,table.sum th{border-bottom:1px solid var(--line);padding:3px 12px 3px 0;text-align:left}
table.sum .n{text-align:right;font-variant-numeric:tabular-nums}
.filters{display:flex;gap:14px;margin:8px 0;font-size:14px;color:var(--mute)}
section.sample{background:var(--panel);border:1px solid var(--line);border-radius:16px;padding:14px 16px 16px;margin:0 0 18px}
.sample>header{display:grid;grid-template-columns:auto minmax(0,1fr);column-gap:10px;row-gap:2px}
.code{grid-row:span 3;font:600 14px/1 Consolas,monospace;background:var(--ink);color:#fff;border-radius:8px;padding:6px 8px;height:fit-content}
.sample h2{font-size:17px;margin:0;overflow-wrap:anywhere}
.meta,.facts{color:var(--mute);font-size:13px}
.meta a{color:var(--acc);overflow-wrap:anywhere}
.facts span+span::before{content:" · "}
.err{color:var(--bad)}.warn{color:var(--warn)}
.styles{display:grid;grid-template-columns:repeat(auto-fill,minmax(390px,1fr));gap:12px;margin-top:12px;align-items:start}
.style{border:1px solid var(--line);border-radius:14px;overflow:hidden;min-width:0;background:var(--chat)}
.style>.head{background:var(--panel);padding:8px 12px;border-bottom:1px solid var(--line);font-size:13px;color:var(--mute)}
.style>.head b{color:var(--ink);font-size:15px;margin-right:6px}
.style>.head .shape{color:var(--ink);margin-top:2px}
.style>.head code{font:12px Consolas,monospace;background:var(--chat);border-radius:4px;padding:0 4px}
.chat{padding:10px 10px 14px}
.row{display:flex;gap:8px;margin-top:10px;align-items:flex-start}
.ava{width:32px;height:32px;border-radius:50%;flex:none;display:grid;place-items:center;font-size:13px;color:#fff;background:#9aa1ab}
.ava.me{background:#ff7aa8}
.col{min-width:0;max-width:calc(100% - 40px)}
.name{font-size:12px;color:var(--faint);margin:0 0 2px 2px}
.name .n{color:var(--mute);margin-left:4px}
.bubble{background:var(--bub);border-radius:4px 12px 12px 12px;padding:8px 10px;white-space:pre-wrap;overflow-wrap:anywhere;width:fit-content;max-width:100%}
.bubble.media-only{padding:4px;background:transparent}
.bubble img,.bubble .vid{display:block;border-radius:6px;margin:4px 0;max-width:100%;height:auto;background:#e3e5e8}
.bubble .card{outline:1px solid var(--line)}
.reply{background:var(--quote);border-left:3px solid #c4c8ce;border-radius:4px;padding:3px 8px;margin:0 0 6px;font-size:13px;color:var(--mute);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:300px}
.ph{display:inline-block;color:var(--faint);font-size:13px}
.vid{position:relative;overflow:hidden}
.vid img{margin:0;border-radius:0;width:100%}
.vid .play{position:absolute;left:50%;top:50%;width:44px;height:44px;margin:-22px 0 0 -22px;border-radius:50%;background:rgba(0,0,0,.45);display:grid;place-items:center}
.vid .play::after{content:"";border-left:15px solid #fff;border-top:9px solid transparent;border-bottom:9px solid transparent;margin-left:4px}
.vid .vm{position:absolute;right:6px;bottom:6px;font-size:12px;color:#fff;background:rgba(0,0,0,.55);border-radius:4px;padding:0 5px}
.vid.nocover{width:240px;height:135px}
.badge{position:absolute;left:6px;top:6px;font:600 11px/1.4 Consolas,monospace;color:#fff;background:rgba(0,0,0,.55);border-radius:4px;padding:0 4px}
.pic{position:relative;width:fit-content;max-width:100%}
.fwd{background:var(--bub);border-radius:4px 12px 12px 12px;width:280px;max-width:100%;padding:10px 12px 0;cursor:default}
.fwd .t{font-size:15px;margin-bottom:4px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.fwd .l{font-size:13px;color:var(--faint);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.fwd .f{border-top:1px solid var(--line);margin-top:8px;padding:6px 0 8px;font-size:12px;color:var(--faint)}
.prompt{font-size:12px;color:var(--faint);margin-top:2px}
details.nodes{margin:6px 0 0 40px;border-left:2px solid var(--line);padding-left:10px}
details.nodes>summary{font-size:13px;color:var(--acc);cursor:pointer;list-style-position:outside}
.ns{font-size:13px;color:var(--mute);background:var(--panel);border-radius:10px;padding:8px 10px;margin-top:8px}
.ns.err{color:var(--bad)}
.notes{font-size:12px;color:var(--faint);padding:0 12px 10px}
body.only-bad section.sample:not(.bad){display:none}
@media (max-width:900px){body{grid-template-columns:1fr}nav{position:static;height:auto;max-height:40vh}main{padding:16px}}
`

// thumb 是图在气泡里的显示宽度：照真实尺寸缩放，不超过 maxW；没有尺寸给个默认。
func thumb(w, h, maxW int) (int, int) {
	if w <= 0 || h <= 0 {
		return maxW, 0
	}
	if w <= maxW {
		return w, h
	}
	return maxW, h * maxW / w
}

func imgTag(file string, w, h, maxW int, class, alt string) string {
	dw, dh := thumb(w, h, maxW)
	size := fmt.Sprintf(` width="%d"`, dw)
	if dh > 0 {
		size += fmt.Sprintf(` height="%d"`, dh)
	}
	cls := ""
	if class != "" {
		cls = ` class="` + class + `"`
	}
	return fmt.Sprintf(`<img src="%s"%s%s alt="%s" loading="lazy" decoding="async">`, esc(file), size, cls, esc(alt))
}

func sizeText(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1<<20:
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func durText(s float64) string {
	if s <= 0 {
		return ""
	}
	t := int(s + 0.5)
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func writeItems(b *strings.Builder, its []itemOut) {
	for _, it := range its {
		switch it.Type {
		case "text":
			b.WriteString(esc(it.Text))
		case "image", "card", "gif":
			alt := map[string]string{"image": "图片", "card": "卡片图", "gif": "动图"}[it.Type]
			if it.File == "" {
				fmt.Fprintf(b, `<span class="ph">[%s没存下]</span>`, alt)
				continue
			}
			maxW := 260
			cls := ""
			if it.Type == "card" {
				maxW, cls = 340, "card"
			}
			b.WriteString(`<div class="pic">` + imgTag(it.File, it.W, it.H, maxW, cls, alt))
			if it.Type == "gif" {
				b.WriteString(`<span class="badge">GIF</span>`)
			}
			b.WriteString(`</div>`)
		case "video":
			meta := strings.Trim(strings.Join([]string{durText(it.Seconds), sizeText(it.Size), dims(it.W, it.H)}, " · "), " ·")
			meta = strings.ReplaceAll(meta, " ·  · ", " · ")
			open, end := "", ""
			if it.File != "" {
				open, end = `<a href="`+esc(it.File)+`" title="打开视频文件">`, `</a>`
			}
			if it.Cover != "" {
				dw, dh := thumb(it.W, it.H, 260)
				if it.H > it.W && dh > 360 { // 竖屏视频 QQ 里也是按高度收
					dw, dh = dw*360/dh, 360
				}
				fmt.Fprintf(b, `%s<div class="vid" style="width:%dpx">%s<span class="play"></span><span class="vm">%s</span></div>%s`, open, dw, imgTag(it.Cover, dw, dh, dw, "", "视频封面"), esc(meta), end)
			} else {
				fmt.Fprintf(b, `%s<div class="vid nocover"><span class="play"></span><span class="vm">%s</span></div>%s`, open, esc(orText(meta, "视频")), end)
			}
		default:
			fmt.Fprintf(b, `<span class="ph">[%s]</span>`, esc(it.Type))
		}
	}
}

func dims(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	return fmt.Sprintf("%d×%d", w, h)
}

func orText(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// preview 是一段内容在 QQ 预览里的样子：文字压成一行，图片写 [图片]。
func preview(its []itemOut) string {
	var b strings.Builder
	for _, it := range its {
		switch it.Type {
		case "text":
			b.WriteString(it.Text)
		case "image", "card":
			b.WriteString("[图片]")
		case "gif":
			b.WriteString("[动画表情]")
		case "video":
			b.WriteString("[视频]")
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func mediaOnly(its []itemOut) bool {
	for _, it := range its {
		if it.Type == "text" && strings.TrimSpace(it.Text) != "" {
			return false
		}
	}
	return len(its) > 0
}

func nodeName(n nodeOut) string {
	if n.Self {
		// 节点身份固定是发送号自己：QQ 显示发送号的昵称，取不到才显示 Name
		return `Sumika<span class="n">（发送号的 QQ 昵称；取不到时显示“` + esc(orText(n.Name, "Sumika")) + `”）</span>`
	}
	return esc(orText(n.Name, "Sumika"))
}

// shape 是一条消息的形状：文字 / 文字+图×2 / 卡片 / 视频 / 合并转发（N 个节点）。
func shape(m msgOut) string {
	if m.Kind == "forward" {
		return fmt.Sprintf("合并转发（%d 个节点）", len(m.Nodes))
	}
	count := map[string]int{}
	var order []string
	for _, it := range m.Items {
		k := map[string]string{"text": "文字", "image": "图", "card": "卡片", "gif": "动图", "video": "视频"}[it.Type]
		if k == "" {
			k = it.Type
		}
		if it.Type == "text" && strings.TrimSpace(it.Text) == "" {
			continue
		}
		if count[k] == 0 {
			order = append(order, k)
		}
		count[k]++
	}
	var parts []string
	for _, k := range order {
		if count[k] > 1 && k != "文字" {
			parts = append(parts, fmt.Sprintf("%s×%d", k, count[k]))
		} else {
			parts = append(parts, k)
		}
	}
	s := strings.Join(parts, "+")
	if m.Quote {
		s += "（回复原消息）"
	}
	return orText(s, "空消息")
}

func writeMessage(b *strings.Builder, m msgOut, src string) {
	if m.Kind == "forward" {
		title, news, foot := "群聊的聊天记录", []string{}, fmt.Sprintf("查看%d条转发消息", len(m.Nodes))
		prompt := "[聊天记录]"
		if m.Card != nil {
			title = orText(m.Card.Source, title)
			news = m.Card.News
			foot = orText(m.Card.Summary, foot)
			prompt = orText(m.Card.Prompt, prompt)
		}
		if len(news) == 0 {
			for i, n := range m.Nodes {
				if i == 4 {
					break
				}
				who := orText(n.Name, "Sumika")
				if n.Self {
					who = "Sumika"
				}
				news = append(news, who+": "+preview(n.Items))
			}
		}
		b.WriteString(`<div class="row"><div class="ava me">S</div><div class="col"><div class="name">Sumika</div><div class="fwd">`)
		fmt.Fprintf(b, `<div class="t">%s</div>`, esc(title))
		for _, l := range news {
			fmt.Fprintf(b, `<div class="l">%s</div>`, esc(l))
		}
		fmt.Fprintf(b, `<div class="f">%s</div></div><div class="prompt">消息列表里显示：%s</div></div></div>`, esc(foot), esc(prompt))
		fmt.Fprintf(b, `<details class="nodes" open><summary>展开的 %d 个节点</summary>`, len(m.Nodes))
		for _, n := range m.Nodes {
			b.WriteString(`<div class="row"><div class="ava me">S</div><div class="col"><div class="name">` + nodeName(n) + `</div>`)
			cls := "bubble"
			if mediaOnly(n.Items) && n.ReplyTo == 0 {
				cls += " media-only"
			}
			b.WriteString(`<div class="` + cls + `">`)
			if n.ReplyTo > 0 && n.ReplyTo <= len(m.Nodes) {
				ref := m.Nodes[n.ReplyTo-1]
				fmt.Fprintf(b, `<div class="reply">引用第 %d 个节点 %s：%s</div>`, n.ReplyTo, esc(orText(ref.Name, "Sumika")), esc(preview(ref.Items)))
			}
			writeItems(b, n.Items)
			b.WriteString(`</div></div></div>`)
		}
		b.WriteString(`</details>`)
		return
	}
	cls := "bubble"
	if mediaOnly(m.Items) && !m.Quote {
		cls += " media-only"
	}
	b.WriteString(`<div class="row"><div class="ava me">S</div><div class="col"><div class="name">Sumika</div><div class="` + cls + `">`)
	if m.Quote {
		fmt.Fprintf(b, `<div class="reply">群友：%s</div>`, esc(src))
	}
	writeItems(b, m.Items)
	b.WriteString(`</div></div></div>`)
}

func sourceRow(b *strings.Builder, url string) {
	fmt.Fprintf(b, `<div class="row"><div class="ava">友</div><div class="col"><div class="name">群友</div><div class="bubble">%s</div></div></div>`, esc(url))
}

// sampleBad 说这一条在目录里标成什么：取不到 / 线上不发 / 有样式出错 是红，默认关着是黄。
func sampleState(s sampleOut) string {
	if s.Error != "" || s.NotSent != "" {
		return "bad"
	}
	for _, st := range s.Styles {
		if st.Error != "" || st.NotSent != "" {
			return "bad"
		}
	}
	if s.Off != "" {
		return "skip"
	}
	return ""
}

func writeSample(b *strings.Builder, s sampleOut) {
	state := sampleState(s)
	fmt.Fprintf(b, `<section class="sample %s" id="%s"><header><span class="code">%s</span><h2>%s</h2>`, state, esc(s.Code), esc(s.Code), esc(orText(s.Title, "（没有标题）")))
	fmt.Fprintf(b, `<div class="meta">%s / %s / %s`, esc(s.PlatLabel), esc(s.KindLabel), esc(orText(s.Variant, "（清单没写变体）")))
	if s.From != "" {
		fmt.Fprintf(b, `　来源：%s`, esc(s.From))
	}
	fmt.Fprintf(b, `　<a href="%s" target="_blank" rel="noreferrer">%s</a></div><div class="facts">`, esc(s.URL), esc(s.URL))
	facts := []string{fmt.Sprintf("取数 %s", msText(s.FetchMs))}
	if s.Route != "" {
		facts = append(facts, "线路 "+s.Route)
	}
	if s.Platform != "" {
		facts = append(facts, s.Platform+"/"+s.Kind)
	}
	if s.Support != "" {
		facts = append(facts, s.Support)
	}
	if s.Translation != "" {
		facts = append(facts, s.Translation)
	}
	for _, f := range facts {
		fmt.Fprintf(b, `<span>%s</span>`, esc(f))
	}
	b.WriteString(`</div></header>`)
	if s.Error != "" {
		fmt.Fprintf(b, `<p class="err">取不到：%s</p>`, esc(s.Error))
	}
	if s.Off != "" {
		fmt.Fprintf(b, `<p class="warn">%s</p>`, esc(s.Off))
	}
	if len(s.Styles) == 0 {
		b.WriteString(`<div class="styles"><div class="style"><div class="head"><b>线上</b>不发消息</div><div class="chat">`)
		sourceRow(b, s.URL)
		fmt.Fprintf(b, `<div class="ns">%s</div></div></div></div></section>`, esc(orText(s.NotSent, "没有样式")))
		return
	}
	b.WriteString(`<div class="styles">`)
	for _, st := range s.Styles {
		var shapes []string
		for _, m := range st.Messages {
			shapes = append(shapes, shape(m))
		}
		fmt.Fprintf(b, `<div class="style"><div class="head"><b>%s</b>%s`, esc(st.Label), esc(st.Hint))
		setting := []string{}
		if st.Template != "" {
			setting = append(setting, "模板 "+st.Template)
		}
		if st.Forward != "" {
			setting = append(setting, "转发 "+st.Forward)
		}
		if len(setting) > 0 {
			fmt.Fprintf(b, ` <code>%s</code>`, esc(strings.Join(setting, "，")))
		}
		if len(st.Messages) > 0 {
			fmt.Fprintf(b, `<div class="shape">发 %d 条：%s　<span style="color:var(--faint)">准备 %s</span></div>`, len(st.Messages), esc(strings.Join(shapes, "；")), msText(st.PrepareMs))
		}
		b.WriteString(`</div><div class="chat">`)
		sourceRow(b, s.URL)
		for _, m := range st.Messages {
			writeMessage(b, m, s.URL)
		}
		if st.NotSent != "" {
			cls := "ns"
			if st.Error != "" {
				cls += " err"
			}
			fmt.Fprintf(b, `<div class="%s">%s`, cls, esc(st.NotSent))
			if st.Error != "" {
				fmt.Fprintf(b, `：%s`, esc(st.Error))
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
		if len(st.Notes) > 0 {
			fmt.Fprintf(b, `<div class="notes">%s</div>`, esc(strings.Join(st.Notes, "；")))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></section>`)
}

func msText(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

type platStat struct {
	label                string
	total, ok, fail, off int
}

func page(g galleryOut) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>链接样张</title><style>` + pageCSS + `</style></head><body>`)

	// 目录：平台按第一次出现的顺序，类型也按出现顺序
	type kindGroup struct {
		label string
		items []sampleOut
	}
	type platGroup struct {
		key, label string
		kinds      []*kindGroup
		byKind     map[string]*kindGroup
	}
	var plats []*platGroup
	byPlat := map[string]*platGroup{}
	stats := map[string]*platStat{}
	for _, s := range g.Samples {
		pk := orText(s.ListPlat, s.Platform)
		p := byPlat[pk]
		if p == nil {
			p = &platGroup{key: pk, label: s.PlatLabel, byKind: map[string]*kindGroup{}}
			byPlat[pk] = p
			plats = append(plats, p)
			stats[pk] = &platStat{label: s.PlatLabel}
		}
		kk := orText(s.ListKind, s.Kind)
		k := p.byKind[kk]
		if k == nil {
			k = &kindGroup{label: kindLabel(pk, kk, s)}
			p.byKind[kk] = k
			p.kinds = append(p.kinds, k)
		}
		k.items = append(k.items, s)
		st := stats[pk]
		st.total++
		switch {
		case s.Error != "":
			st.fail++
		default:
			st.ok++
		}
		if sampleState(s) == "bad" && s.Error == "" {
			st.off++
		}
	}
	b.WriteString(`<nav><div style="font-weight:600;margin:0 6px">目录</div>`)
	for _, p := range plats {
		st := stats[p.key]
		fmt.Fprintf(&b, `<h2>%s<small>%d 条 · %d 取不到</small></h2>`, esc(p.label), st.total, st.fail)
		for _, k := range p.kinds {
			fmt.Fprintf(&b, `<h3>%s</h3>`, esc(k.label))
			for _, s := range k.items {
				fmt.Fprintf(&b, `<a href="#%s"><span class="dot %s"></span><span class="c">%s</span><span class="v">%s</span></a>`, esc(s.Code), sampleState(s), esc(s.Code), esc(orText(s.Variant, s.Title)))
			}
		}
	}
	b.WriteString(`</nav><main><h1>链接样张</h1>`)
	r := g.Run
	b.WriteString(`<div class="run">`)
	fmt.Fprintf(&b, `<div>真实取数、只取数没有发送；条数、合并转发、图片都照线上 Preparer 的输出画。运行于 %s，用时 %d 秒，清单 <code>%s</code>。</div><ul>`, esc(r.At), r.TotalSecond, esc(r.ListPath))
	fmt.Fprintf(&b, `<li>推特卡片配色：%s</li><li>视频下载：%s</li><li>翻译：%s</li>`, esc(r.Theme), esc(r.Video), esc(r.Translate))
	for _, d := range r.Deviations {
		fmt.Fprintf(&b, `<li class="warn">和线上不同：%s</li>`, esc(d))
	}
	b.WriteString(`</ul><table class="sum"><tr><th>平台</th><th>条数</th><th>取到</th><th>取不到</th><th>取到但有样式不发</th></tr>`)
	tot := platStat{}
	for _, p := range plats {
		st := stats[p.key]
		fmt.Fprintf(&b, `<tr><td>%s</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td></tr>`, esc(st.label), st.total, st.ok, st.fail, st.off)
		tot.total += st.total
		tot.ok += st.ok
		tot.fail += st.fail
		tot.off += st.off
	}
	fmt.Fprintf(&b, `<tr><th>合计</th><th class="n">%d</th><th class="n">%d</th><th class="n">%d</th><th class="n">%d</th></tr></table>`, tot.total, tot.ok, tot.fail, tot.off)
	b.WriteString(`<div class="filters"><label><input type="checkbox" id="onlyBad"> 只看取不到或不发的</label></div></div>`)
	for _, s := range g.Samples {
		writeSample(&b, s)
	}
	b.WriteString(`</main><script>document.getElementById("onlyBad").addEventListener("change",e=>document.body.classList.toggle("only-bad",e.target.checked))</script></body></html>`)
	return b.String()
}

func kindLabel(platform, kind string, s sampleOut) string {
	if k, ok := linktemplate.LookupKind(platform, kind); ok {
		return k.Label
	}
	if kind == "short" {
		return "短链"
	}
	return orText(kind, s.KindLabel)
}

// imageSize 读图片宽高（视频封面帧用），读不出回 0。
func imageSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return c.Width, c.Height
}
