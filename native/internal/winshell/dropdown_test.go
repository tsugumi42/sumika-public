package winshell

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 下拉面板和入口同宽；选项多到要滚动时，右边留出滚动条的位置。
func TestDropdownPanelMatchesTrigger(t *testing.T) {
	k := NewKit(chatui.DefaultSettings(), "")
	open := false
	items := []string{"一", "二", "三", "四", "五", "六", "七", "八", "九", "十", "十一", "十二"}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Width(400).Children(func() {
			k.Dropdown(c, "font", "字体", &open, 0, items, func(int) {}, nil)
		})
	}, 600, 700)
	tt.Frame()
	tt.Frame()
	trigger, ok := tt.Find("字体")
	if !ok {
		t.Fatal("没有入口")
	}
	if err := tt.Click("字体"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	first, ok := tt.Find("一")
	if !ok {
		t.Fatalf("没有打开：%q", tt.Texts())
	}
	// 选项紧贴面板内距；右边比左边多留出滚动条的位置，面板不比入口宽
	left, right := first.X-trigger.X, trigger.X+trigger.W-(first.X+first.W)
	if left < 0 || right <= left || first.X+first.W > trigger.X+trigger.W {
		t.Fatalf("面板没和入口同宽或没留滚动条位置：入口 %+v 选项 %+v（左 %.1f 右 %.1f）", trigger, first, left, right)
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "下拉.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

// 带搜索的分组下拉：打开就能打字，筛完能选，分组名也能搜。
func TestPickerFiltersAndPicks(t *testing.T) {
	k := NewKit(chatui.DefaultSettings(), "")
	open, query, picked := false, "", ""
	pick := func(id string) func() { return func() { picked = id } }
	items := []MenuItem{
		{Label: "跟全局", Action: pick("")},
		{Label: "Agnes 2.5 Flash", Group: "agnes", Action: pick("a")},
		{Label: "DeepSeek V4.1 Flash", Group: "deepseek", Action: pick("d")},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Width(400).Children(func() {
			k.Picker(c, "model", "模型", "跟全局", &open, &query, "搜索", items)
		})
	}, 600, 700)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	for _, want := range []string{"agnes", "deepseek", "DeepSeek V4.1 Flash"} {
		if !tt.HasText(want) {
			t.Fatalf("打开后没有 %s：%q", want, tt.Texts())
		}
	}
	query = "DEEPSEEK"
	tt.Frame()
	tt.Frame()
	if tt.HasText("Agnes 2.5 Flash") || !tt.HasText("DeepSeek V4.1 Flash") {
		t.Fatalf("筛选不对：%q", tt.Texts())
	}
	if err := tt.Click("DeepSeek V4.1 Flash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if picked != "d" {
		t.Fatalf("选到的是 %q", picked)
	}
	if open {
		t.Fatal("选完该收起")
	}
}

func TestFilterMenuItems(t *testing.T) {
	items := []MenuItem{{Label: "Agnes 2.5", Group: "agnes"}, {Label: "V4", Group: "DeepSeek"}}
	if got := filterMenuItems(items, " deep "); len(got) != 1 || got[0].Label != "V4" {
		t.Fatalf("%+v", got)
	}
	if got := filterMenuItems(items, ""); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}
