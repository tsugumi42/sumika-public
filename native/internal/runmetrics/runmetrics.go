// Package runmetrics 是运行页"后台队列"表的注册表（规格-运行页 第四节第 4 条）。
// 各队列在装配处注册一个读数函数；运行页只认注册表，不 import 任何插件。叶子包：没有协程、没有定时器，
// 读数函数只在页面采样那一刻被调用。表是固定的几行，没注册的行显示"未接入"，不显示 0；
// 某一行的读数函数出错或 panic，只那一行写原因，别的行照常。
package runmetrics

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"sumika-native/internal/ctlproto"
)

// 固定的行（规格第四节第 4 条的列表，顺序就是页面上的顺序）
const (
	KeyModelSlots  = "model-slots"  // 模型请求槽位（modelcall）
	KeyBackfill    = "backfill"     // 补录
	KeyVision      = "vision"       // 识图（Go 侧没有调用方，灰色一行）
	KeyMemory      = "memory"       // 记忆整理
	KeyMedia       = "media"        // 媒体下载
	KeyLink        = "link"         // 链接展开
	KeyLocalModels = "local-models" // 本地模型池
)

var fixedRows = []struct{ Key, Label string }{
	{KeyModelSlots, "模型请求槽位"},
	{KeyBackfill, "补录"},
	{KeyVision, "识图"},
	{KeyMemory, "记忆整理"},
	{KeyMedia, "媒体下载"},
	{KeyLink, "链接展开"},
	{KeyLocalModels, "本地模型池"},
}

// Reading 是一个队列这一刻的读数。指针为 nil 表示"这个队列没有这项计数"，不是 0
type Reading struct {
	Backlog    *int64         // 排着队没开始的
	Running    *int64         // 在跑的
	Capacity   *int64         // 容量（同时能跑几个、队列最长多少）
	OldestWait *time.Duration // 最久的一个已经等了多久
	Note       string         // 一句说明，例如"估计值"
}

// I 把整数放进指针，注册处写读数时用
func I(n int) *int64 { v := int64(n); return &v }

// ReadFunc 读一次。出错只影响这一行
type ReadFunc func() (Reading, error)

type queue struct {
	limit time.Duration // 该队列自己的正常等待时限：最久等待超过它而且有积压才算"堵"。写在注册处，页面不猜
	read  ReadFunc
}

// Registry 是一份队列注册表；零值可用
type Registry struct {
	mu sync.Mutex
	m  map[string]queue
}

// Default 是整个进程共用的注册表
var Default = &Registry{}

// Register 注册（或换掉）一个队列的读数函数。limit 是正常等待时限，0 表示这个队列不判"堵"。
// 同一个 key 再注册会替换旧的：后台随接收模式重开时，新的实例接替旧的
func (r *Registry) Register(key string, limit time.Duration, read ReadFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]queue{}
	}
	r.m[key] = queue{limit: limit, read: read}
}

// Unregister 撤销一个队列；之后这一行显示"未接入"
func (r *Registry) Unregister(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, key)
}

// Register、Unregister 作用在共用的注册表上
func Register(key string, limit time.Duration, read ReadFunc) { Default.Register(key, limit, read) }
func Unregister(key string)                                   { Default.Unregister(key) }

// Rows 读出整张表：固定的几行在前，没注册的写 Wired=false；注册了但不在固定列表里的接在后面（按 key 排）
func (r *Registry) Rows() []ctlproto.QueueRow {
	r.mu.Lock()
	snap := make(map[string]queue, len(r.m))
	for k, q := range r.m {
		snap[k] = q
	}
	r.mu.Unlock()
	rows := make([]ctlproto.QueueRow, 0, len(fixedRows)+len(snap))
	seen := map[string]bool{}
	for _, f := range fixedRows {
		seen[f.Key] = true
		rows = append(rows, rowOf(f.Key, f.Label, snap[f.Key]))
	}
	var extra []string
	for k := range snap {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		rows = append(rows, rowOf(k, k, snap[k]))
	}
	return rows
}

// rowOf 读一行。读数函数 panic 当成这一行读失败，不让整张表（和采样）一起倒
func rowOf(key, label string, q queue) (row ctlproto.QueueRow) {
	row = ctlproto.QueueRow{Key: key, Label: label, State: ctlproto.QueueUnknown}
	if q.read == nil {
		return row
	}
	row.Wired = true
	defer func() {
		if p := recover(); p != nil {
			row.Err = fmt.Sprintf("读取失败：%v", p)
			row.State = ctlproto.QueueUnknown
		}
	}()
	rd, err := q.read()
	if err != nil {
		row.Err = "读取失败：" + err.Error()
		return row
	}
	row.Backlog, row.Running, row.Capacity, row.Note = rd.Backlog, rd.Running, rd.Capacity, rd.Note
	if rd.OldestWait != nil {
		ms := rd.OldestWait.Milliseconds()
		row.OldestWaitMs = &ms
	}
	row.State = StateOf(rd, q.limit)
	return row
}

// StateOf 判断空闲、忙、堵（规格第四节第 4 条）：
//   - 堵：积压大于 0，而且最久等待超过这个队列自己的正常时限（limit 为 0 不判堵）；
//   - 忙：有积压或有在跑的；
//   - 空闲：能确定积压和在跑都是 0。积压没有计数但在跑是 0 时也算空闲（没人在跑就不会有人在等）；
//   - 其余（该有的数一个都没有）：未知，不猜。
func StateOf(r Reading, limit time.Duration) string {
	if r.Backlog != nil && *r.Backlog > 0 {
		if limit > 0 && r.OldestWait != nil && *r.OldestWait > limit {
			return ctlproto.QueueJammed
		}
		return ctlproto.QueueBusy
	}
	if r.Running != nil && *r.Running > 0 {
		return ctlproto.QueueBusy
	}
	if r.Running != nil || r.Backlog != nil {
		return ctlproto.QueueIdle // 知道的那一项是 0
	}
	return ctlproto.QueueUnknown
}
