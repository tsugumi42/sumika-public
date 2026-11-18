package theme

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Report every mismatch so a source table exposes all differing tokens in one run.
func check(t *testing.T, label string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", label)
	}
}
func compiled(t *testing.T, d Definition) *Theme {
	t.Helper()
	v, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func base() Definition { return Definition{Version: Version, Base: LightBase} }

func TestCompileErrors(t *testing.T) {
	cases := []struct {
		name, path string
		change     func(*Definition)
	}{
		{"version", "version", func(d *Definition) { d.Version = Version + 1 }},
		{"base", "base", func(d *Definition) { d.Base = "unknown" }},
		{"unknown color", "colors.noSuchRole", func(d *Definition) { d.Colors = map[string]string{"noSuchRole": "#ffffff"} }},
		{"unknown metric", "metrics.message.missing", func(d *Definition) { d.Metrics = map[string]float32{"message.missing": 1} }},
		{"unknown typography", "typography.missing", func(d *Definition) { d.Typography = map[string]TextDefinition{"missing": {}} }},
		{"unknown motion", "motion.missing", func(d *Definition) { d.Motion = map[string]MotionDefinition{"missing": {}} }},
		{"unknown material", "materials.missing", func(d *Definition) { d.Materials = map[string]SurfaceDefinition{"missing": {}} }},
		{"missing alias", "colors.message.out.background -> colors.absent", func(d *Definition) { d.Colors = map[string]string{"message.out.background": "@absent"} }},
		{"self cycle", "colors.window.background: cyclic", func(d *Definition) { d.Colors = map[string]string{"window.background": "@window.background"} }},
		{"cycle", "cyclic reference", func(d *Definition) {
			d.Colors = map[string]string{"window.background": "@message.out.background", "message.out.background": "@window.background"}
		}},
		{"empty color", "colors.window.background", func(d *Definition) { d.Colors = map[string]string{"window.background": ""} }},
		{"short hex", "colors.window.background", func(d *Definition) { d.Colors = map[string]string{"window.background": "#fff"} }},
		{"bad hex", "colors.window.background", func(d *Definition) { d.Colors = map[string]string{"window.background": "#zzzzzz"} }},
		{"negative metric", "metrics.message.radiusLarge", func(d *Definition) { d.Metrics = map[string]float32{"message.radiusLarge": -1} }},
		{"infinite metric", "metrics.message.radiusLarge", func(d *Definition) { d.Metrics = map[string]float32{"message.radiusLarge": float32(math.Inf(1))} }},
		{"nan metric", "metrics.message.radiusLarge", func(d *Definition) { d.Metrics = map[string]float32{"message.radiusLarge": float32(math.NaN())} }},
		{"zero font", "typography.body.size", func(d *Definition) { d.Typography = map[string]TextDefinition{"body": {Size: new(float32(0))}} }},
		{"nan font", "typography.body.size", func(d *Definition) {
			d.Typography = map[string]TextDefinition{"body": {Size: new(float32(math.NaN()))}}
		}},
		{"zero weight", "typography.body.weight", func(d *Definition) { d.Typography = map[string]TextDefinition{"body": {Weight: new(0)}} }},
		{"large weight", "typography.body.weight", func(d *Definition) { d.Typography = map[string]TextDefinition{"body": {Weight: new(1001)}} }},
		{"negative duration", "motion.page.duration", func(d *Definition) { d.Motion = map[string]MotionDefinition{"page": {Duration: new(float64(-1))}} }},
		{"overflow duration", "motion.page.duration", func(d *Definition) {
			d.Motion = map[string]MotionDefinition{"page": {Duration: new(float64(math.MaxInt64))}}
		}},
		{"nan duration", "motion.page.duration", func(d *Definition) { d.Motion = map[string]MotionDefinition{"page": {Duration: new(math.NaN())}} }},
		{"unknown easing", "motion.page.easing", func(d *Definition) { d.Motion = map[string]MotionDefinition{"page": {Easing: new(Easing("bad"))}} }},
		{"unknown kind", "materials.window.kind", func(d *Definition) {
			d.Materials = map[string]SurfaceDefinition{"window": {Kind: new(WindowMaterial("bad"))}}
		}},
		{"system kind on panel", "materials.sidebar.kind", func(d *Definition) { d.Materials = map[string]SurfaceDefinition{"sidebar": {Kind: new(Mica)}} }},
		{"low alpha", "materials.header.alpha", func(d *Definition) { d.Materials = map[string]SurfaceDefinition{"header": {Alpha: new(float32(-.1))}} }},
		{"high alpha", "materials.header.alpha", func(d *Definition) { d.Materials = map[string]SurfaceDefinition{"header": {Alpha: new(float32(1.1))}} }},
		{"nan alpha", "materials.header.alpha", func(d *Definition) {
			d.Materials = map[string]SurfaceDefinition{"header": {Alpha: new(float32(math.NaN()))}}
		}},
		{"negative blur", "materials.header.backdropBlur", func(d *Definition) {
			d.Materials = map[string]SurfaceDefinition{"header": {BackdropBlur: new(float32(-1))}}
		}},
		{"infinite blur", "materials.header.backdropBlur", func(d *Definition) {
			d.Materials = map[string]SurfaceDefinition{"header": {BackdropBlur: new(float32(math.Inf(1)))}}
		}},
		{"unknown glass", "materials.header.glass", func(d *Definition) {
			d.Materials = map[string]SurfaceDefinition{"header": {Glass: new(GlassMode("bad"))}}
		}},
		{"high clarity", "materials.header.clarity", func(d *Definition) { d.Materials = map[string]SurfaceDefinition{"header": {Clarity: new(float32(2))}} }},
		{"material missing reference", "materials.header.background: colors.absent", func(d *Definition) {
			d.Materials = map[string]SurfaceDefinition{"header": {Background: new("@absent")}}
		}},
		{"material bad color", "materials.header.background", func(d *Definition) { d.Materials = map[string]SurfaceDefinition{"header": {Background: new("no")}} }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			d := base()
			tt.change(&d)
			_, e := Compile(d)
			check(t, tt.name, e != nil && strings.Contains(e.Error(), tt.path))
		})
	}
	for _, section := range []string{"dark", "font", "colors.window.background", "metrics.message.maxWidth", "typography.body", "typography.body.size", "typography.body.weight", "motion.page", "motion.page.duration", "motion.page.easing", "materials.header", "materials.header.kind", "materials.header.background", "materials.header.alpha", "materials.header.backdropBlur", "materials.header.glass", "materials.header.clarity"} {
		t.Run("standalone "+section, func(t *testing.T) {
			d := Light().Definition()
			switch section {
			case "dark":
				d.Dark = nil
			case "font":
				d.Font = nil
			case "colors.window.background":
				delete(d.Colors, "window.background")
			case "metrics.message.maxWidth":
				delete(d.Metrics, "message.maxWidth")
			case "typography.body":
				delete(d.Typography, "body")
			case "typography.body.size":
				v := d.Typography["body"]
				v.Size = nil
				d.Typography["body"] = v
			case "typography.body.weight":
				v := d.Typography["body"]
				v.Weight = nil
				d.Typography["body"] = v
			case "motion.page":
				delete(d.Motion, "page")
			case "motion.page.duration":
				v := d.Motion["page"]
				v.Duration = nil
				d.Motion["page"] = v
			case "motion.page.easing":
				v := d.Motion["page"]
				v.Easing = nil
				d.Motion["page"] = v
			case "materials.header":
				delete(d.Materials, "header")
			case "materials.header.kind", "materials.header.background", "materials.header.alpha", "materials.header.backdropBlur", "materials.header.glass", "materials.header.clarity":
				v := d.Materials["header"]
				switch section {
				case "materials.header.kind":
					v.Kind = nil
				case "materials.header.background":
					v.Background = nil
				case "materials.header.alpha":
					v.Alpha = nil
				case "materials.header.backdropBlur":
					v.BackdropBlur = nil
				case "materials.header.glass":
					v.Glass = nil
				case "materials.header.clarity":
					v.Clarity = nil
				}
				d.Materials["header"] = v
			}
			_, e := Compile(d)
			check(t, section, e != nil && strings.Contains(e.Error(), section))
		})
	}
}

func TestImportErrors(t *testing.T) {
	for _, tt := range []struct{ json, path string }{
		{`{"version":1,"base":"desktop-light"}`, "version"},
		{`{"version":2,"base":"desktop-light"}`, "version"},
		{`{"version":3,"base":"desktop-light"}`, "version"},
		{`{"version":4,"base":"desktop-light"}`, "version"},
		{`{"version":5,"base":"desktop-light"}`, "version"},
		{`{"version":6,"base":"desktop-light"}`, "version"},
		{`{"version":7,"base":"desktop-light"}`, "version"},
		{`{"version":8,"base":"desktop-light"}`, "version"},
		{`{"version":9,"base":"desktop-light","typo":1}`, "typo"},
		{`{"version":9,"base":"desktop-light","colors":{"bad":"#ffffff"}}`, "colors.bad"},
		{`{"version":9,"base":"desktop-light","colors":{"window.background":1}}`, "colors.window.background"},
		{`{"version":9,"base":"desktop-light","metrics":{"message.maxWidth":"large"}}`, "metrics.message.maxWidth"},
		{`{"version":9,"base":"desktop-light","typography":{"body":{"typo":1}}}`, "typography.body.typo"},
		{`{"version":9,"base":"desktop-light","motion":{"page":{"typo":1}}}`, "motion.page.typo"},
		{`{"version":9,"base":"desktop-light","materials":{"header":{"typo":1}}}`, "materials.header.typo"},
		{`{"version":9,"base":"desktop-light","materials":{"header":{"alpha":"half"}}}`, "materials.header.alpha"},
		{`{"version":9,"base":"desktop-light","metrics":null}`, "metrics"},
		{`{"version":9,"base":"desktop-light","metrics":{"message.radiusLarge":null}}`, "metrics.message.radiusLarge"},
		{`{"version":9,"base":"desktop-light","dark":null}`, "dark"},
		{`{"version":9,"base":"desktop-light","materials":{"header":{"alpha":null}}}`, "materials.header.alpha"},
		{`null`, "$"}, {`[]`, "$"}, {`{"version":9} trailing`, "$"}, {`{"version":`, "$"},
	} {
		t.Run(tt.path+tt.json, func(t *testing.T) {
			_, e := Import([]byte(tt.json))
			check(t, "keyed error", e != nil && strings.Contains(e.Error(), tt.path))
		})
	}
}

func TestSparseAliasesAndZero(t *testing.T) {
	d := base()
	d.Colors = map[string]string{"window.background": "#12345600", "conversation.background": "@window.background", "message.in.background": "@conversation.background"}
	d.Metrics = map[string]float32{"message.radiusLarge": 0}
	d.Motion = map[string]MotionDefinition{"page": {Duration: new(float64(0))}}
	d.Materials = map[string]SurfaceDefinition{"header": {Background: new("@message.in.background"), Alpha: new(float32(0)), BackdropBlur: new(float32(0))}}
	v := compiled(t, d)
	check(t, "alias chain and transparent RGB", v.Colors().Message().In().Background() == ui.Color{R: 0x12, G: 0x34, B: 0x56, A: 0})
	check(t, "zero radius preserved", v.Metrics().Message().RadiusLarge() == 0)
	check(t, "omitted radius inherited", v.Metrics().Message().RadiusSmall() == 6)
	check(t, "zero duration preserved", v.Motion().Page().Duration() == 0)
	check(t, "zero surface alpha preserved", v.Materials().Header().Alpha() == 0)
	check(t, "zero backdrop preserved", v.Materials().Header().BackdropBlur() == 0)
	d.Colors["window.background"] = "#abcdef80"
	d.Materials["header"] = SurfaceDefinition{Background: new("@window.background"), Alpha: new(float32(.5)), BackdropBlur: new(float32(12)), Glass: new(GlassClear), Clarity: new(float32(.75))}
	d.Materials["window"] = SurfaceDefinition{Kind: new(Acrylic)}
	v = compiled(t, d)
	check(t, "alpha multiplies rather than replaces", v.Materials().Header().Background() == ui.Color{R: 0xab, G: 0xcd, B: 0xef, A: 64})
	check(t, "opaque fallback keeps RGB", v.Materials().Header().OpaqueBackground() == ui.RGB(0xab, 0xcd, 0xef))
	check(t, "backdrop independent of system material", v.Materials().Header().BackdropBlur() == 12)
	check(t, "glass mode and clarity", v.Materials().Header().Glass() == GlassClear && v.Materials().Header().Clarity() == .75)
	check(t, "window acrylic", v.Materials().Window().Kind().Vibrancy() == mygo.VibrancyAcrylic)
}

func TestPortableRoundTripAndOwnership(t *testing.T) {
	d := base()
	d.Font = new("Example Font")
	d.Colors = map[string]string{"window.background": "#11223380", "conversation.background": "@window.background"}
	d.Metrics = map[string]float32{"message.radiusLarge": 0}
	d.Typography = map[string]TextDefinition{"body": {Size: new(float32(15)), Weight: new(500)}}
	d.Motion = map[string]MotionDefinition{"page": {Duration: new(12.5), Easing: new(EaseInOut)}}
	d.Materials = map[string]SurfaceDefinition{"header": {Alpha: new(float32(0)), BackdropBlur: new(float32(9)), Glass: new(GlassRegular), Clarity: new(float32(.25))}}
	first := compiled(t, d)
	same := compiled(t, d)
	check(t, "unchanged definition reuses result", first == same)
	encoded, e := Export(d)
	if e != nil {
		t.Fatal(e)
	}
	imported, e := Import(encoded)
	if e != nil {
		t.Fatal(e)
	}
	second := compiled(t, imported)
	check(t, "portable base omitted", imported.Base == "")
	check(t, "alias retained", imported.Colors["conversation.background"] == "@window.background")
	check(t, "full definition roundtrip", reflect.DeepEqual(first.Definition(), second.Definition()))
	check(t, "all typed values roundtrip", first.colors == second.colors && first.metrics == second.metrics && first.typography == second.typography && first.motion == second.motion && first.materials == second.materials)
	again, e := Export(imported)
	if e != nil {
		t.Fatal(e)
	}
	check(t, "canonical export stable", string(encoded) == string(again))

	*d.Font = "Changed"
	*d.Typography["body"].Size = 20
	*d.Motion["page"].Duration = 99
	*d.Materials["header"].Alpha = 1
	d.Colors["window.background"] = "#000000"
	d.Metrics["message.radiusLarge"] = 3
	check(t, "input mutation cannot change compiled values", first.Typography().Font() == "Example Font" && first.Typography().Body().Size() == 15 && first.Motion().Page().Duration() == 12500*time.Microsecond && first.Materials().Header().Alpha() == 0 && first.Metrics().Message().RadiusLarge() == 0 && first.Colors().Window().Background() == ui.Color{R: 17, G: 34, B: 51, A: 128})
	changed := compiled(t, d)
	check(t, "mutated input recompiles", changed != first && changed.Typography().Body().Size() == 20)
	exported := first.Definition()
	*exported.Dark = true
	*exported.Font = "Other"
	*exported.Typography["body"].Weight = 900
	*exported.Motion["page"].Easing = Linear
	*exported.Materials["header"].Clarity = 1
	exported.Colors["window.background"] = "#ffffff"
	exported.Metrics["message.radiusLarge"] = 8
	check(t, "returned definition detached", !first.Dark() && first.Typography().Font() == "Example Font" && first.Typography().Body().Weight() == 500 && first.Motion().Page().Easing() == EaseInOut && first.Materials().Header().Clarity() == .25 && first.Metrics().Message().RadiusLarge() == 0 && first.Colors().Window().Background().R == 17)
	_, e = Export(Definition{Version: 99})
	check(t, "export validates", e != nil && strings.Contains(e.Error(), "version"))
}

func TestSourceColors(t *testing.T) {
	c := Light().Colors()
	cases := []struct {
		name string
		got  ui.Color
		want string
	}{
		{"window", c.Window().Background(), "#ffffff"},                           // colors.palette:9
		{"text", c.Window().Text(), "#000000"},                                   // colors.palette:10
		{"hover", c.Window().BackgroundHover(), "#f1f1f1"},                       // colors.palette:11
		{"muted", c.Window().MutedText(), "#999999"},                             // colors.palette:14
		{"primary alias", c.Control().PrimaryBackground(), "#40a7e3"},            // colors.palette:32 → 18
		{"primary hover", c.Control().PrimaryBackgroundHover(), "#39a5db"},       // colors.palette:33
		{"primary pressed", c.Control().PrimaryBackgroundPressed(), "#2095d0"},   // colors.palette:34
		{"surface hover", c.Control().SurfaceBackgroundHover(), "#e3f1fa"},       // colors.palette:43
		{"border", c.Control().Border(), "#e0e0e0"},                              // colors.palette:75
		{"focus", c.Control().Focus(), "#37a1de"},                                // colors.palette:39
		{"active conversation", c.Conversation().BackgroundActive(), "#419fd9"},  // colors.palette:218
		{"draft", c.Conversation().DraftForeground(), "#dd4b39"},                 // colors.palette:189
		{"muted badge", c.Conversation().UnreadBackgroundMuted(), "#bbbbbb"},     // colors.palette:195
		{"in alias", c.Message().In().Background(), "#ffffff"},                   // colors.palette:349 → 9
		{"in selected", c.Message().In().BackgroundSelected(), "#c2dcf2"},        // colors.palette:350
		{"out", c.Message().Out().Background(), "#effdde"},                       // colors.palette:351
		{"out selected", c.Message().Out().BackgroundSelected(), "#b7dbdb"},      // colors.palette:352
		{"in time", c.Message().In().TimestampForeground(), "#a0acb6"},           // colors.palette:363
		{"out time", c.Message().Out().TimestampForeground(), "#6db566"},         // colors.palette:365
		{"in code", c.Message().In().CodeForeground(), "#4e7391"},                // colors.palette:375
		{"out code", c.Message().Out().CodeForeground(), "#459866"},              // colors.palette:376
		{"quote in", c.Quote().In().Bar(), "#37a1de"},                            // colors.palette:370 → 39
		{"quote out", c.Quote().Out().Bar(), "#5eb854"},                          // colors.palette:372
		{"service", c.Service().Background(), "#517c417f"},                       // colors.palette:368
		{"compose", c.Compose().Background(), "#ffffff"},                         // colors.palette:458 → 349 → 9
		{"sidebar", c.Sidebar().Background(), "#293a4c"},                         // colors.palette:645
		{"sidebar text", c.Sidebar().TextForegroundActive(), "#64b9fa"},          // colors.palette:649
		{"menu", c.Menu().SeparatorForeground(), "#f1f1f1"},                      // colors.palette:60
		{"tooltip", c.Tooltip().Background(), "#eef2f5"},                         // colors.palette:94
		{"scrollbar alpha", c.Scrollbar().ThumbBackground(), "#00000053"},        // colors.palette:62
		{"fallback left value", c.Avatar().Peer1UserpicBackground2(), "#d45246"}, // colors.palette:320
		{"bot keyboard alpha", c.Keyboard().PrimaryBackground(), "#298acfcc"},    // colors.palette:84
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { check(t, tt.name, tt.got == ui.Hex(tt.want)) })
	}
	n := Dark().Colors()
	check(t, "night window", n.Window().Background() == ui.Hex("#17212b"))          // night colors.tdesktop-theme:1
	check(t, "night message", n.Message().In().Background() == ui.Hex("#182533"))   // night colors.tdesktop-theme:256
	check(t, "night outbound", n.Message().Out().Background() == ui.Hex("#2b5278")) // night colors.tdesktop-theme:258
}

func TestSourceMetricsAndType(t *testing.T) {
	m := Light().Metrics()
	for _, tt := range []struct {
		name      string
		got, want float32
	}{
		{"row", m.Conversation().RowHeight(), 62},                     // dialogs.style:91
		{"avatar", m.Conversation().AvatarSize(), 46},                 // dialogs.style:98
		{"row left", m.Conversation().PaddingLeft(), 10},              // dialogs.style:97
		{"row top", m.Conversation().PaddingTop(), 8},                 // dialogs.style:97
		{"header", m.Header().Height(), 54},                           // info.style:919
		{"max width", m.Message().MaxWidth(), 430},                    // chat.style:14
		{"msg avatar", m.Message().AvatarSize(), 33},                  // chat.style:23
		{"msg left", m.Message().PaddingLeft(), 11},                   // chat.style:25
		{"msg top", m.Message().PaddingTop(), 8},                      // chat.style:25
		{"large radius", m.Message().RadiusLarge(), 16},               // chat.style:327
		{"small radius", m.Message().RadiusSmall(), 6},                // chat.style:326 → basic.style:107
		{"quote radius", m.Quote().Radius(), 5},                       // chat_helpers.style:1287
		{"quote bar", m.Quote().OutlineWidth(), 3},                    // chat_helpers.style:1285
		{"reaction gap", m.Reaction().Gap(), 4},                       // chat.style:687
		{"media max", m.Media().MaxSize(), 430},                       // chat.style:169
		{"sticker max", m.Media().MaxStickerSize(), 224},              // chat.style:170
		{"album gap", m.Media().AlbumGap(), 4},                        // chat.style:510
		{"field min", m.Compose().FieldMinHeight(), 36},               // chat_helpers.style:1313
		{"field max", m.Compose().FieldMaxHeight(), 72},               // chat_helpers.style:1314
		{"expanded max", m.Compose().ExpandedMaxHeight(), 224},        // chat_helpers.style:1327
		{"sidebar width", m.Sidebar().Width(), 72},                    // window.style:229
		{"menu min", m.Menu().MinWidth(), 156},                        // widgets.style:980
		{"menu radius", m.Menu().Radius(), 8},                         // widgets.style:995
		{"to latest outer width", m.Navigation().ToLatestWidth(), 52}, // chat_helpers.style:1208
		{"to latest ripple", m.Navigation().ToLatestRippleSize(), 42}, // chat_helpers.style:1218
		{"base font", Light().Typography().Body().Size(), 13},         // basic.style:53
		{"sidebar font", Light().Typography().Sidebar().Size(), 11},   // window.style:236
	} {
		t.Run(tt.name, func(t *testing.T) { check(t, tt.name, tt.got == tt.want) })
	}
	check(t, "title semibold", Light().Typography().ConversationTitle().Weight() == 600)  // basic.style:55, numeric mapping documented
	check(t, "date fade", Light().Motion().DateFade().Duration() == 200*time.Millisecond) // chat.style:313
	check(t, "page duration", Light().Motion().Page().Duration() == 240*time.Millisecond) // basic.style:96
}

func TestApplyAndWindowIsolation(t *testing.T) {
	var got *ui.Theme
	var current *Theme
	source := Light()
	tester := ui.NewTester(func(c *ui.Context) { Apply(c, source); got = c.Theme(); current = T(c); ui.Text(c, "theme") }, 320, 200)
	want := ui.Theme{
		Background: ui.Hex("#ffffff"), Surface: ui.Hex("#ffffff"), SurfaceHover: ui.Hex("#e3f1fa"), SurfacePressed: ui.Hex("#c9e4f6"), Border: ui.Hex("#e0e0e0"),
		Text: ui.Hex("#000000"), TextMuted: ui.Hex("#999999"), Accent: ui.Hex("#40a7e3"), AccentHover: ui.Hex("#39a5db"), AccentPressed: ui.Hex("#2095d0"), AccentText: ui.Hex("#ffffff"),
		Danger: ui.Hex("#d14e4e"), Warning: ui.Hex("#eba52d"), Success: ui.Hex("#4ab44a"), Selection: ui.Hex("#c2dcf2"), Focus: ui.Hex("#37a1de"), Inverse: ui.Hex("#2c3033e5"), InverseText: ui.Hex("#ffffff"),
		Scrollbar: ui.Hex("#00000053"), ScrollbarWidth: 4, Radius: 6, Spacing: 4, FontSize: 13,
	}
	actual, expected := reflect.ValueOf(*got), reflect.ValueOf(want)
	for i := range actual.NumField() {
		// mygo 0.8.0 起 ui.Theme 带未导出的内部字段，reflect 读不了，只比导出的。
		if !actual.Type().Field(i).IsExported() {
			continue
		}
		check(t, "ui.Theme."+actual.Type().Field(i).Name, reflect.DeepEqual(actual.Field(i).Interface(), expected.Field(i).Interface()))
	}
	check(t, "T returns source", current == source)
	pointer := got
	tester.Frame()
	check(t, "Apply reuses mapped pointer", got == pointer)
	tester.SetPreferences(ui.Preferences{TextScale: 1.5})
	check(t, "system font scaled once", got.FontSize == 19.5 && source.Typography().Body().Size() == 13)
	tester.Frame()
	check(t, "scale never compounds", got.FontSize == 19.5)
	source = Dark()
	tester.Frame()
	check(t, "dark source applied", got.Dark && got.Background == ui.Hex("#17212b") && current == source)
	var other *Theme
	second := ui.NewTester(func(c *ui.Context) { other = T(c); ui.Text(c, "other") }, 320, 200)
	second.Frame()
	check(t, "windows isolated", other == Light() && current == Dark())
	d := base()
	d.Font = new("Example")
	d.Colors = map[string]string{"window.background": "#123456"}
	source = compiled(t, d)
	tester.Frame()
	check(t, "edited source updates ui", got.Background == ui.Hex("#123456") && got.Font == "Example" && current == source)
}
