package config

// X 取数和推特卡片的设置（规格-链接发送改版 C1、C2）：linkReader.x、linkReader.expand.tweetCard。

// LinkX 是 X 取数线路的设置。fx_v2 和 vx 同时取、先到的成功结果为准；两条都失败才试备用路线 syndication。
type LinkX struct {
	// FXTimeoutSeconds 是 fx_v2 主请求单独的时限（1～10 秒）
	FXTimeoutSeconds int `yaml:"fxTimeoutSeconds"`
	// Syndication 开关 X 官方嵌入接口这条备用路线
	Syndication bool `yaml:"syndication"`
}

// X 取数的默认值
const DefaultFXTimeoutSeconds = 4

func defaultLinkX() LinkX { return LinkX{FXTimeoutSeconds: DefaultFXTimeoutSeconds, Syndication: true} }

func (x LinkX) check() []string {
	if x.FXTimeoutSeconds < 1 || x.FXTimeoutSeconds > 10 {
		return []string{"linkReader.x.fxTimeoutSeconds：必须在1～10秒"}
	}
	return nil
}

// LinkTweetCard 是推特卡片（x 站点的 @推特卡片 样式）的设置
type LinkTweetCard struct {
	// Theme 是配色：auto 按北京时间（06:00～18:00 浅色，其余深色）；light / dark 固定
	Theme string `yaml:"theme"`
	// MaxHeight 是卡片高度封顶（图上像素）：默认 0 不封顶，长推文整张画出来（正文不截断，硬上限 8000，到了再截）；
	// 要封顶写 480～8000 的像素值（原来的封顶是 960）。用户 2026-11-13 定 QQ 这边默认不封顶
	MaxHeight int `yaml:"maxHeight"`
}

// DefaultTweetCardMaxHeight 是 MaxHeight 的默认值，和 linkcard.DefaultTweetMaxHeight 一致（config 不引 linkcard）
const DefaultTweetCardMaxHeight = 0

// 推特卡片配色的取值
const (
	TweetThemeAuto  = "auto"
	TweetThemeLight = "light"
	TweetThemeDark  = "dark"
)

func (t LinkTweetCard) check() []string {
	var bad []string
	switch t.Theme {
	case "", TweetThemeAuto, TweetThemeLight, TweetThemeDark: // 空当 auto
	default:
		bad = append(bad, "linkReader.expand.tweetCard.theme：只能为 auto / light / dark")
	}
	if t.MaxHeight != 0 && (t.MaxHeight < 480 || t.MaxHeight > 8000) {
		bad = append(bad, "linkReader.expand.tweetCard.maxHeight：0（不封顶）或 480～8000 像素")
	}
	return bad
}
