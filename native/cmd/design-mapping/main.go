// design-mapping 写出“旧字段 → 新令牌”的对照表（规格-设计令牌 第十节第 2、3 步）：
// chatui.Tokens、winshell.Geometry 的每个字段对到 internal/design 的令牌，列默认、最小、最大三档设置下的旧值和新值。
// 用法：go run ./cmd/design-mapping -out <文件>（写到 data/dev/design-tokens/ 下时用仓库根拼）。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design"
	"sumika-native/internal/design/colorx"
	"sumika-native/internal/winshell"
)

type look struct {
	name                  string
	text, radius, density float32
}

var looks = []look{
	{"默认", design.TextDefault, design.RadiusDefault, design.DensityDefault},
	{"最小", design.TextMin, design.RadiusMin, design.DensityMin},
	{"最大", design.TextMax, design.RadiusMax, design.DensityMax},
}

type snap struct {
	old chatui.Tokens
	geo winshell.Geometry
	new design.Tokens
}

type row struct {
	old, token, note string
	get              func(s snap) (float32, float32)
}

func ms(d time.Duration) float32 { return float32(d.Milliseconds()) }

func rows() []row {
	c := func(old, token string, o func(chatui.Tokens) float32, n func(design.Tokens) float32) row {
		return row{old: "chatui." + old, token: token, get: func(s snap) (float32, float32) { return o(s.old), n(s.new) }}
	}
	g := func(old, token string, o func(winshell.Geometry) float32, n func(design.Tokens) float32) row {
		return row{old: "winshell." + old, token: token, get: func(s snap) (float32, float32) { return o(s.geo), n(s.new) }}
	}
	var out []row
	for i := range 7 {
		out = append(out, c(fmt.Sprintf("FS[%d]", i), fmt.Sprintf("font.size.%d", i), func(t chatui.Tokens) float32 { return t.FS[i] }, func(t design.Tokens) float32 {
			d := t.Dim
			return [...]float32{d.FontSize0, d.FontSize1, d.FontSize2, d.FontSize3, d.FontSize4, d.FontSize5, d.FontSize6}[i]
		}))
	}
	out = append(out,
		c("BodyLine", "font.line.body", func(t chatui.Tokens) float32 { return t.BodyLine }, func(t design.Tokens) float32 { return t.Dim.FontLineBody }),
		c("ControlLine", "font.line.ui", func(t chatui.Tokens) float32 { return t.ControlLine }, func(t design.Tokens) float32 { return t.Dim.FontLineUi }),
		c("S1", "space.s1", func(t chatui.Tokens) float32 { return t.S1 }, func(t design.Tokens) float32 { return t.Dim.SpaceS1 }),
		c("S2（= G）", "space.s2", func(t chatui.Tokens) float32 { return t.S2 }, func(t design.Tokens) float32 { return t.Dim.SpaceS2 }),
		c("S3", "space.s3", func(t chatui.Tokens) float32 { return t.S3 }, func(t design.Tokens) float32 { return t.Dim.SpaceS3 }),
		c("S4", "space.s4", func(t chatui.Tokens) float32 { return t.S4 }, func(t design.Tokens) float32 { return t.Dim.SpaceS4 }),
		c("S5", "space.s5", func(t chatui.Tokens) float32 { return t.S5 }, func(t design.Tokens) float32 { return t.Dim.SpaceS5 }),
		c("S6", "space.s6", func(t chatui.Tokens) float32 { return t.S6 }, func(t design.Tokens) float32 { return t.Dim.SpaceS6 }),
		c("Inset", "space.inset", func(t chatui.Tokens) float32 { return t.Inset }, func(t design.Tokens) float32 { return t.Dim.SpaceInset }),
		c("InsetTight", "space.inset-tight", func(t chatui.Tokens) float32 { return t.InsetTight }, func(t design.Tokens) float32 { return t.Dim.SpaceInsetTight }),
		c("InsetCtl", "space.inset-ctl", func(t chatui.Tokens) float32 { return t.InsetCtl }, func(t design.Tokens) float32 { return t.Dim.SpaceInsetCtl }),
		c("RCtl", "radius.ctl", func(t chatui.Tokens) float32 { return t.RCtl }, func(t design.Tokens) float32 { return t.Dim.RadiusCtl }),
		c("R1", "radius.1", func(t chatui.Tokens) float32 { return t.R1 }, func(t design.Tokens) float32 { return t.Dim.Radius1 }),
		c("R2", "radius.2", func(t chatui.Tokens) float32 { return t.R2 }, func(t design.Tokens) float32 { return t.Dim.Radius2 }),
		c("RWin", "radius.win", func(t chatui.Tokens) float32 { return t.RWin }, func(t design.Tokens) float32 { return t.Dim.RadiusWin }),
		c("RJoin", "radius.join", func(t chatui.Tokens) float32 { return t.RJoin }, func(t design.Tokens) float32 { return t.Dim.RadiusJoin }),
		c("RInnerMin", "radius.inner-min", func(t chatui.Tokens) float32 { return t.RInnerMin }, func(t design.Tokens) float32 { return t.Dim.RadiusInnerMin }),
		c("HSm", "size.ctl.sm", func(t chatui.Tokens) float32 { return t.HSm }, func(t design.Tokens) float32 { return t.Dim.SizeCtlSm }),
		c("HMd", "size.ctl.md", func(t chatui.Tokens) float32 { return t.HMd }, func(t design.Tokens) float32 { return t.Dim.SizeCtlMd }),
		c("HLg", "size.ctl.lg", func(t chatui.Tokens) float32 { return t.HLg }, func(t design.Tokens) float32 { return t.Dim.SizeCtlLg }),
		c("BadgeHit", "size.hit.min", func(t chatui.Tokens) float32 { return t.BadgeHit }, func(t design.Tokens) float32 { return t.Dim.SizeHitMin }),
		c("Fast（毫秒）", "motion.fast", func(t chatui.Tokens) float32 { return ms(t.Fast) }, func(t design.Tokens) float32 { return ms(t.Motion.MotionFast) }),
		c("Msg（毫秒）", "motion.msg", func(t chatui.Tokens) float32 { return ms(t.Msg) }, func(t design.Tokens) float32 { return ms(t.Motion.MotionMsg) }),
		c("Panel（毫秒）", "motion.panel", func(t chatui.Tokens) float32 { return ms(t.Panel) }, func(t design.Tokens) float32 { return ms(t.Motion.MotionPanel) }),

		g("SpaceMicro", "space.s0-5", func(x winshell.Geometry) float32 { return x.SpaceMicro }, func(t design.Tokens) float32 { return t.Dim.SpaceS0Half }),
		g("SpaceTight", "space.s1", func(x winshell.Geometry) float32 { return x.SpaceTight }, func(t design.Tokens) float32 { return t.Dim.SpaceS1 }),
		g("SpaceSmall", "space.s2", func(x winshell.Geometry) float32 { return x.SpaceSmall }, func(t design.Tokens) float32 { return t.Dim.SpaceS2 }),
		g("SpaceMedium", "space.s3", func(x winshell.Geometry) float32 { return x.SpaceMedium }, func(t design.Tokens) float32 { return t.Dim.SpaceS3 }),
		g("SpacePanel", "space.s4", func(x winshell.Geometry) float32 { return x.SpacePanel }, func(t design.Tokens) float32 { return t.Dim.SpaceS4 }),
		g("SpaceSection", "space.s5", func(x winshell.Geometry) float32 { return x.SpaceSection }, func(t design.Tokens) float32 { return t.Dim.SpaceS5 }),
		g("SpaceLarge", "space.s6", func(x winshell.Geometry) float32 { return x.SpaceLarge }, func(t design.Tokens) float32 { return t.Dim.SpaceS6 }),
		g("BodySize", "font.size.4", func(x winshell.Geometry) float32 { return x.BodySize }, func(t design.Tokens) float32 { return t.Dim.FontSize4 }),
		g("BodyLine", "font.line.body", func(x winshell.Geometry) float32 { return x.BodyLine }, func(t design.Tokens) float32 { return t.Dim.FontLineBody }),
		g("LabelSize", "font.size.3", func(x winshell.Geometry) float32 { return x.LabelSize }, func(t design.Tokens) float32 { return t.Dim.FontSize3 }),
		g("LabelLine", "font.line.ui", func(x winshell.Geometry) float32 { return x.LabelLine }, func(t design.Tokens) float32 { return t.Dim.FontLineUi }),
		g("MetaSize", "font.size.2", func(x winshell.Geometry) float32 { return x.MetaSize }, func(t design.Tokens) float32 { return t.Dim.FontSize2 }),
		g("MetaLine", "font.line.meta", func(x winshell.Geometry) float32 { return x.MetaLine }, func(t design.Tokens) float32 { return t.Dim.FontLineMeta }),
		g("TitleSize", "font.size.6", func(x winshell.Geometry) float32 { return x.TitleSize }, func(t design.Tokens) float32 { return t.Dim.FontSize6 }),
		g("TitleLine", "font.line.title", func(x winshell.Geometry) float32 { return x.TitleLine }, func(t design.Tokens) float32 { return t.Dim.FontLineTitle }),
		g("HitSmall", "size.ctl.sm", func(x winshell.Geometry) float32 { return x.HitSmall }, func(t design.Tokens) float32 { return t.Dim.SizeCtlSm }),
		g("RowHeight", "size.ctl.md（临时留 36，第 6 步定）", func(x winshell.Geometry) float32 { return x.RowHeight }, func(t design.Tokens) float32 { return t.Dim.SizeCtlMd }),
		g("HitRegular", "size.ctl.md", func(x winshell.Geometry) float32 { return x.HitRegular }, func(t design.Tokens) float32 { return t.Dim.SizeCtlMd }),
		g("IconSize", "size.icon.md", func(x winshell.Geometry) float32 { return x.IconSize }, func(t design.Tokens) float32 { return t.Dim.SizeIconMd }),
		g("IconSmall", "size.icon.sm", func(x winshell.Geometry) float32 { return x.IconSmall }, func(t design.Tokens) float32 { return t.Dim.SizeIconSm }),
		g("RadiusControl", "radius.ctl", func(x winshell.Geometry) float32 { return x.RadiusControl }, func(t design.Tokens) float32 { return t.Dim.RadiusCtl }),
		g("RadiusPanel", "panel.radius（组件层）", func(x winshell.Geometry) float32 { return x.RadiusPanel }, func(t design.Tokens) float32 { return t.Dim.PanelRadius }),
		g("RadiusSegment", "segment.indicator-radius（组件层）", func(x winshell.Geometry) float32 { return x.RadiusSegment }, func(t design.Tokens) float32 { return t.Dim.SegmentIndicatorRadius }),
		g("RadiusSend", "send.radius（组件层）", func(x winshell.Geometry) float32 { return x.RadiusSend }, func(t design.Tokens) float32 { return t.Dim.SendRadius }),
		g("NavWidth", "layout.nav（大字号时撑宽，临时）", func(x winshell.Geometry) float32 { return x.NavWidth }, func(t design.Tokens) float32 { return t.Dim.LayoutNav }),
		g("BreakpointNarrow", "断点 narrow（派生）", func(x winshell.Geometry) float32 { return x.BreakpointNarrow }, func(t design.Tokens) float32 { return t.Breakpoint.Narrow }),
		g("BreakpointWide", "断点 wide（派生）", func(x winshell.Geometry) float32 { return x.BreakpointWide }, func(t design.Tokens) float32 { return t.Breakpoint.Wide }),
		row{old: "winshell.MotionRoute（毫秒）", token: "motion.panel", get: func(s snap) (float32, float32) { return ms(winshell.MotionRoute), ms(s.new.Motion.MotionPanel) }},
		row{old: "winshell.ListMin / ListMax / ChatMin / SideMin / SideMax", token: "layout.list.min / max、chat.min、side.min / max", get: func(s snap) (float32, float32) {
			d := s.new.Dim
			return winshell.ListMin + winshell.ListMax + winshell.ChatMin + winshell.SideMin + winshell.SideMax, d.LayoutListMin + d.LayoutListMax + d.LayoutChatMin + d.LayoutSideMin + d.LayoutSideMax
		}, note: "五个数的和，相等即全部一致"},
	)
	return out
}

func f(v float32) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

func main() {
	out := flag.String("out", "", "写到哪个文件（必填）")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "必须给 -out（写到哪个文件）")
		os.Exit(2)
	}
	var snaps []snap
	for _, l := range looks {
		o := chatui.DefaultSettings()
		o.Text, o.Radius, o.Density = l.text, l.radius, l.density
		ot := chatui.Derive(o)
		n := design.DefaultSettings()
		n.Text, n.Radius, n.Density = l.text, l.radius, l.density
		snaps = append(snaps, snap{old: ot, geo: winshell.NewGeometry(ot), new: design.Derive(n)})
	}
	var b strings.Builder
	b.WriteString("# 对照：旧字段到新令牌（规格-设计令牌 第十节第 3 步）\n\n")
	b.WriteString("> 由 `go run ./cmd/design-mapping` 生成，别手改。三档设置：默认（字号 16、圆角 12、疏密 8）、最小（12、4、6）、最大（20、24、10）。\n")
	b.WriteString("> 第 3 步起 `chatui.Tokens`、`winshell.Geometry` 是从 `internal/design` 取值的薄壳，旧值一列就是现在界面上的值。★ = 三档里至少一档还和令牌不同，都是标了“临时”、等第 6 步迁移时定的。\n\n")
	b.WriteString("## 尺寸和时长\n\n| 旧字段 | 新令牌 | 默认 旧 → 新 | 最小 旧 → 新 | 最大 旧 → 新 | |\n|---|---|---|---|---|---|\n")
	diff := 0
	for _, r := range rows() {
		var cells []string
		mark := ""
		for _, s := range snaps {
			o, n := r.get(s)
			cell := f(o) + " → " + f(n)
			if o != n {
				mark = "★"
			} else {
				cell = f(o)
			}
			cells = append(cells, cell)
		}
		if mark != "" {
			diff++
		}
		note := mark
		if r.note != "" {
			note = strings.TrimSpace(mark + " " + r.note)
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s |\n", r.old, r.token, cells[0], cells[1], cells[2], note)
	}
	fmt.Fprintf(&b, "\n共 %d 行，带 ★ 的 %d 行。\n", len(rows()), diff)
	b.WriteString("\n## 颜色\n\n12 种模式（亮暗 × 冷中暖 × 有无壁纸）下，`chatui.Palette` 的 25 个颜色和 8 个默认角色色的阶梯，第 2 步验过和旧 chatui 逐字节一致；第 3 步起 chatui 直接从令牌取色，全部颜色钉在 `internal/design/testdata/colors.json`（`TestColorsPinned`）。")
	b.WriteString("和旧 chatui 唯一的差别：暖色亮色下 `color.danger-text` 差一档（旧 159,46,34 → 令牌 159,47,35），原因是旧 chatui 把 diff 淡底浓度存成 float32（0.15 → 0.15000000596），令牌用精确的 0.15；对比度照样 ≥ 4.5。\n\n")
	b.WriteString("`winshell.Palette` 的名字改成语义层的名字：Canvas、Pane → `color.bg`；Bubble → `color.sunken`；Composer → `color.surface`；Ink → `color.text`；Muted → `color.text-2`；Meta → `color.text-3`；Selected → `color.sunken-hover`；Line → `color.line`；Hover、Press → `color.hover`、`color.press`；Danger → `color.danger`；Accent、OnAccent、Self、Strong 一族 → `AccentOf(角色色)` 的 Base、On、Soft、Strong。\n")
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("写好了 %s：%d 行，★ %d 行\n", *out, len(rows()), diff)
	cpath := filepath.Join(filepath.Dir(*out), "对比度-12种模式.md")
	if err := os.WriteFile(cpath, []byte(contrastTable()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("写好了", cpath)
}

// telegramNameHues 照 chatui（Telegram colors.palette:292-313），群友名字色：明度 0.62、彩度 0.14。
var telegramNameHues = []float64{29, 52, 300, 140, 205, 245, 355}

// contrastTable 是规格 V4 的结果表：每一对在 12 种模式里的最低对比度。
func contrastTable() string {
	var chars []colorx.OKLCH
	for _, c := range chatui.CharColors {
		chars = append(chars, colorx.OKLCH{L: c.L, C: c.C, H: c.H, A: c.A})
	}
	for _, h := range telegramNameHues {
		chars = append(chars, colorx.New(0.62, 0.14, h))
	}
	type key struct{ name, bg string }
	type agg struct {
		min, need float64
		where     string
		req       bool
		fails     int
	}
	var order []key
	res := map[key]*agg{}
	for _, dark := range []bool{false, true} {
		for _, n := range []string{design.NeutralCool, design.NeutralPlain, design.NeutralWarm} {
			for _, wp := range []bool{true, false} {
				s := design.DefaultSettings()
				s.Dark, s.Neutral, s.Wallpaper = dark, n, wp
				mode := fmt.Sprintf("%s/%s/%s", map[bool]string{false: "亮", true: "暗"}[dark], n, map[bool]string{true: "有壁纸", false: "无壁纸"}[wp])
				for _, c := range design.Derive(s).ContrastChecks(chars...) {
					k := key{c.Name, c.Bg}
					a, ok := res[k]
					if !ok {
						a = &agg{min: 99, need: c.Min, req: c.Required}
						res[k] = a
						order = append(order, k)
					}
					if c.Ratio < a.min {
						a.min, a.where = c.Ratio, mode
					}
					if !c.Pass() {
						a.fails++
					}
				}
			}
		}
	}
	var b strings.Builder
	b.WriteString("# 对比度：12 种模式（规格-设计令牌 V4）\n\n")
	b.WriteString("> 由 `go run ./cmd/design-mapping` 生成。角色色：8 个默认角色色 + 群友七色（明度 0.62、彩度 0.14）。“强制”的对由 `internal/design` 的 `TestContrast` 拦；不强制的是现行配色本来就不保证的（都是群友名字色当实底，界面上没有这种用法）。\n\n")
	b.WriteString("| 字 / 图标 | 底色 | 最低对比度 | 出现在 | 要求 | 强制 | 不达标的模式数 |\n|---|---|---|---|---|---|---|\n")
	fails := 0
	for _, k := range order {
		a := res[k]
		mark := ""
		if a.fails > 0 {
			mark = fmt.Sprintf("**%d / 12**", a.fails)
			fails++
		}
		req := "否"
		if a.req {
			req = "是"
		}
		fmt.Fprintf(&b, "| %s | %s | %.2f | %s | %.1f | %s | %s |\n", k.name, k.bg, a.min, a.where, a.need, req, mark)
	}
	fmt.Fprintf(&b, "\n共 %d 对，有不达标模式的 %d 对。\n", len(order), fails)
	return b.String()
}
