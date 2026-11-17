package main

import (
	"encoding/csv"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 喂 CSV 再算，覆盖平的、缓慢上涨、启动抖动、第一小时预热；基线取第二小时（ADR-219），不能拿第一行或最后一小时冒充。
func TestSummary(t *testing.T) {
	cases := []struct {
		name       string
		value      func(int) uint64
		base, last float64
		pass       bool
	}{
		{"平的", func(int) uint64 { return 1000 }, 1000, 1000, true},
		{"慢慢涨", func(i int) uint64 { return 1000 + uint64(i/6)*2 }, 1002, 1142, false},
		{"第一小时预热后平的", func(i int) uint64 {
			if i < 6 {
				return 900
			}
			return 1000
		}, 1000, 1000, true},
		{"开头抖一下后平的", func(i int) uint64 {
			if i == 0 {
				return 9000
			}
			return 1000
		}, 1000, 1000, true},
		{"正好百分之十也不过", func(i int) uint64 {
			if i >= 426 {
				return 1100
			}
			return 1000
		}, 1000, 1100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "samples.csv")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			w := csv.NewWriter(f)
			if err = w.Write(csvHeader); err != nil {
				t.Fatal(err)
			}
			for i := range 433 {
				value := tc.value(i)
				row := sample{elapsed: time.Duration(i) * 10 * time.Minute, workingSet: value, private: value, heap: value, heapGC: value, goroutines: 12, handles: 40}
				if err = w.Write(row.fields()); err != nil {
					t.Fatal(err)
				}
			}
			w.Flush()
			if err = w.Error(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			rows, err := readSamples(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := summarize(rows, 72*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			m := s.metrics[0]
			wantPercent := (tc.last - tc.base) / tc.base * 100
			if !s.complete || s.window != time.Hour || s.pass != tc.pass || m.pass != tc.pass || m.baseline != tc.base || m.last != tc.last || math.Abs(m.percent-wantPercent) > 0.00001 {
				t.Fatalf("首末一小时中位及涨幅不对：%+v；内存=%+v；要 %.1f → %.1f，过线=%t", s, m, tc.base, tc.last, tc.pass)
			}
		})
	}
	// 协程多一个也不过；句柄只报诊断，不擅自加 B6 的门槛。
	for _, which := range []string{"协程", "句柄"} {
		t.Run(which, func(t *testing.T) {
			rows := []sample{{elapsed: 0, workingSet: 100, private: 100, goroutines: 10, handles: 20}, {elapsed: time.Hour, workingSet: 100, private: 100, goroutines: 10, handles: 20}, {elapsed: 72 * time.Hour, workingSet: 100, private: 100, goroutines: 10, handles: 20}}
			if which == "协程" {
				rows[2].goroutines++
			} else {
				rows[2].handles++
			}
			s, err := summarize(rows, 72*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if s.pass != (which == "句柄") || s.metrics[4].pass != (which != "协程") || s.metrics[5].pass != (which != "句柄") {
				t.Fatalf("协程和句柄的线混了：%+v", s)
			}
		})
	}
	t.Run("短跑不冒充七十二小时", func(t *testing.T) {
		rows := []sample{{elapsed: 0, workingSet: 100, private: 100}, {elapsed: 3 * time.Minute, workingSet: 105, private: 105}}
		s, err := summarize(rows, 3*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if s.complete || !s.pass || s.window != 90*time.Second || s.metrics[0].percent != 5 {
			t.Fatalf("短跑应该用分开的前后半程，只报观察值：%+v", s)
		}
	})
}
