package main

// 窗口主题只在加载和有效编辑时编译；根视图每帧取缓存，系统切色不覆盖用户的另一份主题。
import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
	"sumika-native/internal/lookpreset"
)

type appearanceState struct {
	settings              desksettings.Appearance
	light, night, current *theme.Theme
	editor                lookEditor
	revision              uint64
	notice, invalid       string
	busy                  bool
	appliedMaterial       theme.WindowMaterial
	appliedDark           bool
	textDirty             bool // 字号在滑块上拖着改了、还没写盘（松手才写）
	dark                  bool // 这一帧窗口是不是夜间（随系统或用户选的）：新界面据此挑令牌的深浅
	buttonsSet            bool
	save                  func(desksettings.Appearance, uint64)
	choose                func(bool, theme.Definition)
	bundled               bundledFont
}

type lookEditor struct {
	mode                       int
	accent, incoming, outgoing string
	large, small, alpha        float64
	sizeIdx                    int
	fontLabel                  string
	material                   string
	textDirty                  bool   // 字号在滑块上拖着改了、还没写盘（松手才写）
	panel                      int    // 面板材质分段当前选中第几项（新界面的标题栏、输入框）
	panelSeen                  string // panel 对应的材质名；外面改了才重新对齐
	preset                     string // 配色预设：原型、Telegram、自定义
}

func (a *app) initLook() {
	if a.look.light == nil {
		a.look.settings = desksettings.DefaultAppearance()
		a.look.light, a.look.night, _ = a.look.settings.Compile() // 默认外观一定能编译
	}
}

func (a *app) applyLook(c *ui.Context) {
	a.initLook()
	s := &a.look
	dark := c.Theme().Dark // Apply 之前还是 mygo 按系统给的基础主题。
	if s.settings.Mode != "system" {
		dark = s.settings.Mode == "night"
	}
	s.dark = dark
	active := s.light
	if dark {
		active = s.night
	}
	theme.Apply(c, active)
	if s.current != active {
		s.current = active
		s.syncEditor()
	}
	// c.Vibrancy() 在绘制背景时判断实际能力，选择 none 不会丢掉再次启用的窗口能力。
	kind := active.Materials().Window().Kind()
	if a.win != nil && s.appliedMaterial != kind {
		a.win.SetVibrancy(kind.Vibrancy())
		s.appliedMaterial = kind
	}
	if a.win != nil && (!s.buttonsSet || s.appliedDark != dark) {
		if dark {
			a.win.SetWindowButtonsTheme(mygo.ThemeDark)
		} else {
			a.win.SetWindowButtonsTheme(mygo.ThemeLight)
		}
		s.appliedDark, s.buttonsSet = dark, true
	}
	// 背景由根容器画一次；再盖一层根背景会把半透明叠成实色。
	c.Root().Background(ui.Transparent)
}

func colorHex(v ui.Color) string {
	if v.A == 255 {
		return fmt.Sprintf("#%02x%02x%02x", v.R, v.G, v.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", v.R, v.G, v.B, v.A)
}

func (s *appearanceState) syncEditor() {
	t := s.current
	s.editor = lookEditor{
		accent:   colorHex(t.Colors().Control().PrimaryBackground()),
		incoming: colorHex(t.Colors().Message().In().Background()), outgoing: colorHex(t.Colors().Message().Out().Background()),
		large: float64(t.Metrics().Message().RadiusLarge()), small: float64(t.Metrics().Message().RadiusSmall()),
		sizeIdx: textSizeIndex(t.Typography().Body().Size()), alpha: float64(t.Materials().Window().Alpha()) * 100,
		material: string(t.Materials().Window().Kind()),
		preset:   lookpreset.Name(t),
	}
	switch s.settings.Mode {
	case "light":
		s.editor.mode = 1
	case "night":
		s.editor.mode = 2
	}
}

func (a *app) changeLook(edit func(*theme.Definition)) {
	s := &a.look
	d := s.current.Definition()
	edit(&d)
	t, err := theme.Compile(d)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	s.invalid = ""
	if t.Dark() {
		s.settings.Night, s.night = d, t
	} else {
		s.settings.Light, s.light = d, t
	}
	// 这里只换主题缓存，不重置正在输入的文本；切换版本时才重建编辑字段。
	s.current = t
	a.persistLook()
}

// applyPreset 换整套配色和圆角（亮色、夜间一起换，深浅模式不动）；字体、字号、动效保留。
func (a *app) applyPreset(name string) {
	s := &a.look
	light, night, ok := lookpreset.Pair(name, s.settings.Light, s.settings.Night)
	if !ok {
		return
	}
	lt, err := lookpreset.Compile(light)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	nt, err := lookpreset.Compile(night)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	s.invalid = ""
	s.settings.Light, s.settings.Night, s.light, s.night, s.current = light, night, lt, nt, nil
	a.persistLook()
}

func (a *app) persistLook() {
	s := &a.look
	s.revision++
	s.notice = ""
	if s.save != nil {
		s.save(s.settings, s.revision)
	}
}

func (a *app) resetLook() {
	s := &a.look
	s.settings = desksettings.DefaultAppearance()
	s.light, s.night, _ = s.settings.Compile()
	s.current = nil
	s.invalid = ""
	a.persistLook()
}

func (a *app) importLook(d theme.Definition) error {
	t, err := lookpreset.Compile(d)
	if err != nil {
		return err
	}
	s := &a.look
	if t.Dark() {
		s.settings.Night, s.night, s.settings.Mode = d, t, "night"
	} else {
		s.settings.Light, s.light, s.settings.Mode = d, t, "light"
	}
	s.current, s.invalid = nil, ""
	a.persistLook()
	return nil
}

func windowBackground(c *ui.Context) ui.Color {
	s := theme.T(c).Materials().Window()
	if !c.Vibrancy() || s.Kind() == theme.None {
		return s.OpaqueBackground()
	}
	return s.Background()
}
