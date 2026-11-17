package assetsvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

// consumer 是只依赖素材服务的测试插件：Setup 里经 turn.AssetsOf 拿服务，拿到就交给 got。
type consumer struct {
	got func(turn.Assets)
}

func (c consumer) ID() string { return "asset-consumer" }
func (c consumer) Manifest() turn.Manifest {
	return turn.Manifest{Name: "素材用户", Requires: []string{turn.ServiceAssets}}
}
func (c consumer) Setup(r *turn.Registry) (func(), error) {
	a, ok := turn.AssetsOf(r)
	if !ok {
		return nil, errors.New("素材服务没有")
	}
	c.got(a)
	return nil, nil
}

func state(h *turn.Hub, id string) string {
	for _, s := range h.PluginStatuses() {
		if s.ID == id {
			return s.State
		}
	}
	return "missing"
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 依赖方先开：等服务（pending）；素材插件一开，两个都起来，依赖方拿到的服务能取图标、表情、字体。
// 放回旧代码（没有服务、插件各自找素材目录）就红：AssetsOf 和 Requires 都是新的。
func TestAssetsServiceThroughPluginRegistry(t *testing.T) {
	t.Parallel()
	k := turntest.New(t, turntest.Options{})

	emojiDir, fontDir, winDir := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, filepath.Join(emojiDir, "1f44d.svg"), `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 36 36"><circle cx="18" cy="18" r="16"/></svg>`)
	write(t, filepath.Join(fontDir, fonts.WenYuanFull), "x")
	write(t, filepath.Join(fontDir, "MapleMono-NF-CN-Regular.ttf"), "x")
	write(t, filepath.Join(winDir, "msyh.ttc"), "x")

	var svc turn.Assets
	if err := k.Hub.Use(consumer{got: func(a turn.Assets) { svc = a }}); err != nil {
		t.Fatalf("等服务不是错误：%v", err)
	}
	if got := state(k.Hub, "asset-consumer"); got != "pending" {
		t.Fatalf("素材服务还没有，依赖方应该等服务：%s", got)
	}
	if err := k.Hub.Use(&Plugin{EmojiDir: emojiDir, Fonts: fonts.Options{Dirs: []string{fontDir}, SystemDir: winDir}}); err != nil {
		t.Fatal(err)
	}
	if state(k.Hub, ID) != "active" || state(k.Hub, "asset-consumer") != "active" || svc == nil {
		t.Fatalf("素材插件一开两个都该起来：%s %s %v", state(k.Hub, ID), state(k.Hub, "asset-consumer"), svc)
	}

	// 图标
	m, err := svc.Icon("add", false)
	if err != nil || !strings.HasPrefix(m, "<svg") || !strings.Contains(m, "currentColor") {
		t.Fatalf("图标：%q %v", m, err)
	}
	if _, err := svc.Icon("no_such_icon", false); err == nil {
		t.Fatal("没有的图标应当报错")
	}
	if names := svc.IconNames(); len(names) < 1600 || names[0] > names[len(names)-1] {
		t.Fatalf("图标名单不对：%d 个", len(names))
	}
	// 表情
	if e, ok := svc.Emoji("👍"); !ok || !strings.Contains(e, "<circle") {
		t.Fatalf("表情：%q %v", e, ok)
	}
	if _, ok := svc.Emoji("🎉"); ok {
		t.Fatal("没有这张图不该有表情")
	}
	pieces := svc.SplitEmoji("好👍呀")
	if len(pieces) != 3 || pieces[0].Emoji || !pieces[1].Emoji || pieces[1].Text != "👍" || pieces[2].Emoji {
		t.Fatalf("切表情：%+v", pieces)
	}
	// 字体
	body := svc.Fonts(turn.AssetFontBody)
	if len(body) != 3 || body[0].Source != "wenyuan" || body[1].Source != "maple" || body[1].Weight != 400 || body[2].Source != "system" || body[2].Script != "zh" {
		t.Fatalf("正文字体：%+v", body)
	}
	if mono := svc.Fonts(turn.AssetFontMono); len(mono) != 3 || mono[0].Source != "maple" || mono[1].Source != "wenyuan" {
		t.Fatalf("等宽字体：%+v", mono)
	}
}

// 没有 Twemoji 目录、没有字体时服务照样在：表情取不到、字体只有系统兜底，都不是错误。
func TestAssetsServiceWithoutFiles(t *testing.T) {
	t.Parallel()
	k := turntest.New(t, turntest.Options{})
	var svc turn.Assets
	empty := t.TempDir()
	p := &Plugin{EmojiDir: filepath.Join(empty, "没有"), Fonts: fonts.Options{Dirs: []string{empty}, SystemDir: empty}}
	if err := k.Hub.Use(p); err != nil {
		t.Fatal(err)
	}
	if err := k.Hub.Use(consumer{got: func(a turn.Assets) { svc = a }}); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Emoji("👍"); ok {
		t.Fatal("没有目录不该有表情")
	}
	if got := svc.Fonts(turn.AssetFontBody); len(got) != 0 {
		t.Fatalf("什么字体都没有：%+v", got)
	}
	if _, err := svc.Icon("add", true); err != nil {
		t.Fatalf("图标编进程序，和目录无关：%v", err)
	}
}

func TestManifestIsEssentialProvider(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if !m.Essential || len(m.Provides) != 1 || m.Provides[0] != turn.ServiceAssets || m.Name == "" || m.Sub == "" {
		t.Fatalf("清单：%+v", m)
	}
}
