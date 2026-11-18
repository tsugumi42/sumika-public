package catalog

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// saveSettingsShot 在设了 GALLERY_SHOT 时把当前画面存成 PNG。
func saveSettingsShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("GALLERY_SHOT")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// reveal 把内容区往下（或往回）滚到 name 露出来。
func reveal(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		if r, ok := tt.Find(name); ok && r.H > 0 && r.Y > 100 && r.Y < 900 {
			return
		}
		dy := float32(120)
		if r, ok := tt.Find(name); ok && r.Y <= 100 {
			dy = -120
		}
		tt.Scroll(1100, 500, 0, dy)
		tt.Advance(50 * time.Millisecond)
	}
	t.Fatalf("滚不到 %q", name)
}

// clickControl 点名字叫 name 的控件本身（不是旁边同名的文字）：开关、下拉入口。
func clickControl(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	for _, a := range tt.AccessAll() {
		if a.Name == name && a.Role != ui.RoleText && a.Bounds.H > 0 {
			tt.ClickAt(a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2)
			tt.Advance(50 * time.Millisecond)
			return
		}
	}
	t.Fatalf("找不到控件 %q", name)
}

// 设置页样板里的控件是真的：开关能拨、加减能加、下拉能选、分类能换、折叠能展开；改过的项出小圆点，改回默认小圆点消失。
func TestSettingsDemoWorks(t *testing.T) {
	font := look()
	defer func() { chatui.EmojiDir = "" }()
	e, ok := Find("settings")
	if !ok {
		t.Fatal("登记表里没有设置页")
	}
	for _, dark := range []bool{false, true} {
		mode := map[bool]string{false: "light", true: "dark"}[dark]
		t.Run(mode, func(t *testing.T) {
			s := chatui.DefaultSettings()
			s.Dark = dark
			tt := openEntry(t, e, s, font)
			for _, want := range []string{"记忆和学习", "插件", "回话时想起什么", "每次回话最多想起几条", "每轮回复最多带入这么多条记忆"} {
				if !tt.HasText(want) {
					t.Fatalf("记忆页应有 %q；现在有 %q", want, tt.Texts())
				}
			}
			if tt.HasText("默认值") || tt.HasText("[suzune]") || tt.HasText("owner-private") {
				t.Error("界面上不该出现“默认值”和程序里的原样值")
			}
			saveSettingsShot(t, tt, "settings-memory-"+mode)

			// 加减：每次回话最多想起几条，12 是改过的（默认 8），减 4 次回到默认，小圆点应消失
			dots := func() int {
				n := 0
				for _, a := range tt.AccessAll() {
					if a.Name == "改过的项" {
						n++
					}
				}
				return n
			}
			before := dots()
			if before == 0 {
				t.Fatal("样板里有改过的项，应有小圆点")
			}
			for i := 0; i < 4; i++ {
				if err := tt.Click("每次回话最多想起几条 减"); err != nil {
					t.Fatal(err)
				}
				tt.Advance(50 * time.Millisecond)
			}
			if got := dots(); got != before-1 {
				t.Errorf("改回默认后小圆点应少一个：%d → %d", before, got)
			}
			if err := tt.Click("每次回话最多想起几条 加"); err != nil {
				t.Fatal(err)
			}
			if got := dots(); got != before {
				t.Errorf("再改一下小圆点应回来：%d", got)
			}

			// 开关、下拉
			reveal(t, tt, "待确认的事实自动生效")
			clickControl(t, tt, "待确认的事实自动生效")
			tt.Advance(300 * time.Millisecond)
			if got := dots(); got != before+1 {
				t.Errorf("拨开“待确认的事实自动生效”（默认关）应多一个小圆点：%d", got)
			}
			reveal(t, tt, "重排用的模型")
			clickControl(t, tt, "重排用的模型")
			tt.Advance(300 * time.Millisecond)
			if err := tt.Click("本地小模型"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(300 * time.Millisecond)
			if !tt.HasText("本地小模型") {
				t.Error("下拉选了“本地小模型”，入口应显示它")
			}

			// 换到插件页，展开“设置”和折叠列表
			if err := tt.Click("插件"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(400 * time.Millisecond)
			for _, want := range []string{"内置插件", "免费接口（天气、节假日、汇率）", "链接解析", "另有 17 个内置插件（只读）"} {
				if !tt.HasText(want) {
					t.Fatalf("插件页应有 %q；现在有 %q", want, tt.Texts())
				}
			}
			if tt.HasText("已注册 · 只读") {
				t.Error("十七行“已注册 · 只读”应收进折叠里")
			}
			saveSettingsShot(t, tt, "settings-plugins-"+mode)
			if err := tt.Click("另有 17 个内置插件（只读）"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(400 * time.Millisecond)
			if !tt.HasText(settingsDemoPlugins[0]) || !tt.HasText(settingsDemoPlugins[16]) {
				t.Error("展开后应看到十七个插件名")
			}
			if err := tt.Click("链接解析"); err == nil {
				// 名字文字不是按钮；点“设置”才展开，下面单独点
			}
			tt.Advance(50 * time.Millisecond)
			var settingsBtn []ui.Rect
			for _, a := range tt.AccessAll() {
				if a.Name == "设置" {
					settingsBtn = append(settingsBtn, a.Bounds)
				}
			}
			if len(settingsBtn) != 2 {
				t.Fatalf("应有两个“设置 ›”（免费接口、链接解析），现在 %d 个", len(settingsBtn))
			}
			b := settingsBtn[1]
			tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
			tt.Advance(400 * time.Millisecond)
			if !tt.HasText("每条消息最多几个链接") {
				t.Error("点链接解析的“设置”后应展开它的细项")
			}
			saveSettingsShot(t, tt, "settings-plugins-open-"+mode)
		})
	}
}

// 记忆页滚到最底下：调试开关在最后一组，组名“调试”。
func TestSettingsDemoDebugAtBottom(t *testing.T) {
	font := look()
	defer func() { chatui.EmojiDir = "" }()
	e, _ := Find("settings")
	for _, dark := range []bool{false, true} {
		mode := map[bool]string{false: "light", true: "dark"}[dark]
		s := chatui.DefaultSettings()
		s.Dark = dark
		tt := openEntry(t, e, s, font)
		for i := 0; i < 8; i++ {
			tt.Scroll(1100, 500, 0, 2000)
			tt.Advance(100 * time.Millisecond)
		}
		if !tt.HasText("调试") || !tt.HasText("显示配置键名和来源") {
			t.Fatalf("滚到底应看到“调试”组和“显示配置键名和来源”：%q", tt.Texts())
		}
		saveSettingsShot(t, tt, "settings-memory-bottom-"+mode)
	}
}
