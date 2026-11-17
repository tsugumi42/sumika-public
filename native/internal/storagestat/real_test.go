package storagestat

import (
	"context"
	"os"
	"testing"
	"time"

	"sumika-native/internal/paths"
)

// 验收④：在真实大小的数据目录上量首次冷扫和热扫用时。手动跑：
//
//	SUMIKA_STORAGESTAT_REAL=<数据目录> go test -run TestRealDataTiming -v ./internal/storagestat/
//
// 只读：不建 Store（不碰任何数据库）、只列目录和读目录项，不删不写。日志里只有类别名和数字，没有文件名
func TestRealDataTiming(t *testing.T) {
	dir := os.Getenv("SUMIKA_STORAGESTAT_REAL")
	if dir == "" {
		t.Skip("没设 SUMIKA_STORAGESTAT_REAL")
	}
	p, err := paths.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(p, Options{})
	for _, round := range []string{"第一次（冷）", "第二次（热）", "第三次（热）"} {
		start := time.Now()
		if err := s.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s 完整统计用时 %v，处理 %d 项", round, time.Since(start).Round(time.Millisecond), s.entries.Load())
	}
	start := time.Now()
	rep, err := s.Storage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("打开页面（小目录现量、大目录用快照）用时 %v", time.Since(start).Round(time.Millisecond))
	for _, c := range rep.Categories {
		t.Logf("类别 %s：%.2f MB，%d 个文件，联接 %d，读不了 %d", c.Key, float64(c.Bytes)/(1<<20), c.Files, c.Links, c.Errors)
	}
	t.Logf("合计 %.2f MB", float64(rep.Total)/(1<<20))
}
