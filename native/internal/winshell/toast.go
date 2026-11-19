package winshell

import (
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 提示条（照 Fluid 的 IslandToast）：一闪而过的结果，比如“已复制”“设置已保存”“已允许运行命令”。
//
//	收起：一颗小胶囊，藏在窗口顶上外面；
//	弹出：宽高一起长成“图标 + 一行字”的胶囊（“灵敏”弹簧），同时从上方落到位（“弹跳”弹簧）；字和图标晚一点点淡入，带一点模糊；
//	约 2 秒后自动收起：字先淡出，胶囊缩回小胶囊并升回去（升回去用“利落”）。
//
// 它是浮层（ui.Overlay），不占位置，不挤动下面任何元素，也不收点击。同一时刻只有一条：新的 Show 替换旧的，
// 胶囊直接改宽度，不重新落一次。系统要求减少动态效果时只淡入淡出，不位移、不变宽高。
// 材质跟着 Kit 当前设置（液态玻璃、磨砂、实色），影子是浮层档；圆角是胶囊。
//
// 用法：状态放在调用方（比如 State 或页面的结构体）里，每帧在构建里调一次 k.Toast；要提示时在事件里调 Show。
//
//	var saved winshell.ToastState
//	k.Toast(c, "saved", &saved)               // 每帧
//	if k.Button(…) { saved.Show("设置已保存", chatui.Icon("check", false)) }

// 提示条的几个数。小胶囊 40×36、落下的距离、字晚淡入的 140 毫秒和 4 的模糊都来自 Fluid IslandToast（笔记 data/dev/design-language-20261114/17-动画参考/Fluid-笔记.md）；
// 展开的高度取几何令牌的 HitRegular（常规单行控件高），宽是内容宽，不小于 minWidth；停留时间是主人要的“约 2 秒”。
const (
	toastCollapsedW float32 = 40  // Fluid IslandToast：收起的小胶囊 40×36（随字号缩放）
	toastCollapsedH float32 = 36  //
	toastMinWidth   float32 = 120 // 展开后宽不小于它（随字号缩放）
	toastBlur       float32 = 4   // Fluid IslandToast：内容带 4 的模糊淡入
	toastHold               = 2 * time.Second
	toastTextDelay          = 140 * time.Millisecond // Fluid IslandToast：内容晚 140 毫秒才开始淡入
	toastCloseLag           = 140 * time.Millisecond // 收起：字先淡出（和淡变同长），胶囊再缩回去
	toastShadowRoom float32 = 24                     // 藏起来时再往上多走这么远，浮层档阴影（模糊 24）也出了窗口
)

type toastPhase uint8

const (
	toastIdle    toastPhase = iota // 收起，藏着
	toastOpen                      // 弹出，停留计时中
	toastClosing                   // 收回去的路上
)

// ToastState 是一条提示条的状态：零值就是“没有提示”。
type ToastState struct {
	// Top 是胶囊落下后离窗口顶的距离；0 用“标题栏高度”，落在标题栏下面。
	Top float32

	text   string
	icon   *ui.SVG
	phase  toastPhase
	armed  bool // Show 过了，下一帧盖停留的起点
	openAt time.Time
}

// Show 弹出（或替换）提示：text 是一行字，icon 放在左边强调色浅底的小圆里（可以是 nil，那就只有字）。
// 正显示着的会被替换：胶囊直接改宽度，停留重新计时；正收回去的会再弹出来。
func (s *ToastState) Show(text string, icon *ui.SVG) {
	s.text, s.icon = text, icon
	s.phase, s.armed = toastOpen, true
}

// Dismiss 提前收起。
func (s *ToastState) Dismiss() {
	if s.phase == toastOpen {
		s.phase = toastClosing
	}
}

// Visible 说现在是不是有提示条在界面上（弹出着、收回去的路上都算）。
func (s *ToastState) Visible() bool { return s.phase != toastIdle }

// Toast 在这一帧画提示条；每帧都要调，没有提示时什么也不画（元素藏着，也不要帧）。
func (k *Kit) Toast(c *ui.Context, key string, s *ToastState) {
	g, p := k.G, k.P
	reduce := c.Preferences().ReduceMotion
	kt := k.T.S.Text / chatui.TextDefault

	// 停留计时：Show 后的第一帧盖起点，到点开始收；没到点就让界面在到点时再来一帧（不 Sleep、不开 goroutine）。
	now := c.Now()
	if s.phase == toastOpen {
		if s.armed {
			s.openAt, s.armed = now, false
		}
		if left := s.openAt.Add(slowDur(toastHold)).Sub(now); left <= 0 {
			s.phase = toastClosing
		} else {
			c.After(left)
		}
	}
	open := s.phase == toastOpen

	// 量内容：左内距 + 图标圆 + 间距 + 字 + 右内距；高度是常规单行控件高，图标圆和胶囊同心（内距 SpaceTight）。
	h := g.HitRegular
	inset := g.SpaceTight
	disc := h - 2*inset
	textW, _ := c.MeasureText(1e6, ui.Span{Text: s.text, Size: g.LabelSize, Weight: g.StrongWeight, Font: k.Font})
	content := inset + g.SpaceMedium + textW
	if s.icon != nil {
		content = inset + disc + g.SpaceSmall + textW + g.SpaceMedium
	}
	w := max(toastMinWidth*kt, content)

	// 目标：弹出时是 (w, h, 0)；收起时是小胶囊、藏到窗口顶上外面。减少动态效果时几何一直是弹出的样子，只淡入淡出。
	top := s.Top
	if top == 0 {
		top = g.HeaderHeight
	}
	cw, ch := toastCollapsedW*kt, toastCollapsedH*kt
	hiddenY := -(top + ch + toastShadowRoom)
	tw, th, ty := w, h, float32(0)
	if !open && !(reduce && s.phase == toastClosing) {
		tw, th, ty = cw, ch, hiddenY
	}
	if reduce {
		tw, th, ty = w, h, 0 // 不变宽高、不位移
	}
	ui.Overlay(c, func() {
		wrap := ui.Row(c.Key(key + "-toast")).Absolute().Left(0).Right(0).Top(top).Justify(ui.Center).AlignItems(ui.Start).PassThrough()
		wrap.Children(func() {
			capsule := ui.Row(c.Key(key + "-capsule")).Role(ui.RoleStatus).Label(s.text).Shrink(0).Justify(ui.Center).AlignItems(ui.Center).PassThrough()

			// 宽高：“灵敏”；位移：弹出落下用“弹跳”，收起升回去用“利落”（并且晚一点开始，让字先淡出）。
			grow, drop := ui.Animation{Spring: k.snappy()}, ui.Animation{Spring: k.springy()}
			fade := ControlFade // 淡变是时长，不是位移：带 NonMotion，减少动态效果时也淡
			textFade := fade
			if !open {
				lag := slowDur(toastCloseLag)
				grow.Delay, drop = lag, ui.Animation{Spring: k.firm(), Delay: lag}
			} else if !reduce {
				textFade.Delay = slowDur(toastTextDelay)
			}
			wv, hv := capsule.AnimateTo("w", tw, grow), capsule.AnimateTo("h", th, grow)
			yv := capsule.AnimateTo("y", ty, drop)
			to, ov := float32(0), float32(1)
			if open {
				to = 1
			}
			if reduce && !open { // 减少动态效果：整颗胶囊淡出，不收起
				ov = 0
			}
			tv := capsule.AnimateTo("t", to, textFade)
			ovv := capsule.AnimateTo("o", ov, fade)

			capsule.Width(wv).Height(hv).Radius(hv / 2)
			k.FloatPanel(capsule)
			capsule.Translate(0, yv)
			if reduce {
				capsule.Opacity(ovv)
			}
			capsule.Children(func() {
				row := ui.Row(c.Key(key+"-content")).Shrink(0).Width(content).Padding(0, g.SpaceMedium, 0, inset).Gap(g.SpaceSmall).AlignItems(ui.Center).Opacity(tv)
				if s.icon == nil {
					row.Padding(0, g.SpaceMedium, 0, g.SpaceMedium+inset)
				}
				if sigma := toastBlur * (1 - tv); sigma > .01 && !reduce {
					row.Blur(sigma)
				}
				row.Children(func() {
					if s.icon != nil {
						ui.Box(c.Key(key+"-disc")).Size(disc, disc).Shrink(0).Radius(disc / 2).Background(p.Self).TextColor(p.OnSelf).Center().Children(func() {
							ui.Icon(c, s.icon).Size(g.IconSmall, g.IconSmall)
						})
					}
					ui.Text(c.Key(key+"-text"), s.text).FontSize(g.LabelSize).FontWeight(g.StrongWeight).FixedLineHeight(g.LabelLine).TextColor(p.Ink).SingleLine()
				})
			})
			// 收回去的路上全部到位了：变回“藏着”，不再要帧
			if s.phase == toastClosing && capsule.Settled("w") && capsule.Settled("h") && capsule.Settled("y") && capsule.Settled("t") && capsule.Settled("o") {
				s.phase = toastIdle
			}
			wrap.Hidden(s.phase == toastIdle)
		})
	})
}
