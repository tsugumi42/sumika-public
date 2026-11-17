package linkplugin

// 画师的推文发原图（规格-画师原图）：展开前查画师表决定发法，展开时按需判断不在表里的作者。
// 匹配先按作者的账号数字 ID（fx_v2、嵌入接口给，vx 不给）；没有 ID 时按 @账号 找确认过的画师兜底（ADR-372，用户 2026-11-13 定，
// 修正 ADR-371 第 2 条），有 ID 的结果顺手更新表里存的 @账号，让兜底不过时。
// 判断本身不发请求，只用这次取回的作者资料；日志只写计数，不写账号、名字、简介。

import (
	"strings"

	"sumika-native/internal/artists"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

// artistPlatform 是现在做了画师表的平台
const artistPlatform = "x"

// authorID 是推文作者的账号数字 ID；不是 X 帖子、线路没给、格式不对都回 false
func authorID(r *linkreader.Result) (string, bool) {
	if r == nil || r.Platform != "x" || r.Kind != "post" || r.Author.ID.State != "available" {
		return "", false
	}
	id := r.Author.ID.Value
	return id, artists.ValidID(artistPlatform, id)
}

// authorHandle 是推文作者的 @账号（不带 @）；不是 X 帖子或线路没给回 false
func authorHandle(r *linkreader.Result) (string, bool) {
	if r == nil || r.Platform != "x" || r.Kind != "post" || r.Author.Account.State != "available" {
		return "", false
	}
	h := strings.TrimPrefix(strings.TrimSpace(r.Author.Account.Value), "@")
	return h, h != ""
}

// ArtistMode 是这条结果该用的画师发法：配置的模式不是 off、作者是表里 confirmed 的画师才非空。
// 认作者：有合格的数字 ID 只按 ID 认；没有 ID（vx 线路）才按 @账号 找确认过的画师（ADR-372）。
// 待确认、不是、没有这一行一律空串（旧行为）。ctl link preview 也用它，预览看到的和真发的一样；这里只读不写。
func ArtistMode(book *artists.Book, r *linkreader.Result, cfg config.LinkArtist) string {
	mode := cfg.EffectiveMode()
	if book == nil || mode == config.ArtistOff {
		return ""
	}
	st, err := book.Platform(artistPlatform)
	if err != nil {
		return ""
	}
	if id, ok := authorID(r); ok {
		if !st.Confirmed(id) {
			return ""
		}
	} else if r == nil || r.Author.ID.State == "available" {
		return "" // 线路给了 ID 但格式不对：数据本身可疑，不拿 @账号 兜底
	} else if h, ok := authorHandle(r); !ok {
		return ""
	} else if _, ok := st.ConfirmedHandle(h); !ok {
		return ""
	}
	if mode == config.ArtistOriginalsOnly {
		return linkexpand.ArtistOriginalsOnly
	}
	return linkexpand.ArtistCardAndOriginals
}
