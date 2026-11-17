package freeapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Limit 是一个工具每个人、每个群的次数；0 = 不限
type Limit struct {
	PerActorDaily         int
	PerConversationHourly int
}

// ProviderCap 是用户手写覆盖的某个提供方上限；0 = 用注册表的
type ProviderCap struct {
	DailyCap  int
	PerMinute int
}

// Settings 是每次调用时读的设置（改了马上生效）
type Settings struct {
	NetworkRegion string // 节假日、汇率这类看网络线路的类别用：cn 或 global；空按 cn
	ProbeEnabled  bool
	ProbeInterval time.Duration
	Limits        map[string]Limit
	Providers     map[string]ProviderCap
	Proxy         string // 和 linkReader.proxy 同一个；只用来区分路线，真正走哪条由 HTTP 决定
}

// Options 是装配处给的东西
type Options struct {
	Catalog   Catalog // 零值用内嵌的
	StateFile string  // 空 = 只放内存（测试）
	Settings  func() Settings
	// HTTP 给出走哪条路线的客户端：direct 为真不走代理
	HTTP func(direct bool) (*http.Client, error)
	Now  func() time.Time
	Zone func() *time.Location // 算“每天”用的时区；nil 用本机
	Logf func(string, ...any)  // 只写类别、提供方 id、结局、毫秒
	// Trace 每次真的调提供方（不含探测）以后调一次，给插件日志用；nil 不调。不能阻塞
	Trace func(Attempt)
	// 下面几项只给测试
	Rand       func(n int) int           // [0,n) 的随机数
	URLs       map[string]string         // 提供方 id → 换掉注册表里的地址
	Backoff    func(n int) time.Duration // 第 n 次退避多久（含抖动）
	AttemptMax time.Duration             // 单次尝试时限（4 秒）
}

// Engine 是注册表加状态加调用
type Engine struct {
	o       Options
	cat     Catalog
	byID    map[string]Provider
	mu      sync.Mutex
	st      stateFile
	minutes map[string][]time.Time // 提供方 → 最近一分钟内的请求时刻
	force   map[string]time.Time   // 类别 → 上次强试的时刻
	probes  map[string]ProbeFn
	lastMan map[string]time.Time // 提供方 → 上次手动检测的时刻
	cache   map[string]cacheEntry
	flights map[string]*flight
	saveErr bool

	probeMu   sync.Mutex
	probeStop context.CancelFunc
	probeWG   sync.WaitGroup
}

type cacheEntry struct {
	v   any
	p   Provider
	exp time.Time
}

type flight struct {
	done chan struct{}
	res  Result
	err  error
	// waiters 是正在等这一次的调用数（持 e.mu 改）；abandoned 是领头的调用自己被取消、没有可共享的结果。
	waiters   int
	abandoned bool
}

// 日志和状态里“最近事件”的一条
type Event struct {
	T    int64  `json:"t"`
	ID   string `json:"id"`
	Text string `json:"text"`
}

type sample struct {
	T     int64 `json:"t"`
	K     Kind  `json:"k"`
	MS    int32 `json:"ms"`
	Probe bool  `json:"p,omitempty"`
}

type provState struct {
	Samples      []sample `json:"samples,omitempty"`
	Streak       int      `json:"streak,omitempty"`
	SchemaStreak int      `json:"schemaStreak,omitempty"`
	BackoffN     int      `json:"backoffN,omitempty"`
	OpenUntil    int64    `json:"openUntil,omitempty"`
	PausedUntil  int64    `json:"pausedUntil,omitempty"`
	Attention    Kind     `json:"attention,omitempty"` // 非空 = 需处理，值是原因
	LastOK       int64    `json:"lastOK,omitempty"`
	LastCheck    int64    `json:"lastCheck,omitempty"`
	LastKind     Kind     `json:"lastKind,omitempty"`
}

type manualState struct {
	Pin bool `json:"pin,omitempty"`
	Off bool `json:"off,omitempty"`
}

type counter struct {
	W string `json:"w"`
	N int    `json:"n"`
}

type stateFile struct {
	Providers map[string]*provState  `json:"providers"` // 键：提供方 id|路线
	Manual    map[string]manualState `json:"manual"`
	Counters  map[string]counter     `json:"counters"`
	Events    []Event                `json:"events,omitempty"`
}

const (
	maxSamples = 100
	maxEvents  = 50
)

// New 造引擎；有状态文件就读进来（读不出来、坏了都从头来，只记日志）
func New(o Options) *Engine {
	if o.Catalog.Providers == nil {
		o.Catalog = DefaultCatalog()
	}
	if o.Settings == nil {
		o.Settings = func() Settings { return DefaultSettings() }
	}
	if o.HTTP == nil {
		o.HTTP = defaultHTTP
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Rand == nil {
		o.Rand = rand.IntN
	}
	if o.Backoff == nil {
		o.Backoff = defaultBackoff(o.Rand)
	}
	if o.AttemptMax <= 0 {
		o.AttemptMax = 4 * time.Second
	}
	e := &Engine{o: o, cat: o.Catalog, byID: map[string]Provider{}, minutes: map[string][]time.Time{}, force: map[string]time.Time{},
		probes: map[string]ProbeFn{}, lastMan: map[string]time.Time{}, cache: map[string]cacheEntry{}, flights: map[string]*flight{}}
	for _, p := range e.cat.Providers {
		e.byID[p.ID] = p
	}
	e.st = stateFile{Providers: map[string]*provState{}, Manual: map[string]manualState{}, Counters: map[string]counter{}}
	e.load()
	return e
}

// DefaultSettings 是不读配置时的起步值（和 config.DefaultFreeAPI 一致，测试用）
func DefaultSettings() Settings {
	return Settings{NetworkRegion: RegionCN, ProbeEnabled: true, ProbeInterval: 12 * time.Hour,
		Limits: map[string]Limit{"weather": {20, 15}, "exchange_rate": {20, 15}}}
}

// defaultBackoff：1 分钟起，每次翻倍，封顶 1 小时，±20% 抖动
func defaultBackoff(rnd func(int) int) func(int) time.Duration {
	return func(n int) time.Duration {
		d := time.Minute
		for i := 1; i < n && d < time.Hour; i++ {
			d *= 2
		}
		if d > time.Hour {
			d = time.Hour
		}
		jitter := time.Duration(rnd(401)-200) * d / 1000 // ±20%
		return d + jitter
	}
}

// Catalog 给界面和测试看
func (e *Engine) Catalog() Catalog { return e.cat }

func (e *Engine) now() time.Time { return e.o.Now() }

func (e *Engine) zone() *time.Location {
	if e.o.Zone != nil {
		if z := e.o.Zone(); z != nil {
			return z
		}
	}
	return time.Local
}

func (e *Engine) dayKey(t time.Time) string  { return t.In(e.zone()).Format("2006-01-02") }
func (e *Engine) hourKey(t time.Time) string { return t.In(e.zone()).Format("2006-01-02T15") }

// routeOf：这个提供方这次走哪条路线（统计按路线分开）
func routeOf(p Provider, s Settings) string {
	if p.Network == NetDirect || s.Proxy == "" {
		return NetDirect
	}
	return NetProxy
}

func stKey(id, route string) string { return id + "|" + route }

func (e *Engine) pstate(id, route string) *provState {
	k := stKey(id, route)
	ps := e.st.Providers[k]
	if ps == nil {
		ps = &provState{}
		e.st.Providers[k] = ps
	}
	return ps
}

// ---- 持久化 ----

func (e *Engine) load() {
	if e.o.StateFile == "" {
		return
	}
	raw, err := os.ReadFile(e.o.StateFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			e.o.Logf("免费接口：读不了状态文件，从头开始：%v", errKindText(err))
		}
		return
	}
	var s stateFile
	if err := json.Unmarshal(raw, &s); err != nil {
		e.o.Logf("免费接口：状态文件坏了，从头开始")
		return
	}
	if s.Providers != nil {
		e.st.Providers = s.Providers
	}
	if s.Manual != nil {
		e.st.Manual = s.Manual
	}
	if s.Counters != nil {
		e.st.Counters = s.Counters
	}
	e.st.Events = s.Events
}

// save 写回（先写临时文件再换名）。调用时持锁。写不进只记一次日志，不让功能停
func (e *Engine) save() {
	e.prune()
	if e.o.StateFile == "" {
		return
	}
	raw, err := json.Marshal(e.st)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(e.o.StateFile), 0o755); err == nil {
			tmp := e.o.StateFile + ".tmp"
			if err = os.WriteFile(tmp, raw, 0o600); err == nil {
				err = os.Rename(tmp, e.o.StateFile)
			}
		}
	}
	if err != nil && !e.saveErr {
		e.saveErr = true
		e.o.Logf("免费接口：写不进状态文件，重启后统计和次数会少算")
	} else if err == nil {
		e.saveErr = false
	}
}

func errKindText(err error) string {
	k, _ := Classify(err)
	return string(k)
}

// event 记一条状态变化：写日志和最近事件。调用时持锁
func (e *Engine) event(id, text string) {
	e.o.Logf("免费接口：%s %s", id, text)
	e.st.Events = append(e.st.Events, Event{T: e.now().UnixMilli(), ID: id, Text: text})
	if n := len(e.st.Events); n > maxEvents {
		e.st.Events = append([]Event(nil), e.st.Events[n-maxEvents:]...)
	}
}

// hashID 把用户号、会话号变成短哈希再落盘
func hashID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// prune 清掉过了窗口的计数（每群每小时的看小时，别的看天）。调用时持锁
func (e *Engine) prune() {
	now := e.now()
	day, hour := e.dayKey(now), e.hourKey(now)
	for k, c := range e.st.Counters {
		want := day
		if len(k) > 2 && k[:2] == "g:" {
			want = hour
		}
		if c.W != want {
			delete(e.st.Counters, k)
		}
	}
}
