package config

// 画师的推文发原图的设置（规格-画师原图 第六节）：linkReader.expand.artist。

// 画师模式的取值
const (
	// ArtistCardAndOriginals：卡片、干净链接、再把每张原图发出来（默认，待用户确认）
	ArtistCardAndOriginals = "cardAndOriginals"
	// ArtistOriginalsOnly：只发干净链接和原图
	ArtistOriginalsOnly = "originalsOnly"
	// ArtistOff：不特殊处理，当作不是画师
	ArtistOff = "off"
)

// LinkArtist 是画师的推文怎么发
type LinkArtist struct {
	// Mode 是命中确认过的画师时的发法；空串当默认 cardAndOriginals
	Mode string `yaml:"mode"`
	// AutoDetect 开着时，展开遇到不在表里的带图原创推文作者，用已取到的资料判断，像画师的记进待确认表（不生效）
	AutoDetect bool `yaml:"autoDetect"`
}

// DefaultLinkArtist 是画师设置的默认值：卡片加原图（待用户确认），按需发现开
func DefaultLinkArtist() LinkArtist {
	return LinkArtist{Mode: ArtistCardAndOriginals, AutoDetect: true}
}

// EffectiveMode 是实际用的模式：空串按默认
func (a LinkArtist) EffectiveMode() string {
	if a.Mode == "" {
		return ArtistCardAndOriginals
	}
	return a.Mode
}

func (a LinkArtist) check() []string {
	switch a.Mode {
	case "", ArtistCardAndOriginals, ArtistOriginalsOnly, ArtistOff:
		return nil
	}
	return []string{"linkReader.expand.artist.mode：只能为 cardAndOriginals / originalsOnly / off"}
}
