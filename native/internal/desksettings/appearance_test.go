package desksettings

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestAppearancePersistence(t *testing.T) {
	s := New(newDir(t))
	want := DefaultAppearance()
	want.Mode = "night"
	want.Night.Colors = map[string]string{"control.primaryBackground": "#123456", "message.out.background": "#abcdea"}
	want.Night.Metrics = map[string]float32{"message.radiusLarge": 0}
	if err := s.SetAutoReceive(true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAppearance(want); err != nil {
		t.Fatal(err)
	}
	got, err := New(s.Dir()).Appearance()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("往返：%+v %v", got, err)
	}
	on, err := s.AutoReceive()
	if err != nil || !on {
		t.Fatalf("写主题破坏了其他桌面设置：%v %v", on, err)
	}
	v := theme.Version
	for _, raw := range []string{
		`broken`, `{"appearance":null}`,
		fmt.Sprintf(`{"appearance":{"mode":"night","light":{"version":%d,"base":"desktop-light"},"night":{"version":%d,"base":"nope"}}}`, v, v),
		fmt.Sprintf(`{"appearance":{"mode":"other","light":{"version":%d,"base":"desktop-light"},"night":{"version":%d,"base":"desktop-dark"}}}`, v, v),
		fmt.Sprintf(`{"appearance":{"mode":"light","light":{"version":%d,"base":"desktop-light","colors":{"window.text":"@window.text"}},"night":{"version":%d,"base":"desktop-dark"}}}`, v, v),
	} {
		if err := os.WriteFile(s.File(), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err = s.Appearance()
		if err == nil || !reflect.DeepEqual(got, DefaultAppearance()) {
			t.Fatalf("坏文件 %s：%+v %v", raw, got, err)
		}
	}
	// 主题格式升了版本：旧配色不要了，但选的模式要留着（2026-11-03 主人选的亮色被整份退回跟随系统）
	stale := fmt.Sprintf(`{"appearance":{"mode":"light","light":{"version":%d,"dark":false,"colors":{"window.text":"#000000ff"}},"night":{"version":%d,"base":"desktop-dark"}}}`, v-3, v)
	if err := os.WriteFile(s.File(), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = s.Appearance()
	keep := DefaultAppearance()
	keep.Mode = "light"
	// 文件里存着的夜间主题没写字号，读出来仍是预设字号；只有没存过的设置才用默认字号（2026-11-08 时是 16，ADR-350 起 15）
	keep.Night = theme.Definition{Version: theme.Version, Base: theme.DarkBase, Font: new(DefaultFont)}
	if err == nil || !reflect.DeepEqual(got, keep) {
		t.Fatalf("旧版本主题：%+v %v", got, err)
	}
	// 宿主把换好的写回一次，下次读就不再报（2026-11-08 每次启动都报"读主题失败"）
	if !errors.Is(err, ErrThemeUpgraded) {
		t.Fatalf("旧版本主题要能认出来写回：%v", err)
	}
	if err := s.SetAppearance(got); err != nil {
		t.Fatal(err)
	}
	if again, err := s.Appearance(); err != nil || !reflect.DeepEqual(again, keep) {
		t.Fatalf("写回后再读：%+v %v", again, err)
	}
	if err := os.Remove(s.File()); err != nil {
		t.Fatal(err)
	}
	got, err = s.Appearance()
	if err != nil || !reflect.DeepEqual(got, DefaultAppearance()) {
		t.Fatalf("缺文件：%+v %v", got, err)
	}
}

// 字体：空的读成默认字体（加默认字体之前存的外观也换新默认），明确选的系统字体和别的字体名原样保留。
func TestAppearanceFontDefaults(t *testing.T) {
	s := New(newDir(t))
	v := theme.Version
	for font, want := range map[string]string{`""`: DefaultFont, `"` + SystemFont + `"`: SystemFont, `"Microsoft YaHei"`: "Microsoft YaHei"} {
		raw := fmt.Sprintf(`{"appearance":{"mode":"light","light":{"version":%d,"base":"desktop-light","font":%s},"night":{"version":%d,"base":"desktop-dark"}}}`, v, font, v)
		writeFile(t, s, raw)
		got, err := s.Appearance()
		if err != nil || *got.Light.Font != want || *got.Night.Font != DefaultFont {
			t.Fatalf("font=%s：%q %q %v", font, *got.Light.Font, *got.Night.Font, err)
		}
	}
	// 字号离谱或字体名带控制字符：改回默认并报 ErrTextReset，其余设置留着
	bad := fmt.Sprintf(`{"appearance":{"mode":"night","light":{"version":%d,"base":"desktop-light","typography":{"body":{"size":900,"weight":400}},"font":"a\u0000b"},"night":{"version":%d,"base":"desktop-dark"}}}`, v, v)
	writeFile(t, s, bad)
	got, err := s.Appearance()
	if !errors.Is(err, ErrTextReset) || got.Mode != "night" {
		t.Fatalf("坏值：%+v %v", got.Mode, err)
	}
	light, _, cerr := got.Compile()
	if cerr != nil || light.Typography().Body().Size() != theme.Light().Typography().Body().Size() || light.Typography().Font() != DefaultFont {
		t.Fatalf("坏值没改回默认：%v", cerr)
	}
}

// 没存过设置时正文字号是 16（原型默认，主人 2026-11-08），亮色和夜间一样；字号档和间距跟着同比例缩放。
func TestDefaultAppearanceTextSize16(t *testing.T) {
	light, night, err := DefaultAppearance().Compile()
	if err != nil {
		t.Fatal(err)
	}
	for name, th := range map[string]*theme.Theme{"亮色": light, "夜间": night} {
		if got := th.Typography().Body().Size(); got != DefaultTextSize {
			t.Errorf("%s正文字号 %v，应为 %v", name, got, DefaultTextSize)
		}
		if got, preset := th.Typography().Sidebar().Size(), theme.Light().Typography().Sidebar().Size(); got <= preset {
			t.Errorf("%s侧栏字号 %v 没跟着放大（预设 %v）", name, got, preset)
		}
	}
}
