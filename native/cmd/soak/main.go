// soak 用编的对话和假网关检查真实内核的长时间开销，不读取真实数据。
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	duration, interval time.Duration
	out                string
	rate               float64
}

func main() {
	o := options{}
	flag.DurationVar(&o.duration, "duration", 72*time.Hour, "运行多久")
	flag.DurationVar(&o.interval, "interval", time.Minute, "采样间隔")
	flag.StringVar(&o.out, "out", "", "CSV 和汇总目录；默认主工作区 data/dev/soak/<开始时刻>")
	// 两个角色合计每两秒一句，即每分钟 30 句；这是密集短句聊天的保守负载假设，不是真实聊天统计。
	flag.Float64Var(&o.rate, "rate", 0.5, "每秒几句（默认合计每分钟 30 句）")
	again := flag.String("report", "", "不跑负载，按现在的判定重算这个目录里已有的 samples.csv；-duration 当作那次实际跑了多久")
	flag.Parse()
	if *again != "" {
		if err := rereport(*again, o.duration, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// rereport 重算已有的采样，不改原来的 summary.txt（判定改了以后看旧的运行用）
func rereport(dir string, elapsed time.Duration, output io.Writer) error {
	rows, err := readSamples(filepath.Join(dir, "samples.csv"))
	if err != nil {
		return err
	}
	report, err := summarize(rows, elapsed)
	if err != nil {
		return err
	}
	if last := rows[len(rows)-1].elapsed; last > elapsed {
		return fmt.Errorf("最后一个采样在 %s，比 -duration %s 还晚", last, elapsed)
	}
	fmt.Fprintf(output, "B6 挂机重算：%s\n采样：%d 行；时长按 %s 算\n%s", dir, len(rows), elapsed, report.text())
	return nil
}

func defaultOutput(now time.Time) (string, error) {
	b, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("找主工作区失败，请给 -out：%w", err)
	}
	return filepath.Join(filepath.Dir(strings.TrimSpace(string(b))), "data", "dev", "soak", now.Format("20060102-150405.000000000")), nil
}

func run(parent context.Context, o options, output io.Writer) error {
	if o.duration <= 0 || o.interval <= 0 || math.IsNaN(o.rate) || math.IsInf(o.rate, 0) || o.rate <= 0 || float64(time.Second)/o.rate < 1 || float64(time.Second)/o.rate >= float64(math.MaxInt64) {
		return errors.New("duration、interval、rate 要大于零，rate 还要能换算成有效的定时间隔")
	}
	started := time.Now()
	var err error
	if o.out == "" {
		o.out, err = defaultOutput(started)
		if err != nil {
			return err
		}
	}
	if err = os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(o.out, "samples.csv"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	writer := csv.NewWriter(f)
	if err = writer.Write(csvHeader); err != nil {
		return err
	}
	// 临时内核放在这次输出下面，避开系统 TEMP；正常退出时整份删除，只留下采样和汇总。
	dir, err := os.MkdirTemp(o.out, ".kernel-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	k, err := newKernel(ctx, dir)
	if err != nil {
		return err
	}
	defer func() { cancel(); k.close() }()
	s := newSampler(k.dbdir)
	start := time.Now()
	counts := &counters{}
	record := func() error {
		row, err := s.take(time.Since(start), counts.snapshot())
		if err != nil {
			return err
		}
		if err = writer.Write(row.fields()); err != nil {
			return err
		}
		writer.Flush()
		if err = writer.Error(); err != nil {
			return err
		}
		return f.Sync()
	}
	if err = record(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- k.work(ctx, o.rate, counts) }()
	deadline := time.NewTimer(o.duration)
	defer deadline.Stop()
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	reason := "到点"
	var workErr error
running:
	for {
		select {
		case <-parent.Done():
			reason = "Ctrl+C 或上层取消"
			break running
		case <-deadline.C:
			break running
		case workErr = <-done:
			reason = "负载提前结束"
			done = nil
			break running
		case <-ticker.C:
			if err = record(); err != nil {
				reason = "采样失败"
				break running
			}
		}
	}
	// 最后一行仍在同样的宿主和工作线程存活时取，避免收尾少几个协程被算作下降。
	if err == nil {
		err = record()
	}
	elapsed := time.Since(start)
	cancel()
	if done != nil {
		workErr = <-done
	}
	k.hub.Idle()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	rows, readErr := readSamples(filepath.Join(o.out, "samples.csv"))
	if err == nil {
		err = readErr
	}
	if err != nil {
		return err
	}
	report, err := summarize(rows, elapsed)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("B6 挂机：%s\n开始：%s\n时长：%s；目标：%s；rate：%g 句/秒\n采样：%d 行；回合：%d；工具：%d；插话：%d；停止：%d；压缩：%d；冷建：%d\n", reason, started.Format(time.RFC3339), elapsed.Round(time.Millisecond), o.duration, o.rate, len(rows), counts.turns.Load(), counts.tools.Load(), counts.steers.Load(), counts.stops.Load(), counts.compacts.Load(), counts.fresh.Load())
	text += report.text()
	if workErr != nil {
		text += fmt.Sprintf("负载错误：%v\n", workErr)
	}
	if err = os.WriteFile(filepath.Join(o.out, "summary.txt"), []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Fprint(output, text)
	fmt.Fprintf(output, "输出：%s\n", o.out)
	return workErr
}
