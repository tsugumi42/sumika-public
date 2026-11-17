package main

// 系统三键的避让和拖动带由 layout.Window 的 Title 槽管理，不再给每栏重复补高度。
import (
	"embed"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"strings"
)

// 仅嵌入当前外壳使用的 Lucide 素材；授权见 assets/nav/LICENSE.txt。
//
//go:embed assets/nav/*.svg
var navFiles embed.FS

var navIcons = func() map[string]*ui.SVG {
	entries, err := navFiles.ReadDir("assets/nav")
	if err != nil {
		panic(err)
	}
	icons := make(map[string]*ui.SVG, len(entries))
	for _, entry := range entries {
		data, err := navFiles.ReadFile("assets/nav/" + entry.Name())
		if err != nil {
			panic(err)
		}
		icons[strings.TrimSuffix(entry.Name(), ".svg")] = ui.MustParseSVG(data)
	}
	return icons
}()

func navIcon(name string) *ui.SVG {
	icon, ok := navIcons[name]
	if !ok {
		panic("unknown navigation icon: " + name)
	}
	return icon
}

// 查看器尚未迁移，保留它使用的原生标题控件边界。
type barRoom struct{ Bar, Left, Right float32 }

func roomOf(b ui.TitleBar) barRoom {
	if b == (ui.TitleBar{}) {
		return barRoom{}
	}
	return barRoom{Bar: max(b.Height, 32), Left: max(b.Left, 0), Right: max(b.Right, 0)}
}

func (a *app) titleView(c *ui.Context) {
	ui.Row(c).Fill().AlignItems(ui.Center).Label("窗口标题带").Children(func() {
		if a.demoMode {
			ui.Text(c, "演示数据").SingleLine().TextColor(theme.T(c).Colors().Window().MutedText())
		}
	})
}
