package api

import (
	"context"
	"errors"
)

// VideoDownloadRef 定位可见 QQ 事件中的视频或文件；Forward 为逐层转发路径，Node 为终点树节点下标。
// 名称沿用现有下载契约；不接受调用方提供地址、文件标识或本地路径。
type VideoDownloadRef struct {
	Source  EventRef
	Index   int
	Forward []string
	Node    int
}

type VideoDownloadState string

const (
	VideoNotDownloaded     VideoDownloadState = "not_downloaded"
	VideoAutoDownloading   VideoDownloadState = "auto_downloading"
	VideoAwaitingManual    VideoDownloadState = "awaiting_manual"
	VideoManualDownloading VideoDownloadState = "manual_downloading"
	VideoDownloaded        VideoDownloadState = "downloaded"
	VideoDownloadFailed    VideoDownloadState = "failed"
	VideoDownloadCanceled  VideoDownloadState = "canceled"
)

// VideoDownloadProgress 是当前尝试的累计字节；重试从零开始。Total=0 表示未知。
// 下载完正文后仍可能在生成封面，只有 Downloaded 才能播放。
type VideoDownloadProgress struct {
	State    VideoDownloadState
	Received int64
	Total    int64
	Hash     string
	Error    string // 固定短原因，不含地址、密钥或本地路径。
}

var (
	ErrVideoDownloadBusy        = errors.New("已有两个手动媒体下载")
	ErrVideoDownloadUnavailable = errors.New("媒体下载未启用")
	ErrVideoDownloadSource      = errors.New("媒体没有可用的下载地址")
)

// VideoDownloadActions 提供视频和文件的媒体下载，是本机桌面的可选能力，不扩大 Reader。
// 请求上下文只约束接收请求；任务由后台持有，取消须显式调用 CancelVideoDownload。
// 订阅先给当前快照，后续可合并；关闭订阅不取消任务。取消等待临时文件清理完成。
type VideoDownloadActions interface {
	RequestVideoDownload(context.Context, VideoDownloadRef) (VideoDownloadProgress, error)
	WatchVideoDownload(context.Context, VideoDownloadRef) (Subscription[VideoDownloadProgress], error)
	CancelVideoDownload(context.Context, VideoDownloadRef) error
}
