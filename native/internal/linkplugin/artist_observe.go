package linkplugin

// 展开时的按需判断（规格-画师原图 第七节）：判断本身不发请求，只用这次取回的作者资料；日志只写计数，不写账号、名字、简介。

import (
	"strings"

	"sumika-native/internal/artists"
	"sumika-native/internal/config"
	"sumika-native/internal/linkreader"
)

// ownPhotos 是这条推文自己的图（不含被引推文、外链卡的图）
func ownPhotos(r *linkreader.Result) int {
	n := 0
	for _, m := range r.Media.Value {
		if (m.Type == "image" || m.Type == "photo") && m.URL.State == "available" && m.URL.Value != "" {
			n++
		}
	}
	return n
}

// observeArtist 是展开时的按需判断（规格-画师原图 七点一）：作者不在表里、是带图的原创推文、作者资料可用，才判断一次，
// 像画师的记 pending、不像的记 declined（保证只判一次）；任何一条不成立都什么都不做。已有的行一律不动，所以不会影响这一次的发送。
func (p *Plugin) observeArtist(r *linkreader.Result, cfg config.LinkArtist) {
	if p.opt.Expand == nil || p.opt.Expand.Artists == nil || !cfg.AutoDetect {
		return
	}
	book := p.opt.Expand.Artists
	id, ok := authorID(r)
	if !ok {
		return
	}
	st, err := book.Platform(artistPlatform)
	if err != nil {
		return
	}
	if _, seen := st.Lookup(id); seen {
		// 表里已有的作者：这次带了数字 ID，顺手把存的 @账号 更新成现在的（ADR-372，vx 线路按 @账号 兜底要靠它不过时）
		if h, ok := authorHandle(r); ok {
			if changed, err := st.RefreshHandle(id, h); err != nil {
				p.warnf("画师表的账号名没更新（%s）", reason(err))
			} else if changed {
				p.debugf("画师表：一位作者改过账号名，已更新")
			}
		}
		return
	}
	if r.Author.Profile == nil || r.ReplyTo.State == "available" || ownPhotos(r) == 0 {
		return
	}
	pr := r.Author.Profile
	j := artists.Judge(artists.Profile{
		Name: r.Author.Name.Value, Bio: pr.Bio, Location: pr.Location, Links: pr.Links,
		MediaCount: pr.MediaCount, Statuses: pr.Statuses, Followers: pr.Followers,
	}, strings.TrimSpace(r.Text.Value))
	status, err := st.AddAuto(id, r.Author.Account.Value, r.Author.Name.Value, j)
	switch {
	case err != nil:
		p.warnf("画师表没写进去（%s）", reason(err))
	case status == artists.StatusPending:
		p.opt.Logf("发现像画师的作者，待确认 +1")
	case status == artists.StatusDeclined:
		p.debugf("判断过一个作者，不像画师")
	}
}
