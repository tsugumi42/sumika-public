package desksettings

import (
	"encoding/json"
	"fmt"
	"math"
)

const keyLook = "look"

// Look 是新界面外观里原型有的那几项（圆角、疏密、底色冷暖、聊天背景图案、等宽字连字）。
// 字号、字体、深浅在桌面主题里（appearance）；面板材质是另一个键（panelMaterial）。
type Look struct {
	Radius    float32 `json:"radius"`    // 按钮圆角 4～24
	Density   float32 `json:"density"`   // 间距单位 6～10
	Neutral   int     `json:"neutral"`   // 底色冷暖：0 偏冷 1 中性 2 偏暖
	Wallpaper bool    `json:"wallpaper"` // 聊天背景图案
	Ligatures bool    `json:"ligatures"` // 等宽字连字
	// 聊天区（消息列和输入框，两者永远同宽）占聊天栏宽度的百分比，40～100，步长 5。群聊默认 100：对方贴左、自己贴右，两边拉满；
	// 私聊默认 0：没设，按原型的居中版式（最宽 752）；设了就是百分比，不再封顶。
	GroupWidth   float32 `json:"groupWidth"`
	PrivateWidth float32 `json:"privateWidth"`
}

// DefaultLook 是原型的默认外观（chatui.DefaultSettings）：圆角 12、疏密 8、偏冷、有背景图案、连字开。
func DefaultLook() Look {
	return Look{Radius: 12, Density: 8, Neutral: 0, Wallpaper: true, Ligatures: true, GroupWidth: 100, PrivateWidth: 0}
}

func inRange(v, lo, hi float32) bool {
	f := float64(v)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && v >= lo && v <= hi
}

// Valid 说这份外观是不是在范围里。
func (l Look) Valid() bool {
	return inRange(l.Radius, 4, 24) && inRange(l.Density, 6, 10) && l.Neutral >= 0 && l.Neutral <= 2 &&
		inRange(l.GroupWidth, 40, 100) && (l.PrivateWidth == 0 || inRange(l.PrivateWidth, 40, 100))
}

// Look 读新界面外观。没有这项、文件不存在是默认；坏值（缺字段按默认补、越界、不是对象）整份回默认并返回错误。
func (s *Store) Look() (Look, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return DefaultLook(), err
	}
	raw, ok := m[keyLook]
	if !ok {
		return DefaultLook(), nil
	}
	l := DefaultLook() // 缺的字段保持默认
	if err := json.Unmarshal(raw, &l); err != nil {
		return DefaultLook(), fmt.Errorf("%s 里的 %s 读不懂：%w", s.File(), keyLook, err)
	}
	if !l.Valid() {
		return DefaultLook(), fmt.Errorf("%s 里的 %s 有越界的值，按默认", s.File(), keyLook)
	}
	return l, nil
}

// SetLook 只接受范围内的值。其他键原样留着。
func (s *Store) SetLook(l Look) error {
	if !l.Valid() {
		return fmt.Errorf("外观值越界：%+v", l)
	}
	return s.setKey(keyLook, l)
}
