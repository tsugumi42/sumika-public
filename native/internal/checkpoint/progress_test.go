package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/kernel"
)

// checkpointLimit 是这几个测试整个流程的上限，只防卡死：要真写几十个文件、每个都落盘，机器忙时 96 个文件的恢复能到十几秒
// （2026-11-09 合并队列 10 秒红过）。卡在发布里的改法到这里照样红。
const checkpointLimit = 2 * time.Minute

func TestCheckpointProgressCancelResume(t *testing.T) {
	for _, action := range []string{"recovery", "resume"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t, nil)
			const count = 96
			for i := range count {
				require(t, os.WriteFile(filepath.Join(f.work, fmt.Sprintf("%03d", i)), []byte("before\r\n"), 0600))
			}
			rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
				for i := range count {
					if err := os.WriteFile(filepath.Join(f.work, fmt.Sprintf("%03d", i)), []byte("after\r\n"), 0600); err != nil {
						return err
					}
				}
				return nil
			})
			require(t, err)
			plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
			require(t, err)
			old := publishReplacement
			release := make(chan struct{})
			var once sync.Once
			defer func() { once.Do(func() { close(release) }); publishReplacement = old }()
			var entered atomic.Int32
			publishReplacement = func(root *os.Root, from, to string, mode os.FileMode) error {
				if entered.Add(1) > 1 {
					<-release
				}
				return old(root, from, to, mode)
			}
			ctx, cancel := context.WithTimeout(t.Context(), checkpointLimit)
			defer cancel()
			result := make(chan Record, 1)
			failed := make(chan error, 1)
			in := f.in()
			go func() { r, e := f.s.Restore(ctx, f.owner, in, rec.ID, "step", plan.Version); result <- r; failed <- e }()
			for entered.Load() < 2 {
				select {
				case <-ctx.Done():
					t.Fatal("未进入发布")
				case <-time.After(time.Millisecond):
				}
			}
			var p Progress
			for {
				p, err = f.s.Progress(ctx, f.owner, rec.ID)
				require(t, err)
				if p.Done > 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("未收到进行中完成数", p)
				case <-time.After(time.Millisecond):
				}
			}
			if p.Total != count || p.State != "restoring" {
				t.Fatal("进度总量/阶段不符", p)
			}
			other := f.owner
			other.Conversation += ":topic:other"
			short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
			if _, e := f.s.Cancel(short, other, rec.ID); !errors.Is(e, ErrUnavailable) {
				t.Fatal("错会话可取消", e)
			}
			stop()
			cancelled := make(chan Progress, 1)
			go func() {
				v, e := f.s.Cancel(ctx, f.owner, rec.ID)
				if e != nil {
					failed <- e
				}
				cancelled <- v
			}()
			// 已经在发布中的 I/O 不受 context 中断，取消必须等它们退出。
			select {
			case v := <-cancelled:
				t.Fatal("I/O 未退出就返回取消", v)
			case <-time.After(20 * time.Millisecond):
			}
			once.Do(func() { close(release) })
			p = <-cancelled
			r := <-result
			err = <-failed
			if !errors.Is(err, context.Canceled) || p.State != "partial" || !p.OutcomeSaved || p.Error == "" || p.Done == 0 || p.Done >= count || p.Done != len(r.Done) || p.Total != count || len(p.Remaining)+p.Done != count {
				t.Fatal("取消结局不一致", p, r.Done, err)
			}
			publishReplacement = old
			for i := range count {
				name := fmt.Sprintf("%03d", i)
				want := "after\r\n"
				if slices.Contains(p.Completed, name) {
					want = "before\r\n"
				}
				checkFile(t, filepath.Join(f.work, name), []byte(want))
			}
			cold, e := New(f.data, f.s.opts)
			require(t, e)
			f.s = cold
			got, e := f.s.Progress(ctx, f.owner, r.ID)
			require(t, e)
			if got.Done != p.Done || !got.OutcomeSaved || !slices.Equal(got.Remaining, p.Remaining) {
				t.Fatal("冷开丢失结局", got, p)
			}
			for _, mutate := range []func(*kernel.Scope){
				func(s *kernel.Scope) { s.Scene = "group" },
				func(s *kernel.Scope) { s.Platform, s.Channel = "qq", "qq" },
				func(s *kernel.Scope) { s.Platform, s.Channel, s.Scene = "qq", "qq", "group" },
				func(s *kernel.Scope) { s.CharacterID = "other" },
				func(s *kernel.Scope) { s.AccountID = "other" },
				func(s *kernel.Scope) { s.AuthorityID = "spoof" },
				func(s *kernel.Scope) { s.ActorKind = "human" },
			} {
				bad := f.owner
				mutate(&bad)
				if _, e := f.s.Progress(ctx, bad, r.ID); e == nil {
					t.Fatal("越域读取恢复状态", bad)
				}
				if _, e := f.s.Cancel(ctx, bad, r.ID); e == nil {
					t.Fatal("越域取消恢复", bad)
				}
			}
			next, e := f.s.Preview(ctx, f.owner, r.ID, action)
			require(t, e)
			wantCount := p.Done
			if action == "resume" {
				wantCount = count - p.Done
			}
			if len(next.Files) != wantCount {
				t.Fatal("继续/撤回路径错误", len(next.Files), wantCount)
			}
			_, e = f.s.Restore(ctx, f.owner, f.in(), r.ID, action, next.Version)
			require(t, e)
			want := "after\r\n"
			if action == "resume" {
				want = "before\r\n"
			}
			for i := range count {
				checkFile(t, filepath.Join(f.work, fmt.Sprintf("%03d", i)), []byte(want))
			}
			t.Logf("cancel smoke action=%s completed=%d remaining=%d all=%d", action, p.Done, len(p.Remaining), count)
		})
	}
}

func TestCheckpointProgressWatchKeepsTerminal(t *testing.T) {
	for _, delayedRun := range []bool{false, true} {
		for _, otherScope := range []bool{false, true} {
			t.Run(fmt.Sprintf("delayed-run=%t/other-scope=%t", delayedRun, otherScope), func(t *testing.T) {
				f := newFixture(t, nil)
				require(t, os.WriteFile(filepath.Join(f.work, "a"), []byte("before-a\r\n"), 0600))
				a := f.capture(t, "a", "after-a")
				other := *f
				other.work = t.TempDir()
				if otherScope {
					other.owner.Conversation += ":topic:other"
				}
				require(t, os.WriteFile(filepath.Join(other.work, "b"), []byte("before-b\r\n"), 0600))
				b := other.capture(t, "b", "after-b")
				ctx, cancel := context.WithTimeout(t.Context(), checkpointLimit)
				defer cancel()
				watch, err := f.s.WatchProgress(ctx, f.owner, a.ID)
				require(t, err)
				idle, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				defer once.Do(func() { close(release) })
				type observation struct {
					progress Progress
					err      error
				}
				observed := make(chan observation, 1)
				run := func() {
					var final Progress
					calls := 0
					err := watch.Run(func(p Progress) error {
						calls++
						final = p
						if !delayedRun && calls == 1 && p.State == "idle" {
							close(idle)
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
							return nil
						}
						if p.State != "ready" || p.TargetID != a.ID {
							return fmt.Errorf("恢复 A 的终态丢失或被替换：%+v", p)
						}
						return nil
					})
					observed <- observation{final, err}
				}
				if !delayedRun {
					go run()
					select {
					case <-idle:
					case got := <-observed:
						t.Fatal("首次回调没有进入 idle", got)
					case <-ctx.Done():
						t.Fatal("首次 idle 回调未到达", ctx.Err())
					}
				}
				plan, err := f.s.Preview(ctx, f.owner, a.ID, "step")
				require(t, err)
				inverse, err := f.s.Restore(ctx, f.owner, f.in(), a.ID, "step", plan.Version)
				require(t, err)
				plan, err = f.s.Preview(ctx, other.owner, b.ID, "step")
				require(t, err)
				_, err = f.s.Restore(ctx, other.owner, other.in(), b.ID, "step", plan.Version)
				require(t, err)
				if delayedRun {
					go run()
				} else {
					once.Do(func() { close(release) })
				}
				select {
				case got := <-observed:
					require(t, got.err)
					p := got.progress
					if p.InverseID != inverse.ID || !p.OutcomeSaved || p.Error != "" || p.Done != 1 || p.Total != 1 || !slices.Equal(p.Completed, []string{"a"}) || len(p.Remaining) != 0 {
						t.Fatal("订阅终态与 A 的实际恢复结局不符", p, inverse)
					}
				case <-ctx.Done():
					t.Fatal("订阅没有在 A 真实退出后结束", ctx.Err())
				}
				checkFile(t, filepath.Join(f.work, "a"), []byte("before-a\r\n"))
				checkFile(t, filepath.Join(other.work, "b"), []byte("before-b\r\n"))
			})
		}
	}
}
