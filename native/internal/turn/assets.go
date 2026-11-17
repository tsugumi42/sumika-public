package turn

// 软件自带素材（图标、表情、字体）给插件用的接口（规格-插件两层 第九节）。
// 素材本身在 internal/assets 下，由 internal/assetsvc 这个插件经 Registry.Provide 登记成服务 "assets"；
// 别的插件在清单里写 Requires: []string{ServiceAssets}，Setup 里用 AssetsOf 拿，不直接 import internal/assets
// （archcheck 的 TestPluginsDoNotImportAssetsDirectly 查这一条）。接口只用字符串和数字，内核不依赖界面库。

// ServiceAssets 是素材服务在插件服务表里的名字。
const ServiceAssets = "assets"

// AssetFontPurpose 是字体的用途。
type AssetFontPurpose string

const (
	AssetFontBody AssetFontPurpose = "body" // 正文：文渊圆体 → Maple → 系统
	AssetFontMono AssetFontPurpose = "mono" // 等宽：Maple → 文渊圆体 → 系统
)

// AssetFont 是一个找到了的字体文件。
type AssetFont struct {
	Name   string // 给人看的名字
	Source string // wenyuan、maple、system
	Path   string // 文件完整路径
	Weight int    // 400 常规、700 粗体；可变字体（文渊圆体）是 0
	Script string // 系统字体管的文字：zh、ja、ko、symbol、latin；通用字体为空
}

// EmojiPiece 是切表情切出来的一段：普通字，或一个表情序列。
type EmojiPiece struct {
	Text  string
	Emoji bool
}

// Assets 是素材服务。
type Assets interface {
	// Icon 返回 MingCute 图标的完整 SVG 文本（24×24，颜色 currentColor）；名字是 MingCute 官网上的名字去掉 _line、_fill，
	// fill 为真取实心版。名字不存在返回错误。
	Icon(name string, fill bool) (string, error)
	// IconNames 是全部图标名，已排序。
	IconNames() []string
	// Emoji 返回表情序列的 Twemoji SVG 文本；没有 Twemoji 目录或没有这张图返回 false。
	Emoji(seq string) (string, bool)
	// SplitEmoji 把文字切成普通字和表情序列，拼起来是原文。
	SplitEmoji(text string) []EmojiPiece
	// Fonts 按用途给出能找到的字体文件，先后就是回退的先后；一个都没找到是空的。
	Fonts(purpose AssetFontPurpose) []AssetFont
}

// AssetsOf 取素材服务；素材插件没在跑返回 false。
func AssetsOf(r *Registry) (Assets, bool) {
	v, ok := r.Service(ServiceAssets)
	if !ok {
		return nil, false
	}
	a, ok := v.(Assets)
	return a, ok
}
