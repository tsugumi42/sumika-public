package gallery

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func settingsAssert(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func TestSettingsDescriptionLink(t *testing.T) {
	test := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); settingsSections(c) }, 640, 600)
	if err := test.Click("查看示例"); err != nil {
		t.Fatal(err)
	}
	settingsAssert(t, "description-inline-link-action", test.HasText("说明链接已激活"))
}
func TestModelSettingsHostAcceptance(t *testing.T) {
	tr := theme.Light()
	test := ui.NewTester(func(c *ui.Context) { theme.Apply(c, tr); settingsModels(c) }, 640, 768)
	if err := test.Click("澪的模型"); err != nil {
		t.Fatal(err)
	}
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	test.Type("Aurora 10")
	test.Key(0, ui.KeyDown)
	test.Key(0, ui.KeyEnter)
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	settingsAssert(t, "model-example-accepts-selected-id", test.HasText("青岚 Aurora 10"))
	if err := test.Click("最大输出"); err != nil {
		t.Fatal(err)
	}
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	test.Key(ui.Ctrl, ui.KeyA)
	test.Type("0")
	test.Key(0, ui.KeyEnter)
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	settingsAssert(t, "model-example-saves-explicit-zero", test.HasText("0 tokens"))
	tr = theme.Dark()
	test.Frame()
	settingsAssert(t, "model-example-preserves-state-on-theme-change", test.HasText("青岚 Aurora 10") && test.HasText("0 tokens"))
}
