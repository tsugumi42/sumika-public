package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/filelog"
)

// 规格-日志分级 A8：过滤先于截行数；旧行（没有级别）在过滤时不参与并计数；Scanned、Matched 对得上
func TestQueryFiltersLevelModuleAndCountsOld(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	body := strings.Join([]string{
		"2026-11-13 10:00:01 INFO app 启动",
		"2026-11-13 10:00:02 WARN snowluma 连不上",
		"2026-11-13 10:00:03 ERROR channel 没收下",
		"2026-11-13 10:00:04 DEBUG turn 每步",
		"2026-11-12 19:37:00 snowluma: 旧行",
		"2026-11-13 10:00:05 WARN turn 压缩没成",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logs, filelog.FileName("desk", now)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(filelog.New(dir, "desk", nil, func() time.Time { return now }), func() time.Time { return now })
	t.Cleanup(func() { r.Close() })

	res, err := r.Query(100, Filter{})
	if err != nil || len(res.Lines) != 6 || res.Old != 0 || res.Scanned != 6 {
		t.Fatalf("不过滤时全部保留：%+v %v", res, err)
	}

	res, _ = r.Query(100, Filter{Level: "warn"})
	if res.Matched != 3 || res.Old != 1 || res.Scanned != 6 || len(res.Lines) != 3 {
		t.Errorf("warn 及以上：要 3 行、旧行 1、扫 6，实际 %+v", res)
	}
	for _, l := range res.Lines {
		if strings.Contains(l, "DEBUG") || strings.Contains(l, "INFO") || strings.Contains(l, "旧行") {
			t.Errorf("warn 过滤混进了 %q", l)
		}
	}

	res, _ = r.Query(100, Filter{Module: "turn"})
	if res.Matched != 2 || res.Old != 1 {
		t.Errorf("模块 turn：要 2 行（DEBUG 和 WARN），实际 %+v", res)
	}

	res, _ = r.Query(100, Filter{Level: "warn", Module: "turn"})
	if len(res.Lines) != 1 || !strings.HasSuffix(res.Lines[0], "WARN turn 压缩没成") {
		t.Errorf("warn + turn：要 1 行，实际 %+v", res)
	}

	res, _ = r.Query(1, Filter{Level: "warn"})
	if len(res.Lines) != 1 || res.Matched != 3 || !strings.Contains(res.Lines[0], "压缩没成") {
		t.Errorf("先过滤再截：n=1 要最后一条匹配，Matched 仍是 3，实际 %+v", res)
	}
}
