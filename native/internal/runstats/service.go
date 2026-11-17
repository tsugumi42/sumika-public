package runstats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/store"
)

var _ api.StatsReader = (*Service)(nil)

// DefaultTimeout 是单次统计查询的时限（规格第八节第 5 条：超过 2 秒就取消，显示"统计超时"，不卡界面）
const DefaultTimeout = 2 * time.Second

// Options 是 Service 的选项，零值可用
type Options struct {
	Loc     *time.Location   // 按天、按周的边界用的时区；nil 用本地时区
	Now     func() time.Time // nil 用 time.Now；测试里固定
	Timeout time.Duration    // 单次查询时限；0 用 DefaultTimeout
	Storage StorageSource    // 存储标签的数据来源（internal/storagestat.Service）；nil 表示没接入
	Redact  RedactFunc       // 质量标签失败摘要的脱敏；nil 只走 taskrun.Detail 兜底
	// Resources 是资源标签的数据来源（internal/resstat.Sampler）；nil 表示没接入。runstats 不 import resstat，也不碰库
	Resources ResourceSource
}

// RedactFunc 给质量标签的失败摘要先过一遍脱敏（装配处传 app.RedactLog 那一路，runstats 不能 import app）；
// 之后还会再过 taskrun.Detail 兜底，所以不传也不会漏号码、URL、令牌
type RedactFunc func(string) string

// StorageSource 是存储统计的来源。runstats 不 import storagestat，由装配的地方把它放进来
type StorageSource interface {
	Storage(ctx context.Context) (ctlproto.StorageReport, error)
}

// ResourceSource 是资源采样的来源。runstats 不 import resstat，由装配的地方把它放进来
type ResourceSource interface {
	Sample(ctx context.Context, gpu bool) (ctlproto.ResourcesReport, error)
}

// ErrNoStorage 是没接存储统计
var ErrNoStorage = errors.New("存储统计没接入")

// ErrNoResources 是没接资源采样
var ErrNoResources = errors.New("资源采样没接入")

// Resources 采一次资源，交给装配进来的来源。一行都不碰 s.db：资源采样不持有任何数据库连接（规格-运行页 第四节第 5 条）
func (s *Service) Resources(ctx context.Context) (ctlproto.ResourcesReport, error) {
	if s.resources == nil {
		return ctlproto.ResourcesReport{}, ErrNoResources
	}
	return s.resources.Sample(ctx, true)
}

// Storage 读存储标签的数据，交给装配进来的来源
func (s *Service) Storage(ctx context.Context) (ctlproto.StorageReport, error) {
	if s.storage == nil {
		return ctlproto.StorageReport{}, ErrNoStorage
	}
	return s.storage.Storage(ctx)
}

// Service 读运行记录库做统计。它手里的连接必须是只读的（OpenFile 开的就是）；一个 Service 可以同时被几处用
type Service struct {
	db        *sql.DB
	own       bool
	loc       *time.Location
	now       func() time.Time
	timeout   time.Duration
	storage   StorageSource
	redact    RedactFunc
	resources ResourceSource
}

// New 用一个已经打开的只读连接建服务（连接归调用方关）。传可写连接不会被拒绝，但统计查询只有 SELECT，
// 而且应当用只读连接，免得抢写连接（规格第二节第 5 条）
func New(db *sql.DB, o Options) *Service {
	s := &Service{db: db, loc: o.Loc, now: o.Now, timeout: o.Timeout, storage: o.Storage, redact: o.Redact, resources: o.Resources}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.timeout <= 0 {
		s.timeout = DefaultTimeout
	}
	return s
}

// OpenFile 只读打开 trace.db 建服务（mode=ro、query_only、单连接，和 store.OpenReadOnlyFile 一样）。用完要 Close
func OpenFile(traceDB string, o Options) (*Service, error) {
	db, err := store.OpenReadOnlyFile(traceDB)
	if err != nil {
		return nil, err
	}
	s := New(db, o)
	s.own = true
	return s, nil
}

// Close 关掉 OpenFile 开的连接；New 传进来的连接不关
func (s *Service) Close() error {
	if s.own {
		return s.db.Close()
	}
	return nil
}

// RangeOf 把 --days / --year / --all 算成具体的时间段，边界是本地时区的零点。一个都不写是最近 7 天（含今天）。
// days 包含今天：--days 1 是今天零点到明天零点
func (s *Service) RangeOf(a ctlproto.StatsArgs) (ctlproto.UsageRange, error) {
	if err := a.Validate(); err != nil {
		return ctlproto.UsageRange{}, err
	}
	now := s.now().In(s.loc)
	y, m, d := now.Date()
	zone := s.loc.String()
	switch {
	case a.All:
		return ctlproto.UsageRange{Kind: "all", Label: "全部", FromMS: 0, ToMS: math.MaxInt64, Zone: zone}, nil
	case a.Year != 0:
		from := time.Date(a.Year, 1, 1, 0, 0, 0, 0, s.loc)
		to := time.Date(a.Year+1, 1, 1, 0, 0, 0, 0, s.loc)
		return ctlproto.UsageRange{Kind: "year", Label: fmt.Sprintf("%d 年", a.Year), FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Zone: zone}, nil
	}
	n := a.Days
	if n == 0 {
		n = ctlproto.StatsDefaultDays
	}
	from := time.Date(y, m, d-(n-1), 0, 0, 0, 0, s.loc)
	to := time.Date(y, m, d+1, 0, 0, 0, 0, s.loc)
	return ctlproto.UsageRange{Kind: "days", Label: fmt.Sprintf("最近 %d 天", n), FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Zone: zone}, nil
}

// ErrTimeout 是统计查询超过时限被取消
var ErrTimeout = errors.New("统计超时")

const dayMS = int64(24 * time.Hour / time.Millisecond)

// aggCols 是对一组行做的十个合计，用量查询和汇总表重建共用一份，触发器（迁移 0013）手写了同样的算法，测试互相对拍。
// NULL 的处理：Bun 时代的行 usage_seen、incomplete、purpose、cache_write_tokens、reasoning_tokens 都可能是 NULL，
// usage_seen 为 NULL 当作"拿到了"（没有证据说它没拿到），其余 NULL 当 0 或空。
// 耗时只算成功的请求（error 为 NULL）。出错的请求如果有 token 也照算（那些 token 是真花了的）。
const aggCols = `COUNT(*),
 SUM(error IS NOT NULL),
 SUM(COALESCE(usage_seen, 1) = 0 OR COALESCE(incomplete, 0) = 1),
 SUM(input_tokens), SUM(cache_read_tokens), SUM(COALESCE(cache_write_tokens, 0)), SUM(output_tokens), SUM(COALESCE(reasoning_tokens, 0)),
 SUM(error IS NULL AND total_ms IS NOT NULL),
 SUM(CASE WHEN error IS NULL AND total_ms IS NOT NULL THEN total_ms ELSE 0 END)`

// usageSQL 是直接查原始行：只碰窄列。day 是"本地时区的天序号"：(started_at + 时区偏移) / 一天的毫秒数，
// 偏移由 Go 按本地时区算好传入，SQL 里不写时区
const usageSQL = `SELECT COALESCE(purpose, ''), stage, model, (started_at + ?1) / ?2 AS day, ` + aggCols + `
FROM model_runs WHERE started_at >= ?3 AND started_at < ?4
GROUP BY 1, 2, 3, 4`

// rollupSQL 查汇总表：列的顺序和 usageSQL 一样，共用同一段扫描代码
const rollupSQL = `SELECT purpose, stage, model, day, runs, failed, no_usage, uncached, cache_read, cache_write, output, reasoning, ok_runs, ok_ms
FROM usage_rollup WHERE day >= ?1 AND day < ?2`

// 报告的来源
const (
	SourceRollup = "rollup" // 汇总表
	SourceRaw    = "raw"    // 直接查 model_runs
)

// Usage 查一段时间的用量，给出按类别、按模型、按天、按周四种切法（Shape 裁）。
// 范围是 [FromMS, ToMS)，按本地时区的零点对齐由调用方（RangeOf）负责；这里只负责按天、按周分组。
// 汇报的来源：汇总表和本次时区偏移一致、范围正好落在本地零点上就读汇总表，否则（没做过 SyncRollup、时区变了、
// 自定义的不整天范围、库里还没有汇总表）直接查原始行，结果一样，只是慢
func (s *Service) Usage(ctx context.Context, r ctlproto.UsageRange) (ctlproto.UsageReport, error) {
	if r.FromMS < 0 || r.ToMS <= r.FromMS {
		return ctlproto.UsageReport{}, errors.New("时间范围不对")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// 时区偏移取范围末尾（或现在，取较早的）那一刻的：中国没有夏令时，偏移是常数；有夏令时的时区在换令那天的
	// 前后一小时内，行可能记到相邻的一天（已知的小误差，不为它拆多段查询）
	anchor := s.now()
	if r.ToMS != math.MaxInt64 {
		if e := time.UnixMilli(r.ToMS - 1); e.Before(anchor) {
			anchor = e
		}
	}
	_, offSec := anchor.In(s.loc).Zone()
	off := int64(offSec) * 1000

	// 一个只读事务里先看汇总表能不能用、再查，两步看到同一个快照
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ctlproto.UsageReport{}, s.queryErr(ctx, err)
	}
	defer tx.Rollback()
	source, query, args := SourceRaw, usageSQL, []any{off, dayMS, r.FromMS, r.ToMS}
	if days, ok := rollupDays(r, off); ok && rollupUsable(ctx, tx, off) {
		source, query, args = SourceRollup, rollupSQL, []any{days[0], days[1]}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return ctlproto.UsageReport{}, s.queryErr(ctx, err)
	}
	defer rows.Close()
	g := newGrouper()
	for rows.Next() {
		var purpose, stage, model string
		var day int64
		var a acc
		if err := rows.Scan(&purpose, &stage, &model, &day, &a.runs, &a.failed, &a.noUsage, &a.uncached, &a.cacheRead, &a.cacheWrite, &a.output, &a.reasoning, &a.okRuns, &a.okMs); err != nil {
			return ctlproto.UsageReport{}, s.queryErr(ctx, err)
		}
		g.add(purpose, stage, model, day, a)
	}
	if err := rows.Err(); err != nil {
		return ctlproto.UsageReport{}, s.queryErr(ctx, err)
	}
	rep := g.report()
	r.Zone = s.loc.String()
	rep.Range = r
	rep.Source = source
	return rep, nil
}

// rollupDays 把范围换成汇总表的天序号区间 [起, 止)：全部范围取全部；否则起止都得正好落在"本地零点"上（按这次的偏移），
// 汇总表里一天是整天，切不开
func rollupDays(r ctlproto.UsageRange, off int64) ([2]int64, bool) {
	if r.FromMS == 0 && r.ToMS == math.MaxInt64 {
		return [2]int64{-1 << 62, 1 << 62}, true
	}
	if r.ToMS == math.MaxInt64 || (r.FromMS+off)%dayMS != 0 || (r.ToMS+off)%dayMS != 0 {
		return [2]int64{}, false
	}
	return [2]int64{(r.FromMS + off) / dayMS, (r.ToMS + off) / dayMS}, true
}

// rollupUsable：库里有汇总表，而且它记的偏移就是这次用的偏移（不一致说明时区变了、还没重建）
func rollupUsable(ctx context.Context, tx *sql.Tx, off int64) bool {
	var n int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('usage_rollup', 'usage_rollup_meta')`).Scan(&n) != nil || n != 2 {
		return false
	}
	var got int64
	return tx.QueryRowContext(ctx, `SELECT off_ms FROM usage_rollup_meta WHERE id = 1`).Scan(&got) == nil && got == off
}

// SyncRollup 让汇总表和本地时区对上：meta 里的偏移就是现在的就什么都不做；没有或不一样，在一个事务里清空、按原始行
// 重建、写上新的偏移（之后写 model_runs 的触发器按这个偏移累加）。要传可写连接（store 的 Trace），桌面程序启动时调一次。
// 返回有没有重建。偏移取"现在"的：夏令时的时区换令以后下一次启动会重建
func SyncRollup(ctx context.Context, db *sql.DB, loc *time.Location, now time.Time) (rebuilt bool, err error) {
	if loc == nil {
		loc = time.Local
	}
	_, offSec := now.In(loc).Zone()
	off := int64(offSec) * 1000
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var got int64
	switch err := tx.QueryRowContext(ctx, `SELECT off_ms FROM usage_rollup_meta WHERE id = 1`).Scan(&got); {
	case err == nil && got == off:
		return false, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("读汇总表的时区偏移：%w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_rollup`); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_rollup (purpose, stage, model, day, runs, failed, no_usage, uncached, cache_read, cache_write, output, reasoning, ok_runs, ok_ms)
 SELECT COALESCE(purpose, ''), stage, model, (started_at + ?1) / ?2, `+aggCols+` FROM model_runs GROUP BY 1, 2, 3, 4`, off, dayMS); err != nil {
		return false, fmt.Errorf("重建汇总表：%w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_rollup_meta (id, off_ms) VALUES (1, ?1) ON CONFLICT (id) DO UPDATE SET off_ms = excluded.off_ms`, off); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Service) queryErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return fmt.Errorf("读运行记录库：%w", err)
}
