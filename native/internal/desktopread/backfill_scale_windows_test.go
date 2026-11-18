package desktopread

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"sumika-native/internal/api"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type gapMemoryCounters struct {
	cb, faults                                                                                            uint32
	peakWorkingSet, workingSet, peakPaged, paged, peakNonPaged, nonPaged, pagefile, peakPagefile, private uintptr
}

func gapProcessResources(t *testing.T) (time.Duration, uint64, uint64) {
	t.Helper()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	ticks := func(v windows.Filetime) uint64 { return uint64(v.HighDateTime)<<32 | uint64(v.LowDateTime) }
	m := gapMemoryCounters{}
	m.cb = uint32(unsafe.Sizeof(m))
	call := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	if ok, _, err := call.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&m)), uintptr(m.cb)); ok == 0 {
		t.Fatal(err)
	}
	return time.Duration(ticks(kernel)+ticks(user)) * 100 * time.Nanosecond, uint64(m.workingSet), uint64(m.private)
}

// P01 的后台部分；三档各三遍，200 次真实读取。没有构造 UI，不声称量过帧或呈现。
func TestGapAlignmentScale(t *testing.T) {
	if os.Getenv("SUMIKA_SCALE") == "" {
		t.Skip("量测：SUMIKA_SCALE=1")
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, n := range []int{10000, 100000, 300000} {
		for rep := 1; rep <= 3; rep++ {
			t.Run(fmt.Sprintf("%d-%d", n, rep), func(t *testing.T) {
				started := time.Now()
				cpuStart, _, _ := gapProcessResources(t)
				p, err := paths.Init(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				set, err := store.OpenGo(p, "local", "qq")
				if err != nil {
					t.Fatal(err)
				}
				defer set.Close()
				st, _ := set.For("qq")
				chars := make([]api.CharacterView, 10)
				for i := range chars {
					chars[i] = api.CharacterView{ID: fmt.Sprintf("role%d", i), Name: "编造角色"}
				}
				s := New(set, chars, nil)
				defer s.Close()
				s.WithBackfills(BackfillProvider{Account: "91001", Project: GapProjection})
				tx, err := st.Events.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				_, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
   INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,created_at)
   SELECT 'gap-scale-'||i,'qq','qq:group:'||(92000+i%10),'human','qq:93001','message','编造历史',
    CASE WHEN i<1000 THEN '{"segs":[{"kind":"image","hash":"`+strings.Repeat("a", 64)+`"}]}' ELSE '{"segs":[]}' END,
    ?-(i*?/?) FROM n`, n-1, base, (30 * 24 * time.Hour).Milliseconds(), n)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`INSERT INTO observations(character_id,event_seq) SELECT 'role'||(seq%10),seq FROM events`); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<999)
   INSERT INTO channel_gaps(channel,conversation,from_ms,to_ms,reason,state,tries,last_try_ms,created_ms,updated_ms)
   SELECT 'qq','qq:group:'||(92000+i%10),?-i*10000-5000,?-i*10000,'outage',CASE i%3 WHEN 0 THEN 'open' WHEN 1 THEN 'abandoned' ELSE 'filled' END,0,1,1,1 FROM n`, base, base); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				var walPeak int64
				if f, err := os.Stat(p.ChatDB("qq") + "-wal"); err == nil {
					walPeak = f.Size()
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				gapLatency, msgLatency := make([]time.Duration, 0, 200), make([]time.Duration, 0, 200)
				statusLatency, accountLatency := make([]time.Duration, 0, 200), make([]time.Duration, 0, 200)
				var peakWS, peakPrivate, peakHeap uint64
				peakG := 0
				for i := range 200 {
					conv := fmt.Sprintf("qq:group:%d", 92000+i%10)
					begin := time.Now()
					gaps, err := s.ReadGaps(ctx, api.GapQuery{ConversationKey: keyOf(conv), Limit: 100})
					gapLatency = append(gapLatency, time.Since(begin))
					if err != nil {
						t.Fatal(err)
					}
					want := 0
					for j := i % 10; j < 1000; j += 10 {
						if j%3 != 2 {
							want++
						}
					}
					if len(gaps.Value.Items) != want || gaps.Value.HasMore {
						t.Fatalf("缺口分页实际=%d 预期=%d", len(gaps.Value.Items), want)
					}
					begin = time.Now()
					page, err := s.ReadMessages(ctx, api.ConversationRequest{Key: keyOf(conv), Limit: 200}, "")
					msgLatency = append(msgLatency, time.Since(begin))
					if err != nil {
						t.Fatal(err)
					}
					if len(page.Value.Items) != 200 {
						t.Fatal("消息页没有200条")
					}
					for j := 1; j < len(page.Value.Items); j++ {
						if page.Value.Items[j].CreatedAt < page.Value.Items[j-1].CreatedAt || page.Value.Items[j].Ref.Event == page.Value.Items[j-1].Ref.Event {
							t.Fatal("乱序或重复")
						}
					}
					begin = time.Now()
					open, abandoned, excluded, err := st.AlignmentGapCounts(ctx, "91001", func(string) bool { return true })
					statusLatency = append(statusLatency, time.Since(begin))
					if err != nil || open != 334 || abandoned != 333 || excluded != 0 {
						t.Fatal("状态统计不是原始登记数", open, abandoned, excluded, err)
					}
					begin = time.Now()
					account, err := s.ReadAlignmentGaps(ctx, "", 200)
					accountLatency = append(accountLatency, time.Since(begin))
					if err != nil || len(account.Value.Items) != 200 || !account.Value.HasMore || account.Value.NextCursor == "" {
						t.Fatal("账号分页不完整", account, err)
					}
					_, ws, private := gapProcessResources(t)
					peakWS = max(peakWS, ws)
					peakPrivate = max(peakPrivate, private)
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					peakHeap = max(peakHeap, m.HeapAlloc)
					peakG = max(peakG, runtime.NumGoroutine())
				}
				sort.Slice(gapLatency, func(i, j int) bool { return gapLatency[i] < gapLatency[j] })
				sort.Slice(msgLatency, func(i, j int) bool { return msgLatency[i] < msgLatency[j] })
				sort.Slice(statusLatency, func(i, j int) bool { return statusLatency[i] < statusLatency[j] })
				sort.Slice(accountLatency, func(i, j int) bool { return accountLatency[i] < accountLatency[j] })
				mediaStarted := time.Now()
				targets, err := st.AlignmentMediaTargets(ctx, "91001", base-(7*24*time.Hour).Milliseconds(), int64(n), int64(n), func(string) bool { return true })
				mediaQuery := time.Since(mediaStarted)
				if err != nil || len(targets) != 1000 {
					t.Fatal("千段媒体目标缺失", len(targets), err)
				}
				if mediaQuery > time.Second {
					t.Fatalf("补扫SQL超过既有1秒预算：%v", mediaQuery)
				}
				for _, db := range set.Each() {
					if _, err = db.Store.Events.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = set.Trace().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
					t.Fatal(err)
				}
				files, dbs := 0, 0
				var total, walRemaining int64
				err = filepath.Walk(p.Root, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if info.Mode().IsRegular() {
						files++
						total += info.Size()
						if strings.HasSuffix(path, ".db") {
							dbs++
						}
						if strings.HasSuffix(path, "-wal") {
							walRemaining += info.Size()
						}
						if strings.Contains(path, ".tmp") {
							return fmt.Errorf("存在无主临时文件")
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				cpuEnd, _, _ := gapProcessResources(t)
				elapsed := time.Since(started)
				// P50 / P95 使用 nearest-rank，200 次的第 100 / 190 个样本。
				t.Logf("P01 backend messages=%d roles=10 conversations=10 gaps=1000 media=1000 repeat=%d reads=200 gapP50=%v gapP95=%v gapMax=%v messageP50=%v messageP95=%v messageMax=%v statusP95=%v accountGapP95=%v mediaQuery=%v elapsed=%v cpu=%v singleCorePercent=%.3f workingSetSamplePeak=%d privateSamplePeak=%d heapSamplePeak=%d goroutinesPeak=%d files=%d databases=%d bytes=%d walSamplePeak=%d walCheckpointRemaining=%d histories=0 downloads=0", n, rep, gapLatency[99], gapLatency[189], gapLatency[199], msgLatency[99], msgLatency[189], msgLatency[199], statusLatency[189], accountLatency[189], mediaQuery, elapsed, cpuEnd-cpuStart, 100*float64(cpuEnd-cpuStart)/float64(elapsed), peakWS, peakPrivate, peakHeap, peakG, files, dbs, total, walPeak, walRemaining)
				if max(gapLatency[189], msgLatency[189], statusLatency[189], accountLatency[189]) > 50*time.Millisecond {
					t.Fatalf("读取超出50ms预算 gap=%v messages=%v status=%v account=%v", gapLatency[189], msgLatency[189], statusLatency[189], accountLatency[189])
				}
				if peakPrivate > 512<<20 {
					t.Fatal("私有提交超过512MiB", peakPrivate)
				}
				if walRemaining > 4<<20 {
					t.Fatal("checkpoint残留超过4MiB", walRemaining)
				}
			})
		}
	}
}
