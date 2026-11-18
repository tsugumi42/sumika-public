package mediafetch

import (
	"context"
	"math"
	"sync"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

func isTask(kind string) bool { return kind == types.SegVideo || kind == types.SegFile }

func (f *Fetcher) fileURL(j *job) (string, error) {
	if f.o.FileResolver == nil {
		return "", api.ErrVideoDownloadSource
	}
	conv := j.conv
	if j.forward == "" {
		var ok bool
		var err error
		conv, _, ok, err = f.o.Store.EventRef(j.seq)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", api.ErrVideoDownloadSource
		}
	} else {
		conv = ""
		if j.fileGroup != "" {
			conv = (types.Conversation{Channel: f.o.Platform, Scene: types.SceneGroup, ID: j.fileGroup}).String()
		}
	}
	return f.o.FileResolver.FileURL(j.ctx, conv, types.Seg{Kind: types.SegFile, FileID: j.fileID, FileHash: j.fileHash})
}

// 活跃任务只存内存；终局复用 media_fetch，不保留无限增长的进度历史。
type videoTask struct {
	job      *job
	progress api.VideoDownloadProgress
}

func jobKey(j *job) key { return key{seq: j.seq, idx: j.idx, forward: j.forward, node: j.node} }

// 以下 Locked 方法都由 f.mu 保护，登记与限额检查在同一临界区。
func (f *Fetcher) startVideoLocked(j *job) {
	j.ctx, j.cancel = context.WithCancel(f.ctx)
	j.done = make(chan struct{})
	state := api.VideoAutoDownloading
	if j.manual {
		state = api.VideoManualDownloading
		f.manualVideos++
	}
	f.videos[jobKey(j)] = &videoTask{job: j, progress: api.VideoDownloadProgress{State: state, Total: j.size}}
	f.notifyVideoLocked(jobKey(j))
}

func (f *Fetcher) notifyVideoLocked(k key) {
	for wake := range f.videoWatches[k] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (f *Fetcher) videoProgress(j *job, n, total int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.videos[jobKey(j)]
	t.progress.Received, t.progress.Total = n, total
	f.notifyVideoLocked(jobKey(j))
}

func (f *Fetcher) downloadVideo(j *job) (string, int64, error) {
	limit, timeout := f.dl.videoMax, f.dl.timeout
	if j.manual {
		// Put 的限额探测会加一，不能溢出；大视频不受图片总时限限制。
		limit, timeout = math.MaxInt64-1, 0
	}
	progress := func(n, total int64) { f.videoProgress(j, n, total) }
	if j.kind == types.SegFile {
		return f.dl.downloadChecked(j.ctx, j.url, f.put, false, limit, timeout, progress, 64<<10, func(h []byte) error {
			j.format = mediastore.FileKind(j.file, h)
			if !j.manual && j.format == "" {
				return &Error{State: store.MediaManual, Reason: "扩展名与文件头不一致或格式未知"}
			}
			if j.manual && j.format == "" {
				j.format = mediastore.FileMediaKind(h)
			}
			return nil
		})
	}
	return f.dl.download(j.ctx, j.url, f.put, true, limit, timeout, progress)
}

// RequestVideo 的段已经由桌面入口按可见事件核对；相同段只复用现有任务。
func (f *Fetcher) RequestVideo(seq int64, idx int, segs []types.Seg) (api.VideoDownloadProgress, error) {
	return f.requestVideo(key{seq: seq, idx: idx}, "", segs)
}

func (f *Fetcher) RequestForwardVideo(id string, node, idx int, conv string, segs []types.Seg) (api.VideoDownloadProgress, error) {
	return f.requestVideo(key{forward: id, node: node, idx: idx}, conv, segs)
}

func (f *Fetcher) requestVideo(k key, conv string, segs []types.Seg) (api.VideoDownloadProgress, error) {
	f.videoRequestMu.Lock()
	defer f.videoRequestMu.Unlock()
	state, err := f.taskState(k, segs[k.idx])
	if err != nil {
		return state, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.started {
		return api.VideoDownloadProgress{}, api.ErrVideoDownloadUnavailable
	}
	if t := f.videos[k]; t != nil {
		return t.progress, nil
	}
	if state.State == api.VideoDownloaded {
		return state, nil
	}
	if f.manualVideos >= 2 {
		return state, api.ErrVideoDownloadBusy
	}
	var j *job
	for _, candidate := range jobsOf(k.seq, segs) {
		if candidate.idx == k.idx {
			j = candidate
			break
		}
	}
	if j == nil {
		return state, api.ErrVideoDownloadSource
	}
	j.manual, j.maxTries = true, 1
	j.forward, j.node, j.conv = k.forward, k.node, conv
	f.inflight[k] = struct{}{}
	f.startVideoLocked(j)
	f.wg.Add(1)
	go func() { defer f.wg.Done(); f.run(j) }()
	return f.videos[k].progress, nil
}

func (f *Fetcher) videoState(seq int64, idx int, seg types.Seg) (api.VideoDownloadProgress, error) {
	return f.taskState(key{seq: seq, idx: idx}, seg)
}

func (f *Fetcher) taskState(k key, seg types.Seg) (api.VideoDownloadProgress, error) {
	f.mu.Lock()
	t := f.videos[k]
	if t != nil {
		p := t.progress
		f.mu.Unlock()
		return p, nil
	}
	f.mu.Unlock()
	var m store.MediaFetch
	if k.forward == "" {
		rows, err := f.o.Store.MediaFetchRows(k.seq)
		if err != nil {
			return api.VideoDownloadProgress{}, err
		}
		m = rows[k.idx]
	} else {
		rows, err := f.o.Store.ForwardMediaRows(f.ctx, k.forward)
		if err != nil {
			return api.VideoDownloadProgress{}, err
		}
		m = rows[[2]int{k.node, k.idx}]
	}
	p := api.VideoDownloadProgress{State: api.VideoNotDownloaded, Total: seg.Size}
	if seg.Hash != "" {
		p.State, p.Hash = api.VideoDownloaded, seg.Hash
	}
	if m.Kind != seg.Kind || m.URLSig != store.MediaSegSig(seg) {
		return p, nil
	}
	p.Total, p.Error = m.Size, m.LastError
	switch m.State {
	case store.MediaDone:
		p.State, p.Hash, p.Received = api.VideoDownloaded, m.Hash, m.Size
	case store.MediaTooBig, store.MediaManual:
		p.State = api.VideoAwaitingManual
	case store.MediaCanceled:
		p.State = api.VideoDownloadCanceled
	case store.MediaPending:
		p.State = api.VideoNotDownloaded // 重启后没有活跃工人，不能假称仍在下。
	default:
		p.State = api.VideoDownloadFailed
	}
	return p, nil
}

func (f *Fetcher) CancelVideo(ctx context.Context, seq int64, idx int) error {
	return f.cancelVideo(ctx, key{seq: seq, idx: idx})
}

func (f *Fetcher) CancelForwardVideo(ctx context.Context, id string, node, idx int) error {
	return f.cancelVideo(ctx, key{forward: id, node: node, idx: idx})
}

func (f *Fetcher) cancelVideo(ctx context.Context, k key) error {
	f.mu.Lock()
	t := f.videos[k]
	if t == nil {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	t.job.terminalMu.Lock()
	if !t.job.completed {
		t.job.cancel()
	}
	t.job.terminalMu.Unlock()
	done := t.job.done
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type videoSubscription struct {
	out    chan api.Snapshot[api.VideoDownloadProgress]
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *videoSubscription) Updates() <-chan api.Snapshot[api.VideoDownloadProgress] { return s.out }
func (s *videoSubscription) Close()                                                  { s.once.Do(s.cancel); <-s.done }

func (f *Fetcher) WatchVideo(ctx context.Context, seq int64, idx int, seg types.Seg) (api.Subscription[api.VideoDownloadProgress], error) {
	return f.watchVideo(ctx, key{seq: seq, idx: idx}, seg)
}

func (f *Fetcher) WatchForwardVideo(ctx context.Context, id string, node, idx int, seg types.Seg) (api.Subscription[api.VideoDownloadProgress], error) {
	return f.watchVideo(ctx, key{forward: id, node: node, idx: idx}, seg)
}

func (f *Fetcher) watchVideo(ctx context.Context, k key, seg types.Seg) (api.Subscription[api.VideoDownloadProgress], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, api.ErrVideoDownloadUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	sub := &videoSubscription{out: make(chan api.Snapshot[api.VideoDownloadProgress]), cancel: cancel, done: make(chan struct{})}
	wake := make(chan struct{}, 1)
	if f.videoWatches[k] == nil {
		f.videoWatches[k] = map[chan struct{}]struct{}{}
	}
	f.videoWatches[k][wake] = struct{}{}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		defer close(sub.done)
		defer close(sub.out)
		defer cancel()
		defer func() {
			f.mu.Lock()
			delete(f.videoWatches[k], wake)
			if len(f.videoWatches[k]) == 0 {
				delete(f.videoWatches, k)
			}
			f.mu.Unlock()
		}()
		var version uint64
		for {
			p, err := f.taskState(k, seg)
			version++
			state := api.Ready
			if err != nil {
				state = api.Failed
			}
			next := api.Snapshot[api.VideoDownloadProgress]{Version: version, Result: api.Result[api.VideoDownloadProgress]{State: state, Value: p, Err: err}}
			// 慢消费者只保留最新快照；初始值仍先于后续版本，不阻塞下载工人。
			select {
			case sub.out <- next:
				select {
				case <-wake:
				case <-ctx.Done():
					return
				case <-f.ctx.Done():
					return
				}
			case <-wake:
			case <-ctx.Done():
				return
			case <-f.ctx.Done():
				return
			}
		}
	}()
	return sub, nil
}
