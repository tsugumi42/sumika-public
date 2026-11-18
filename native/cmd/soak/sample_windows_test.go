//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSample(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{"local.db": "123", "local.db-wal": "12345", "local.db-shm": "1234567", "ignore.txt": "不算"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newSampler(dir)
	collected := false
	reads := 0
	s.heap = func() uint64 {
		reads++
		if collected {
			return 55
		}
		return 111
	}
	s.gc = func() {
		if reads != 1 {
			t.Fatalf("自然状态的堆还没记就 GC 了，读取次数=%d", reads)
		}
		collected = true
	}
	row, err := s.take(37*time.Second, totals{turns: 42, tools: 6, steers: 2, stops: 1, compacts: 3, fresh: 4})
	if err != nil {
		t.Fatal(err)
	}
	if row.elapsed != 37*time.Second || row.heap != 111 || row.heapGC != 55 || reads != 2 || !collected || row.db != 15 || row.totals != (totals{42, 6, 2, 1, 3, 4}) {
		t.Fatalf("采样顺序或内容不对：%+v；堆读取=%d，GC=%t", row, reads, collected)
	}
	if row.workingSet == 0 || row.private == 0 || row.handles == 0 || row.goroutines == 0 {
		t.Fatalf("Windows 应该给真实进程的统计：%+v", row)
	}
}
