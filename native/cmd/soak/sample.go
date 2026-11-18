package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type totals struct{ turns, tools, steers, stops, compacts, fresh uint64 }
type sample struct {
	elapsed                           time.Duration
	workingSet, private, heap, heapGC uint64
	goroutines, handles               uint64
	db                                uint64
	totals
}

var csvHeader = []string{"elapsed_ns", "working_set_bytes", "private_bytes", "heap_inuse_bytes", "heap_inuse_after_gc_bytes", "goroutines", "handles", "db_bytes", "turns", "tools", "steers", "stops", "compacts", "fresh"}

func (s sample) fields() []string {
	values := []uint64{uint64(s.elapsed), s.workingSet, s.private, s.heap, s.heapGC, s.goroutines, s.handles, s.db, s.turns, s.tools, s.steers, s.stops, s.compacts, s.fresh}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strconv.FormatUint(v, 10)
	}
	return out
}

// 只把堆读取和 GC 换成函数，测试能确定采样前没有偷偷 GC，不依赖睡眠或 GC 何时自动发生。
type sampler struct {
	dbdir string
	heap  func() uint64
	gc    func()
}

func newSampler(dbdir string) sampler {
	return sampler{dbdir: dbdir, heap: func() uint64 { var m runtime.MemStats; runtime.ReadMemStats(&m); return m.HeapInuse }, gc: runtime.GC}
}
func (s sampler) take(elapsed time.Duration, count totals) (sample, error) {
	row := sample{elapsed: elapsed, totals: count}
	row.heap = s.heap()
	var err error
	row.workingSet, row.private, row.handles, err = processStats()
	if err != nil {
		return row, err
	}
	row.goroutines = uint64(runtime.NumGoroutine())
	// WAL 和 SHM 也算；只遍历这次临时内核的 go/，不碰输出 CSV 和真实库。
	err = filepath.WalkDir(s.dbdir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".db") && !strings.HasSuffix(path, ".db-wal") && !strings.HasSuffix(path, ".db-shm") {
			return nil
		}
		info, err := d.Info()
		if os.IsNotExist(err) {
			return nil
		} // SQLite 正好移除了 WAL。
		if err != nil {
			return err
		}
		row.db += uint64(info.Size())
		return nil
	})
	if err != nil {
		return row, err
	}
	s.gc()
	row.heapGC = s.heap()
	return row, nil
}
func readSamples(path string) ([]sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if strings.Join(header, ",") != strings.Join(csvHeader, ",") {
		return nil, fmt.Errorf("采样 CSV 表头不对：%v", header)
	}
	var out []sample
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var n [14]uint64
		for i, f := range fields {
			n[i], err = strconv.ParseUint(f, 10, 64)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, sample{time.Duration(n[0]), n[1], n[2], n[3], n[4], n[5], n[6], n[7], totals{n[8], n[9], n[10], n[11], n[12], n[13]}})
	}
}
