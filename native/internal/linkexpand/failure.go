package linkexpand

import (
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/taskrun"
)

// SourceError 说这个媒体在取数阶段就没拿到（匿名被拒、作品不存在、被限流……）；Reason 是取数结果里记的原因代码。
// 动图 ZIP、页图这类要靠取数结果里的地址：地址本来就没有时，下载阶段回这个，不回“媒体容器无效”。
type SourceError struct{ Reason string }

func (e *SourceError) Error() string { return ReasonText(e.Reason) }

func anonymousDenied(reason string) bool {
	switch reason {
	case "anonymous_access_denied", "api_rejected_anonymous_request", "login_required", "note_requires_login":
		return true
	}
	return false
}

// ReasonText 把取数结果里的原因代码写成一句人话。匿名被拒和不存在在 pixiv 上分不开（限制级内容匿名取也是 404），
// 所以“不存在”这句同时提到需要登录；不绕登录，原样说明。
func ReasonText(reason string) string {
	switch {
	case anonymousDenied(reason):
		return "匿名取不到，需要登录"
	case reason == "not_found":
		return "匿名取不到（已删除，或需要登录才能看）"
	case reason == "rate_limited":
		return "被对方限流了，稍后再试"
	case reason == "timeout":
		return "取数超时"
	case reason == "network":
		return "网络不通"
	}
	// 内部原因代码不外露：消息会发到群里，不给看英文代码
	return "取数时没拿到"
}

// sayableReasons 是能写成人话的原因代码白名单；别的代码（interface_error、canceled……）是内部状态，不进消息。
var sayableReasons = map[string]bool{
	"anonymous_access_denied": true, "api_rejected_anonymous_request": true, "login_required": true,
	"note_requires_login": true, "not_found": true, "rate_limited": true, "timeout": true, "network": true,
}

// sourceReason 从取数结果里找一个说得清的原因：ZIP、帧表没拿到时用；只认白名单，其余返回空串。
func sourceReason(fields ...string) string {
	for _, r := range fields {
		if sayableReasons[r] {
			return r
		}
	}
	return ""
}

// mediaNote 是消息末尾补的一句：作品本来有图，但取数阶段就没拿到（pixiv 页图匿名被拒或 404）。
// 没这一句时，用户只会看到一条没有图的消息，不知道为什么。
func mediaNote(r *linkreader.Result) string {
	if r.Platform != "pixiv" || r.Kind != "artwork" {
		return ""
	}
	reason := sourceReason(r.Media.Reason)
	switch {
	case r.Media.State != "available" && reason != "":
		return "图片没取到：" + ReasonText(reason)
	case r.Media.State == "available" && r.Media.Truncated && reason != "" && reason != "page_count_not_returned":
		return "只取到第一页：" + ReasonText(reason)
	}
	return ""
}

func sourceClass(e *SourceError) (class, outcome string) {
	switch {
	case anonymousDenied(e.Reason):
		return taskrun.ClassLoginRequired, taskrun.OutcomeFailed
	case e.Reason == "not_found":
		return taskrun.ClassNotFound, taskrun.OutcomeFailed
	case e.Reason == "rate_limited":
		return taskrun.ClassRateLimited, taskrun.OutcomeFailed
	}
	return taskrun.ClassUnknown, taskrun.OutcomeFailed
}

// lostMediaNote 是整条消息一个图、视频都没发出去、又是因为我们自己的限制（下载范围、大小上限、本机工具，quietMediaFailure）时补的一句说明：
// 这类原因本身不进群（用户 2026-11-12），但群友收到一条没图也没说明的消息会以为是解析坏了（用户 2026-11-13，TikTok、Bluesky 视频样张）。
// 已经有别的说明（pixiv 的 mediaNote，other 非空）、还有别的媒体（缩略图顶上、卡片）时不写。
func lostMediaNote(lost map[string]bool, parts []types.Part, other string) string {
	if len(lost) == 0 || other != "" || hasMedia(parts) {
		return ""
	}
	switch {
	case lost["视频"]:
		return "视频没取到"
	case lost["动图"]:
		return "动图没取到"
	}
	return "图片没取到"
}

func hasMedia(parts []types.Part) bool {
	has := func(body []types.BodySegment) bool {
		for _, s := range body {
			if s.Kind == types.BodyImage || s.Kind == types.BodyGIF || s.Kind == types.BodyVideo {
				return true
			}
		}
		return false
	}
	for _, p := range parts {
		if has(p.Body) {
			return true
		}
		for _, n := range p.Nodes {
			if has(n.Body) {
				return true
			}
		}
	}
	return false
}

// addLeadNote 把一句话接在准备好的第一条消息最后一段文字后面（第一段是合并转发就接在第一个节点里）；一条消息都没有就单独发一条。
func addLeadNote(p *Prepared, note, quote string) {
	join := func(body []types.BodySegment) []types.BodySegment {
		if n := len(body); n > 0 && body[n-1].Kind == types.BodyText {
			body[n-1].Text += "\n" + note
			return body
		}
		return append(body, types.BodySegment{Kind: types.BodyText, Text: note})
	}
	switch {
	case len(p.Parts) == 0:
		p.Parts = []types.Part{{Kind: types.PartMessage, Quote: quote, Body: join(nil)}}
	case p.Parts[0].Kind == types.PartForward && len(p.Parts[0].Nodes) > 0:
		p.Parts[0].Nodes[0].Body = join(p.Parts[0].Nodes[0].Body)
	default:
		p.Parts[0].Body = join(p.Parts[0].Body)
	}
}
