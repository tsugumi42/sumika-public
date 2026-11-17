package chatui

import "github.com/egoist/mygo/ui"

// MediaState describes the host's current media or download state.
type MediaState int

const (
	MediaMissing MediaState = iota
	MediaLoading
	MediaReady
	MediaFailed
	MediaDownloading
	MediaDownloaded
)

// MediaResult is a snapshot; a negative Progress means the total is unknown.
// Description contains a face's textual fallback when no image is available.
type MediaResult struct {
	Image       ui.ImageSource
	State       MediaState
	Description string
	Progress    float32
}

// ThumbKind 是引用里被引消息的缩略图种类；空表示没有。
type ThumbKind string

const (
	ThumbImage   ThumbKind = "image"
	ThumbSticker ThumbKind = "sticker"
	ThumbVideo   ThumbKind = "video" // 视频取封面，叠一个小播放标记
)

// QuoteMedia 是 MediaResolver 的可选扩展：取引用块里被引消息的缩略图。
// messageID 是引用别人的那条消息（引用块所在的消息）；宿主用自己存的被引媒体凭据走同一条图片加载链路，
// 被引消息不在已加载范围里也能按需加载。没实现就不画缩略图，引用块退回只有文字。
type QuoteMedia interface {
	QuoteThumb(messageID string, wantPx int) MediaResult
}

// MediaResolver queries host-owned media on the UI thread. It must not read or
// decode files synchronously. Position is the original segment index.
type MediaResolver interface {
	Image(messageID string, position int, cover bool, wantPx int) MediaResult
	Face(id string) MediaResult
	Download(messageID string, position int) MediaResult
}
