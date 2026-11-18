package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	randv2 "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/turn"
)

type options struct {
	sessions                 string
	characters, turns, tools int
	steer, stop, compact     float64
	out                      string
	diagnose                 bool
}
type metrics struct {
	WorkingSet, Private, Handles uint64
	Goroutines                   int
	Threads                      int
}
type result struct {
	Sessions, Completed, Requests, Failures, Stuck, Steers, Stops, Compacts, Fresh int
	Elapsed, Median, P95                                                           time.Duration
	Before, Peak, After                                                            metrics
	Isolation, Accounting, Errors                                                  []string
	Pass                                                                           bool
}

func sample() (metrics, error) {
	w, p, h, e := processStats()
	t, te := threadCount()
	if e == nil {
		e = te
	}
	return metrics{WorkingSet: w, Private: p, Handles: h, Goroutines: runtime.NumGoroutine(), Threads: t}, e
}
func peak(a, b metrics) metrics {
	return metrics{WorkingSet: max(a.WorkingSet, b.WorkingSet), Private: max(a.Private, b.Private), Handles: max(a.Handles, b.Handles), Goroutines: max(a.Goroutines, b.Goroutines), Threads: max(a.Threads, b.Threads)}
}
func main() {
	o := options{}
	flag.StringVar(&o.sessions, "sessions", "10,50,100", "会话档位，逗号分隔")
	flag.IntVar(&o.characters, "characters", 4, "角色数")
	flag.IntVar(&o.turns, "turns", 10, "每会话回合数")
	flag.IntVar(&o.tools, "parallel-tools", 3, "每轮并行只读工具数")
	flag.Float64Var(&o.steer, "steer-ratio", .05, "插话比例")
	flag.Float64Var(&o.stop, "stop-ratio", .05, "停止比例")
	flag.Float64Var(&o.compact, "compact-ratio", .05, "手动压缩比例")
	flag.StringVar(&o.out, "out", "", "报告及合成夹具目录（不给就是仓库根下 data/dev/stress/<时间>）")
	flag.BoolVar(&o.diagnose, "diagnose-handles", false, "每档同进程连跑三次，按类型检查句柄稳定")
	flag.Parse()
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(o options) error {
	root, err := config.FindRoot(".")
	if err != nil {
		return err
	}
	oldPath := os.Getenv("PATH")
	if err = os.Setenv("PATH", filepath.Join(root, "vendor", "bin")+string(os.PathListSeparator)+oldPath); err != nil {
		return err
	}
	defer os.Setenv("PATH", oldPath)
	if o.out == "" {
		o.out = filepath.Join(root, "data", "dev", "stress", time.Now().Format("20060102-150405.000"))
	}
	if o.characters < 1 || o.turns < 1 || o.tools < 0 {
		return fmt.Errorf("characters、turns 必须为正，parallel-tools 不能为负")
	}
	for _, v := range []float64{o.steer, o.stop, o.compact} {
		if math.IsNaN(v) || v < 0 || v > 1 {
			return fmt.Errorf("交错比例必须在 0 到 1 之间")
		}
	}
	var tiers []int
	for _, v := range strings.Split(o.sessions, ",") {
		n, e := strconv.Atoi(v)
		if e != nil || n < 2 {
			return fmt.Errorf("sessions 每档至少 2 个会话：%q", v)
		}
		tiers = append(tiers, n)
	}
	sort.Ints(tiers)
	if err := os.MkdirAll(o.out, 0755); err != nil {
		return err
	}
	if o.diagnose {
		return diagnose(o, tiers)
	}
	var results []result
	for _, n := range tiers {
		r, err := runTier(o, n, filepath.Join(o.out, fmt.Sprintf("sessions-%d", n)))
		if err != nil {
			return err
		}
		results = append(results, r)
		fmt.Printf("sessions=%d completed=%d requests=%d failures=%d stuck=%d elapsed=%s pass=%v\n", n, r.Completed, r.Requests, r.Failures, r.Stuck, r.Elapsed, r.Pass)
		if err = report(o, results); err != nil {
			return err
		}
	}
	for _, r := range results {
		if !r.Pass {
			return fmt.Errorf("压力测试未过线，见 %s", filepath.Join(o.out, "summary.md"))
		}
	}
	if len(results) > 1 && !linear(points(results)) {
		return fmt.Errorf("线性判定未过线，见 summary.md")
	}
	return nil
}
func runTier(o options, n int, dir string) (r result, err error) {
	r.Sessions = n
	defer func() {
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
		}
		if e := writeJSON(filepath.Join(dir, "result.json"), r); e != nil && err == nil {
			err = e
		}
	}()
	r.Before, err = sample()
	if err != nil {
		return r, err
	}
	r.Peak = r.Before
	k, err := newKernel(dir, n, o.characters, o.tools)
	if err != nil {
		return r, err
	}
	defer k.close()
	stopSample := make(chan struct{})
	sampleDone := make(chan struct{})
	var sampleErr error
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			m, e := sample()
			if e != nil {
				sampleErr = e
				return
			}
			r.Peak = peak(r.Peak, m)
			select {
			case <-stopSample:
				return
			case <-ticker.C:
			}
		}
	}()
	start := make(chan struct{})
	var ready, wg sync.WaitGroup
	ready.Add(n)
	wg.Add(n)
	var mu sync.Mutex
	roundArrived := make([]int, o.turns)
	roundRelease := make([]chan struct{}, o.turns)
	for i := range roundRelease {
		roundRelease[i] = make(chan struct{})
	}
	var durations []time.Duration
	var rounds []*roundRecord
	issue := func(rec *roundRecord, message string) {
		mu.Lock()
		defer mu.Unlock()
		rec.Errors = append(rec.Errors, message)
		r.Errors = append(r.Errors, fmt.Sprintf("%s 第 %d 轮：%s", rec.Session, rec.Round, message))
	}
	want := map[string]counts{}
	began := time.Now()
	for _, s := range k.sessions {
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			rng := randv2.New(randv2.NewPCG(randv2.Uint64(), randv2.Uint64()))
			expected := counts{}
			defer func() { mu.Lock(); want[s.id] = expected; mu.Unlock() }()
			for round := range o.turns {
				rec := &roundRecord{Session: s.id, Round: round + 1, Started: time.Now()}
				mu.Lock()
				rounds = append(rounds, rec)
				mu.Unlock()
				mu.Lock()
				roundArrived[round]++
				if roundArrived[round] == n {
					close(roundRelease[round])
				}
				mu.Unlock()
				select {
				case <-roundRelease[round]:
				case <-time.After(30 * time.Second):
					dump(dir, rec, "等本轮屏障")
					issue(rec, "等本轮屏障超过 30 秒")
					k.cancel()
					mu.Lock()
					r.Stuck++
					r.Failures++
					mu.Unlock()
					return
				}
				text := fmt.Sprintf("[session=%s] [round=%d] 讨论短篇小说。", s.id, round) + strings.Repeat("先读短篇小说，再讨论人物和结局。", 32)
				if round == 0 {
					text += " 暗号：" + s.secret
				}
				seq, e := k.append(s, text)
				if e != nil {
					issue(rec, "入库失败："+e.Error())
					mu.Lock()
					r.Failures++
					mu.Unlock()
					return
				}
				expected.Messages++
				expected.Turns++
				steer, stop := rng.Float64() < o.steer, rng.Float64() < o.stop
				if stop {
					steer = false
				}
				rec.Steer, rec.Stop = steer, stop
				var b *gate
				if steer || stop {
					b = &gate{make(chan struct{}), make(chan struct{})}
					s.mu.Lock()
					s.hold = b
					s.mu.Unlock()
				}
				t := time.Now()
				out := k.hub.Respond(turn.Input{CharacterID: s.character, Conversation: s.id, Seqs: []int64{seq}, Text: text, Audience: audience.Console(s.id), Addressed: true, Scope: turn.ConsoleScope})
				timer := time.NewTimer(30 * time.Second)
				if b != nil {
					select {
					case <-b.arrived:
						if stop {
							if !k.hub.StopIn(s.character, s.id) {
								issue(rec, "停止未收下")
							}
							mu.Lock()
							r.Stops++
							mu.Unlock()
						} else {
							steerText := "[session=" + s.id + "] 插一句：先读短篇。"
							_, ok := k.hub.SteerIn(s.character, s.id, steerText, nil)
							var se error
							if !ok {
								se = fmt.Errorf("%s 插话未收下", s.id)
							} else {
								expected.Messages++
							}
							mu.Lock()
							r.Steers++
							mu.Unlock()
							if se != nil {
								issue(rec, se.Error())
							}
						}
						close(b.release)
					case <-timer.C:
						dump(dir, rec, "等假网关到达")
						issue(rec, "等假网关到达超过 30 秒")
						mu.Lock()
						r.Stuck++
						r.Failures++
						mu.Unlock()
						k.cancel()
						return
					}
				}
				select {
				case outcome := <-out:
					timer.Stop()
					rec.Elapsed = time.Since(t)
					rec.TurnID = outcome.TurnID
					if outcome.Err != nil {
						issue(rec, "回合失败："+outcome.Err.Error())
					}
					mu.Lock()
					durations = append(durations, time.Since(t))
					r.Completed++
					if outcome.Err != nil {
						r.Failures++
					}
					mu.Unlock()
					if !stop {
						expected.Messages++
						expected.Steps += o.tools
					}
					if outcome.Result == nil || outcome.Result.Stopped != stop {
						issue(rec, fmt.Sprintf("停止状态不符：预期 %v，实际 %+v", stop, outcome.Result))
					}
				case <-timer.C:
					dump(dir, rec, "等回合结束")
					issue(rec, "等回合结束超过 30 秒")
					mu.Lock()
					r.Stuck++
					r.Failures++
					mu.Unlock()
					k.cancel()
					return
				}
				if !stop && rng.Float64() < o.compact {
					compact, ce := k.hub.Compact(s.character, s.id, "manual", 0)
					rec.Compact = true
					if ce != nil || (!compact.OK && compact.Err != "前面的聊天还不多，没有可以压的") {
						issue(rec, fmt.Sprintf("压缩失败：%v %+v", ce, compact))
					}
					mu.Lock()
					if ce != nil || (!compact.OK && compact.Err != "前面的聊天还不多，没有可以压的") {
						r.Failures++
					} else if compact.OK {
						r.Compacts++
					}
					mu.Unlock()
				}
			}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	r.Elapsed = time.Since(began)
	close(stopSample)
	<-sampleDone
	if e := k.evidence(dir, rounds); e != nil {
		return r, e
	}
	if sampleErr != nil {
		return r, sampleErr
	}
	got := map[string]counts{}
	for _, s := range k.sessions {
		var c counts
		st, e := k.set.ForConversation(s.id)
		if e != nil {
			return r, e
		}
		queries := []struct {
			q     string
			dest  *int
			trace bool
		}{{"SELECT count(*) FROM turns WHERE conversation = ?", &c.Turns, true}, {"SELECT count(*) FROM events WHERE conversation = ?", &c.Messages, false}, {"SELECT count(*) FROM steps WHERE conversation = ?", &c.Steps, true}}
		for _, q := range queries {
			db := st.Events
			if q.trace {
				db = k.set.Trace()
			}
			if e = db.QueryRow(q.q, s.id).Scan(q.dest); e != nil {
				return r, e
			}
		}
		got[s.id] = c
	}
	var toolErrors int
	if err = k.set.Trace().QueryRow("SELECT count(*) FROM steps WHERE kind = 'tool' AND (is_error != 0 OR ended_at IS NULL)").Scan(&toolErrors); err != nil {
		return r, err
	}
	if toolErrors > 0 {
		r.Failures += toolErrors
		r.Errors = append(r.Errors, fmt.Sprintf("工具出错或未结束 %d 次", toolErrors))
		rows, e := k.set.Trace().Query("SELECT conversation, turn_id, name, preview FROM steps WHERE kind = 'tool' AND (is_error != 0 OR ended_at IS NULL)")
		if e != nil {
			return r, e
		}
		for rows.Next() {
			var conversation, turnID, name, preview string
			if e = rows.Scan(&conversation, &turnID, &name, &preview); e != nil {
				rows.Close()
				return r, e
			}
			r.Errors = append(r.Errors, fmt.Sprintf("%s 回合 %s 工具 %s：%s", conversation, turnID, name, preview))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return r, e
		}
	}
	if err = k.set.Trace().QueryRow("SELECT count(*) FROM model_runs WHERE rebuild = 'fresh'").Scan(&r.Fresh); err != nil {
		return r, err
	}
	secrets := map[string]string{}
	for _, s := range k.sessions {
		secrets[s.id] = s.secret
	}
	r.Requests = len(k.records)
	r.Isolation = isolation(k.records, secrets)
	r.Accounting = accounting(want, got)
	if err = writeJSON(filepath.Join(dir, "counts.json"), struct{ Want, Got map[string]counts }{want, got}); err != nil {
		return r, err
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if len(durations) > 0 {
		r.Median = durations[len(durations)/2]
		r.P95 = durations[int(math.Ceil(float64(len(durations))*.95))-1]
	}
	k.close()
	k.cancel = nil
	k.hub = nil
	k.server = nil
	k.set = nil
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		r.After, err = sample()
		if err != nil {
			return r, err
		}
		if r.After.Goroutines <= r.Before.Goroutines+2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if abs(r.After.Goroutines-r.Before.Goroutines) > 2 {
		r.Errors = append(r.Errors, fmt.Sprintf("关闭后协程未回到基线 ±2：%d→%d", r.Before.Goroutines, r.After.Goroutines))
	}
	r.Pass = r.Failures == 0 && r.Stuck == 0 && len(r.Isolation) == 0 && len(r.Accounting) == 0 && len(r.Errors) == 0 && abs(r.After.Goroutines-r.Before.Goroutines) <= 2
	return r, nil
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func dump(dir string, rec *roundRecord, phase string) {
	name := fmt.Sprintf("goroutines-%s-round-%d-%d.txt", strings.ReplaceAll(rec.Session, ":", "-"), rec.Round, time.Now().UnixNano())
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		fmt.Fprintln(os.Stderr, "协程栈落盘失败：", err)
	} else {
		fmt.Fprintf(f, "%s 第 %d 轮：%s\n", rec.Session, rec.Round, phase)
		_ = pprof.Lookup("goroutine").WriteTo(f, 2)
		_ = f.Close()
	}
	fmt.Fprintln(os.Stderr, "===== 超时时的协程栈 =====", name)
	_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
}
func points(rs []result) []scalePoint {
	var p []scalePoint
	for _, r := range rs {
		p = append(p, scalePoint{r.Sessions, float64(r.Peak.Private - r.Before.Private), float64(r.Peak.Goroutines - r.Before.Goroutines)})
	}
	return p
}
func report(o options, rs []result) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# B7 第一段压力测试\n\n角色 %d；每会话 %d 轮；每轮 %d 个并行只读工具；插话/停止/压缩比例 %.3f / %.3f / %.3f。MaxLive=max(1,sessions/4)。\n\n", o.characters, o.turns, o.tools, o.steer, o.stop, o.compact)
	b.WriteString("单次运行的过线列只判负载与协程；句柄须另用 -diagnose-handles 在同一进程每档连跑三次检查 handles.md，不能据本表宣称句柄已稳定。\n\n")
	b.WriteString("峰值每 5 ms 采样；内存为本进程，grep 子进程不计。线性暂定门槛：扣除启动基线的每会话 PrivateUsage 和协程峰值不超过最小档的两倍。关闭后等最多 5 秒；协程 ±2。单档不能判断线性。\n\n| 会话 | 总耗时 | 中位 | p95 | 完成轮 | 请求 | 失败 | 卡住 | 串会话 | 计数差异 | WS峰值 MiB | Private峰值 MiB | Private增量 KiB/会话 | 协程峰值/增量每会话 | 协程起→终 | 句柄起→峰→终 | 插话/停止/压缩 | 冷建 | 负载过线 |\n|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | %d | %d | %d | %d | %d | %.2f | %.2f | %.2f | %d / %.2f | %d→%d | %d→%d→%d | %d/%d/%d | %d | %v |\n", r.Sessions, r.Elapsed, r.Median, r.P95, r.Completed, r.Requests, r.Failures, r.Stuck, len(r.Isolation), len(r.Accounting), float64(r.Peak.WorkingSet)/1048576, float64(r.Peak.Private)/1048576, float64(r.Peak.Private-r.Before.Private)/1024/float64(r.Sessions), r.Peak.Goroutines, float64(r.Peak.Goroutines-r.Before.Goroutines)/float64(r.Sessions), r.Before.Goroutines, r.After.Goroutines, r.Before.Handles, r.Peak.Handles, r.After.Handles, r.Steers, r.Stops, r.Compacts, r.Fresh, r.Pass)
	}
	for _, r := range rs {
		for _, errors := range [][]string{r.Isolation, r.Accounting, r.Errors} {
			for _, e := range errors {
				fmt.Fprintf(&b, "\n- %d 会话：%s\n", r.Sessions, e)
			}
		}
	}
	fmt.Fprintf(&b, "\n线性判定：%v（至少两档）。完整请求和计数在各档 requests.json、counts.json；fixture/factory/work 都是本次生成的合成数据。\n", linear(points(rs)))
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(o.out, "results.json"), data, 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o.out, "summary.md"), []byte(b.String()), 0644)
}
