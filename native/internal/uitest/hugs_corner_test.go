package uitest

import "testing"

// CornerExempt（主人 2026-11-15 定改几何检查规则，独立复核后收紧）：对应角的等距同心检查只跳过
// “两边距离差得多（超过父圆角的四分之一）、而且圆弧上离父圆弧比离直边远”的子元素——一行按钮里离角远的那颗。
// 两边等距的一律严格检查（等距本身就是贴着角）；差几像素的不等距也严格检查。
// 数大多取自 sumika-proto“最大”外观的审批卡：气泡圆角 39；反例来自复核。
func TestCornerExempt(t *testing.T) {
	cases := []struct {
		name       string
		R, a, b, r float32
		exempt     bool
	}{
		// 该跳过的：一行里离角远的按钮
		{"一行里第二颗胶囊按钮（离右 30.5、离下 12.5、圆角 25）", 39, 30.5, 12.5, 25, true},
		{"同一颗按钮圆角换成 26.5", 39, 30.5, 12.5, 26.5, true},
		{"一行里离角远的窄圆点按钮（离右 28、离下 8、圆角 8）", 24, 28 - 4, 8, 8, true},
		// 该严格检查的
		{"同心的子元素（等距 12.5、圆角 26.5）", 39, 12.5, 12.5, 26.5, false},
		{"等距但圆角太小（4）", 39, 12.5, 12.5, 4, false},
		{"等距的尖角", 39, 12.5, 12.5, 0, false},
		{"等距但圆角偏大（32）", 39, 12.5, 12.5, 32, false},
		{"等距但圆角偏大（36，整宽胶囊行）", 39, 12.5, 12.5, 36, false},
		{"小圆角父元素、等距 6、圆角 7（下限区间的错圆角）", 8, 6, 6, 7, false},
		{"不等距差 4.5（12.5 / 17）", 39, 12.5, 17, 26.5, false},
		{"不等距 4 / 8，父圆角 24", 24, 4, 8, 20, false},
		{"不等距 6 / 11，父圆角 24", 24, 6, 11, 18, false},
		{"离两边都 30 的尖角块（尖角离圆弧比离直边还近）", 39, 30, 30, 0, false},
	}
	for _, c := range cases {
		if got := CornerExempt(c.R, c.a, c.b, c.r); got != c.exempt {
			t.Errorf("%s：CornerExempt(%v, %v, %v, %v) = %v，应为 %v", c.name, c.R, c.a, c.b, c.r, got, c.exempt)
		}
		if got := CornerExempt(c.R, c.b, c.a, c.r); got != c.exempt { // 两条边对调，结果一样
			t.Errorf("%s（两边对调）：%v，应为 %v", c.name, got, c.exempt)
		}
	}
}

// HugsCorner 本身：贴着父圆弧的判断（CornerExempt 用它）。
func TestHugsCorner(t *testing.T) {
	cases := []struct {
		name       string
		R, a, b, r float32
		want       bool
	}{
		{"同心", 39, 12.5, 12.5, 26.5, true},
		{"圆角太小", 39, 12.5, 12.5, 4, true},
		{"尖角", 39, 12.5, 12.5, 0, true},
		{"一行里第二颗按钮", 39, 30.5, 12.5, 25, false},
		{"父元素没有圆角", 0, 12, 12, 0, false},
	}
	for _, c := range cases {
		if got := HugsCorner(c.R, c.a, c.b, c.r); got != c.want {
			t.Errorf("%s：HugsCorner(%v, %v, %v, %v) = %v，应为 %v", c.name, c.R, c.a, c.b, c.r, got, c.want)
		}
	}
}
