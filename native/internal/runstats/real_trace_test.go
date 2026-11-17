package runstats

// 手动对真实 trace.db 的只读副本跑一遍失败分类，只打印各类别的计数，不打印原文（规格第 4 步验收：交回计数）。
// 用法：SUMIKA_REAL_TRACE=<副本路径> go test ./internal/runstats -run TestRealTraceClasses -v
// 没设环境变量就跳过。副本要拷到草稿目录再读，用完删掉；这里以 mode=ro&immutable=1 打开，不写。

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
)

func TestRealTraceClasses(t *testing.T) {
	file := os.Getenv("SUMIKA_REAL_TRACE")
	if file == "" {
		t.Skip("没设 SUMIKA_REAL_TRACE")
	}
	u := url.URL{Scheme: "file", Path: "/" + strings.ReplaceAll(file, "\\", "/"), RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cut := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	type key struct{ window, task, class string }
	counts := map[key]int{}
	totals := map[[2]string]int{} // 窗口、任务 -> 总次数
	failed := map[[2]string]int{}
	rows, err := db.Query(`SELECT COALESCE(purpose, ''), stage, started_at, error IS NOT NULL, '', COALESCE(substr(error, 1, 2000), '') FROM model_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var purpose, stage, class, text string
		var at int64
		var bad bool
		if err := rows.Scan(&purpose, &stage, &at, &bad, &class, &text); err != nil {
			t.Fatal(err)
		}
		task := TaskOf(purpose, stage)
		for _, w := range []string{"全部", "最近7天"} {
			if w == "最近7天" && at < cut {
				continue
			}
			totals[[2]string{w, task}]++
			if !bad {
				continue
			}
			failed[[2]string{w, task}]++
			if class == "" {
				class = llm.ClassifyText(text)
			}
			counts[key{w, task, class}]++
		}
	}
	var keys []key
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.window != b.window {
			return a.window < b.window
		}
		if a.task != b.task {
			return a.task < b.task
		}
		return counts[a] > counts[b]
	})
	for _, k := range keys {
		t.Logf("%s | %s | %s | %d", k.window, k.task, k.class, counts[k])
	}
	for k, n := range totals {
		t.Logf("合计 %s | %s | 次数 %d | 失败 %d", k[0], k[1], n, failed[k])
	}
}

// 同一个副本再复制一份到 t.TempDir()，迁移到最新（只动这份临时拷贝），用 Quality 查"全部"，打印总数和耗时，
// 确认真实形状的数据能查、旧行的类别读时现算对得上上面的计数。不打印原文。
func TestRealTraceQuality(t *testing.T) {
	file := os.Getenv("SUMIKA_REAL_TRACE")
	if file == "" {
		t.Skip("没设 SUMIKA_REAL_TRACE")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir() + string(os.PathSeparator) + "trace.db"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	mig, err := store.OpenTraceFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	mig.Close()
	svc, err := OpenFile(tmp, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	a := ctlproto.StatsArgs{All: true}
	r, err := svc.RangeOf(a)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rep, err := svc.Quality(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Quality(全部) 用时 %v；总 %d 次，失败 %d，超时 %d，取消 %d；未知占比 %.1f%%（标红 %v）；最近失败共 %d 条", time.Since(start), rep.Totals.Runs, rep.Totals.Failed, rep.Totals.Timeout, rep.Totals.Cancelled, rep.Unknown.Share*100, rep.Unknown.Alarm, rep.RecentOf)
	for _, row := range rep.Rows {
		if row.Runs > 0 {
			t.Logf("%s：%d 次，失败 %d，超时 %d，取消 %d，p50 %dms，p95 %dms", row.Key, row.Runs, row.Failed, row.Timeout, row.Cancelled, row.P50Ms, row.P95Ms)
		}
	}
}
