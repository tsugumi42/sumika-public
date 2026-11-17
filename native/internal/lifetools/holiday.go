package lifetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/freeapi"
)

// 法定假日和调休：国务院每年发通知定，算不出来，只能联网取。数据用 holiday-cn（每年一个 JSON，
// {year, papers, days:[{name, date, isOffDay}]}），缓存在数据目录，隔几天更新一次，断网用旧的。
//
// 判定规矩（不猜）：
//   - 表里有这天：isOffDay 为真放假，为假是调休上班；
//   - 表里没有这天：只有该年数据已发布才能说"普通周末"或"普通工作日"；
//   - 该年数据还没发布（HTTP 404）、数据坏了、取不到又没有旧缓存：如实说，不按周末规则猜。
// 12 月下旬的日期还要看下一年的表：元旦调休的补班日（12 月底）写在下一年的通知里，下一年取不到就不下结论。

// YearState 是某年数据的状态
type YearState int

const (
	YearOK           YearState = iota // 有可用的数据
	YearNotPublished                  // 还没公布（服务器上没有这一年）
	YearUnavailable                   // 这次取不到（断网、出错、数据坏了）且没有旧缓存
)

// Entry 是表里的一天
type Entry struct {
	Name   string
	OffDay bool
}

// YearTable 是一年的假日表，键是 2027-01-31 这样的日期
type YearTable struct {
	Year int
	Days map[string]Entry
}

type rawYear struct {
	Year int `json:"year"`
	Days []struct {
		Name     string `json:"name"`
		Date     string `json:"date"`
		IsOffDay *bool  `json:"isOffDay"`
	} `json:"days"`
}

// parseYear 校验并解析一年的数据：年份对得上、至少有一天、每天的日期合法、isOffDay 有值、同一天不冲突
func parseYear(b []byte, want int) (*YearTable, error) {
	var r rawYear
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("数据不是合法 JSON")
	}
	if r.Year != want {
		return nil, errors.New("数据的年份对不上")
	}
	if len(r.Days) == 0 {
		return nil, errors.New("数据里没有任何一天")
	}
	t := &YearTable{Year: want, Days: make(map[string]Entry, len(r.Days))}
	for _, d := range r.Days {
		if _, err := time.Parse("2006-01-02", d.Date); err != nil || d.IsOffDay == nil || len(d.Date) != 10 {
			return nil, errors.New("数据里有不合格的一天")
		}
		e := Entry{Name: cleanName(d.Name, 20), OffDay: *d.IsOffDay}
		if old, dup := t.Days[d.Date]; dup && old.OffDay != e.OffDay {
			return nil, errors.New("数据里同一天写了相反的安排")
		}
		t.Days[d.Date] = e
	}
	return t, nil
}

// HolidayOptions 是假日数据的设置；零值用括号里的默认
type HolidayOptions struct {
	Dir          string // 缓存目录
	URL          func() string
	Proxy        func() string
	Clients      *clients
	Engine       *freeapi.Engine // 免费接口注册表：地址没被用户改过时从它取（jsDelivr、raw、timor 依次回退）
	Region       func() string   // 网络线路所在区域（cn 或 global）
	Now          func() time.Time
	Logf         func(string, ...any)
	RefreshAfter time.Duration // 缓存超过这么久就在后台更新（72 小时）
	SyncTimeout  time.Duration // 没有缓存时同步取的时限（5 秒）
	NegativeTTL  time.Duration // "没公布"的结论记多久（1 小时）
	FailureTTL   time.Duration // 取失败后多久内不再同步重试（1 分钟）
	// Trace 记一次用户自配地址的取数（插件日志）；nil 不记
	Trace func(rawURL string, took time.Duration, err error)
}

// Holidays 管假日数据的缓存和更新
type Holidays struct {
	o      HolidayOptions
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	locks    map[int]*sync.Mutex
	inflight map[int]bool
	notPub   map[int]time.Time // 年 → 查到"没公布"的时刻
	failedAt map[int]time.Time // 年 → 上一次同步取失败的时刻
}

// NewHolidays 造假日数据管理；Close 取消后台更新并等它们退出
func NewHolidays(o HolidayOptions) *Holidays {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.RefreshAfter <= 0 {
		o.RefreshAfter = 72 * time.Hour
	}
	if o.SyncTimeout <= 0 {
		o.SyncTimeout = 5 * time.Second
	}
	if o.NegativeTTL <= 0 {
		o.NegativeTTL = time.Hour
	}
	if o.FailureTTL <= 0 {
		o.FailureTTL = time.Minute
	}
	if o.Clients == nil {
		o.Clients = newClients(nil)
	}
	h := &Holidays{o: o, locks: map[int]*sync.Mutex{}, inflight: map[int]bool{}, notPub: map[int]time.Time{}, failedAt: map[int]time.Time{}}
	h.base, h.cancel = context.WithCancel(context.Background())
	return h
}

func (h *Holidays) Close() {
	h.cancel()
	h.wg.Wait()
	h.o.Clients.closeIdle()
}

// Wait 等后台更新做完（测试用）
func (h *Holidays) Wait() { h.wg.Wait() }

func (h *Holidays) cachePath(y int) string { return filepath.Join(h.o.Dir, strconv.Itoa(y)+".json") }

// readCache 读缓存：返回表和缓存的年龄；文件不存在、数据坏了都当没有
func (h *Holidays) readCache(y int) (*YearTable, time.Duration, bool) {
	p := h.cachePath(y)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, 0, false
	}
	t, err := parseYear(b, y)
	if err != nil {
		h.o.Logf("日历：%d 年的假日缓存不可用：%v", y, err)
		return nil, 0, false
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, 0, false
	}
	return t, h.o.Now().Sub(st.ModTime()), true
}

func (h *Holidays) url(y int) string {
	u := ""
	if h.o.URL != nil {
		u = h.o.URL()
	}
	if u == "" {
		u = defaultHolidayURL
	}
	return strings.ReplaceAll(u, "{year}", strconv.Itoa(y))
}

// usesRegistry：地址没配或和默认一样，就用注册表里的提供方
func (h *Holidays) usesRegistry() bool {
	u := ""
	if h.o.URL != nil {
		u = h.o.URL()
	}
	return u == "" || u == defaultHolidayURL
}

// fetchURL 只用一个地址取（用户自己配的）；404 = 还没公布
func (h *Holidays) fetchURL(ctx context.Context, y int) ([]byte, error) {
	proxy := ""
	if h.o.Proxy != nil {
		proxy = h.o.Proxy()
	}
	cl, err := h.o.Clients.get(proxy)
	if err != nil {
		return nil, err
	}
	u := h.url(y)
	began := time.Now()
	b, err := freeapi.Get(ctx, cl, u, nil, holidayBodyMax)
	err = notFoundOn404(err)
	if h.o.Trace != nil {
		h.o.Trace(u, time.Since(began), err)
	}
	return b, err
}

// fetchRegistry 走注册表：不缓存（磁盘缓存就是缓存），不合并（同一年同时只有一个取数）
func (h *Holidays) fetchRegistry(ctx context.Context, y int) ([]byte, error) {
	region := freeapi.RegionCN
	if h.o.Region != nil {
		region = h.o.Region()
	}
	res, err := h.o.Engine.Do(ctx, freeapi.Request{Category: "holiday", Region: region, Fetch: map[string]freeapi.Fetcher{
		"holiday-jsdelivr": holidayFetcher("holiday-jsdelivr", y),
		"holiday-raw":      holidayFetcher("holiday-raw", y),
		"holiday-timor":    holidayFetcher("holiday-timor", y),
	}})
	if err != nil {
		return nil, err
	}
	return res.Value.([]byte), nil
}

// defaultHolidayURL 和 config.DefaultHolidayURL 一样（这里不引 config，避免装配以外的依赖）
const defaultHolidayURL = "https://raw.githubusercontent.com/NateScarlet/holiday-cn/master/{year}.json"

// fetch 取一年的数据并写进缓存（先写临时文件再改名）。404 或各家都说没有这一年返回 YearNotPublished；别的失败返回 YearUnavailable 和原因。
// 用户在设置里改了假日数据地址就只用他的地址；没改（默认）就走注册表，jsDelivr 不通换 raw，再换 timor.tech
func (h *Holidays) fetch(ctx context.Context, y int) (*YearTable, YearState, error) {
	var b []byte
	var err error
	if h.o.Engine != nil && h.usesRegistry() {
		b, err = h.fetchRegistry(ctx, y)
	} else {
		b, err = h.fetchURL(ctx, y)
	}
	if err != nil {
		if errors.Is(err, freeapi.ErrNotFound) {
			return nil, YearNotPublished, nil
		}
		return nil, YearUnavailable, err
	}
	t, err := parseYear(b, y)
	if err != nil {
		return nil, YearUnavailable, err
	}
	if err := os.MkdirAll(h.o.Dir, 0o755); err != nil {
		return t, YearOK, nil // 写不进缓存也不影响这次用
	}
	tmp, err := os.CreateTemp(h.o.Dir, strconv.Itoa(y)+"-*.tmp")
	if err == nil {
		_, werr := tmp.Write(b)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			err = os.Rename(tmp.Name(), h.cachePath(y))
		}
		if werr != nil || cerr != nil || err != nil {
			os.Remove(tmp.Name())
		}
	}
	return t, YearOK, nil
}

func (h *Holidays) yearLock(y int) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.locks[y]
	if l == nil {
		l = &sync.Mutex{}
		h.locks[y] = l
	}
	return l
}

// refreshAsync 后台更新一年的缓存，同一年同时只有一个；失败保留旧缓存
func (h *Holidays) refreshAsync(y int) {
	h.mu.Lock()
	if h.inflight[y] || h.base.Err() != nil {
		h.mu.Unlock()
		return
	}
	h.inflight[y] = true
	h.wg.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.wg.Done()
		ctx, cancel := context.WithTimeout(h.base, 15*time.Second)
		defer cancel()
		_, st, err := h.fetch(ctx, y)
		if st != YearOK {
			// 没公布也好、取不到也好，旧缓存照用；把时刻往后挪，不要每次查询都再起一次
			h.mu.Lock()
			h.failedAt[y] = h.o.Now()
			h.mu.Unlock()
			if err != nil {
				h.o.Logf("日历：后台更新 %d 年假日数据没成功：%v", y, errKind(err))
			}
		}
		h.mu.Lock()
		delete(h.inflight, y)
		h.mu.Unlock()
	}()
}

// errKind 日志里只写类别，不写网址
func errKind(err error) string {
	switch {
	case errors.Is(err, freeapi.ErrNoProvider):
		return "没有可用的接口"
	case errors.Is(err, freeapi.ErrAllFailed):
		var ce *freeapi.CallError
		if errors.As(err, &ce) {
			return "接口都没成功（" + string(ce.Last) + "）"
		}
		return "接口都没成功"
	}
	k, _ := freeapi.Classify(err)
	switch k {
	case freeapi.KTimeout:
		return "超时"
	case freeapi.KCanceled:
		return "取消"
	case freeapi.KTooLarge:
		return "响应太大"
	}
	var se *freeapi.StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("HTTP %d", se.Code)
	}
	return "网络或数据错误"
}

// Year 取某年的假日表。有可用缓存就马上用（旧了顺手在后台更新）；完全没有才同步联网取，有时限，刚失败过的短时间内不再重试
func (h *Holidays) Year(ctx context.Context, y int) (*YearTable, YearState) {
	if t, age, ok := h.readCache(y); ok {
		if age > h.o.RefreshAfter {
			h.mu.Lock()
			recent := h.o.Now().Sub(h.failedAt[y]) < h.o.FailureTTL
			h.mu.Unlock()
			if !recent {
				h.refreshAsync(y)
			}
		}
		return t, YearOK
	}
	if st, done := h.recentVerdict(y); done {
		return nil, st
	}
	lk := h.yearLock(y)
	lk.Lock()
	defer lk.Unlock()
	if t, _, ok := h.readCache(y); ok {
		return t, YearOK
	}
	if st, done := h.recentVerdict(y); done {
		return nil, st
	}
	fctx, cancel := context.WithTimeout(ctx, h.o.SyncTimeout)
	defer cancel()
	t, st, err := h.fetch(fctx, y)
	h.mu.Lock()
	switch st {
	case YearNotPublished:
		h.notPub[y] = h.o.Now()
	case YearUnavailable:
		h.failedAt[y] = h.o.Now()
	}
	h.mu.Unlock()
	if err != nil {
		h.o.Logf("日历：取 %d 年假日数据没成功：%v", y, errKind(err))
	}
	return t, st
}

// recentVerdict：刚查到"没公布"或刚取失败，短时间内直接沿用结论
func (h *Holidays) recentVerdict(y int) (YearState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.o.Now()
	if at, ok := h.notPub[y]; ok && now.Sub(at) < h.o.NegativeTTL {
		return YearNotPublished, true
	}
	if at, ok := h.failedAt[y]; ok && now.Sub(at) < h.o.FailureTTL {
		return YearUnavailable, true
	}
	return YearUnavailable, false
}

// Kind 是一天的放假状态
type Kind int

const (
	KindOff          Kind = iota // 法定放假
	KindMakeupWork               // 调休上班
	KindWeekend                  // 普通周末（该年数据已发布，表里没有这天）
	KindWorkday                  // 普通工作日（同上）
	KindNotPublished             // 这年的安排还没公布
	KindUnavailable              // 这次取不到
)

// Status 是一天的放假状态和说明
type Status struct {
	Kind Kind
	Name string // 节日名（放假、调休上班才有）
	// KindNotPublished 时：到底是哪一年的通知还没公布（12 月下旬看的是下一年）；OwnYear 为真表示本年通知已有、这天不在里面
	Year    int
	OwnYear bool
}

// Lookup 判定某一天。weekend 是这天按日历是不是周六日（表里没有时才用它，且只在数据已发布时）
func (h *Holidays) Lookup(ctx context.Context, y int, m time.Month, d int) Status {
	key := fmt.Sprintf("%04d-%02d-%02d", y, int(m), d)
	wd := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday()
	t, st := h.Year(ctx, y)
	switch st {
	case YearNotPublished:
		return Status{Kind: KindNotPublished, Year: y}
	case YearUnavailable:
		return Status{Kind: KindUnavailable}
	}
	if e, ok := t.Days[key]; ok {
		return fromEntry(e)
	}
	if m == time.December && d >= 20 {
		// 元旦调休的补班日写在下一年的通知里
		nt, nst := h.Year(ctx, y+1)
		switch nst {
		case YearUnavailable:
			return Status{Kind: KindUnavailable}
		case YearNotPublished:
			// 补班日、元旦连休都可能在下一年的通知里：这天按本年通知不是假日，也不能就此说是周末或工作日
			return Status{Kind: KindNotPublished, Year: y + 1, OwnYear: true}
		case YearOK:
			if e, ok := nt.Days[key]; ok {
				return fromEntry(e)
			}
		}
	}
	if wd == time.Saturday || wd == time.Sunday {
		return Status{Kind: KindWeekend}
	}
	return Status{Kind: KindWorkday}
}

func fromEntry(e Entry) Status {
	if e.OffDay {
		return Status{Kind: KindOff, Name: e.Name}
	}
	return Status{Kind: KindMakeupWork, Name: e.Name}
}
