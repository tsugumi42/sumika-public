package winshell

import (
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// NumberRoll 让一段数字文字变化时逐位滚动，不变的位不动（规格-过渡动画 第五节铁律 7 新规则）：
// 变大时新数字从下面升上来，变小时从上面落下来（down 为真）。字体的数字不一定等宽（文渊圆体就不是），
// 旁边有别的东西时，调用方要用 chatui.DigitSlotWidth 给它留固定宽。
// 弹簧由调用方从预设里取（k.snappy() 等，按画廊慢放换算）；系统要求减少动态效果时 mygo 自己只交叉淡变、不位移。
// 只给“一秒最多变一次”的数字用：比这更勤的（拖滑块、token 计数）先节流到一秒一次再滚，或者不滚（跟手的不加延迟）。
// 元素必须是单行纯文字（带 span 的 mygo 不走过渡），key 每次一样。
func (k *Kit) NumberRoll(e ui.Element, spring ui.SpringOptions, down bool) ui.Element {
	return e.FontFeatures(chatui.Tabular).ContentTransition(ui.ContentTransition{Numeric: true, Down: down, Animation: &ui.Animation{Spring: spring}})
}

// numberRollGap 是同一个数字两次滚动之间最少隔多久：规则是“一秒最多变一次的才逐位滚”，变得更勤的先压到一秒一次再滚（规格-过渡动画 铁律 7）。
// 一秒是主人 2026-11-15 定的，不是量出来的上限；倒计时正好每秒一次。
const numberRollGap = time.Second

// RollNumber 是一个会逐位滚的数字，返回那个 Text 让调用方设字号颜色等：v 变了就滚到新值，方向在变化的那一刻定下（变大从下面升、变小从上面落），
// 滚动期间不翻回去；v 变得比 numberRollGap 勤，显示的数字最多一秒换一次，停下后补到最新值（拖滑块时数字不会疯转）。
// key 是这个数字的标识，状态按它记；format 把值写成要显示的字。
func (k *Kit) RollNumber(c *ui.Context, key string, v float64, format func(float64) string) ui.Element {
	if k.numbers == nil {
		k.numbers = map[string]*numberState{}
	}
	st := k.numbers[key]
	if st == nil {
		st = &numberState{v: v}
		k.numbers[key] = st
	}
	if v != st.v {
		now := c.Now()
		if wait := st.at.Add(numberRollGap).Sub(now); wait <= 0 {
			st.down, st.v, st.at = v < st.v, v, now
		} else {
			c.After(wait)
		}
	}
	return k.NumberRoll(ui.Text(c.Key(key), format(st.v)), k.snappy(), st.down)
}

// SettingNumber 是设置行右边只读的数值：和 SettingValue 一样的外观，变化时逐位滚（RollNumber）。
func (k *Kit) SettingNumber(c *ui.Context, key string, v float64, format func(float64) string) {
	g, p := k.G, k.P
	// 宽度按最宽的数字留死（字体的数字不等宽，见 chatui.DigitSlotWidth），右对齐贴着行尾，数字变了左边的名字不被推动
	w := chatui.DigitSlotWidth(c, format(v), ui.Span{Font: k.Font, Size: g.LabelSize, Features: chatui.Tabular})
	k.RollNumber(c, key, v, format).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).SingleLine().Width(w).TextAlign(ui.End).Shrink(0)
}

// numberState 记着一个数字现在显示的值、方向和上次换值的时刻。
type numberState struct {
	v    float64
	down bool
	at   time.Time
}

// IconMorph 是一个会变形的图标：icon 是 chatui.Icon 的名字，换了名字就从旧图标的轮廓流到新图标的轮廓（灵敏弹簧，按慢放换算）。
// key 是这个位置的标识，状态按它记；到位后画的就是普通图标元素。系统要求减少动态效果时直接换。
// 描边写法的图标不变形，退回 mygo 自带的替换（旧的缩走、新的长出来）。算法和画法在 chatui（iconmorph*.go）。
func (k *Kit) IconMorph(c *ui.Context, key, icon string, size float32, color ui.Color) ui.Element {
	if k.morphs == nil {
		k.morphs = map[string]*chatui.IconMorph{}
	}
	m := k.morphs[key]
	if m == nil {
		m = &chatui.IconMorph{}
		k.morphs[key] = m
	}
	return m.Show(c, key, chatui.MorphGlyph(icon, false), size, color, k.snappy())
}
