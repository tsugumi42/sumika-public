package desksettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/lookpreset"
)

// Appearance 是窗口自己的主题；两份配色分开存，跟随系统时不拿亮色颜色硬套夜间。
type Appearance struct {
	Mode  string           `json:"mode"`
	Light theme.Definition `json:"light"`
	Night theme.Definition `json:"night"`
}

// DefaultFont 是默认界面字体的家族名：程序启动时从 exe 旁边的 fonts/ 注册文渊圆体；没找到时 mygo 对不认识的家族名回退到系统字体。
const DefaultFont = "WY Rounded"

// SystemFont 是"明确选了系统字体"存的值。它不是真的字体名，mygo 对不认识的家族名回退到系统字体，正好是想要的效果；
// 空字符串和缺这一项都读成默认字体，这样加默认字体之前存下来的外观（字体是空的）也会换成新默认。
const SystemFont = "系统字体"

// DefaultTextSize 是没存过设置时的正文字号：原来是 16，主人 2026-11-11 和 Telegram、Codex 实测对比后定 15（ADR-350）。
// 已经存过的设置不受影响，只改没有设置时用的值。
const DefaultTextSize float32 = 15

// ScaleSpacing 是跟着字号一起缩放的间距：控件间距和气泡内边距。圆角、三栏宽度、头像和图标像素不跟。
var ScaleSpacing = []string{
	"control.spacing",
	"message.paddingLeft", "message.paddingTop", "message.paddingRight", "message.paddingBottom",
}

// ScaleText 把主题的所有字号按“新正文字号 ÷ 现在的正文字号”同比例缩放，正文字号直接取新值；
// 间距（ScaleSpacing）不按“现在的”乘，而是从预设（正文字号 13）按 新字号 ÷ 预设正文字号 重算：
// 默认外观只缩了字号没写间距，按现在的乘就会和预设对不上，来回切也会累积误差。
func ScaleText(d *theme.Definition, size float32) {
	body, ok := d.Typography["body"]
	if !ok || body.Size == nil || *body.Size <= 0 {
		return
	}
	ratio := size / *body.Size
	for key, value := range d.Typography {
		if value.Size != nil {
			value.Size = new(*value.Size * ratio)
			d.Typography[key] = value
		}
	}
	body = d.Typography["body"]
	body.Size = new(size)
	d.Typography["body"] = body
	preset := theme.Light()
	if d.Base == theme.DarkBase {
		preset = theme.Dark()
	}
	pm, presetBody := preset.Definition().Metrics, preset.Typography().Body().Size()
	for _, key := range ScaleSpacing {
		if _, ok := d.Metrics[key]; ok && presetBody > 0 {
			d.Metrics[key] = pm[key] * size / presetBody
		}
	}
}

// defaultDefinition 是一份默认主题：预设的字号按 DefaultTextSize 缩放后写进 typography。
// 不写 metrics：只要有尺寸覆盖项，lookpreset.Bare 就不再把它当“没自定义”，配色会被打回 Telegram。
func defaultDefinition(base string, preset *theme.Theme) theme.Definition {
	d := theme.Definition{Version: theme.Version, Base: base, Font: new(DefaultFont), Typography: preset.Definition().Typography}
	ScaleText(&d, DefaultTextSize)
	return d
}

func DefaultAppearance() Appearance {
	return Appearance{Mode: "system",
		Light: defaultDefinition(theme.LightBase, theme.Light()),
		Night: defaultDefinition(theme.DarkBase, theme.Dark())}
}

// Compile 在外部输入边界检查模式与两份主题；渲染时直接选缓存，不逐帧编译。
// 只有 base、没有覆盖项的主题按内置的原型预设编译（lookpreset）。
func (a Appearance) Compile() (light, night *theme.Theme, err error) {
	switch a.Mode {
	case "system", "light", "night":
	default:
		return nil, nil, fmt.Errorf("appearance.mode: 不认识 %q", a.Mode)
	}
	light, err = lookpreset.Compile(a.Light)
	if err != nil {
		return nil, nil, fmt.Errorf("appearance.light: %w", err)
	}
	night, err = lookpreset.Compile(a.Night)
	if err != nil {
		return nil, nil, fmt.Errorf("appearance.night: %w", err)
	}
	if light.Dark() || !night.Dark() {
		return nil, nil, fmt.Errorf("appearance: light 必须是亮色，night 必须是夜间")
	}
	return light, night, nil
}

// Appearance 缺项用默认；坏文件也交出默认，同时把错误交给宿主提示。
func (s *Store) Appearance() (Appearance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fallback := DefaultAppearance()
	m, err := s.load()
	if err != nil {
		return fallback, err
	}
	raw, ok := m["appearance"]
	if !ok {
		return fallback, nil
	}
	var wire struct {
		Mode  string          `json:"mode"`
		Light json.RawMessage `json:"light"`
		Night json.RawMessage `json:"night"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return fallback, fmt.Errorf("appearance: %w", err)
	}
	// 主题格式升过版本时旧配色不迁移（照当前格式重来），但用户选的亮色 / 夜间 / 跟随系统要留着，不能连模式一起丢。
	var stale []string
	if old := definitionVersion(wire.Light); old != theme.Version {
		stale = append(stale, fmt.Sprintf("亮色是第 %d 版", old))
		wire.Light = nil
	}
	if old := definitionVersion(wire.Night); old != theme.Version {
		stale = append(stale, fmt.Sprintf("夜间是第 %d 版", old))
		wire.Night = nil
	}
	light, night := fallback.Light, fallback.Night
	if wire.Light != nil {
		if light, err = theme.Import(wire.Light); err != nil {
			return fallback, fmt.Errorf("appearance.light: %w", err)
		}
	}
	if wire.Night != nil {
		if night, err = theme.Import(wire.Night); err != nil {
			return fallback, fmt.Errorf("appearance.night: %w", err)
		}
	}
	light, night = defaultFont(light), defaultFont(night)
	light, lightBad := sanitizeText(light, theme.Light())
	night, nightBad := sanitizeText(night, theme.Dark())
	a := Appearance{Mode: wire.Mode, Light: light, Night: night}
	if _, _, err := a.Compile(); err != nil {
		return fallback, err
	}
	if bad := strings.Join(append(lightBad, nightBad...), "、"); bad != "" {
		return a, fmt.Errorf("%w：%s", ErrTextReset, bad)
	}
	if len(stale) > 0 {
		return a, fmt.Errorf("%w：主题格式已经是第 %d 版（存的%s），配色换回默认，模式保留", ErrThemeUpgraded, theme.Version, strings.Join(stale, "、"))
	}
	return a, nil
}

// ErrThemeUpgraded：存的主题格式旧了、已按默认配色重来（模式保留）。返回的 Appearance 可用，宿主应写回一次，免得每次启动都重报
var ErrThemeUpgraded = errors.New("主题格式升级")

// defaultFont 把没写或写成空的字体换成默认字体。
func defaultFont(d theme.Definition) theme.Definition {
	if d.Font == nil || *d.Font == "" {
		d.Font = new(DefaultFont)
	}
	return d
}

// ErrTextReset：存的字号或字体不合理，已改回默认值。返回的 Appearance 可用，宿主应写回一次。
var ErrTextReset = errors.New("文字设置不合理")

// 字号的合理范围；设置页只给 13 到 18，手改文件放宽一点，再外面的当坏值。
const minTextSize, maxTextSize = 8, 40

// sanitizeText 检查一份主题的字号和字体：字号超范围就换回预设的整套字号，字体名为空以外的奇怪值（过长或带控制字符）换回默认字体。
// 返回改过哪几项；没问题原样返回。编译不过的交给后面统一的编译检查报错。
func sanitizeText(d theme.Definition, preset *theme.Theme) (theme.Definition, []string) {
	t, err := theme.Compile(d)
	if err != nil {
		return d, nil
	}
	var bad []string
	if body := t.Typography().Body().Size(); !(body >= minTextSize && body <= maxTextSize) {
		bad = append(bad, fmt.Sprintf("字号 %v 已改回默认字号", body))
		d.Typography = preset.Definition().Typography
	}
	if font := t.Typography().Font(); len(font) > 128 || strings.ContainsFunc(font, unicode.IsControl) {
		bad = append(bad, "字体名不合法，已改回默认字体")
		d.Font = new(DefaultFont)
	}
	return d, bad
}

// definitionVersion 只看 version 字段；读不出来当 0（也会按旧格式重置）
func definitionVersion(raw json.RawMessage) int {
	var v struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Version
}

// SetAppearance 只接收宿主已编译通过的快照，校验留在读取与编辑边界。
func (s *Store) SetAppearance(a Appearance) error {
	return s.setKey("appearance", a)
}
