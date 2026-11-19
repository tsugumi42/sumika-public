package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// MenuMorph（主人 2026-11-15：菜单展开收缩做成 morph）：面板从贴着按钮的那条边展开、关掉时缩回那条边。
// 朝下弹出（顶边贴按钮）和朝上弹出（窗口下面放不下，底边贴按钮）都要对；减少动态效果时直接到位。
func TestMenuMorphUnfoldsFromTrigger(t *testing.T) {
	ink := ui.RGB(90, 90, 90)
	for _, up := range []bool{false, true} {
		for _, reduce := range []bool{false, true} {
			name := map[bool]string{false: "朝下", true: "朝上"}[up] + "/" + reduceName(reduce)
			t.Run(name, func(t *testing.T) {
				open := false
				var trig ui.Rect
				var trigH ui.Handle
				tt := ui.NewTester(func(c *ui.Context) {
					trig = trigH.Bounds(c)
					// 朝上那组：按钮下面只留 40，放得下长到一半的小面板、放不下全尺寸的（方向要按全尺寸定）
					top := float32(10)
					if up {
						top = 300 - 30 - 40
					}
					col := ui.Column(c.Key("root")).Padding(top, 10, 0, 10).Fill()
					col.Children(func() {
						m := ui.MenuBase(c.Key("m"), &open)
						m.Trigger.Bind(&trigH).Label("开").Size(80, 30).Children(func() { ui.Text(c, "开") })
						m.Popup(func(panel ui.Element) {
							Derive(DefaultSettings()).MenuMorph(c, panel.Width(60).Padding(4).Background(ink), 0, nil)
							for _, s := range []string{"甲", "乙", "丙"} {
								m.Item().Label(s).Height(28).Children(func() { ui.Text(c, s) })
							}
						})
					})
				}, 300, 300)
				tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
				tt.Frame()
				tt.Frame()
				// 面板在画面里的上下沿：x=40 这一列（面板里、按钮外不重要，按钮在面板上方或下方）里深灰的范围
				span := func() (int, int) {
					img := tt.Image()
					y0, y1 := -1, -1
					for y := 0; y < img.Bounds().Dy(); y++ {
						i := img.PixOffset(40, y)
						if img.Pix[i] < 250 {
							if y0 < 0 {
								y0 = y
							}
							y1 = y
						}
					}
					return y0, y1
				}
				if err := tt.Click("开"); err != nil {
					t.Fatal(err)
				}
				var opens [][2]int
				settle(t, tt, func(int) { a, b := span(); opens = append(opens, [2]int{a, b}) })
				full := opens[len(opens)-1]
				if full[0] < 0 || full[1]-full[0] < 80 {
					t.Fatalf("展开到位后面板 %v，应有三行那么高", full)
				}
				edge := func(s [2]int) int { // 贴着按钮的那条边
					if up {
						return s[1]
					}
					return s[0]
				}
				if up && full[1] > int(trig.Y)+1 || !up && full[0] < int(trig.Y+trig.H)-1 {
					t.Fatalf("面板 %v 没在按钮 %+v 的%s", full, trig, map[bool]string{false: "下面", true: "上面"}[up])
				}
				if reduce {
					for _, s := range opens { // 不长：看得见的每一帧都是全高（淡入照常）
						if s[0] >= 0 && s != full {
							t.Errorf("减少动态：展开中途 %v 和全高 %v 不同，应直接是全高：%v", s, full, opens)
							break
						}
					}
				} else {
					mid := 0
					for i, s := range opens {
						if s[0] < 0 {
							continue
						}
						if abs(edge(s)-edge(full)) > 1 {
							t.Fatalf("展开第 %d 帧贴按钮的边 %d，应一直在 %d（从按钮那条边长出来）：%v", i, edge(s), edge(full), opens)
						}
						if h := s[1] - s[0]; h > 5 && h < full[1]-full[0]-5 {
							mid++
						}
						if i > 0 && opens[i-1][0] >= 0 && s[1]-s[0] < opens[i-1][1]-opens[i-1][0]-1 {
							t.Fatalf("展开时高度回头了：%v", opens)
						}
					}
					if mid < 3 {
						t.Errorf("展开中途只有 %d 帧，应是逐帧长开：%v", mid, opens)
					}
				}
				tt.Key(0, ui.KeyEscape)
				var shuts [][2]int
				tt.Frame()
				a, b := span()
				shuts = append(shuts, [2]int{a, b})
				settle(t, tt, func(int) { a, b := span(); shuts = append(shuts, [2]int{a, b}) })
				if last := shuts[len(shuts)-1]; last[0] >= 0 {
					t.Errorf("收完以后面板还在 %v", last)
				}
				if !reduce {
					mid := 0
					for i, s := range shuts {
						if s[0] < 0 {
							continue
						}
						if abs(edge(s)-edge(full)) > 2 {
							t.Fatalf("收起第 %d 帧贴按钮的边 %d，应一直在 %d（缩回按钮那条边）：%v", i, edge(s), edge(full), shuts)
						}
						if h := s[1] - s[0]; h > 5 && h < full[1]-full[0]-5 {
							mid++
						}
					}
					if mid < 2 {
						t.Errorf("收起中途只有 %d 帧，应是逐帧缩回：%v", mid, shuts)
					}
					// 关了再开：重新从按钮那条边长，不是直接整块出现
					if err := tt.Click("开"); err != nil {
						t.Fatal(err)
					}
					tt.Advance(16 * time.Millisecond)
					tt.Frame()
					if a, b := span(); a >= 0 && b-a > full[1]-full[0]-5 {
						t.Errorf("再开第一帧就是全高 %d-%d：没有重新长", a, b)
					}
					settle(t, tt, nil)
				}
			})
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
