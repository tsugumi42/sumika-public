package lookpreset

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/chatui"
)

// 没自定义的外观编出来，关键令牌逐个等于原型值（深浅两套）。
func TestPresetEqualsPrototypeTokens(t *testing.T) {
	for _, dark := range []bool{false, true} {
		base := theme.LightBase
		if dark {
			base = theme.DarkBase
		}
		got, err := Compile(theme.Definition{Version: theme.Version, Base: base})
		if err != nil {
			t.Fatal(err)
		}
		tok := Tokens(dark)
		c, acc := tok.C, tok.AccentOf(chatui.DefaultChar)
		cs := got.Colors()
		check := func(name string, have, want ui.Color) {
			t.Helper()
			if have != want {
				t.Errorf("dark=%v %s：%v，原型 %v", dark, name, have, want)
			}
		}
		check("窗口底", cs.Window().Background(), c.Bg)
		check("文字", cs.Window().Text(), c.Text)
		check("次要文字", cs.Window().MutedText(), c.Text2)
		check("细线", cs.Control().Border(), c.Line)
		check("内容层", cs.Control().SurfaceBackground(), c.Surface)
		check("导航底", cs.Sidebar().Background(), c.Rail)
		check("会话列表底", cs.Conversation().Background(), c.Bg)
		check("会话选中行", cs.Conversation().BackgroundActive(), acc.Strong)
		check("入站气泡", cs.Message().In().Background(), c.Bubble)
		check("我的气泡", cs.Message().Out().Background(), acc.Soft)
		check("强调色", cs.Control().PrimaryBackground(), acc.Base)
		check("强调色上的字", cs.Control().PrimaryForeground(), acc.On)
		check("危险色", cs.Semantic().Danger(), c.Danger)
		if got.Metrics().Message().RadiusLarge() != tok.R1 || got.Metrics().Message().RadiusSmall() != tok.RJoin {
			t.Errorf("dark=%v 气泡圆角 %v/%v，原型 %v/%v", dark, got.Metrics().Message().RadiusLarge(), got.Metrics().Message().RadiusSmall(), tok.R1, tok.RJoin)
		}
		if !Pristine(got) || got.Dark() != dark {
			t.Errorf("dark=%v 没自定义的预设应是 Pristine", dark)
		}
	}
	// 浅色我的气泡就是主人看到的那个浅绿以外的颜色：不得是 Telegram 的 (239,253,222)
	if out := Light().Colors().Message().Out().Background(); out == (ui.Color{R: 239, G: 253, B: 222, A: 255}) {
		t.Fatal("我的气泡还是 Telegram 的浅绿")
	}
}

// 预设里的每个键 MewTypeUI 都认得（Compile 会拒未知键）。
func TestPresetCompiles(t *testing.T) {
	for _, dark := range []bool{false, true} {
		if _, err := theme.Compile(Definition(dark)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBareAndPristine(t *testing.T) {
	bare := theme.Definition{Version: theme.Version, Base: theme.LightBase}
	if !Bare(bare) {
		t.Fatal("只有 base 是没自定义")
	}
	font := ""
	withFont := bare
	withFont.Font = &font
	if !Bare(withFont) {
		t.Fatal("只改字体不该打回 Telegram")
	}
	r := Resolve(withFont)
	if r.Font == nil || r.Colors["window.background"] == "" {
		t.Fatalf("字体要带上、配色要换成预设：%+v", r)
	}
	edited := bare
	edited.Colors = map[string]string{"message.out.background": "#112233"}
	if Bare(edited) || Resolve(edited).Colors["window.background"] != "" {
		t.Fatal("有覆盖项的主题要原样保留，不掺预设")
	}
	// 用户改了一个颜色：编出来的是用户的值，不再 Pristine
	d := Light().Definition()
	d.Colors["message.out.background"] = "#112233ff"
	user, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	if user.Colors().Message().Out().Background() != ui.Hex("#112233") || Pristine(user) {
		t.Fatal("用户的颜色要优先、且不再 Pristine")
	}
	// 选了 Telegram：完整导出，不会被换回原型
	tg, err := Compile(Telegram(false))
	if err != nil {
		t.Fatal(err)
	}
	if Pristine(tg) || tg.Colors().Message().Out().Background() != theme.Light().Colors().Message().Out().Background() {
		t.Fatal("Telegram 预设应保持 Telegram 配色")
	}
	// 字体改了仍是 Pristine
	d = Light().Definition()
	size := float32(18)
	ty := d.Typography["message"]
	ty.Size = &size
	d.Typography["message"] = ty
	sized, err := Compile(d)
	if err != nil || !Pristine(sized) {
		t.Fatalf("改字号不该离开预设：%v", err)
	}
}

// 每个颜色键都由预设写上：没有一个键会落回 Telegram 的默认值，也没有来历不明的键。
func TestPresetCoversEveryColorKey(t *testing.T) {
	all := AllColorKeys()
	if len(all) < 500 {
		t.Fatalf("颜色键只有 %d 个，MewTypeUI 的键表读错了", len(all))
	}
	for _, dark := range []bool{false, true} {
		def, prov := Definition(dark), Provenance(dark)
		same, derived := 0, 0
		tg := Telegram(dark).Colors
		for _, k := range all {
			v, ok := def.Colors[k]
			if !ok || v == "" {
				t.Errorf("dark=%v 颜色键 %s 没有预设值", dark, k)
				continue
			}
			switch prov[k] {
			case FromProto:
			case FromDerived:
				derived++
			default:
				t.Errorf("dark=%v 颜色键 %s 没有来历", dark, k)
			}
			if v == tg[k] {
				same++
			}
		}
		for k := range def.Colors {
			if prov[k] == "" {
				t.Errorf("dark=%v 预设里多出不认识的键 %s", dark, k)
			}
		}
		t.Logf("dark=%v 共 %d 个颜色键，原型 %d、推导 %d；值恰好等于 Telegram 的 %d 个（透明、纯白黑一类）", dark, len(all), len(all)-derived, derived, same)
		if same > len(all)/5 {
			t.Errorf("dark=%v 有 %d 个键和 Telegram 一样，像是在套旧主题", dark, same)
		}
	}
}

// 设 LOOKPRESET_DUMP=文件路径 时，把全部颜色键的 来历 / 浅色值 / 夜间值 写成 TSV（给对接的人对照）；平时跳过。
func TestDumpTokens(t *testing.T) {
	path := os.Getenv("LOOKPRESET_DUMP")
	if path == "" {
		t.Skip("设 LOOKPRESET_DUMP 才导出")
	}
	keys := AllColorKeys()
	sort.Strings(keys)
	l, n, prov := Definition(false).Colors, Definition(true).Colors, Provenance(false)
	var b strings.Builder
	b.WriteString("键\t来历\t浅色\t夜间\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", k, prov[k], l[k], n[k])
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
