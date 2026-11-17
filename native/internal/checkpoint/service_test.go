package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/kernel"
)

type fixture struct {
	s          *Service
	work, data string
	owner      kernel.Scope
	next       int
}

func newFixture(t *testing.T, edit func(*Options)) *fixture {
	t.Helper()
	f := &fixture{work: filepath.Join(t.TempDir(), "work"), data: filepath.Join(t.TempDir(), "snapshots")}
	require(t, os.MkdirAll(f.work, 0700))
	f.owner = kernel.Scope{CharacterID: "invented", Conversation: "console:invented", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "fixture-desktop", Scene: "private", ActorKind: "owner", ActorID: "fixture-owner", AuthorityID: "fixture-grant", AuthorityRevision: 1}
	opts := Options{Authorize: func(_ context.Context, s kernel.Scope, _ string) error {
		if s.AuthorityID != "fixture-grant" || s.AuthorityRevision != 1 {
			return ErrUnavailable
		}
		return nil
	}}
	if edit != nil {
		edit(&opts)
	}
	var err error
	f.s, err = New(f.data, opts)
	require(t, err)
	return f
}
func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) in() kernel.Invocation {
	f.next++
	return kernel.Invocation{Scope: f.owner, RootID: "fixture-root", RunID: "fixture-run", ToolID: strings.Repeat("t", f.next), Kind: kernel.KindForeground}
}
func (f *fixture) capture(t *testing.T, name, body string) Record {
	t.Helper()
	path := filepath.Join(f.work, name)
	rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{path}}, func() error { return os.WriteFile(path, []byte(body), 0600) })
	require(t, err)
	return rec
}
func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	require(t, err)
	if !bytes.Equal(got, want) {
		t.Fatalf("原始字节不符：got=%q want=%q", got, want)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	f := newFixture(t, nil)
	original := []byte{0, 255, '\r', '\n', 'A'}
	require(t, os.WriteFile(filepath.Join(f.work, "旧文件🌙.bin"), original, 0600))
	first := f.capture(t, "旧文件🌙.bin", "修改\r\n")
	f.capture(t, "新文件.txt", "")
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	f.s = cold
	plan, err := f.s.Preview(t.Context(), f.owner, first.ID, "turn")
	require(t, err)
	if len(plan.Files) != 2 {
		t.Fatalf("整回合计划遗漏新建/改写：%+v", plan)
	}
	inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), first.ID, "turn", plan.Version)
	require(t, err)
	checkFile(t, filepath.Join(f.work, "旧文件🌙.bin"), original)
	if _, err := os.Stat(filepath.Join(f.work, "新文件.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("新文件未撤销：%v", err)
	}
	undo, err := f.s.Preview(t.Context(), f.owner, inverse.ID, "step")
	require(t, err)
	_, err = f.s.Restore(t.Context(), f.owner, f.in(), inverse.ID, "step", undo.Version)
	require(t, err)
	checkFile(t, filepath.Join(f.work, "旧文件🌙.bin"), []byte("修改\r\n"))
	checkFile(t, filepath.Join(f.work, "新文件.txt"), nil)
	if _, err := f.s.Restore(t.Context(), f.owner, f.in(), inverse.ID, "step", undo.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧审批被重复使用：%v", err)
	}
}

func TestCheckpointConflictsAndOwners(t *testing.T) {
	t.Run("审批后变化", func(t *testing.T) {
		f := newFixture(t, nil)
		rec := f.capture(t, "a", "agent")
		plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
		require(t, err)
		require(t, os.WriteFile(filepath.Join(f.work, "a"), []byte("人工修改"), 0600))
		if _, err := f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version); !errors.Is(err, ErrConflict) {
			t.Fatalf("覆盖了人工修改：%v", err)
		}
		checkFile(t, filepath.Join(f.work, "a"), []byte("人工修改"))
	})
	t.Run("四场合和冒充", func(t *testing.T) {
		f := newFixture(t, nil)
		rec := f.capture(t, "a", "agent")
		for _, mutate := range []func(*kernel.Scope){func(s *kernel.Scope) { s.Scene = "group" }, func(s *kernel.Scope) { s.Platform = "qq"; s.Channel = "qq" }, func(s *kernel.Scope) { s.Platform = "qq"; s.Channel = "qq"; s.Scene = "group" }, func(s *kernel.Scope) { s.ActorKind = "human"; s.ActorID = "owner" }, func(s *kernel.Scope) { s.AuthorityRevision = 0 }, func(s *kernel.Scope) { s.AuthorityID = "spoof" }, func(s *kernel.Scope) { s.CharacterID = "other" }, func(s *kernel.Scope) { s.AccountID = "other" }, func(s *kernel.Scope) { s.Conversation = "console:invented:topic:other" }} {
			scope := f.owner
			mutate(&scope)
			if _, err := f.s.Preview(t.Context(), scope, rec.ID, "step"); err == nil {
				t.Fatalf("越权得到计划：%+v", scope)
			}
		}
	})
	t.Run("别的执行域即使回到同字节也不吞", func(t *testing.T) {
		f := newFixture(t, nil)
		rec := f.capture(t, "a", "first")
		f.owner.Conversation = "console:invented:topic:other"
		f.capture(t, "a", "other")
		f.capture(t, "a", "first")
		f.owner.Conversation = "console:invented"
		if _, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step"); !errors.Is(err, ErrConflict) {
			t.Fatalf("另一域的 ABA 变化被回退：%v", err)
		}
	})
}

func TestCheckpointLimitsRecoveryAndCorruption(t *testing.T) {
	t.Run("超限前不执行及后态失败保留", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.MaxFileBytes = 8; o.MaxScanBytes = 16 })
		path := filepath.Join(f.work, "a")
		require(t, os.WriteFile(path, []byte("超过八字节的原文"), 0600))
		ran := false
		before, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{path}}, func() error { ran = true; return nil })
		if err != nil || !ran || before.Status != "skipped" {
			t.Fatalf("前态超限应照常执行并标 skipped：ran=%v %+v err=%v", ran, before, err)
		}
		require(t, os.WriteFile(path, []byte("old"), 0600))
		rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{path}}, func() error { return os.WriteFile(path, []byte("oversized-new"), 0600) })
		if !errors.Is(err, ErrLimit) || rec.Status != "interrupted" {
			t.Fatalf("后态超限冒充成功：%+v %v", rec, err)
		}
		require(t, os.WriteFile(path, []byte("new"), 0600))
		cold, err := New(f.data, f.s.opts)
		require(t, err)
		f.s = cold
		if _, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step"); !errors.Is(err, ErrConflict) {
			t.Fatalf("缺后态可直接恢复：%v", err)
		}
		plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "recovery")
		require(t, err)
		_, err = f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "recovery", plan.Version)
		require(t, err)
		checkFile(t, path, []byte("old"))
	})
	t.Run("损坏包不写及不当孤儿删除", func(t *testing.T) {
		f := newFixture(t, nil)
		require(t, os.WriteFile(filepath.Join(f.work, "a"), []byte("old"), 0600))
		rec := f.capture(t, "a", "new")
		pack := filepath.Join(f.data, rec.ID, rec.Pack)
		file, err := os.OpenFile(pack, os.O_WRONLY, 0600)
		require(t, err)
		_, err = file.WriteAt([]byte("BAD"), rec.Blobs[rec.Before["a"].Hash].Offset)
		require(t, err)
		require(t, file.Close())
		if _, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step"); err == nil {
			t.Fatal("损坏对象仍可恢复")
		}
		checkFile(t, filepath.Join(f.work, "a"), []byte("new"))
		require(t, os.Remove(pack))
		if _, err := New(f.data, f.s.opts); err == nil {
			t.Fatal("缺包被当作无记录孤儿")
		}
		if _, err := os.Stat(filepath.Join(f.data, rec.ID, "record.json")); err != nil {
			t.Fatal("有效恢复元数据被删", err)
		}
	})
	t.Run("空目录预算与轮换", func(t *testing.T) {
		now := time.Unix(100, 0)
		f := newFixture(t, func(o *Options) { o.MaxEntries = 2; o.MaxRecords = 2; o.Now = func() time.Time { return now } })
		for _, name := range []string{"d1", "d2", "d3"} {
			require(t, os.Mkdir(filepath.Join(f.work, name), 0700))
		}
		ran := false
		skipped, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error { ran = true; return nil })
		if err != nil || !ran || skipped.Status != "skipped" {
			t.Fatalf("空目录超预算应照常执行并标 skipped：%v %+v %v", ran, skipped, err)
		}
		first := f.capture(t, "a", "1")
		f.capture(t, "a", "2")
		last := f.capture(t, "a", "3")
		records, err := f.s.List(t.Context(), f.owner)
		require(t, err)
		if len(records) != 2 {
			t.Fatalf("轮换条数不符：%d", len(records))
		}
		if _, err := f.s.Preview(t.Context(), f.owner, first.ID, "step"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("已轮换记录仍可用：%v", err)
		}
		if _, err := f.s.Preview(t.Context(), f.owner, last.ID, "turn"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("首条已轮换却缩小整回合承诺：%v", err)
		}
	})
	t.Run("过期完整记录不可恢复", func(t *testing.T) {
		now := time.Unix(100, 0)
		f := newFixture(t, func(o *Options) { o.Now = func() time.Time { return now } })
		record := f.capture(t, "a", "agent")
		now = now.Add(31 * 24 * time.Hour)
		if _, err := f.s.Preview(t.Context(), f.owner, record.ID, "step"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("过期记录仍可恢复：%v", err)
		}
	})
	for _, kind := range []string{"files", "bytes", "store", "depth"} {
		t.Run("预算-"+kind, func(t *testing.T) {
			f := newFixture(t, func(o *Options) {
				switch kind {
				case "files":
					o.MaxFiles = 1
				case "bytes":
					o.MaxScanBytes = 15
				case "store":
					o.MaxStoreBytes = 2 * metadataLimit
				case "depth":
					o.MaxDepth = 1
				}
			})
			for _, name := range []string{"a", "b"} {
				require(t, os.WriteFile(filepath.Join(f.work, name), []byte("12345678"), 0600))
			}
			if kind == "depth" {
				require(t, os.MkdirAll(filepath.Join(f.work, "d", "deep"), 0700))
			}
			ran := false
			record, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error { ran = true; return nil })
			if err != nil || !ran || record.Status != "skipped" || record.SkipReason == "" {
				t.Fatalf("超预算应照常执行并标 skipped：kind=%s ran=%v %+v err=%v", kind, ran, record, err)
			}
		})
	}
	t.Run("超额时未知结局也被轮换", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.MaxRecords = 1; o.MaxFileBytes = 8 })
		path := filepath.Join(f.work, "a")
		require(t, os.WriteFile(path, []byte("old"), 0600))
		interrupted, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{path}}, func() error { return os.WriteFile(path, []byte("oversized-new"), 0600) })
		if !errors.Is(err, ErrLimit) || interrupted.Status != "interrupted" {
			t.Fatal("未生成真实未知后态", interrupted, err)
		}
		other := filepath.Join(f.work, "b")
		ran := false
		_, err = f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{other}}, func() error { ran = true; return os.WriteFile(other, []byte("new"), 0600) })
		if err != nil || !ran {
			t.Fatal("新变更应照常执行", ran, err)
		}
		records, err := f.s.List(t.Context(), f.owner)
		require(t, err)
		if len(records) != 1 || records[0].ID == interrupted.ID || records[0].Status != "ready" {
			t.Fatal("超额时最旧的 interrupted 应被清出", records)
		}
	})
}

func TestCheckpointRunWaitsForExit(t *testing.T) {
	f := newFixture(t, nil)
	path := filepath.Join(f.work, "late.txt")
	require(t, os.WriteFile(path, []byte("old"), 0600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, exit, exited, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(exit) })
	defer release()
	type runResult struct {
		record Record
		err    error
	}
	result := make(chan runResult, 1)
	go func() {
		record, err := f.s.Run(ctx, Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{path}}, func() error {
			close(entered)
			defer close(exited)
			<-exit
			return errors.Join(os.WriteFile(path, []byte("after-real-exit"), 0600), ctx.Err())
		})
		result <- runResult{record, err}
		close(finished)
	}()
	select {
	case <-entered:
	case early := <-result:
		t.Fatalf("没有执行工具：%+v", early)
	}
	defer func() { release(); <-exited; <-finished }()
	cancel()
	blocked, stop := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer stop()
	if _, err := f.s.List(blocked, f.owner); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("取消后实际退出前释放了服务门：%v", err)
	}
	release()
	got := <-result
	if !errors.Is(got.err, context.Canceled) || got.record.Status != "ready" {
		t.Fatalf("实际退出结局不符：%+v %v", got.record, got.err)
	}
	checkFile(t, path, []byte("after-real-exit"))
	plan, err := f.s.Preview(t.Context(), f.owner, got.record.ID, "step")
	require(t, err)
	if len(plan.Files) != 1 || plan.Files[0].After.Hash != digestBytes([]byte("after-real-exit")) || plan.Files[0].Before.Hash != digestBytes([]byte("old")) {
		t.Fatal("取消后未采集真实最终字节", plan)
	}
}
