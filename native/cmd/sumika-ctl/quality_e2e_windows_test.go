package main

// stats quality 端到端（规格-运行页 第十二节第 4 步）：命令行 → 带测试名的真命名管道 → internal/ctlserver 的校验 →
// 真的 runstats.Service 读临时 trace.db，再走回来变成退出码和输出。库只在 t.TempDir() 里，不碰真实 data/。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/paths"
	"sumika-native/internal/runstats"
	"sumika-native/internal/store"
)

type qualityBackend struct {
	stubBackend
	file  string
	zone  *time.Location
	now   time.Time
	mu    sync.Mutex
	asked []ctlproto.StatsArgs
}

func (b *qualityBackend) Quality(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	b.mu.Lock()
	b.asked = append(b.asked, a)
	b.mu.Unlock()
	svc, err := runstats.OpenFile(b.file, runstats.Options{Loc: b.zone, Now: func() time.Time { return b.now }})
	if err != nil {
		return ctlproto.QualityReport{}, err
	}
	defer svc.Close()
	rng, err := svc.RangeOf(a)
	if err != nil {
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	return svc.Quality(ctx, rng, a)
}

func TestStatsQualityEndToEnd(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, zone)
	p := paths.Paths{Root: t.TempDir()}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	insert := func(i int, purpose any, stage string, at time.Time, ms int, errText, class any, conv string) {
		t.Helper()
		if _, err := st.Trace.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, sections_json, injected_json, error, error_class, purpose)
 VALUES (?, 'c', ?, ?, 'm/a', ?, ?, '[]', '[]', ?, ?, ?)`, fmt.Sprintf("q%d", i), conv, stage, at.UnixMilli(), ms, errText, class, purpose); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, "foreground", "reply", now.Add(-time.Hour), 1000, nil, nil, "console:c")
	insert(2, "foreground", "reply", now.Add(-2*time.Hour), 3000, nil, nil, "console:c")
	insert(3, "foreground", "reply", now.Add(-3*time.Hour), 50, "Open Design: ECONNRESET: The socket connection was closed unexpectedly.", nil, "qq:group:424242") // 旧行：读时现算
	insert(4, "guard", "guard", now.Add(-26*time.Hour), 100, "context deadline exceeded", "timeout", "qq:private:1:2")
	insert(5, nil, "mystery-stage", now.Add(-5*time.Hour), 1, "没见过", "unknown", "console:c")
	insert(6, "foreground", "reply", now.Add(-10*24*time.Hour), 7, "boom", "server_error", "console:c") // 7 天之外
	if _, err := st.Trace.Exec(`INSERT INTO task_runs (at, module, outcome, class, ms, attempts, detail) VALUES (?, 'send', 'failed', 'retry_exhausted', 12, 3, ?)`,
		now.Add(-time.Minute).UnixMilli(), "发给 "+strings.Repeat("9", 11)+" 失败"); err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf(`\\.\pipe\sumika-ctl-test-quality-%d`, time.Now().UnixNano())
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	b := &qualityBackend{stubBackend: stubBackend{quit: make(chan struct{})}, file: p.BunTraceDB(), zone: zone, now: now}
	srv := ctlserver.New(b, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb, dial)
		return code, out.String(), errb.String()
	}
	report := func(args ...string) ctlproto.QualityReport {
		t.Helper()
		code, out, errs := do(append([]string{"--json"}, args...)...)
		var r ctlproto.Reply
		var rep ctlproto.QualityReport
		if code != 0 || json.Unmarshal([]byte(out), &r) != nil || !r.OK || json.Unmarshal(r.Data, &rep) != nil {
			t.Fatalf("%v：退出码 %d，输出 %q，错误 %q", args, code, out, errs)
		}
		return rep
	}

	rep := report("stats", "quality")
	if rep.Totals.Runs != 6 || rep.Totals.Failed != 3 || rep.Totals.Timeout != 1 || rep.Range.Kind != "days" { // 前台 3 + 守卫 1 + 未知 1 + 发送 1
		t.Fatalf("默认报告不对：%+v", rep.Totals)
	}
	byKey := map[string]ctlproto.QualityRow{}
	for _, r := range rep.Rows {
		byKey[r.Key] = r
	}
	if fg := byKey[ctlproto.TaskForeground]; fg.Runs != 3 || fg.OK != 2 || fg.Failed != 1 || fg.P95Ms != 3000 || len(fg.Reasons) != 1 || fg.Reasons[0].Class != "network" {
		t.Errorf("前台：%+v", fg)
	}
	if byKey["backfill"].Wired || byKey["backfill"].Runs != 0 {
		t.Errorf("补录应是未接入的预留行：%+v", byKey["backfill"])
	}
	if !rep.Unknown.Alarm {
		t.Errorf("unknown 占失败的 1/4，应标红：%+v", rep.Unknown)
	}
	if only := report("stats", "quality", "--task", "send"); len(only.Rows) != 1 || only.Totals.Runs != 1 {
		t.Errorf("--task send：%+v", only.Rows)
	}
	if r := report("stats", "quality", "--failures", "2"); len(r.Recent) != 2 || r.RecentOf != 4 || r.Recent[0].Key != "send" {
		t.Errorf("--failures 2：of=%d len=%d", r.RecentOf, len(r.Recent))
	}
	if r := report("stats", "quality", "--all"); r.Totals.Runs != 7 || r.Range.Kind != "all" {
		t.Errorf("--all：%+v", r.Totals)
	}

	// 号码、会话名不出现在任何输出里；场合只写四种说法
	code, out, errs := do("stats", "quality")
	for _, want := range []string{"质量（最近 7 天", "前台回合  3 次", "network  1 次", "补录  未接入", "原因没分出来（unknown）", "超过一成", "最近的失败", "failed/retry_exhausted", "QQ 群"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("文字输出缺 %q（退出码 %d，错误 %q）：\n%s", want, code, errs, out)
		}
	}
	_, jsonOut, _ := do("--json", "stats", "quality")
	for _, secret := range []string{"424242", strings.Repeat("9", 11), "qq:group", "qq:private"} {
		if strings.Contains(out, secret) || strings.Contains(jsonOut, secret) {
			t.Errorf("输出里不该有 %q", secret)
		}
	}

	// 形状表：全部被拒、退出码 3、后端一次都没调到
	b.mu.Lock()
	before := len(b.asked)
	b.mu.Unlock()
	for _, bad := range [][]string{
		{"stats", "quality", "extra"},
		{"stats", "quality", "--year", "2026"},
		{"stats", "quality", "--by", "task"},
		{"stats", "quality", "--full"},
		{"stats", "quality", "--days", "7", "--all"},
		{"stats", "quality", "--days", "0"},
		{"stats", "quality", "--task", "nonsense"},
		{"stats", "quality", "--task="},
		{"stats", "quality", "--failures", "0"},
		{"stats", "quality", "--failures", "-1"},
		{"stats", "quality", "--failures", "100000"},
		{"stats", "quality", "--failures", "abc"},
		{"stats", "quality", "--failures"},
		{"stats", "usage", "--task", "send"},
		{"stats", "usage", "--failures", "5"},
		{"stats", "storage", "--task", "send"},
	} {
		if code, _, errs := do(bad...); code != 3 {
			t.Errorf("%v 应被拒（退出码 3），得到 %d，错误 %q", bad, code, errs)
		}
	}
	b.mu.Lock()
	after := len(b.asked)
	b.mu.Unlock()
	if after != before {
		t.Errorf("被拒的请求不该走到后端：%d → %d", before, after)
	}
}
