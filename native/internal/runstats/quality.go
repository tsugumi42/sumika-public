package runstats

// 质量标签（规格-运行页 第七节）：模型调用的成败读 model_runs，其余任务读 task_runs，回合读 turns，工具读 steps。
// 全部走只读连接，只碰窄列：不 select sections_json、injected_json、context_json 三列大文本（规格第九节）。
// 失败原因：新行读 error_class；旧行（error_class 为空）对 error 原文现算（llm.ClassifyText），不回写真实库。

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/llm"
	"sumika-native/internal/taskrun"
)

// 分位的样本上限：一个组超过它就均匀抽样，界面标"抽样"（规格第二节第 5 条）
const maxPercentileSamples = 200_000

// task_runs 的结局和 model 行的结局对应
const (
	outOK        = taskrun.OutcomeOK
	outFailed    = taskrun.OutcomeFailed
	outTimeout   = taskrun.OutcomeTimeout
	outCancelled = taskrun.OutcomeCancelled
	outDegraded  = taskrun.OutcomeDegraded
	outSkipped   = taskrun.OutcomeSkipped
)

// percentiles 收一个组的耗时样本，超过上限就蓄水池抽样（确定性的种子，同样的输入同样的结果）
type percentiles struct {
	vals    []int64
	seen    int64
	sampled bool
	rng     uint64
}

func (p *percentiles) add(v int64) {
	p.seen++
	if len(p.vals) < maxPercentileSamples {
		p.vals = append(p.vals, v)
		return
	}
	p.sampled = true
	if p.rng == 0 {
		p.rng = 0x9E3779B97F4A7C15
	}
	p.rng ^= p.rng << 13
	p.rng ^= p.rng >> 7
	p.rng ^= p.rng << 17
	if j := int64(p.rng % uint64(p.seen)); j < maxPercentileSamples {
		p.vals[j] = v
	}
}

// nearestRank 是 nearest-rank 分位：升序后取第 ceil(q×n) 个（从 1 数）。样本为空是 0
func nearestRank(sorted []int64, q float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(n))) - 1
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	return sorted[i]
}

func (p *percentiles) quantiles() (p50, p95 int64) {
	sort.Slice(p.vals, func(i, j int) bool { return p.vals[i] < p.vals[j] })
	return nearestRank(p.vals, 0.5), nearestRank(p.vals, 0.95)
}

// qacc 是一行的原始计数
type qacc struct {
	stats    ctlproto.QualityStats
	lat      percentiles
	reasons  map[string]*reasonAcc
	nonOK    int64
	unkCount int64 // 类别为 unknown 的失败、超时、退化（被停下和没做的不算），给 unknown 占比用
}

type reasonAcc struct {
	count int64
	last  int64
}

func newQacc() *qacc { return &qacc{reasons: map[string]*reasonAcc{}} }

// add 记一条结果：outcome 是 ok/failed/timeout/cancelled/degraded/skipped；ms<0 表示没有耗时；class 只对非 ok 有意义
func (q *qacc) add(outcome, class string, ms, at int64) {
	q.stats.Runs++
	switch outcome {
	case outOK:
		q.stats.OK++
		if ms >= 0 {
			q.lat.add(ms)
		}
		return
	case outTimeout:
		q.stats.Timeout++
	case outCancelled:
		q.stats.Cancelled++
	case outDegraded:
		q.stats.Degraded++
	case outSkipped:
		q.stats.Skipped++
	default:
		q.stats.Failed++
	}
	if class == "" {
		class = llm.ClassUnknown
	}
	r := q.reasons[class]
	if r == nil {
		r = &reasonAcc{}
		q.reasons[class] = r
	}
	r.count++
	if at > r.last {
		r.last = at
	}
	q.nonOK++
	if class == llm.ClassUnknown && outcome != outCancelled && outcome != outSkipped {
		q.unkCount++
	}
}

// finish 把计数变成对外的结构
func (q *qacc) finish() (ctlproto.QualityStats, []ctlproto.QualityReason) {
	s := q.stats
	s.P50Ms, s.P95Ms = q.lat.quantiles()
	s.Samples, s.Sampled = int64(len(q.lat.vals)), q.lat.sampled
	if den := s.OK + s.Failed + s.Timeout + s.Degraded; den > 0 {
		s.FailRate = float64(s.Failed+s.Timeout) / float64(den)
	}
	var rs []ctlproto.QualityReason
	for class, r := range q.reasons {
		share := 0.0
		if q.nonOK > 0 {
			share = float64(r.count) / float64(q.nonOK)
		}
		rs = append(rs, ctlproto.QualityReason{Class: class, Count: r.count, Share: share, LastMS: r.last})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Count != rs[j].Count {
			return rs[i].Count > rs[j].Count
		}
		return rs[i].Class < rs[j].Class
	})
	return s, rs
}

// placeOf 把库里的会话串变成"本地私聊 / 本地群聊 / QQ 私聊 / QQ 群"，认不出写"未知场合"，不猜、不带号码。
// 本地会话（console:<角色>…）没有对方号，ParseConversation 不认；本地群聊现在还没有入口，带 :group: 的 console 会话算本地群聊
func placeOf(conv string) string {
	if conv == "" {
		return ctlproto.ScenePlaceUnknown
	}
	if c, err := types.ParseConversation(conv); err == nil {
		switch {
		case c.Channel == "qq" && c.Scene == types.ScenePrivate:
			return ctlproto.ScenePlaceQQPrivate
		case c.Channel == "qq" && c.Scene == types.SceneGroup:
			return ctlproto.ScenePlaceQQGroup
		}
		return ctlproto.ScenePlaceUnknown // 别的平台还没有自己的说法，不硬套 QQ
	}
	if audience.IsWhisper(conv) { // 先于下面的 console: 判断：悄悄话会话的串也以 console: 开头，不能算本地私聊
		return ctlproto.ScenePlaceWhisper
	}
	if strings.HasPrefix(conv, "console:") {
		if strings.Contains(conv, ":group:") {
			return ctlproto.ScenePlaceLocalGroup
		}
		return ctlproto.ScenePlaceLocalPrivate
	}
	return ctlproto.ScenePlaceUnknown
}

// modelOutcome 把一次失败的模型请求按类别分成结局：超时、被停下各自单列，其余是失败
func modelOutcome(class string) string {
	switch class {
	case llm.ClassTimeout:
		return outTimeout
	case llm.ClassCancelled:
		return outCancelled
	}
	return outFailed
}

var taskLabels = func() map[string]string {
	m := map[string]string{}
	for _, t := range Tasks {
		m[t.Key] = t.Label
	}
	m[ctlproto.TaskUnknown] = "未知"
	return m
}()

var moduleMeta = func() map[string]ctlproto.QualityModule {
	m := map[string]ctlproto.QualityModule{}
	for _, mod := range ctlproto.QualityModules {
		m[mod.Key] = mod
	}
	return m
}()

// slowKey 是最慢的环节的分组键
type slowKey struct{ kind, key, model string }

type slowAcc struct {
	label string
	p     percentiles
}

// Quality 查一段时间的质量：总表、最慢的环节、unknown 占比、最近的失败、回合层、工具表。
// 范围是 [FromMS, ToMS)；任务过滤（Task）和"最近的失败"条数（Failures）由 StatsArgs 给，这里按 r 和 a 里的值取
func (s *Service) Quality(ctx context.Context, r ctlproto.UsageRange, a ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	if r.FromMS < 0 || r.ToMS <= r.FromMS {
		return ctlproto.QualityReport{}, errors.New("时间范围不对")
	}
	// 界面直接用服务时不经过服务端：这里只认 Task、Failures 两个质量自己的参数（范围由调用方算好交进来）
	if a.Task != "" && !ctlproto.ValidQualityTask(a.Task) {
		return ctlproto.QualityReport{}, errors.New("不认识的任务类别或模块")
	}
	if a.Failures < 0 || a.Failures > ctlproto.StatsMaxFailures {
		return ctlproto.QualityReport{}, errors.New("失败条数超出范围")
	}
	limit := a.Failures
	if limit == 0 {
		limit = ctlproto.StatsDefaultFailures
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ctlproto.QualityReport{}, s.queryErr(ctx, err)
	}
	defer tx.Rollback()

	wanted := func(kind, key string) bool { return a.Task == "" || a.Task == key }
	rows := map[string]*qacc{}     // "model:<键>" 或 "module:<键>"
	unknownRaw := []string{}       // unknown 任务的原文（最多留几个）
	slow := map[slowKey]*slowAcc{} // 最慢的环节
	total := newQacc()
	var recent, modRecent []ctlproto.QualityFailure
	var recentOf int64
	rowOf := func(kind, key string) *qacc {
		k := kind + ":" + key
		q := rows[k]
		if q == nil {
			q = newQacc()
			rows[k] = q
		}
		return q
	}
	addSlow := func(kind, key, label, model string, ms int64) {
		k := slowKey{kind, key, model}
		x := slow[k]
		if x == nil {
			x = &slowAcc{label: label}
			slow[k] = x
		}
		x.p.add(ms)
	}

	// 1. 模型调用成功的行：只要窄列
	okRows, err := tx.QueryContext(ctx, `SELECT COALESCE(purpose, ''), stage, model, total_ms FROM model_runs
 WHERE started_at >= ?1 AND started_at < ?2 AND error IS NULL`, r.FromMS, r.ToMS)
	if err != nil {
		return ctlproto.QualityReport{}, s.queryErr(ctx, err)
	}
	for okRows.Next() {
		var purpose, stage, model string
		var ms sql.NullInt64
		if err := okRows.Scan(&purpose, &stage, &model, &ms); err != nil {
			okRows.Close()
			return ctlproto.QualityReport{}, s.queryErr(ctx, err)
		}
		task := TaskOf(purpose, stage)
		if !wanted("model", task) {
			continue
		}
		d := int64(-1)
		if ms.Valid {
			d = ms.Int64
		}
		rowOf("model", task).add(outOK, "", d, 0)
		total.add(outOK, "", d, 0)
		if d >= 0 {
			addSlow("model", task, taskLabels[task], model, d)
		}
	}
	if err := okRows.Err(); err != nil {
		okRows.Close()
		return ctlproto.QualityReport{}, s.queryErr(ctx, err)
	}
	okRows.Close()

	// 2. 模型调用失败的行：走部分索引 ix_runs_failed（WHERE 里字面写 error IS NOT NULL）。
	// error 只取前 2000 字节，够分类和摘要；三列大文本不碰
	bad, err := tx.QueryContext(ctx, `SELECT id, COALESCE(purpose, ''), stage, model, started_at, total_ms, COALESCE(error_class, ''),
 COALESCE(substr(error, 1, 2000), ''), conversation, COALESCE(turn_id, ''), COALESCE(route_provider, ''), COALESCE(route_model, '')
 FROM model_runs WHERE started_at >= ?1 AND started_at < ?2 AND error IS NOT NULL ORDER BY started_at DESC, id`, r.FromMS, r.ToMS)
	if err != nil {
		return ctlproto.QualityReport{}, s.queryErr(ctx, err)
	}
	for bad.Next() {
		var id, purpose, stage, model, class, text, conv, turnID, rp, rm string
		var at int64
		var ms sql.NullInt64
		if err := bad.Scan(&id, &purpose, &stage, &model, &at, &ms, &class, &text, &conv, &turnID, &rp, &rm); err != nil {
			bad.Close()
			return ctlproto.QualityReport{}, s.queryErr(ctx, err)
		}
		task := TaskOf(purpose, stage)
		if !wanted("model", task) {
			continue
		}
		if class == "" {
			class = llm.ClassifyText(text) // 旧行：读时现算，不回写
		}
		if class == "" {
			class = llm.ClassUnknown
		}
		outcome := modelOutcome(class)
		d := int64(-1)
		if ms.Valid {
			d = ms.Int64
		}
		q := rowOf("model", task)
		q.add(outcome, class, d, at)
		total.add(outcome, class, d, at)
		if stage == "reply-retry" {
			q.stats.Retries++
			total.stats.Retries++
		}
		if task == ctlproto.TaskUnknown && len(unknownRaw) < 3 {
			unknownRaw = append(unknownRaw, rawOf(purpose, stage))
		}
		if outcome != outCancelled { // 被停下的不进"最近的失败"：用户自己按的停止，不是出了问题
			recentOf++
			if len(recent) < limit {
				recent = append(recent, ctlproto.QualityFailure{
					ID: id, AtMS: at, Kind: "model", Key: task, Label: taskLabels[task], Model: ctlproto.CleanRaw(model), Outcome: outcome, Class: class,
					Place: placeOf(conv), Ms: d, Summary: s.summary(class, text), TurnID: turnID, RouteProvider: ctlproto.CleanRaw(rp), RouteModel: ctlproto.CleanRaw(rm)})
			}
		}
	}
	if err := bad.Err(); err != nil {
		bad.Close()
		return ctlproto.QualityReport{}, s.queryErr(ctx, err)
	}
	bad.Close()

	// 3. 模块（task_runs）。库里还没有这张表（迁移 0015 之前的库）就当没有记录，不报错
	if hasTable(ctx, tx, "task_runs") {
		tr, err := tx.QueryContext(ctx, `SELECT at, module, outcome, class, ms, attempts, COALESCE(detail, '') FROM task_runs
 WHERE at >= ?1 AND at < ?2 ORDER BY at DESC, id DESC`, r.FromMS, r.ToMS)
		if err != nil {
			return ctlproto.QualityReport{}, s.queryErr(ctx, err)
		}
		for tr.Next() {
			var at int64
			var module, outcome, class, detail string
			var ms sql.NullInt64
			var attempts int
			if err := tr.Scan(&at, &module, &outcome, &class, &ms, &attempts, &detail); err != nil {
				tr.Close()
				return ctlproto.QualityReport{}, s.queryErr(ctx, err)
			}
			if !wanted("module", module) {
				continue
			}
			d := int64(-1)
			if ms.Valid {
				d = ms.Int64
			}
			if outcome == "" || !validOutcome(outcome) {
				outcome = outFailed // 认不出的结局当失败显示，类别写 unknown，不悄悄丢
				class = llm.ClassUnknown
			}
			q := rowOf("module", module)
			q.add(outcome, class, d, at)
			total.add(outcome, class, d, at)
			if attempts > 1 {
				q.stats.Retries += int64(attempts - 1)
				total.stats.Retries += int64(attempts - 1)
			}
			if outcome == outOK && d >= 0 {
				label := module
				if m, ok := moduleMeta[module]; ok {
					label = m.Label
				}
				addSlow("module", module, label, "", d)
			}
			if outcome != outOK && outcome != outCancelled {
				recentOf++
				if len(modRecent) < limit { // tr 已按时间倒序，前 limit 条就是模块里最近的
					label := module
					if m, ok := moduleMeta[module]; ok {
						label = m.Label
					}
					modRecent = append(modRecent, ctlproto.QualityFailure{AtMS: at, Kind: "module", Key: ctlproto.CleanRaw(module), Label: ctlproto.CleanRaw(label), Outcome: outcome,
						Class: ctlproto.CleanRaw(class), Ms: d, Attempts: attempts, Summary: taskrun.Detail(class, detail)})
				}
			}
		}
		if err := tr.Err(); err != nil {
			tr.Close()
			return ctlproto.QualityReport{}, s.queryErr(ctx, err)
		}
		tr.Close()
	}

	// 回合和工具：只在没有按任务过滤时给（它们不属于任何一类任务）
	var turns ctlproto.QualityTurns
	var tools []ctlproto.QualityTool
	if a.Task == "" {
		var err error
		if turns, err = s.qualityTurns(ctx, tx, r); err != nil {
			return ctlproto.QualityReport{}, err
		}
		var toolSlow map[string]*percentiles
		if tools, toolSlow, err = s.qualityTools(ctx, tx, r); err != nil {
			return ctlproto.QualityReport{}, err
		}
		for name, p := range toolSlow {
			k := slowKey{"tool", name, ""}
			slow[k] = &slowAcc{label: name, p: *p}
		}
	}

	rep := ctlproto.QualityReport{Range: r, Task: a.Task, Turns: turns, Tools: tools, RecentOf: recentOf}
	rep.Range.Zone = s.loc.String()
	rep.Totals, _ = total.finish()

	// 总表：模型调用的任务类别（含预留行）→ 模块（含预留行）。没接入且没有记录的写"未接入"，不写 0 次失败
	for _, meta := range Tasks {
		if !wanted("model", meta.Key) {
			continue
		}
		row := ctlproto.QualityRow{Kind: "model", Key: meta.Key, Label: meta.Label, Wired: meta.Wired}
		if q := rows["model:"+meta.Key]; q != nil {
			row.QualityStats, row.Reasons = q.finish()
		}
		rep.Rows = append(rep.Rows, row)
	}
	if q := rows["model:"+ctlproto.TaskUnknown]; q != nil && wanted("model", ctlproto.TaskUnknown) {
		row := ctlproto.QualityRow{Kind: "model", Key: ctlproto.TaskUnknown, Label: "未知", Raw: strings.Join(unknownRaw, "、"), Wired: true}
		row.QualityStats, row.Reasons = q.finish()
		rep.Rows = append(rep.Rows, row)
	}
	for _, m := range ctlproto.QualityModules {
		if !wanted("module", m.Key) {
			continue
		}
		row := ctlproto.QualityRow{Kind: "module", Key: m.Key, Label: m.Label, Wired: m.Wired}
		if q := rows["module:"+m.Key]; q != nil {
			row.QualityStats, row.Reasons = q.finish()
		}
		rep.Rows = append(rep.Rows, row)
	}
	// task_runs 里有、模块表里没有的模块：单列，不丢
	var extra []string
	for k := range rows {
		if mod, ok := strings.CutPrefix(k, "module:"); ok {
			if _, known := moduleMeta[mod]; !known {
				extra = append(extra, mod)
			}
		}
	}
	sort.Strings(extra)
	for _, mod := range extra {
		row := ctlproto.QualityRow{Kind: "module", Key: ctlproto.CleanRaw(mod), Label: ctlproto.CleanRaw(mod), Wired: true}
		row.QualityStats, row.Reasons = rows["module:"+mod].finish()
		rep.Rows = append(rep.Rows, row)
	}

	// unknown 占比：分母是所有失败、超时、退化的记录（被停下和没做的不算），分子是类别为 unknown 的
	var unk, den int64
	for _, q := range rows {
		den += q.stats.Failed + q.stats.Timeout + q.stats.Degraded
		unk += q.unkCount
	}
	rep.Unknown = ctlproto.QualityUnknown{Count: unk, Total: den}
	if den > 0 {
		rep.Unknown.Share = float64(unk) / float64(den)
		rep.Unknown.Alarm = unknownAlarm(unk, den)
	}

	// 最慢的环节：按 p95 降序前 N 个；样本少于 QualityThinSamples 的照列但标 Thin
	var slows []ctlproto.QualitySlow
	for k, x := range slow {
		p50, p95 := x.p.quantiles()
		slows = append(slows, ctlproto.QualitySlow{Kind: k.kind, Key: k.key, Label: ctlproto.CleanRaw(x.label), Model: ctlproto.CleanRaw(k.model), P50Ms: p50, P95Ms: p95,
			Samples: int64(len(x.p.vals)), Thin: len(x.p.vals) < ctlproto.QualityThinSamples, Sampled: x.p.sampled})
	}
	sort.Slice(slows, func(i, j int) bool {
		a, b := slows[i], slows[j]
		if a.P95Ms != b.P95Ms {
			return a.P95Ms > b.P95Ms
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Model < b.Model
	})
	if len(slows) > ctlproto.QualitySlowRows {
		slows = slows[:ctlproto.QualitySlowRows]
	}
	rep.Slowest = slows

	// 最近的失败：模型行和模块行合并，最近的在前，取前 limit 条
	recent = append(recent, modRecent...)
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].AtMS > recent[j].AtMS })
	if len(recent) > limit {
		recent = recent[:limit]
	}
	rep.Recent = recent
	return rep, nil
}

// unknownAlarm：占比严格大于一成才标红，用整数比较免得 0.1 的浮点误差（10 条里 1 条正好一成，不标）
func unknownAlarm(unk, den int64) bool { return unk*10 > den }

func validOutcome(o string) bool {
	switch o {
	case outOK, outFailed, outTimeout, outCancelled, outDegraded, outSkipped:
		return true
	}
	return false
}

func hasTable(ctx context.Context, tx *sql.Tx, name string) bool {
	var n int
	return tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?1`, name).Scan(&n) == nil && n == 1
}

// summary 是失败摘要：先过装配进来的脱敏（RedactLog 那一路），再过 taskrun.Detail 兜底（遮 URL、号码、令牌，
// 模型输出类的只留前缀，截到 500 字节）。原文只在这里短暂存在，不进报告
func (s *Service) summary(class, text string) string {
	if s.redact != nil {
		text = s.redact(text)
	}
	return taskrun.Detail(class, text)
}

func (s *Service) qualityTurns(ctx context.Context, tx *sql.Tx, r ctlproto.UsageRange) (ctlproto.QualityTurns, error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(outcome, ''), started_at, ended_at FROM turns WHERE started_at >= ?1 AND started_at < ?2`, r.FromMS, r.ToMS)
	if err != nil {
		return ctlproto.QualityTurns{}, s.queryErr(ctx, err)
	}
	defer rows.Close()
	var t ctlproto.QualityTurns
	counts := map[string]int64{}
	var lat percentiles
	for rows.Next() {
		var outcome string
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&outcome, &started, &ended); err != nil {
			return ctlproto.QualityTurns{}, s.queryErr(ctx, err)
		}
		t.Total++
		if outcome == "" {
			outcome = "unfinished" // 没有结局：还在跑或没收尾，单列
		}
		counts[outcome]++
		if outcome == "done" && ended.Valid && ended.Int64 >= started {
			lat.add(ended.Int64 - started)
		}
	}
	if err := rows.Err(); err != nil {
		return ctlproto.QualityTurns{}, s.queryErr(ctx, err)
	}
	for o, n := range counts {
		t.Outcomes = append(t.Outcomes, ctlproto.QualityOutcome{Outcome: ctlproto.CleanRaw(o), Count: n})
	}
	sort.Slice(t.Outcomes, func(i, j int) bool {
		if t.Outcomes[i].Count != t.Outcomes[j].Count {
			return t.Outcomes[i].Count > t.Outcomes[j].Count
		}
		return t.Outcomes[i].Outcome < t.Outcomes[j].Outcome
	})
	t.P50Ms, t.P95Ms = lat.quantiles()
	t.Samples = int64(len(lat.vals))
	return t, nil
}

func (s *Service) qualityTools(ctx context.Context, tx *sql.Tx, r ctlproto.UsageRange) ([]ctlproto.QualityTool, map[string]*percentiles, error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(name, ''), status, is_error, started_at, ended_at FROM steps
 WHERE kind = 'tool' AND started_at >= ?1 AND started_at < ?2`, r.FromMS, r.ToMS)
	if err != nil {
		return nil, nil, s.queryErr(ctx, err)
	}
	defer rows.Close()
	type acc struct {
		t   ctlproto.QualityTool
		lat percentiles
	}
	by := map[string]*acc{}
	for rows.Next() {
		var name, status string
		var isErr int
		var started, ended sql.NullInt64
		if err := rows.Scan(&name, &status, &isErr, &started, &ended); err != nil {
			return nil, nil, s.queryErr(ctx, err)
		}
		x := by[name]
		if x == nil {
			x = &acc{t: ctlproto.QualityTool{Name: ctlproto.CleanRaw(name)}}
			by[name] = x
		}
		x.t.Runs++
		switch {
		case isErr != 0 && status == "error":
			x.t.Failed++
		case status == "done" && isErr == 0:
			x.t.Done++
			if started.Valid && ended.Valid && ended.Int64 >= started.Int64 {
				x.lat.add(ended.Int64 - started.Int64)
			}
		default:
			x.t.Incomplete++ // 过期、被拒、没结束：没有走到成功或出错
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, s.queryErr(ctx, err)
	}
	var out []ctlproto.QualityTool
	slow := map[string]*percentiles{}
	for name, x := range by {
		p50, _ := x.lat.quantiles()
		x.t.MedianMs, x.t.Samples = p50, int64(len(x.lat.vals))
		out = append(out, x.t)
		if len(x.lat.vals) > 0 {
			p := x.lat
			slow[name] = &p
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Name < out[j].Name
	})
	return out, slow, nil
}
