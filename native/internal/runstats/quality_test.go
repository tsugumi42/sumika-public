package runstats

// 规格-运行页 第 4 步验收 ③④⑤⑥ 和质量查询本身：旧行读时现算不回写、分位按已知序列、unknown 占比标红的边界、
// 详情和失败摘要的脱敏。测试数据都是编的，库在 t.TempDir()。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/llm"
)

// qr 是一行要灌进 model_runs 的数据（质量测试用，带会话、类别、回合）
type qr struct {
	purpose, stage, model string
	at                    int64
	ms                    *int64
	err, class            string // err 空 = 成功；class 空 = NULL（旧行）
	conv, turn            string
}

func insertQ(t *testing.T, db *sql.DB, rows ...qr) {
	t.Helper()
	for _, r := range rows {
		var purpose, e, class, turn, msv any
		if r.purpose != "" {
			purpose = r.purpose
		}
		if r.err != "" {
			e = r.err
		}
		if r.class != "" {
			class = r.class
		}
		if r.turn != "" {
			turn = r.turn
		}
		if r.ms != nil {
			msv = *r.ms
		}
		conv := r.conv
		if conv == "(空)" {
			conv = ""
		} else if conv == "" {
			conv = "console:c"
		}
		if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, sections_json, injected_json, error, error_class, purpose, turn_id)
 VALUES (?, 'c', ?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?, ?)`, fmt.Sprintf("q%09d", nextID.Add(1)), conv, r.stage, r.model, r.at, msv, e, class, purpose, turn); err != nil {
			t.Fatal(err)
		}
	}
}

func insertTask(t *testing.T, db *sql.DB, at int64, module, outcome, class string, ms *int64, attempts int, detail string) {
	t.Helper()
	var m, d any
	if ms != nil {
		m = *ms
	}
	if detail != "" {
		d = detail
	}
	if _, err := db.Exec(`INSERT INTO task_runs (at, module, outcome, class, ms, attempts, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`, at, module, outcome, class, m, attempts, d); err != nil {
		t.Fatal(err)
	}
}

var (
	day7 = ms(2026, 11, 7, 10, 0, 0, 0) // 范围内
	day8 = ms(2026, 11, 8, 9, 0, 0, 0)
)

func quality(t *testing.T, s *Service, a ctlproto.StatsArgs) ctlproto.QualityReport {
	t.Helper()
	r, err := s.RangeOf(a)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Quality(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func row(rep ctlproto.QualityReport, key string) ctlproto.QualityRow {
	for _, r := range rep.Rows {
		if r.Key == key {
			return r
		}
	}
	return ctlproto.QualityRow{Key: "(没有这一行)"}
}

func reasons(r ctlproto.QualityRow) map[string]int64 {
	m := map[string]int64{}
	for _, x := range r.Reasons {
		m[x.Class] = x.Count
	}
	return m
}

// ④ 已知序列：1 到 100 毫秒各一次成功，nearest-rank 的 p50 是 50、p95 是 95；失败、超时再慢也不进分位
func TestQualityPercentilesKnownSequence(t *testing.T) {
	db, file := tempTrace(t)
	for i := int64(1); i <= 100; i++ {
		insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7 + i, ms: i64(i)})
	}
	insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7, ms: i64(99999), err: "boom", class: "server_error"},
		qr{purpose: "foreground", stage: "reply", model: "m", at: day7, ms: i64(88888), err: "context deadline exceeded", class: "timeout"})
	rep := quality(t, openService(t, file), ctlproto.StatsArgs{})
	fg := row(rep, ctlproto.TaskForeground)
	if fg.Runs != 102 || fg.OK != 100 || fg.Failed != 1 || fg.Timeout != 1 {
		t.Fatalf("次数不对：%+v", fg.QualityStats)
	}
	if fg.P50Ms != 50 || fg.P95Ms != 95 || fg.Samples != 100 {
		t.Fatalf("分位不对（失败不该混进来）：p50=%d p95=%d 样本=%d", fg.P50Ms, fg.P95Ms, fg.Samples)
	}
	// 失败率 = (失败 + 超时) / (成功 + 失败 + 超时 + 退化) = 2/102
	if want := 2.0 / 102.0; fg.FailRate < want-1e-9 || fg.FailRate > want+1e-9 {
		t.Fatalf("失败率 %v，应是 %v", fg.FailRate, want)
	}
}

func TestNearestRank(t *testing.T) {
	cases := []struct {
		vals   []int64
		q      float64
		expect int64
	}{
		{nil, 0.5, 0}, {[]int64{7}, 0.5, 7}, {[]int64{7}, 0.95, 7},
		{[]int64{10, 20, 30, 40}, 0.5, 20}, {[]int64{10, 20, 30, 40}, 0.95, 40},
		{[]int64{10, 20, 30, 40, 50}, 0.5, 30}, {[]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.95, 10},
	}
	for _, c := range cases {
		if got := nearestRank(c.vals, c.q); got != c.expect {
			t.Errorf("%v q=%v：%d，应是 %d", c.vals, c.q, got, c.expect)
		}
	}
	// 抽样：超过上限不会无限涨，标 sampled，分位仍在合理范围
	var p percentiles
	for i := int64(0); i < maxPercentileSamples+5000; i++ {
		p.add(i % 1000)
	}
	if len(p.vals) != maxPercentileSamples || !p.sampled {
		t.Fatalf("抽样：%d 个，sampled=%v", len(p.vals), p.sampled)
	}
	if p50, _ := p.quantiles(); p50 < 400 || p50 > 600 {
		t.Fatalf("抽样后的 p50 偏离太多：%d", p50)
	}
}

// ③ 旧行（error_class 为 NULL）读时现算，而且不回写真实库；有 error_class 的行以它为准，不重算
func TestQualityOldRowsClassifiedAtReadNotWritten(t *testing.T) {
	db, file := tempTrace(t)
	insertQ(t, db,
		qr{stage: "reply", model: "m", at: day7, err: "Request aborted"},
		qr{stage: "reply", model: "m", at: day7, err: "Request was aborted"},
		qr{stage: "reply", model: "m", at: day7, err: "Open Design: ECONNRESET: The socket connection was closed unexpectedly."},
		qr{stage: "guard", model: "m", at: day7, err: "context deadline exceeded"},
		qr{stage: "review-expression", model: "m", at: day7, err: "输出不是合法 JSON：[{...}]"},
		qr{stage: "review-expression", model: "m", at: day7, err: "思考打转：「ok」挨着出现 4 次"},
		qr{stage: "reply", model: "m", at: day7, err: "something odd", class: "network"}, // 新行：以库里的类别为准
	)
	rep := quality(t, openService(t, file), ctlproto.StatsArgs{})
	fg := reasons(row(rep, ctlproto.TaskForeground))
	if fg[llm.ClassCancelled] != 2 || fg[llm.ClassNetwork] != 2 || len(fg) != 2 {
		t.Fatalf("前台：两种 aborted 要并成 cancelled，裸 ECONNRESET 和新行的 network 合在一起：%v", fg)
	}
	if g := reasons(row(rep, ctlproto.TaskGuard)); g[llm.ClassTimeout] != 1 {
		t.Fatalf("守卫超时：%v", g)
	}
	mem := reasons(row(rep, ctlproto.TaskMemory))
	if mem[llm.ClassParseFailed] != 1 || mem[llm.ClassLoopDetected] != 1 {
		t.Fatalf("记忆整理：%v", mem)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM model_runs WHERE error IS NOT NULL AND error_class IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("读的时候不许回写：库里有类别的行应只剩原来那 1 行：%d %v", n, err)
	}
	// 只读连接：Service 手里的连接写不进去（兜底）
	if _, err := openService(t, file).db.Exec(`UPDATE model_runs SET error_class = 'x'`); err == nil {
		t.Fatal("统计服务的连接应是只读的")
	}
}

// ⑤ unknown 占比：严格大于一成才标红；被停下的 unknown 不进分子也不进分母
func TestQualityUnknownAlarmBoundary(t *testing.T) {
	for _, c := range []struct {
		unk, other int
		alarm      bool
	}{{1, 9, false}, {2, 18, false}, {3, 17, true}, {0, 20, false}, {5, 0, true}} {
		db, file := tempTrace(t)
		for i := 0; i < c.unk; i++ {
			insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7, err: "没见过的错误", class: "unknown"})
		}
		for i := 0; i < c.other; i++ {
			insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7, err: "x", class: "server_error"})
		}
		insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7, err: "停了", class: "unknown"}) // 干扰：下面改成被停下
		db.Exec(`UPDATE model_runs SET error_class = 'cancelled' WHERE error = '停了'`)
		rep := quality(t, openService(t, file), ctlproto.StatsArgs{})
		if rep.Unknown.Alarm != c.alarm || rep.Unknown.Count != int64(c.unk) || rep.Unknown.Total != int64(c.unk+c.other) {
			t.Errorf("unknown %d / 其他 %d：%+v，标红应为 %v", c.unk, c.other, rep.Unknown, c.alarm)
		}
	}
	if !unknownAlarm(11, 100) || unknownAlarm(10, 100) || unknownAlarm(0, 0) {
		t.Error("unknownAlarm 的整数边界不对")
	}
}

// ⑥ 脱敏：失败摘要、task_runs 的详情里塞号码、URL、令牌、会话名、模型输出，报告里一个都出不来
func TestQualityRedactsSummaries(t *testing.T) {
	db, file := tempTrace(t)
	num := strings.Repeat("7", 11)
	key := "sk-" + strings.Repeat("z", 20)
	dirty := "发给 " + num + " 失败 https://example.org/path?token=abc " + key + " 会话 qq:group:123456789"
	modelOut := "只有模型才会说的话：情境是蹭好处"
	insertQ(t, db,
		qr{purpose: "foreground", stage: "reply", model: "m", at: day7, err: dirty, class: "server_error", conv: "qq:group:123456789"},
		qr{stage: "review-expression", model: "m", at: day7 + 1, err: "输出不是合法 JSON：" + modelOut, class: "parse_failed", conv: "qq:private:10001:20002"},
	)
	insertTask(t, db, day7+2, "send", "failed", "retry_exhausted", i64(10), 3, dirty)
	insertTask(t, db, day7+3, "media", "degraded", "parse_failed", nil, 1, "输出不是合法 JSON："+modelOut) // 就算写入侧漏了，读出时还会兜底
	s, err := OpenFile(file, Options{Loc: zone, Now: func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, zone) }, Redact: func(x string) string { return strings.ReplaceAll(x, "例外词", "***") }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rep := quality(t, s, ctlproto.StatsArgs{})
	b, _ := json.Marshal(rep)
	all := string(b)
	for _, bad := range []string{num, "example.org", key, "123456789", "10001", "20002", "qq:group", "只有模型才会说的话", "情境是蹭好处"} {
		if strings.Contains(all, bad) {
			t.Errorf("报告里不该出现 %q：%s", bad, all)
		}
	}
	if len(rep.Recent) != 4 {
		t.Fatalf("应有四条失败：%d", len(rep.Recent))
	}
	for _, f := range rep.Recent {
		if len(f.Summary) > 500 {
			t.Errorf("摘要超过 500 字节：%d", len(f.Summary))
		}
		if f.Class == "parse_failed" && !strings.Contains(f.Summary, "原文已略") {
			t.Errorf("模型输出类的摘要要标原文已略：%q", f.Summary)
		}
	}
	// 会话只显示成场合
	places := map[string]bool{}
	for _, f := range rep.Recent {
		places[f.Place] = true
	}
	if !places[ctlproto.ScenePlaceQQGroup] || !places[ctlproto.ScenePlaceQQPrivate] {
		t.Errorf("场合应是 QQ 群和 QQ 私聊：%v", places)
	}
	// 注入的脱敏函数确实先过一遍
	insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7 + 9, err: "含 例外词 的错误", class: "server_error"})
	rep = quality(t, s, ctlproto.StatsArgs{})
	if strings.Contains(fmt.Sprint(rep.Recent[0].Summary), "例外词") {
		t.Errorf("装配传入的脱敏没生效：%q", rep.Recent[0].Summary)
	}
}

// 模块（task_runs）：成败、耗时、重试、原因；预留模块没有记录时是"未接入"；--task 只看一类；认不出的模块单列
func TestQualityModules(t *testing.T) {
	db, file := tempTrace(t)
	insertTask(t, db, day7, "send", "ok", "", i64(100), 1, "")
	insertTask(t, db, day7+1, "send", "ok", "", i64(300), 2, "")
	insertTask(t, db, day7+2, "send", "failed", "unsupported", i64(5), 1, "")
	insertTask(t, db, day7+3, "send", "skipped", "unsupported", nil, 1, "")
	insertTask(t, db, day7+4, "link", "failed", "queue_full", nil, 1, "")
	insertTask(t, db, day7+5, "newmod", "failed", "whatever", nil, 1, "")
	insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7, ms: i64(5)})
	s := openService(t, file)
	rep := quality(t, s, ctlproto.StatsArgs{})
	send := row(rep, "send")
	if send.Runs != 4 || send.OK != 2 || send.Failed != 1 || send.Skipped != 1 || send.Retries != 1 || send.P95Ms != 300 || send.P50Ms != 100 {
		t.Fatalf("发送：%+v", send.QualityStats)
	}
	if reasons(send)["unsupported"] != 2 {
		t.Fatalf("发送的原因：%v", send.Reasons)
	}
	if bf := row(rep, "backfill"); bf.Wired || bf.Runs != 0 {
		t.Fatalf("补录没接入、没记录：%+v", bf)
	}
	if nm := row(rep, "newmod"); nm.Runs != 1 || nm.Label != "newmod" {
		t.Fatalf("认不出的模块要单列：%+v", nm)
	}
	// 没有失败的预留任务（识图等）也在表里，Wired 为假
	if v := row(rep, ctlproto.TaskVision); v.Wired || v.Runs != 0 {
		t.Fatalf("识图：%+v", v)
	}
	only := quality(t, s, ctlproto.StatsArgs{Task: "send"})
	if len(only.Rows) != 1 || only.Rows[0].Key != "send" || only.Totals.Runs != 4 || len(only.Tools) != 0 || only.Turns.Total != 0 {
		t.Fatalf("--task send：%+v", only.Rows)
	}
	if _, err := s.Quality(context.Background(), only.Range, ctlproto.StatsArgs{Task: "没有这个"}); err == nil {
		t.Fatal("不认识的 task 要拒绝")
	}
	// 没有 task_runs 表的旧库不报错
	if _, err := db.Exec(`DROP TABLE task_runs`); err != nil {
		t.Fatal(err)
	}
	rep = quality(t, s, ctlproto.StatsArgs{})
	if row(rep, "send").Runs != 0 || row(rep, ctlproto.TaskForeground).Runs != 1 {
		t.Fatalf("旧库：%+v", rep.Rows)
	}
}

// 最近的失败：最近的在前、被停下的不列、条数限制、模型和模块合并排序、RecentOf 是总数；场合只写四种说法
func TestQualityRecentFailures(t *testing.T) {
	db, file := tempTrace(t)
	convs := []string{"qq:group:111", "qq:private:5:6", "console:c", "console:c:group:g", "(空)", "telegram:private:1:2", "乱七八糟"}
	for i, c := range convs {
		insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7 + int64(i)*10, err: "boom", class: "server_error", conv: c, turn: fmt.Sprintf("t%d", i)})
	}
	insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7 + 500, err: "Request aborted", class: "cancelled"})
	insertTask(t, db, day7+35, "send", "failed", "retry_exhausted", i64(1), 3, "")
	s := openService(t, file)
	rep := quality(t, s, ctlproto.StatsArgs{Failures: 5})
	if rep.RecentOf != 8 || len(rep.Recent) != 5 {
		t.Fatalf("总数应是 8（不含被停下的）、列 5 条：of=%d len=%d", rep.RecentOf, len(rep.Recent))
	}
	for i := 1; i < len(rep.Recent); i++ {
		if rep.Recent[i-1].AtMS < rep.Recent[i].AtMS {
			t.Fatalf("最近的要在前：%+v", rep.Recent)
		}
	}
	if rep.Recent[0].Place != ctlproto.ScenePlaceUnknown || rep.Recent[0].Class != "server_error" { // 最近一条是"乱七八糟"的会话
		t.Fatalf("最近一条：%+v", rep.Recent[0])
	}
	full := quality(t, s, ctlproto.StatsArgs{Failures: 50})
	want := map[string]string{"t0": ctlproto.ScenePlaceQQGroup, "t1": ctlproto.ScenePlaceQQPrivate, "t2": ctlproto.ScenePlaceLocalPrivate, "t3": ctlproto.ScenePlaceLocalGroup,
		"t4": ctlproto.ScenePlaceUnknown, "t5": ctlproto.ScenePlaceUnknown, "t6": ctlproto.ScenePlaceUnknown}
	got := 0
	for _, f := range full.Recent {
		if w, ok := want[f.TurnID]; ok {
			got++
			if f.Place != w {
				t.Errorf("%s 的场合是 %q，应是 %q", f.TurnID, f.Place, w)
			}
		}
		if f.Kind == "module" && (f.Place != "" || f.Key != "send") {
			t.Errorf("模块行不带场合：%+v", f)
		}
	}
	if got != 7 {
		t.Fatalf("七种会话都要出现：%d", got)
	}
}

// 回合层和工具表
func TestQualityTurnsAndTools(t *testing.T) {
	db, file := tempTrace(t)
	for i, o := range []string{"done", "done", "done", "stopped", "error", "budget_exhausted"} {
		if _, err := db.Exec(`INSERT INTO turns (id, character_id, conversation, started_at, ended_at, outcome) VALUES (?, 'c', 'console:c', ?, ?, ?)`,
			fmt.Sprintf("t%d", i), day7+int64(i), day7+int64(i)+int64(100*(i+1)), o); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range []struct {
		name, status string
		isErr        int
		dur          int64
	}{{"read", "done", 0, 10}, {"read", "done", 0, 30}, {"read", "error", 1, 5}, {"read", "expired", 1, 0}, {"shell", "done", 0, 7}} {
		if _, err := db.Exec(`INSERT INTO steps (id, kind, name, status, is_error, started_at, ended_at) VALUES (?, 'tool', ?, ?, ?, ?, ?)`,
			fmt.Sprintf("s%d", nextID.Add(1)), st.name, st.status, st.isErr, day7, day7+st.dur); err != nil {
			t.Fatal(err)
		}
	}
	rep := quality(t, openService(t, file), ctlproto.StatsArgs{})
	if rep.Turns.Total != 6 || rep.Turns.Outcomes[0].Outcome != "done" || rep.Turns.Outcomes[0].Count != 3 {
		t.Fatalf("回合：%+v", rep.Turns)
	}
	if rep.Turns.Samples != 3 || rep.Turns.P50Ms != 200 || rep.Turns.P95Ms != 300 { // done 的耗时 100、200、300
		t.Fatalf("回合耗时只算 done：%+v", rep.Turns)
	}
	var read ctlproto.QualityTool
	for _, tl := range rep.Tools {
		if tl.Name == "read" {
			read = tl
		}
	}
	if read.Runs != 4 || read.Done != 2 || read.Failed != 1 || read.Incomplete != 1 || read.MedianMs != 10 {
		t.Fatalf("工具：%+v", read)
	}
}

// 范围：范围外的行不算；最慢的环节按 p95 降序、样本少的标 Thin
func TestQualityRangeAndSlowest(t *testing.T) {
	db, file := tempTrace(t)
	old := ms(2026, 10, 1, 10, 0, 0, 0)
	insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "old", at: old, ms: i64(9), err: "x", class: "server_error"})
	for i := int64(0); i < 25; i++ {
		insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "fast", at: day8, ms: i64(10 + i)})
	}
	for i := int64(0); i < 3; i++ {
		insertQ(t, db, qr{purpose: "guard", stage: "guard", model: "slow", at: day8, ms: i64(5000 + i)})
	}
	rep := quality(t, openService(t, file), ctlproto.StatsArgs{Days: 7})
	if rep.Totals.Runs != 28 || rep.Totals.Failed != 0 {
		t.Fatalf("范围外的行不该算：%+v", rep.Totals)
	}
	if len(rep.Slowest) != 2 || rep.Slowest[0].Model != "slow" || !rep.Slowest[0].Thin || rep.Slowest[1].Model != "fast" || rep.Slowest[1].Thin {
		t.Fatalf("最慢的环节：%+v", rep.Slowest)
	}
	all := quality(t, openService(t, file), ctlproto.StatsArgs{All: true})
	if all.Totals.Failed != 1 {
		t.Fatalf("--all 要含旧的：%+v", all.Totals)
	}
}

// 模块键要和 taskrun 的常量对上，改名时这里先红
func TestQualityModuleKeysMatchTaskrun(t *testing.T) {
	for k, v := range map[string]string{ctlproto.ModuleSend: "send", ctlproto.ModuleLink: "link", ctlproto.ModuleMedia: "media", ctlproto.ModuleHooks: "hooks"} {
		if k != v {
			t.Errorf("模块键 %q 变了", v)
		}
	}
}

// 悄悄话那一轮的运行单列一类“悄悄话”，不再并进本地私聊（规格-群聊引用和路由）：串长得像本地私聊，但按悄悄话会话的形状认；
// 形状不对的（段数不对、十六进制不足）不是悄悄话，仍按原样归类。
func TestQualityPlaceWhisperIsOwnClass(t *testing.T) {
	db, file := tempTrace(t)
	whisper := audience.WhisperConversation("c", "qq:group:111")
	convs := map[string]string{"w0": whisper, "w1": "console:c", "w2": "console:c:whisper:0123", "w3": "console:c:whisper:0123456789abcdef:x"}
	i := int64(0)
	for turn, c := range convs {
		insertQ(t, db, qr{purpose: "foreground", stage: "reply", model: "m", at: day7 + i*10, err: "boom", class: "server_error", conv: c, turn: turn})
		i++
	}
	s := openService(t, file)
	rep := quality(t, s, ctlproto.StatsArgs{Failures: 50})
	want := map[string]string{"w0": ctlproto.ScenePlaceWhisper, "w1": ctlproto.ScenePlaceLocalPrivate, "w2": ctlproto.ScenePlaceLocalPrivate, "w3": ctlproto.ScenePlaceLocalPrivate}
	got := 0
	for _, f := range rep.Recent {
		if w, ok := want[f.TurnID]; ok {
			got++
			if f.Place != w {
				t.Errorf("%s 的场合应是 %q，得到 %q", f.TurnID, w, f.Place)
			}
		}
	}
	if got != len(want) {
		t.Fatalf("最近的失败里只找到 %d 条：%+v", got, rep.Recent)
	}
}
