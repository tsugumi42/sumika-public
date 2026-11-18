package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/kernel"
)

func (f *fixture) run(t *testing.T, c Capture, body func() error) (Record, error) {
	t.Helper()
	c.Invocation, c.Owner, c.CWD = f.in(), f.owner, f.work
	if c.Tool == "" {
		c.Tool = "write"
	}
	return f.s.Run(t.Context(), c, body)
}

// 目标文件超过单文件上限：照常写入，记录标 skipped，不能预览回退。
func TestRunSkipsWhenTargetTooBig(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxFileBytes = 8 })
	path := filepath.Join(f.work, "big")
	require(t, os.WriteFile(path, []byte("超过八字节的原文"), 0600))
	ran := false
	rec, err := f.run(t, Capture{Paths: []string{path}}, func() error { ran = true; return os.WriteFile(path, []byte("new"), 0600) })
	require(t, err)
	if !ran || rec.Status != "skipped" || rec.ID == "" || !strings.Contains(rec.SkipReason, "检查点容量或扫描预算不足") {
		t.Fatalf("没有照常执行并留 skipped 记录：ran=%v %+v", ran, rec)
	}
	checkFile(t, path, []byte("new"))
	if _, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step"); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("skipped 记录给出了回退计划或错误不对：%v", err)
	}
	list, err := f.s.List(t.Context(), f.owner)
	require(t, err)
	if len(list) != 1 || list[0].Status != "skipped" {
		t.Fatalf("列表看不出没有快照：%+v", list)
	}
	notices, err := f.s.Notices(t.Context(), f.owner)
	require(t, err)
	if len(notices) != 0 {
		t.Fatalf("skipped 记录不该产生恢复通知：%v", notices)
	}
	cold, err := New(f.data, f.s.opts) // 冷启动能读回
	require(t, err)
	if list, err = cold.List(t.Context(), f.owner); err != nil || len(list) != 1 {
		t.Fatalf("冷启动读不回 skipped：%v %v", list, err)
	}
}

// 整目录超过文件数预算（库层仍支持整目录捕获）：照常执行；前面的正常记录不受后面 skipped 记录影响。
func TestSkippedRecordDoesNotBreakChain(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxFiles = 3 })
	first := f.capture(t, "a", "agent")
	for _, name := range []string{"x1", "x2", "x3", "x4"} {
		require(t, os.WriteFile(filepath.Join(f.work, name), nil, 0600))
	}
	ran := false
	rec, err := f.run(t, Capture{Tool: "powershell"}, func() error { ran = true; return nil })
	require(t, err)
	if !ran || rec.Status != "skipped" {
		t.Fatalf("大目录没有降级：ran=%v %+v", ran, rec)
	}
	for _, mode := range []string{"step", "turn"} {
		plan, err := f.s.Preview(t.Context(), f.owner, first.ID, mode)
		if err != nil || len(plan.Files) != 1 {
			t.Fatalf("%s：skipped 记录挡住了前面的回退：%+v %v", mode, plan, err)
		}
	}
	plan, err := f.s.Preview(t.Context(), f.owner, first.ID, "step")
	require(t, err)
	_, err = f.s.Restore(t.Context(), f.owner, f.in(), first.ID, "step", plan.Version)
	require(t, err)
	if _, err := os.Stat(filepath.Join(f.work, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没有恢复：%v", err)
	}
}

// 身份和授权失败照旧拦住，Run、Skip、Admit 都一样；工具不得执行。
func TestAdmitStillBlocks(t *testing.T) {
	f := newFixture(t, nil)
	bad := []func(*kernel.Scope){func(s *kernel.Scope) { s.AuthorityID = "spoof" }, func(s *kernel.Scope) { s.Scene = "group" }, func(s *kernel.Scope) { s.ActorKind = "human" }, func(s *kernel.Scope) { s.Platform = "qq"; s.Channel = "qq" }}
	for _, mutate := range bad {
		scope := f.owner
		mutate(&scope)
		c := Capture{Invocation: f.in(), Owner: scope, CWD: f.work, Tool: "powershell", Paths: []string{filepath.Join(f.work, "a")}}
		c.Invocation.Scope = scope
		ran := false
		if err := f.s.Admit(t.Context(), c); err == nil {
			t.Fatalf("Admit 放过了 %+v", scope)
		}
		if _, err := f.s.Run(t.Context(), c, func() error { ran = true; return nil }); err == nil || ran {
			t.Fatalf("Run 放过了 %+v", scope)
		}
		if _, err := f.s.Skip(t.Context(), c, "看不清", func() error { ran = true; return nil }); err == nil || ran {
			t.Fatalf("Skip 放过了 %+v", scope)
		}
	}
}

// Skip 过关后照常执行并留 skipped 记录；存储装不下记录时仍执行，只是没有记录。
func TestSkipAndNoRoomForRecord(t *testing.T) {
	f := newFixture(t, nil)
	ran := false
	c := Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}
	rec, err := f.s.Skip(t.Context(), c, "看不清这条命令会动哪些文件", func() error { ran = true; return nil })
	require(t, err)
	if !ran || rec.Status != "skipped" || rec.ID == "" || rec.SkipReason != "看不清这条命令会动哪些文件" {
		t.Fatalf("Skip 不符：%+v", rec)
	}
	tight := newFixture(t, func(o *Options) { o.MaxStoreBytes = 1 << 10 })
	ran = false
	rec, err = tight.run(t, Capture{Tool: "powershell"}, func() error { ran = true; return nil })
	require(t, err)
	if !ran || rec.Status != "skipped" || rec.ID != "" {
		t.Fatalf("存储不足时没有照常执行：ran=%v %+v", ran, rec)
	}
	if list, err := tight.s.List(t.Context(), tight.owner); err != nil || len(list) != 0 {
		t.Fatalf("存储不足不该留记录：%v %v", list, err)
	}
}

func (f *fixture) ids(t *testing.T) map[string]string {
	t.Helper()
	list, err := f.s.List(t.Context(), f.owner)
	require(t, err)
	out := map[string]string{}
	for _, r := range list {
		out[r.ID] = r.Status
	}
	return out
}

// 清理：过期的 interrupted 被清；超额删最旧；无记录的孤儿目录启动时清掉；全被钉住腾不出时报 ErrLimit。
func TestCheckpointCleanup(t *testing.T) {
	t.Run("过期的 interrupted 被清", func(t *testing.T) {
		now := time.Unix(1_800_000_000, 0)
		f := newFixture(t, func(o *Options) { o.MaxFileBytes = 8; o.Now = func() time.Time { return now } })
		path := filepath.Join(f.work, "a")
		// 执行后文件变大：后态拍不成，记录停在 interrupted。
		rec, err := f.run(t, Capture{Paths: []string{path}}, func() error { return os.WriteFile(path, []byte("超过八字节的新内容"), 0600) })
		if err == nil || rec.Status != "interrupted" {
			t.Fatalf("没造出 interrupted：%+v %v", rec, err)
		}
		if ids := f.ids(t); ids[rec.ID] != "interrupted" {
			t.Fatalf("记录不在：%v", ids)
		}
		now = now.Add(31 * 24 * time.Hour)
		f.capture(t, "b", "x")
		if _, ok := f.ids(t)[rec.ID]; ok {
			t.Fatal("过期的 interrupted 没被清")
		}
	})
	t.Run("超额删最旧", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.MaxRecords = 2 })
		a := f.capture(t, "a", "1")
		b := f.capture(t, "b", "1")
		c := f.capture(t, "c", "1")
		ids := f.ids(t)
		if _, ok := ids[a.ID]; ok || ids[b.ID] == "" || ids[c.ID] == "" || len(ids) != 2 {
			t.Fatalf("没删最旧的：%v", ids)
		}
	})
	t.Run("启动清孤儿目录", func(t *testing.T) {
		f := newFixture(t, nil)
		orphan := filepath.Join(f.data, strings.Repeat("a", 32))
		require(t, os.MkdirAll(orphan, 0700))
		require(t, os.WriteFile(filepath.Join(orphan, "data.bin"), []byte("残留"), 0600))
		if _, err := New(f.data, f.s.opts); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("孤儿目录没清：%v", err)
		}
	})
	t.Run("启动清过期记录", func(t *testing.T) {
		now := time.Unix(1_800_000_000, 0)
		f := newFixture(t, func(o *Options) { o.Now = func() time.Time { return now } })
		old := f.capture(t, "a", "1")
		now = now.Add(31 * 24 * time.Hour)
		cold, err := New(f.data, f.s.opts)
		require(t, err)
		f.s = cold
		if _, ok := f.ids(t)[old.ID]; ok {
			t.Fatal("启动时没清过期记录")
		}
	})
	t.Run("全被钉住时腾不出", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.MaxRecords = 2 })
		a := f.capture(t, "a", "1")
		b := f.capture(t, "b", "1")
		root, leave, err := f.s.enter(t.Context())
		require(t, err)
		defer leave()
		records, used, err := f.s.records(root, false)
		require(t, err)
		if err := f.s.reserve(root, records, used, 1, map[string]bool{a.ID: true, b.ID: true}); !errors.Is(err, ErrLimit) {
			t.Fatalf("钉住的记录被清或没报 ErrLimit：%v", err)
		}
		if after, _, err := f.s.records(root, false); err != nil || len(after) != 2 {
			t.Fatalf("钉住的记录丢了：%v %v", after, err)
		}
	})
}
