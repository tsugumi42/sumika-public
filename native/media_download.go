package main

import (
	"context"
	"errors"
	"github.com/tsugumi42/MewTypeUI/content"
	"sumika-native/internal/api"
)

type downloadKey struct {
	source      api.EventRef
	index, node int
	path        string
}
type downloadStore struct {
	frame uint64
	items map[downloadKey]*downloadUI
}
type downloadUI struct {
	ctx            context.Context
	cancel         context.CancelFunc
	ref            api.VideoDownloadRef
	actions        api.VideoDownloadActions
	progress       api.VideoDownloadProgress
	resolved       *seg
	err            string
	busy, watching bool // busy：下载请求还没回；取消另算（canceling）
	canceling      bool
	revision, seen uint64
}

func (s *downloadStore) beginFrame() { s.frame++ }
func (s *downloadStore) endFrame() {
	for k, d := range s.items {
		if d.seen+1 < s.frame {
			d.cancel()
			delete(s.items, k)
		}
	}
}
func (s *downloadStore) reset() {
	for _, d := range s.items {
		d.cancel()
	}
	clear(s.items)
}
func (a *app) downloadStore() *downloadStore {
	if a.forward.rendering != nil {
		return &a.forward.downloads
	}
	return &a.downloads
}

// 凭据及缓存键来自原始展示段，不能从哈希、文件名或地址猜下载身份。
func (a *app) watchDownload(s seg) *downloadUI {
	actions, ok := a.reader.(api.VideoDownloadActions)
	if s.Download == nil || !ok {
		return nil
	}
	store := a.downloadStore()
	if d := store.items[s.downloadKey]; d != nil {
		d.seen = store.frame
		return d
	}
	parent := a.viewCtx
	if parent == nil {
		parent = a.base()
	}
	ctx, cancel := context.WithCancel(parent)
	d := &downloadUI{ctx: ctx, cancel: cancel, ref: *s.Download, actions: actions, seen: store.frame}
	if store.items == nil {
		store.items = map[downloadKey]*downloadUI{}
	}
	store.items[s.downloadKey] = d
	a.startDownloadWatch(d)
	return d
}
func (a *app) startDownloadWatch(d *downloadUI) {
	d.watching = true
	reader, ctx := a.reader, d.ctx
	go func() {
		defer a.post(func() {
			if ctx.Err() == nil {
				d.watching = false
			}
		})
		sub, err := d.actions.WatchVideoDownload(ctx, d.ref)
		if err != nil {
			a.post(func() {
				if ctx.Err() == nil {
					d.err = err.Error()
				}
			})
			return
		}
		defer sub.Close()
		var lastState api.VideoDownloadState
		lastHash := ""
		for {
			select {
			case <-ctx.Done():
				return
			case snap, ok := <-sub.Updates():
				if !ok {
					return
				}
				p, readErr := snap.Value, snap.Err
				var resolved *seg
				if readErr == nil && p.State == api.VideoDownloaded && (lastState != api.VideoDownloaded || lastHash != p.Hash) {
					value, err := reloadDownloadSegment(ctx, reader, d.ref)
					readErr = err
					if err == nil {
						resolved = &value
					}
				}
				lastState, lastHash = p.State, p.Hash
				a.post(func() {
					if ctx.Err() != nil {
						return
					}
					d.progress = p
					d.revision++
					if readErr != nil {
						d.err = readErr.Error()
					} else {
						d.err = ""
					}
					if resolved != nil {
						d.resolved = resolved
					}
				})
			}
		}
	}()
}

// 完成后重读可信投影；文件的实际类型及媒体凭据仍由后台决定。
func reloadDownloadSegment(ctx context.Context, reader api.Reader, ref api.VideoDownloadRef) (seg, error) {
	if n := len(ref.Forward); n > 0 {
		r, ok := reader.(api.ForwardReader)
		if !ok {
			return seg{}, errors.New("当前后台不能重读转发媒体")
		}
		result, err := r.ReadForward(ctx, api.ForwardRequest{Source: ref.Source, ID: ref.Forward[n-1], Via: ref.Forward[:n-1]})
		if err != nil {
			return seg{}, err
		}
		if result.Err != nil {
			return seg{}, result.Err
		}
		if ref.Node >= 0 && ref.Node < len(result.Value.Nodes) && ref.Index >= 0 && ref.Index < len(result.Value.Nodes[ref.Node].Segments) {
			return segFromDisplay(result.Value.Nodes[ref.Node].Segments[ref.Index]), nil
		}
	} else {
		cursor := api.Cursor("")
		for {
			result, err := reader.ReadMessages(ctx, api.ConversationRequest{Key: ref.Source.Conversation, Limit: 1000}, cursor)
			if err != nil {
				return seg{}, err
			}
			if result.Err != nil {
				return seg{}, result.Err
			}
			for _, m := range result.Value.Items {
				if m.Ref.Event == ref.Source.Event && ref.Index >= 0 && ref.Index < len(m.Segments) {
					return segFromDisplay(m.Segments[ref.Index]), nil
				}
			}
			if !result.Value.HasMore {
				break
			}
			cursor = result.Value.NextCursor
		}
	}
	return seg{}, errors.New("下载完成后读不到这条媒体记录")
}

// locked 是下载按钮点不了的时候：取消已经发出去，或者请求还没回、也还没看到它开始下载。
// 看到进度以后取消要能点，真实下载的请求可能比进度晚回来。
func (d *downloadUI) locked() bool {
	if d.canceling {
		return true
	}
	return d.busy && d.progress.State != api.VideoAutoDownloading && d.progress.State != api.VideoManualDownloading
}

func (a *app) changeDownload(d *downloadUI, cancel bool) {
	if d.canceling || (d.busy && !cancel) {
		return
	}
	if cancel {
		d.canceling = true
	} else {
		d.busy = true
	}
	d.err = ""
	if !d.watching {
		a.startDownloadWatch(d)
	}
	revision, reader := d.revision, a.reader
	go func() {
		var p api.VideoDownloadProgress
		var resolved *seg
		var err error
		if cancel {
			err = d.actions.CancelVideoDownload(d.ctx, d.ref)
			p.State = api.VideoDownloadCanceled
		} else {
			p, err = d.actions.RequestVideoDownload(d.ctx, d.ref)
			if err == nil && p.State == api.VideoDownloaded {
				var s seg
				s, err = reloadDownloadSegment(d.ctx, reader, d.ref)
				if err == nil {
					resolved = &s
				}
			}
		}
		a.post(func() {
			if d.ctx.Err() != nil {
				return
			}
			if cancel {
				d.canceling = false
			} else {
				d.busy = false
			}
			if err != nil {
				d.err = err.Error()
				return
			}
			if d.revision == revision {
				d.progress = p
			}
			if cancel {
				d.revision++ // 比它晚回来的下载请求不能把“已取消”盖回去
			}
			if resolved != nil {
				d.resolved = resolved
			}
		})
	}()
}
func downloadPresentation(d *downloadUI, ready bool) (content.MediaStatus, float32, string) {
	if d == nil {
		if ready {
			return content.MediaReady, 0, ""
		}
		return content.MediaRemote, 0, "当前后台不能下载"
	}
	if d.err != "" {
		return content.MediaFailed, 0, d.err
	}
	p := d.progress
	switch p.State {
	case api.VideoAutoDownloading, api.VideoManualDownloading:
		text := "已下载 " + humanSize(p.Received)
		ratio := float32(0)
		if p.Total > 0 {
			ratio = min(1, float32(p.Received)/float32(p.Total))
			text += " / " + humanSize(p.Total)
		}
		return content.MediaLoading, ratio, text
	case api.VideoDownloaded:
		return content.MediaReady, 1, "下载完成"
	case api.VideoDownloadFailed:
		return content.MediaFailed, 0, p.Error
	case api.VideoDownloadCanceled:
		return content.MediaRemote, 0, "已取消，点一下重新下载"
	}
	if ready {
		return content.MediaReady, 1, ""
	}
	return content.MediaRemote, 0, "点一下下载"
}
func downloadLabel(status content.MediaStatus, kind string) string {
	switch status {
	case content.MediaLoading:
		return "取消" + kind + "下载"
	case content.MediaFailed:
		return "重试下载" + kind
	case content.MediaRemote:
		return "下载" + kind
	}
	return "打开" + kind
}
