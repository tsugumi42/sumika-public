package config

// LinkText 是链接展开文字排版的设置（linkReader.expand.text，规格-链接发送改版 B 节）。
type LinkText struct {
	// StripTrailingHashtags：去掉帖子正文结尾连续的 #标签 块（默认开；推文和其他平台都去，用户 2026-11-12）；正文中间的标签不动，整条只有标签时不去
	StripTrailingHashtags bool `yaml:"stripTrailingHashtags"`
}

// 直接发的图数上限（linkReader.expand.directImageMax）：默认 3，超过走合并转发
const (
	DefaultDirectImageMax = 3
	maxDirectImageMax     = 9
)

// checkDirectImageMax 校验直接发的图数上限；错误文字直接给用户看，带键名。
func (e LinkExpand) checkDirectImageMax() []string {
	if e.DirectImageMax < 1 || e.DirectImageMax > maxDirectImageMax {
		return []string{"linkReader.expand.directImageMax：必须在1～9张"}
	}
	return nil
}

// DefaultLinkText 是文字排版设置的默认值。
func DefaultLinkText() LinkText { return LinkText{StripTrailingHashtags: true} }
