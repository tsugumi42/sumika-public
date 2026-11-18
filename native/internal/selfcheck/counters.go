package selfcheck

import (
	"sync"
	"sync/atomic"
	"time"
)

// Counter 是一个计数项。所有项都是进程内的累计量，只增不减；当天的数由 Runner 按"续上今天文件里的数 + 本进程新增"算。
type Counter int

const (
	// 适配器收到的帧（snowluma 的 dispatch）：收到 = 解码失败 + 不是消息 + 交给入库
	FrameReceived Counter = iota
	FrameDecodeFailed
	FrameNonMessage
	FrameToIntake
	// 交给入库一站的帧在接收端（app 的 sink）被丢掉：号对不上（连错了端点），只记日志、不入库，是有意的去向
	FrameSinkDropped
	// 入库一站（Intake.Submit，实时和补录都走它）：交来 = 下面各种结局之和，每次交来恰好落进一个结局
	IntakeSubmitted
	IntakeLive     // 交来的消息里走实时入口（Intake.Inbound，适配器经接收端交来）的那部分，补录不算
	IntakeRejected // 没收下：ctx 已取消、准入检查前的合法性检查没过（不认识的号、Kind 不对、会话不合法），调用方拿到 error
	IntakeClosed   // 入库一站关了：排队时已关、或排着没开始就被丢
	IntakeCancelled
	IntakeAdmitDropped
	IntakePanicked
	IntakeStoreFailed
	IntakeDuplicate
	IntakeStored
	// 出口：等回声等不到，按"结果未知"收场
	OutletReceiptUnknown
	// 适配器：协议端连接断开事件（一次断线里的多次退避只算一次）
	Reconnects
	numCounters
)

var counterNames = [numCounters]string{
	FrameReceived: "received", FrameDecodeFailed: "decodeFailed", FrameNonMessage: "nonMessage", FrameToIntake: "toIntake", FrameSinkDropped: "sinkDropped",
	IntakeSubmitted: "submitted", IntakeLive: "live", IntakeRejected: "rejected", IntakeClosed: "closedDropped", IntakeCancelled: "cancelled",
	IntakeAdmitDropped: "admitDropped", IntakePanicked: "panicked", IntakeStoreFailed: "storeFailed", IntakeDuplicate: "duplicate", IntakeStored: "stored",
	OutletReceiptUnknown: "receiptUnknown", Reconnects: "reconnects",
}

// Name 是计数项写进 JSON 的键
func (c Counter) Name() string { return counterNames[c] }

// disconnectRing 要不小于配置里 reconnectCount 的上限（config/selfcheck.go，100），否则阈值设到窗口以上永远不会触发；check_s5_test 钉住 100 次数得出
const disconnectRing = 128

// Counters 是一组计数器。nil 是合法的：没接自检（测试、命令行工具）时所有方法什么也不做，调用点不用判断。
// 计数路径只有一次原子加，不加锁、不分配（规格第八节：BenchmarkAdd ≤ 30 ns）；断开事件的环形窗口很少用，才加锁。
type Counters struct {
	v          [numCounters]atomic.Int64
	lastSubmit atomic.Int64 // 最近一次交来消息的时间（UnixMilli），静默点判断用
	storeErr   atomic.Value // 最近一次入库失败的错误类别（string）

	mu    sync.Mutex
	disc  [disconnectRing]time.Time
	discN int // 已记的总次数，环形写位置 = discN % disconnectRing
}

// NewCounters 新建一组计数器
func NewCounters() *Counters { return &Counters{} }

// Add 给一个计数项加一
func (c *Counters) Add(k Counter) {
	if c == nil {
		return
	}
	c.v[k].Add(1)
}

// Get 读一个计数项的进程内累计值
func (c *Counters) Get(k Counter) int64 {
	if c == nil {
		return 0
	}
	return c.v[k].Load()
}

// Frame 记适配器收到一帧，只数、不刷新"最近一次"的时间：心跳帧、通知帧也走这里，
// 它们每隔几秒就来一次，要是也刷新时间，静默点（距上一条消息超过 5 秒）就永远等不到
func (c *Counters) Frame() {
	if c == nil {
		return
	}
	c.v[FrameReceived].Add(1)
}

// Touch 记"刚交来一条消息"（实时和补录都算，入库一站的 Submit 调），刷新静默点用的时间；时间不进计数文件
func (c *Counters) Touch(now time.Time) {
	if c == nil {
		return
	}
	c.lastSubmit.Store(now.UnixMilli())
}

// LastSubmit 是最近一次交来消息的时间；没有过是零值
func (c *Counters) LastSubmit() time.Time {
	if c == nil {
		return time.Time{}
	}
	ms := c.lastSubmit.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// StoreFailed 记一次入库失败和它的错误类别（只记类别，不记原文：原文可能带消息内容）
func (c *Counters) StoreFailed(class string) {
	if c == nil {
		return
	}
	c.v[IntakeStoreFailed].Add(1)
	c.storeErr.Store(class)
}

// LastStoreError 是最近一次入库失败的错误类别；没有失败过是空
func (c *Counters) LastStoreError() string {
	if c == nil {
		return ""
	}
	s, _ := c.storeErr.Load().(string)
	return s
}

// Disconnect 记一次协议端断开事件，同时进最近断开的环形窗口
func (c *Counters) Disconnect(now time.Time) {
	if c == nil {
		return
	}
	c.v[Reconnects].Add(1)
	c.mu.Lock()
	c.disc[c.discN%disconnectRing] = now
	c.discN++
	c.mu.Unlock()
}

// DisconnectsSince 是 since 之后（含）记下的断开次数。环形窗口留最近 128 次，比配置允许的最大阈值（100）大，数得出"到没到"
func (c *Counters) DisconnectsSince(since time.Time) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(c.discN, disconnectRing)
	cnt := 0
	for i := 0; i < n; i++ {
		if !c.disc[i].Before(since) {
			cnt++
		}
	}
	return cnt
}

// snapshot 是现在全部计数项的值
func (c *Counters) snapshot() [numCounters]int64 {
	var out [numCounters]int64
	if c == nil {
		return out
	}
	for i := range out {
		out[i] = c.v[i].Load()
	}
	return out
}
