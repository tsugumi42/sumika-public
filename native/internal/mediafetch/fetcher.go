// Package mediafetch 把收到的图片、视频和文件下载进本地媒体库（规格-QQ收图下载和断线补录、规格-QQ视频）。
//
// 入库一站的 OnStored 把媒体段交给 Offer，内存排队后立即返回，不碰库、不联网。
// 工人按媒体种类限额、认头，写 mediastore 后在同一事务回填 hash 和元数据。
// 队列满时由补扫接上；正文视频和文件仅恢复本次接收范围，不批量补旧记录。
// 段里的地址、文件名原样留着，失败状态单独记录。
//
// 常驻的包。不 import 渠道中间件和适配器：装配处接入库回调，刷新媒体地址走 Refresher / FileResolver。
package mediafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

// ErrRefreshUnavailable：这次没能问成（没连上、超时、连接断了、核验没通过），不是协议端说"换不出新地址"。
// Refresher 的实现让没问成的错误能被 errors.Is 认出它就行（ctx 取消和超时也算）；碰到这种错误，图不记 expired，
// 留着等下一次补扫（每次连上时扫）再问。别的错误（协议端明确拒绝、回来的消息对不上）才算"换不出"
var ErrRefreshUnavailable = errors.New("暂时问不到协议端")

// Refresher 向协议端问一条已经收过的消息现在的样子：协议端取消息时会把图片地址刷新成新的（SnowLuma 的 get_msg）。
// conversation 是库里的会话串；返回这条消息当前的段。没有这个能力的渠道不给（nil），过期的图就直接记 expired。
// 没问成的错误要能被 errors.Is(err, ErrRefreshUnavailable) 认出来（见它的说明）
type Refresher interface {
	Message(ctx context.Context, conversation, platformID string) ([]types.Seg, error)
}

// 规格 4.2 的参数
const (
	maxAttempts    = 3                  // 共试 3 次：立即、30 秒后、5 分钟后
	queueSize      = 500                // 内存队列长度，满了丢掉新的
	workers        = 2                  // 同时下载
	sweepWindow    = 7 * 24 * time.Hour // 补扫只看近 7 天
	failedRetryAge = 24 * time.Hour     // failed 的在重连后的补扫里最多再试一次，事件超过这么老的不再试
	tmpMaxAge      = time.Hour          // 启动时清 .tmp 里超过这么久的残留
	sweepPage      = 200
	refreshTTL     = 10 * time.Minute // 同一条消息换来的新地址在这段时间内给同一条消息的几张图共用
	refreshCap     = 512
)

var retryDelays = []time.Duration{30 * time.Second, 5 * time.Minute} // 第 2、3 次之前等多久

// Options 是 Fetcher 的参数
type Options struct {
	Store        *store.Store // 这个平台的聊天库
	Data         string       // 数据目录（媒体库在 <Data>/go/media/<Platform>）
	Platform     string       // 平台名，媒体库的子目录；"qq"
	Downloader   *Downloader  // nil 用默认参数
	Refresher    Refresher    // nil 就不换新地址
	Clock        types.Clock  // nil 用真钟；重试的间隔走它
	FileResolver interface {
		FileURL(context.Context, string, types.Seg) (string, error)
	}
	Logf   func(string, ...any)
	Warnf  func(string, ...any) // WARN：下载、探测、换地址失败，会重试或降级；nil 时用 Logf
	Errorf func(string, ...any) // ERROR：读写库出错，状态或文件对不上；nil 时用 Logf
	// Workers、QueueSize、Window 默认是规格 4.2 的值，测试可以改小
	Workers   int
	QueueSize int
	Window    time.Duration
	// StatsEvery 是多久记一行"这段时间新增多少文件多少字节"；0 是一小时，负数不记
	StatsEvery time.Duration

	afterDownload func() // 只供测试：下载完、写库之前插一步
}

type key struct {
	seq     int64
	idx     int
	forward string
	node    int
}

// job 是一个要下载的段
type job struct {
	seq              int64
	idx              int
	kind             string
	url              string // 当前要下的地址（换过新地址就是新的）
	file             string // 协议端文件名；对段、分类，不作为本地路径
	sig              string // 原段媒体身份指纹，缺址文件也绑定 ID / 取址令牌
	size             int64  // 协议端已知大小；超限不发请求
	fileID, fileHash string
	fileGroup        string
	format           string
	nth, nmedia      int // 这是事件里第几个图片 / 表情段，事件里共几个
	tries            int // 已经试过几次
	maxTries         int
	refreshed        bool // 这次运行里已经换过一次新地址
	triedPNG         bool
	forward          string
	node             int
	conv             string
	manual           bool
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	terminalMu       sync.Mutex // 只串行本任务的取消和提交，不阻塞入库回调的排队锁。
	completed        bool       // hash 提交以后取消不能把成功覆盖成取消
}

type refreshEntry struct {
	done chan struct{}
	at   time.Time
	segs []types.Seg
	err  error
}

// Fetcher 下载收到的图。New 以后要 Start，用完要 Close
type Fetcher struct {
	o              Options
	dl             *Downloader
	clock          types.Clock
	logf           func(string, ...any)
	warnf          func(string, ...any)
	errorf         func(string, ...any)
	ctx            context.Context
	cancel         context.CancelFunc
	queue          chan *job
	sweep1         chan struct{}
	wg             sync.WaitGroup
	videoTools     videoTools
	videoToolsOnce sync.Once     // 第一次真要处理视频时才查 ffmpeg/ffprobe；开后台的路径上不查
	videoGate      chan struct{} // 外部解码程序一次只跑一个

	mu               sync.Mutex
	closed           bool
	started          bool
	inflight         map[key]struct{}
	overflow         bool
	videos           map[key]*videoTask
	videoWatches     map[key]map[chan struct{}]struct{}
	manualVideos     int
	firstVideo       atomic.Int64 // 只恢复本次接收过的新视频和文件，不批量补旧正文。
	videoRequestMu   sync.Mutex   // 请求查库与成功回填串行；不占入库回调的 mu。
	backfillNotify   func()
	backfillRunning  map[key]bool
	backfillSweeping bool

	refreshMu sync.Mutex
	refreshes map[int64]*refreshEntry
	resweeps  atomic.Int64 // 请求过几次补扫；任务开始换地址时记下，问不成放弃时拿它看这期间有没有新补扫错过了自己

	okFiles, okBytes atomic.Int64
	sweeps           atomic.Int64 // 补扫做完了几遍（测试等它）
}

// New 造一个下载器，还没有开始干活
func New(o Options) (*Fetcher, error) {
	if o.Store == nil || o.Data == "" || o.Platform == "" {
		return nil, errors.New("收图下载要库、数据目录和平台名")
	}
	f := &Fetcher{o: o, dl: o.Downloader, clock: o.Clock, logf: o.Logf, inflight: map[key]struct{}{}, refreshes: map[int64]*refreshEntry{}}
	f.videos = map[key]*videoTask{}
	f.videoWatches = map[key]map[chan struct{}]struct{}{}
	if f.dl == nil {
		f.dl = NewDownloader(DownloadOptions{})
	}
	if f.clock == nil {
		f.clock = realClock{}
	}
	if f.logf == nil {
		f.logf = func(string, ...any) {}
	}
	f.warnf, f.errorf = orLogf(o.Warnf, f.logf), orLogf(o.Errorf, f.logf)
	if f.o.Workers <= 0 {
		f.o.Workers = workers
	}
	if f.o.QueueSize <= 0 {
		f.o.QueueSize = queueSize
	}
	if f.o.Window <= 0 {
		f.o.Window = sweepWindow
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.queue = make(chan *job, f.o.QueueSize)
	f.sweep1 = make(chan struct{}, 1)
	f.videoGate = make(chan struct{}, 1)
	return f, nil
}

// Start 清掉上次留下的临时文件并起工人；旧图下载由首次连接、重连成功后的 Resweep 接上。
func (f *Fetcher) Start() {
	f.mu.Lock()
	if f.closed || f.started {
		f.mu.Unlock()
		return
	}
	f.started = true
	f.mu.Unlock()
	if n, err := mediastore.CleanTmp(f.o.Data, f.o.Platform, f.clock.Now().Add(-tmpMaxAge)); err != nil {
		f.warnf("收图：清理媒体库临时文件出错：%v", err)
	} else if n > 0 {
		f.logf("收图：清掉了 %d 个上次留下的临时文件", n)
	}
	for i := 0; i < f.o.Workers; i++ {
		f.wg.Add(1)
		go f.worker()
	}
	f.wg.Add(1)
	go f.sweeper()
	f.wg.Add(1)
	go f.backfillMeta()
	if every := f.o.StatsEvery; every >= 0 {
		if every == 0 {
			every = time.Hour
		}
		f.wg.Add(1)
		go f.stats(every)
	}
}

// Close 取消在途的下载，等工人退出；之后不写库、不写文件。可以重复调用
func (f *Fetcher) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.mu.Unlock()
	f.cancel()
	f.wg.Wait()
	// 工人可能因关闭先退出，队列里的视频也必须结束并释放取消等待者。
	for {
		select {
		case j := <-f.queue:
			f.release(j)
		default:
			return
		}
	}
}

// Resweep 请求补扫一遍（每次重连之后调）。不阻塞；已经有一个在排着就合并
func (f *Fetcher) Resweep() {
	f.backfillSweep(true)
	f.resweeps.Add(1)
	f.forgetRefreshErrors()
	select {
	case f.sweep1 <- struct{}{}:
	default:
	}
}

// Idle 等到没有排着的、在下的、等重试的任务（测试用）
func (f *Fetcher) Idle(ctx context.Context) error {
	for {
		f.mu.Lock()
		n := len(f.inflight)
		f.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (f *Fetcher) live() bool { return f.ctx.Err() == nil }

func (f *Fetcher) nowMs() int64 { return f.clock.Now().UnixMilli() }

// Offer 交刚入库且没有 hash 的图片、视频和文件；文件缺址也入队查询。
// 只在内存排队；满了保留原段，由补扫恢复本轮新视频和文件。
func (f *Fetcher) Offer(seq int64, segs []types.Seg) {
	for _, j := range jobsOf(seq, segs) {
		if isTask(j.kind) {
			for old := f.firstVideo.Load(); old == 0 || seq < old; old = f.firstVideo.Load() {
				if f.firstVideo.CompareAndSwap(old, seq) {
					break
				}
			}
		}
		if !f.claim(j) {
			continue
		}
		select {
		case f.queue <- j:
		default:
			f.release(j)
			f.mu.Lock()
			f.overflow = true
			f.mu.Unlock()
		}
	}
}

// jobsOf 挑出一条事件里要下载的段
func jobsOf(seq int64, segs []types.Seg) []*job {
	var counts [3]int
	for _, s := range segs {
		if downloadable(s.Kind) {
			counts[mediaFamily(s.Kind)]++
		}
	}
	var out []*job
	var nths [3]int
	for i, s := range segs {
		if !downloadable(s.Kind) {
			continue
		}
		family := mediaFamily(s.Kind)
		nth := nths[family]
		nths[family]++
		if (s.URL == "" && s.Kind != types.SegFile) || s.Hash != "" {
			continue
		}
		out = append(out, &job{seq: seq, idx: i, kind: s.Kind, url: s.URL, file: s.File, fileID: s.FileID, fileHash: s.FileHash, fileGroup: s.FileGroup, size: s.Size, sig: store.MediaSegSig(s), nth: nth, nmedia: counts[family], maxTries: maxAttempts})
	}
	return out
}

func downloadable(kind string) bool {
	return kind == types.SegImage || kind == types.SegEmoji || kind == types.SegCard || isTask(kind)
}

func mediaFamily(kind string) int {
	if kind == types.SegFile {
		return 2
	}
	if kind == types.SegVideo {
		return 1
	}
	return 0
}

// claim 把这个段登记成"在处理"；已经在处理（排着、在下、等重试）的返回 false，不重复入队。关闭以后什么都不收
func (f *Fetcher) claim(j *job) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	k := key{seq: j.seq, idx: j.idx, forward: j.forward, node: j.node}
	if _, dup := f.inflight[k]; dup {
		return false
	}
	f.inflight[k] = struct{}{}
	f.notifyBackfillLocked()
	j.ctx = f.ctx
	if isTask(j.kind) {
		f.startVideoLocked(j)
	}
	return true
}

func (f *Fetcher) release(j *job) {
	j.terminalMu.Lock()
	if isTask(j.kind) && !j.completed && j.ctx.Err() != nil {
		// 取消状态仍在关库之前保存；转发任务不能误写正文 media_fetch。
		row := store.MediaFetch{EventSeq: j.seq, Idx: j.idx, Kind: j.kind, URLSig: j.sig, State: store.MediaCanceled, UpdatedAt: f.nowMs(), Size: j.size}
		var err error
		if j.forward != "" {
			err = f.o.Store.PutForwardMedia(j.forward, j.node, j.conv, row)
		} else {
			err = f.o.Store.PutMediaFetch(row)
		}
		if err != nil {
			f.errorf("媒体：保存取消状态失败：%v", err)
		}
	}
	j.terminalMu.Unlock()
	f.mu.Lock()
	k := jobKey(j)
	delete(f.inflight, k)
	delete(f.backfillRunning, k)
	f.notifyBackfillLocked()
	if isTask(j.kind) {
		delete(f.videos, k)
		if j.manual {
			f.manualVideos--
		}
		j.cancel()
		close(j.done)
		f.notifyVideoLocked(k)
	}
	f.mu.Unlock()
}

// enqueueWait 把已经登记的任务放进队列，满了就等（补扫、重试用，它们不是消息入库的路径）
func (f *Fetcher) enqueueWait(j *job) bool {
	select {
	case f.queue <- j:
		return true
	case <-f.ctx.Done():
		return false
	}
}

func (f *Fetcher) worker() {
	defer f.wg.Done()
	for {
		select {
		case <-f.ctx.Done():
			return
		case j := <-f.queue:
			f.run(j)
			// 队列满过、丢了新的：等队列清空了让补扫去捡
			f.mu.Lock()
			again := f.overflow && len(f.queue) == 0
			if again {
				f.overflow = false
			}
			f.mu.Unlock()
			if again {
				f.Resweep()
			}
		}
	}
}

func (f *Fetcher) sweeper() {
	defer f.wg.Done()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-f.sweep1:
			f.backfillSweep(true)
			f.sweep()
			f.backfillSweep(false)
		}
	}
}

func (f *Fetcher) stats(every time.Duration) {
	defer f.wg.Done()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-t.C:
			files, bytes := f.okFiles.Swap(0), f.okBytes.Swap(0)
			if files > 0 {
				f.logf("收图：这一阵新下载 %d 张，共 %d 字节", files, bytes)
			}
		}
	}
}

// run 做一个任务：下载、认格式、存进媒体库、回填 hash。出错的归类：换新地址再试、过一阵重试、记终局状态
func (f *Fetcher) run(j *job) {
	if !f.live() || j.ctx.Err() != nil {
		f.release(j)
		return
	}
	f.backfillStarted(j)
	// 开始时不写库：下载好了或者要等重试、记终局状态时才写（每张图只提交一次；进程死在中途，补扫挑得到没有行的段）
	for {
		if isTask(j.kind) && !j.manual && j.size > f.dl.videoMax {
			f.finish(j, store.MediaTooBig, "超过媒体自动下载上限")
			return
		}
		if j.kind == types.SegFile {
			if !j.manual && mediastore.FileCandidate(j.file) == "" {
				f.finish(j, store.MediaManual, "此文件类型需手动下载")
				return
			}
			if j.url == "" {
				u, err := f.fileURL(j)
				if err != nil {
					f.finish(j, store.MediaFailed, "协议端未提供可用文件地址")
					return
				}
				j.url, j.refreshed = u, true
			}
		}
		var hash string
		var size int64
		var err error
		if isTask(j.kind) {
			hash, size, err = f.downloadVideo(j)
		} else {
			hash, size, err = f.dl.Download(j.ctx, j.url, f.put)
		}
		if f.o.afterDownload != nil {
			f.o.afterDownload()
		}
		if !f.live() || j.ctx.Err() != nil {
			f.release(j)
			return
		}
		if err == nil {
			f.succeed(j, hash, size)
			return
		}
		var de *Error
		if !errors.As(err, &de) {
			de = &Error{State: store.MediaFailed, Retry: true, Reason: "未知错误"}
		}
		if isTask(j.kind) && de.Size > 0 {
			j.size = de.Size
		}
		// 商城表情的 .gif 下不到，换同名的 .png 再试一次（Bun 的做法）
		if de.Status == 404 && !j.triedPNG {
			if png, ok := marketPNG(j.url); ok {
				j.triedPNG, j.url = true, png
				continue
			}
		}
		if de.Refresh {
			if !j.refreshed && (f.o.Refresher != nil || j.kind == types.SegFile && f.o.FileResolver != nil) {
				j.refreshed = true
				gen := f.resweeps.Load()
				fresh, err := f.freshURL(j)
				switch {
				case fresh != "":
					j.url = fresh
					continue
				case unavailable(err):
					// 没问成不等于换不出：不记终局状态，留给下一次连接后的补扫。
					if j.manual {
						f.finish(j, store.MediaFailed, "暂时问不到协议端")
						return
					}
					f.release(j)
					if f.resweeps.Load() != gen {
						f.Resweep() // 问的期间来过补扫，它看到这个段还在处理就跳过了；再请求一遍
					}
					return
				}
				f.finish(j, store.MediaExpired, "地址过期，协议端换不出新的")
				return
			}
			f.finish(j, store.MediaExpired, de.Reason)
			return
		}
		if de.Retry {
			j.tries++
			if j.tries >= j.maxTries {
				f.finish(j, store.MediaFailed, de.Reason)
				return
			}
			delay := retryDelays[min(j.tries-1, len(retryDelays)-1)]
			f.save(j, store.MediaPending, "", de.Reason, f.nowMs()+delay.Milliseconds())
			f.retryLater(j, delay)
			return
		}
		f.finish(j, de.State, de.Reason)
		return
	}
}

// put 是下载器存文件的入口
func (f *Fetcher) put(r io.Reader, maxBytes int64) (string, int64, error) {
	return mediastore.Put(f.o.Data, f.o.Platform, r, maxBytes)
}

func (f *Fetcher) succeed(j *job, hash string, size int64) {
	row := store.MediaFetch{EventSeq: j.seq, Idx: j.idx, Kind: j.kind, URLSig: j.sig, State: store.MediaDone,
		Tries: j.tries, UpdatedAt: f.nowMs(), Hash: hash, Size: size, Format: j.format}
	if j.kind == types.SegVideo || j.format == "video" {
		f.videoMeta(j.ctx, hash, &row)
	} else if j.kind != types.SegFile || j.format == "image" {
		m, err := f.imageMeta(hash)
		if err != nil {
			f.warnf("收图：读取事件 %d 第 %d 段的元数据出错：%v", j.seq, j.idx, err)
		}
		row.Width, row.Height, row.Animated = m.Width, m.Height, m.Animated
	}
	if isTask(j.kind) {
		f.videoRequestMu.Lock()
		defer f.videoRequestMu.Unlock()
	}
	j.terminalMu.Lock()
	if !f.live() || j.ctx.Err() != nil {
		j.terminalMu.Unlock()
		f.release(j)
		return
	}
	var ok bool
	var err error
	if j.forward != "" {
		ok, err = f.o.Store.FinishForwardSegHash(j.forward, j.node, j.conv, row)
	} else {
		ok, err = f.o.Store.FinishSegHash(j.seq, j.idx, j.kind, j.sig, hash, row)
	}
	j.completed = err == nil && ok
	j.terminalMu.Unlock()
	switch {
	case err != nil:
		// 文件已经存下了，但事务失败没有填回 hash：下次补扫会重新下载，媒体库按内容去重复用同一个文件。
		f.errorf("收图：事件 %d 的第 %d 段下载好了，回填 hash 出错：%v", j.seq, j.idx, err)
		if j.manual {
			f.finish(j, store.MediaFailed, "回填媒体库失败")
			return
		}
		f.release(j)
		return
	case !ok:
		f.logf("收图：事件 %d 的第 %d 段下载好了，但段已经变了或已经有 hash，没有回填（文件留着）", j.seq, j.idx)
		if j.manual {
			f.finish(j, store.MediaFailed, "视频段已改变")
			return
		}
	}
	f.okFiles.Add(1)
	f.okBytes.Add(size)
	f.release(j)
}

// finish 记终局状态。日志只写事件号、状态、原因和主机名，不写地址
func (f *Fetcher) finish(j *job, state, reason string) {
	f.save(j, state, "", reason, 0)
	logf := f.logf // 太大、要手动下、地址过期是正常结局，只有失败算 WARN
	if state == store.MediaFailed {
		logf = f.warnf
	}
	logf("收图：事件 %d 的第 %d 段没有下载（%s：%s，主机 %s，试了 %d 次）", j.seq, j.idx, state, reason, HostOf(j.url), j.tries)
	f.release(j)
}

// save 写 media_fetch 的一行；关闭以后不写
func (f *Fetcher) save(j *job, state, hash, lastErr string, nextAt int64) {
	if !f.live() {
		return
	}
	row := store.MediaFetch{EventSeq: j.seq, Idx: j.idx, Kind: j.kind, URLSig: j.sig, State: state, Tries: j.tries, NextAt: nextAt, UpdatedAt: f.nowMs(), Hash: hash, LastError: lastErr, Size: j.size}
	var err error
	if j.forward != "" {
		err = f.o.Store.PutForwardMedia(j.forward, j.node, j.conv, row)
	} else {
		err = f.o.Store.PutMediaFetch(row)
	}
	if err != nil {
		f.errorf("收图：记事件 %d 的第 %d 段的下载状态出错：%v", j.seq, j.idx, err)
	}
}

// retryLater 过 delay 再把任务放回队列；等的时候任务一直算"在处理"，补扫不会重复挑它
func (f *Fetcher) retryLater(j *job, delay time.Duration) {
	f.mu.Lock()
	delete(f.backfillRunning, jobKey(j))
	f.notifyBackfillLocked()
	f.mu.Unlock()
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		t := f.clock.NewTimer(delay)
		select {
		case <-t.C():
		case <-j.ctx.Done():
			t.Stop()
			f.release(j)
			return
		}
		if !f.enqueueWait(j) {
			f.release(j)
		}
	}()
}

// marketPNG：商城表情的地址（gxh.vip.qq.com，路径以 raw300.gif 结尾）换成同目录的 raw300.png
func marketPNG(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Hostname()) != "gxh.vip.qq.com" || !strings.HasSuffix(u.Path, "raw300.gif") {
		return "", false
	}
	u.Path = strings.TrimSuffix(u.Path, "raw300.gif") + "raw300.png"
	return u.String(), true
}

// unavailable 只表示这次没有完成刷新；能力不足、预算耗尽、取消和超时都不是过期证据。
func unavailable(err error) bool {
	return errors.Is(err, ErrRefreshUnavailable) || errors.Is(err, types.ErrUnsupported) || errors.Is(err, types.ErrQueryBudget) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// freshURL 向协议端要这个段现在的地址：按文件名对段，文件名对不上时只有张数一样才按先后对，别的不猜。
// 同一条消息的几张图共用一次查询（每条消息每次运行只问一次）。
// 返回 ("", nil) 是问成了、但换不出新地址（没有平台消息号、协议端拒绝或回来的对不上）；错误满足 unavailable 的是没问成
func (f *Fetcher) freshURL(j *job) (string, error) {
	if j.kind == types.SegFile {
		return f.fileURL(j)
	}
	conv, platformID, ok, err := f.o.Store.EventRef(j.seq)
	if err != nil {
		return "", fmt.Errorf("%w：读事件 %d 的消息号出错", ErrRefreshUnavailable, j.seq)
	}
	if !ok || !store.HasPlatformID(platformID) {
		return "", nil
	}
	segs, err := f.refreshedSegs(j.ctx, j.seq, conv, platformID)
	if err != nil {
		if !unavailable(err) {
			return "", nil // 协议端明确答了"不行"：换不出
		}
		return "", err
	}
	var media []types.Seg
	for _, s := range segs {
		if downloadable(s.Kind) && mediaFamily(s.Kind) == mediaFamily(j.kind) {
			media = append(media, s)
		}
	}
	fresh := ""
	if j.file != "" {
		for _, s := range media {
			if s.File == j.file && s.URL != "" {
				fresh = s.URL
				if s.Size > 0 {
					j.size = s.Size
				}
				break
			}
		}
	}
	if fresh == "" && len(media) == j.nmedia && j.nth < len(media) {
		fresh = media[j.nth].URL
		if media[j.nth].Size > 0 {
			j.size = media[j.nth].Size
		}
	}
	if fresh == "" || fresh == j.url {
		return "", nil
	}
	return fresh, nil
}

// forgetRefreshErrors 忘掉已经查完、结果是出错的缓存。每次补扫请求前调：没连上时问不成的错，
// 不能顶着 refreshTTL 把连上以后的补扫也挡掉
func (f *Fetcher) forgetRefreshErrors() {
	f.refreshMu.Lock()
	defer f.refreshMu.Unlock()
	for seq, e := range f.refreshes {
		select {
		case <-e.done:
			if e.err != nil {
				delete(f.refreshes, seq)
			}
		default:
		}
	}
}

func (f *Fetcher) refreshedSegs(ctx context.Context, seq int64, conv, platformID string) ([]types.Seg, error) {
	f.refreshMu.Lock()
	e := f.refreshes[seq]
	if e != nil && f.clock.Now().Sub(e.at) > refreshTTL {
		select {
		case <-e.done: // 已经查完的才能换掉
			e = nil
		default:
		}
	}
	mine := false
	if e == nil {
		if len(f.refreshes) >= refreshCap {
			f.refreshes = map[int64]*refreshEntry{}
		}
		e = &refreshEntry{done: make(chan struct{}), at: f.clock.Now()}
		f.refreshes[seq] = e
		mine = true
	}
	f.refreshMu.Unlock()
	if mine {
		e.segs, e.err = f.o.Refresher.Message(ctx, conv, platformID)
		close(e.done)
		if e.err != nil && f.live() {
			// 每条消息记一行；同一条消息的几张图共用这个结果，不各记一行
			if unavailable(e.err) {
				f.warnf("收图：向协议端换事件 %d 的新图片地址暂时没问成，这条消息的图留给下次补扫：%v", seq, e.err)
			} else {
				f.warnf("收图：向协议端换事件 %d 的新图片地址没成：%v", seq, e.err)
			}
		}
	} else {
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return e.segs, e.err
}

// sweep 补扫近七天图片 / 文件及本轮新视频 / 文件；failed 仍只额外试一次。
func (f *Fetcher) sweep() {
	defer f.sweeps.Add(1)
	f.sweepForward()
	begin := time.Now()
	since := f.clock.Now().Add(-f.o.Window).UnixMilli()
	var after int64
	queued, events := 0, 0
	for f.live() {
		page, err := f.o.Store.MediaCandidates(since, after, sweepPage, f.firstVideo.Load())
		if err != nil {
			f.errorf("收图补扫：查库出错：%v", err)
			return
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			after = e.Seq
			var body struct {
				Segs []types.Seg `json:"segs"`
			}
			if json.Unmarshal([]byte(e.Segments), &body) != nil {
				continue
			}
			events++
			rows, err := f.o.Store.MediaFetchRows(e.Seq)
			if err != nil {
				f.errorf("收图补扫：读事件 %d 的下载状态出错：%v", e.Seq, err)
				continue
			}
			for _, j := range jobsOf(e.Seq, body.Segs) {
				if isTask(j.kind) && (f.firstVideo.Load() == 0 || j.seq < f.firstVideo.Load()) && (j.kind != types.SegFile || e.CreatedAt < since) {
					continue
				}
				if row, has := rows[j.idx]; has && row.URLSig == j.sig {
					switch row.State {
					case store.MediaPending:
						j.tries = row.Tries
					case store.MediaFailed:
						old := f.clock.Now().UnixMilli()-e.CreatedAt > failedRetryAge.Milliseconds()
						if old || row.Tries != maxAttempts {
							continue // 太老了，或者已经是补扫再试过的那一次
						}
						j.tries, j.maxTries = row.Tries, row.Tries+1
					default:
						continue // 终局状态不再试
					}
				}
				if !f.claim(j) {
					continue
				}
				if !f.enqueueWait(j) {
					f.release(j)
					return
				}
				queued++
			}
		}
		if len(page) < sweepPage {
			break
		}
	}
	if queued > 0 {
		f.logf("收图补扫：看了 %d 条事件，排进 %d 个图片段，用时 %v", events, queued, time.Since(begin).Round(time.Millisecond))
	}
}

// realClock 是真钟（types.Clock）
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(d time.Duration) types.Timer {
	return realTimer{time.NewTimer(d)}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// orLogf 给 WARN / ERROR 出口兜底：没接就和普通日志同一个出口
func orLogf(f, fallback func(string, ...any)) func(string, ...any) {
	if f != nil {
		return f
	}
	return fallback
}
