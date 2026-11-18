package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckpointOutcomeSaveFailure(t *testing.T) {
	for _, stage := range []string{"pack-create", "metadata", "cancelled-metadata", "committed-cleanup"} {
		t.Run(stage, func(t *testing.T) {
			saved, state, coldDone := false, "interrupted", 0
			if stage == "committed-cleanup" {
				saved, state, coldDone = true, "ready", 1
			}
			f := newFixture(t, nil)
			path := filepath.Join(f.work, "a")
			require(t, os.WriteFile(path, []byte("original\r\n"), 0600))
			rec := f.capture(t, "a", "agent")
			plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
			require(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), checkpointLimit)
			defer cancel()
			watch, err := f.s.WatchProgress(ctx, f.owner, rec.ID)
			require(t, err)
			type observation struct {
				progress Progress
				err      error
			}
			observed := make(chan observation, 1)
			go func() {
				var final Progress
				err := watch.Run(func(p Progress) error { final = p; return nil })
				observed <- observation{final, err}
			}()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			original := publishReplacement
			var locked windows.Handle
			defer func() {
				once.Do(func() { close(release) })
				publishReplacement = original
				if locked != 0 {
					windows.CloseHandle(locked)
				}
			}()
			publishReplacement = func(root *os.Root, from, to string, mode os.FileMode) error {
				if err := original(root, from, to, mode); err != nil {
					return err
				}
				p, err := f.s.Progress(ctx, f.owner, rec.ID)
				if err != nil {
					return err
				}
				name := "ready.bin"
				if stage == "committed-cleanup" {
					name = "data.bin"
				} else if stage != "pack-create" {
					name = "record.next"
				}
				faultPath := filepath.Join(f.data, p.InverseID, name)
				if !saved {
					if err := os.WriteFile(faultPath, nil, 0600); err != nil {
						return err
					}
				}
				if stage != "pack-create" {
					p16, err := windows.UTF16PtrFromString(faultPath)
					if err != nil {
						return err
					}
					share := uint32(windows.FILE_SHARE_READ)
					if saved {
						share |= windows.FILE_SHARE_WRITE
					}
					locked, err = windows.CreateFile(p16, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
					if err != nil {
						return err
					}
				}
				close(entered)
				if stage == "cancelled-metadata" {
					<-release
				}
				return nil
			}
			type outcome struct {
				record Record
				err    error
			}
			result := make(chan outcome, 1)
			in := f.in()
			go func() {
				r, err := f.s.Restore(ctx, f.owner, in, rec.ID, "step", plan.Version)
				result <- outcome{r, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("未到达文件发布后的保存故障点", ctx.Err())
			}
			var cancelled observation
			if stage == "cancelled-metadata" {
				requested := make(chan struct{})
				f.s.progress.Lock()
				op := f.s.progress.active[f.owner]
				originalCancel := op.cancel
				op.cancel = func() { originalCancel(); close(requested) }
				f.s.progress.Unlock()
				stopped := make(chan observation, 1)
				go func() { p, err := f.s.Cancel(ctx, f.owner, rec.ID); stopped <- observation{p, err} }()
				<-requested
				select {
				case p := <-stopped:
					t.Fatal("保存故障收尾尚未退出就返回 Cancel", p)
				default:
				}
				once.Do(func() { close(release) })
				cancelled = <-stopped
				require(t, cancelled.err)
			}
			out := <-result
			if out.err == nil || out.record.ID == "" || out.record.Status != state || !reflect.DeepEqual(out.record.Done, []string{"a"}) {
				t.Fatalf("结局保存失败冒充完成：state=%s done=%v err=%v", out.record.Status, out.record.Done, out.err)
			}
			checkFile(t, path, []byte("original\r\n"))
			p, err := f.s.Progress(ctx, f.owner, rec.ID)
			require(t, err)
			seen := <-observed
			require(t, seen.err)
			if p.State != state || p.OutcomeSaved != saved || p.Error != out.err.Error() || p.InverseID != out.record.ID || p.Done != 1 || p.Total != 1 || len(p.Remaining) != 0 || !reflect.DeepEqual(p, seen.progress) {
				t.Fatal("返回值、查询和订阅结局不一致", p, seen.progress)
			}
			if stage == "cancelled-metadata" {
				if !errors.Is(out.err, context.Canceled) || !reflect.DeepEqual(cancelled.progress, p) {
					t.Fatal("Cancel 丢失保存失败结局", cancelled.progress, p, out.err)
				}
			} else if _, err := f.s.Cancel(ctx, f.owner, rec.ID); !errors.Is(err, ErrUnavailable) {
				t.Fatal("已退出操作仍可取消", err)
			}
			if locked != 0 {
				require(t, windows.CloseHandle(locked))
				locked = 0
			}
			cold, err := New(f.data, f.s.opts)
			require(t, err)
			coldProgress, err := cold.Progress(ctx, f.owner, out.record.ID)
			require(t, err)
			if coldProgress.State != p.State || coldProgress.OutcomeSaved != saved || coldProgress.InverseID != p.InverseID || coldProgress.Done != coldDone || coldProgress.Total != 1 {
				t.Fatal("冷开把未保存结局当作完成", coldProgress, p)
			}
			checkFile(t, path, []byte("original\r\n"))
			mode := "recovery"
			if saved {
				mode = "step"
			}
			_, err = cold.Preview(ctx, f.owner, out.record.ID, mode)
			require(t, err)
		})
	}
}
