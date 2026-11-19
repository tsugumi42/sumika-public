package uitest

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 检查器自己的回归：几棵已知的小树，每一类都要能抓到，也不能把正常的报成错。
func auditView(t *testing.T, view func(c *ui.Context)) []Finding {
	t.Helper()
	tt := ui.NewTester(view, 400, 300)
	tt.Frame()
	tt.Frame()
	fs, _ := Audit(Tree(tt), ui.Rect{W: 400, H: 300})
	return fs
}

func has(fs []Finding, class int, sub, name string) bool {
	for _, f := range fs {
		if f.Class == class && f.Sub == sub && strings.Contains(f.Path, name) {
			return true
		}
	}
	return false
}

func TestAuditScrollContent(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Column(c.Key("root")).Children(func() {
			ui.Scroll(c.Key("sc")).Width(200).MaxHeight(50).Children(func() {
				ui.Column(c.Key("content")).Children(func() {
					for i := 0; i < 5; i++ {
						ui.Text(c, "行").Height(20)
					}
				})
			})
		})
	})
	if !has(fs, ClassOverClip, SubScrollAxis, "content") {
		t.Errorf("滚动内容超出视口应该报成滚动方向：%v", fs)
	}
	for _, f := range fs {
		if f.Class == ClassOverParent {
			t.Errorf("滚动容器里的内容不该报成越过父元素：%v", f)
		}
	}
}

func TestAuditClipAndParent(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Column(c.Key("root")).Children(func() {
			ui.Box(c.Key("clipper")).Width(100).Height(30).Clip().Children(func() {
				ui.Box(c.Key("wide")).Width(200).Height(10)
			})
			ui.Box(c.Key("plain")).Width(100).Height(30).Children(func() {
				ui.Box(c.Key("spill")).Width(200).Height(10)
			})
		})
	})
	if !has(fs, ClassOverClip, SubClipAxis, "wide") {
		t.Errorf("Clip 切掉的内容没报：%v", fs)
	}
	if !has(fs, ClassOverParent, SubOverParent, "spill") {
		t.Errorf("越过不裁剪的父元素没报：%v", fs)
	}
	if has(fs, ClassOverParent, SubOverParent, "wide") {
		t.Errorf("Clip 父元素里的不该报第 2 类：%v", fs)
	}
}

func TestAuditHiddenRoom(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Column(c.Key("root")).Children(func() {
			ui.Box(c.Key("ghost")).Size(50, 20).Invisible()
			ui.ButtonBase(c.Key("clear")).Size(50, 20).Opacity(0).Children(func() { ui.Text(c, "钮") })
			ui.Box(c.Key("folded")).Width(100).Height(0).Clip().Children(func() {
				ui.ButtonBase(c.Key("inside")).Size(50, 20).Children(func() { ui.Text(c, "里面") })
			})
		})
	})
	if !has(fs, ClassHiddenRoom, SubInvisible, "ghost") {
		t.Errorf("Invisible 占位没报：%v", fs)
	}
	if !has(fs, ClassHiddenRoom, SubOpacity0, "clear") {
		t.Errorf("透明度 0 没报：%v", fs)
	}
	if !has(fs, ClassHiddenRoom, SubClippedAway, "inside") {
		t.Errorf("收起后还可点的元素没报：%v", fs)
	}
}

func TestAuditTextWide(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Column(c.Key("root")).Width(300).Children(func() {
			ui.Text(c, "短").FillWidth().Selectable()
			ui.Text(c, "普通短字")
		})
	})
	if !has(fs, ClassTextWide, SubTextFill, "短") {
		t.Errorf("拉满且可选中的文字没报：%v", fs)
	}
}

func TestAuditHitOverlap(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Box(c.Key("root")).Size(200, 100).Children(func() {
			ui.ButtonBase(c.Key("a")).Absolute().Left(0).Top(0).Size(60, 30).Children(func() { ui.Text(c, "甲") })
			ui.ButtonBase(c.Key("b")).Absolute().Left(40).Top(0).Size(60, 30).Children(func() { ui.Text(c, "乙") })
			ui.ButtonBase(c.Key("far")).Absolute().Left(120).Top(0).Size(60, 30).Children(func() { ui.Text(c, "丙") })
		})
	})
	if !has(fs, ClassHitArea, SubHitAbsolute, "a") {
		t.Errorf("两个重叠的可点元素没报：%v", fs)
	}
	for _, f := range fs {
		if f.Sub == SubHitAbsolute && strings.Contains(f.Name, "far") {
			t.Errorf("不相交的不该报：%v", f)
		}
	}
}

func TestAuditClean(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Column(c.Key("root")).Width(300).Gap(4).Children(func() {
			ui.Text(c, "标题")
			ui.Row(c.Key("row")).Gap(4).Children(func() {
				ui.ButtonBase(c.Key("x")).Size(40, 20).Children(func() { ui.Text(c, "钮") })
				ui.ButtonBase(c.Key("y")).Size(40, 20).Children(func() { ui.Text(c, "钮") })
			})
		})
	})
	for _, f := range fs {
		if f.Class != ClassTextWide {
			t.Errorf("规矩的小树不该有发现：%v", f)
		}
	}
}

func TestAuditAccessHiddenStillChecked(t *testing.T) {
	fs := auditView(t, func(c *ui.Context) {
		ui.Column(c.Key("root")).Children(func() {
			ui.Box(c.Key("plain")).Width(100).Height(30).Children(func() {
				ui.Box(c.Key("spill")).Width(200).Height(10).AccessHidden(true)
			})
		})
	})
	if !has(fs, ClassOverParent, SubOverParent, "spill") {
		t.Errorf("AccessHidden 的元素照常绘制，越出父元素要报第 2 类：%v", fs)
	}
}

// RequireGlyphs 的判断依据：有字体时两个不同的字画出不同的图，所以不跳过；跳过与否本身随机器字体变，这里只钉住“有字体的机器上不跳过”。
// 缺字体那一侧（跳过）只能在没有中文字体的机器上看到，由 CI 的 Linux 一格走一遍。
func TestGlyphImageDiffersBetweenCharacters(t *testing.T) {
	a, b := glyphImage("汉"), glyphImage("永")
	if !hasInk(a) || !hasInk(b) {
		t.Skip("这台机器画不出中文字")
	}
	if sameImage(a, b) {
		t.Fatal("两个不同的汉字画出了同一张图：判断“是不是真的画出了字”的办法失效")
	}
	if !sameImage(a, glyphImage("汉")) {
		t.Fatal("同一个字两次画出的图不一样")
	}
}
