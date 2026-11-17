// Package assetsvc 是素材服务的插件：把软件自带的图标、表情、字体（internal/assets）登记成插件服务 "assets"（turn.ServiceAssets），
// 别的 Go 插件在清单里写 Requires 再用 turn.AssetsOf 拿，不直接 import 素材包。
// 脚本那层（moejs）以后经 sumika.assets.icon / emoji / font 拿同一份，见规格-插件两层 第九节（本轮只写了草案，没实现）。
package assetsvc

import (
	"sumika-native/internal/assets"
	"sumika-native/internal/assets/emoji"
	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/assets/icons"
	"sumika-native/internal/turn"
)

// ID 是插件代号。
const ID = "assets"

// Plugin 是素材插件；零值用默认找法（exe 旁的 fonts/、twemoji/，开发时 native/ 下的）。
type Plugin struct {
	// EmojiDir 明说 Twemoji 目录；空就按 assets.Twemoji 的顺序找。
	EmojiDir string
	// Fonts 是字体找法的设置；零值是默认。
	Fonts fonts.Options
}

func (p *Plugin) ID() string { return ID }

// Manifest 声明提供 "assets" 服务。关不掉：它是别的插件的依赖，也没有可调的设置。
func (p *Plugin) Manifest() turn.Manifest {
	return turn.Manifest{
		Name:      "素材",
		Sub:       "图标、表情、字体，给别的插件用",
		Provides:  []string{turn.ServiceAssets},
		Essential: true,
	}
}

func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	svc := &service{fonts: p.Fonts}
	if dir, _ := assets.Twemoji(p.EmojiDir); dir != "" {
		svc.emoji = emoji.NewSet(dir)
	}
	return nil, r.Provide(turn.ServiceAssets, svc)
}

type service struct {
	emoji *emoji.Set // nil：没找到 Twemoji 目录
	fonts fonts.Options
}

var _ turn.Assets = (*service)(nil)

func (s *service) Icon(name string, fill bool) (string, error) { return icons.Markup(name, fill) }

func (s *service) IconNames() []string { return icons.Names() }

func (s *service) Emoji(seq string) (string, bool) { return s.emoji.Markup(seq) }

func (s *service) SplitEmoji(text string) []turn.EmojiPiece {
	pieces := emoji.Split(text)
	out := make([]turn.EmojiPiece, len(pieces))
	for i, p := range pieces {
		out[i] = turn.EmojiPiece{Text: p.Text, Emoji: p.Emoji}
	}
	return out
}

func (s *service) Fonts(purpose turn.AssetFontPurpose) []turn.AssetFont {
	p := fonts.Body
	if purpose == turn.AssetFontMono {
		p = fonts.Mono
	}
	var out []turn.AssetFont
	for _, f := range fonts.Chain(p, s.fonts) {
		src := map[fonts.Source]string{fonts.SourceWenYuan: "wenyuan", fonts.SourceMaple: "maple", fonts.SourceSystem: "system"}[f.Source]
		out = append(out, turn.AssetFont{Name: f.Name, Source: src, Path: f.Path, Weight: f.Weight, Script: f.Script})
	}
	return out
}
