package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// A04：archive 不修写 active；receive 重启把它结算为 interrupted，最新表始终最多两行。
func TestAlignmentSnapshotRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state, scope string
		active             bool
	}{
		{"完成", "completed", "account", false}, {"失败", "failed", "account", false}, {"单段", "completed", "gap", false}, {"中断", "running", "account", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			st, err := a.set.For("qq")
			if err != nil {
				t.Fatal(err)
			}
			for i := range 5 {
				r := store.AlignmentRecord{JobID: "编造旧任务", Scope: "account", Trigger: "manual", State: "completed", StartedMS: int64(i + 1), EndedMS: int64(i + 2), Messages: "completed", MediaJSON: `{"State":"skipped"}`}
				if err = st.SaveAlignment(context.Background(), testAccount, r); err != nil {
					t.Fatal(err)
				}
			}
			r := store.AlignmentRecord{JobID: "编造目标任务", Scope: tc.scope, GapID: 7, Trigger: "retry", State: tc.state, StartedMS: 100, EndedMS: 200, Inserted: 7, Duplicates: 2, Messages: "completed", MediaJSON: `{"State":"skipped"}`}
			if tc.state == "failed" {
				r.Reason = "io"
			}
			if tc.active {
				r.EndedMS = 0
				r.Messages = "running"
				r.MediaJSON = `{"Scope":"connection-7d+round-new","State":"waiting","Queued":1,"Running":1}`
			}
			if err = st.SaveAlignment(context.Background(), testAccount, r); err != nil {
				t.Fatal(err)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			opt := e.options()
			opt.Mode = Archive
			opt.Account = ""
			archive, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := archive.Desktop().Reader.WatchAlignment(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var view api.AlignmentView
			select {
			case snap := <-sub.Updates():
				if snap.Err != nil {
					t.Fatal(snap.Err)
				}
				view = snap.Value
			case <-time.After(10 * time.Second):
				t.Fatal("归档对齐快照没有返回")
			}
			sub.Close()
			if view.LastAttempt == nil || view.LastAttempt.ID != r.JobID || view.LastAttempt.Scope != tc.scope || view.LastAttempt.Inserted != 7 || view.LastAttempt.Duplicates != 2 || view.LastAttempt.StartedMS != 100 {
				t.Fatalf("重启后口径改变 %+v", view)
			}
			want := tc.state
			if tc.active {
				want = "canceled"
			}
			if view.LastAttempt.State != want || view.CurrentJob != nil || view.CanRun || view.ReasonCode != "readonly" {
				t.Fatalf("归档冒充完成或可写 %+v", view)
			}
			if tc.active && (view.Media.State != "unchecked" || view.Media.Queued != 0 || view.Media.Running != 0 || view.Media.Unchecked != 2) {
				t.Fatal("断电任务仍冒充正在下载", view.Media)
			}
			ro, _ := archive.set.For("qq")
			active, last, err := ro.ReadAlignment(context.Background(), testAccount)
			if err != nil {
				t.Fatal(err)
			}
			if tc.active {
				if active == nil || active.State != "running" || active.MediaJSON != r.MediaJSON || last.JobID == r.JobID {
					t.Fatal("archive修写遗留active", active, last)
				}
			} else if last.EndedMS != 200 {
				t.Fatal("最近结束时间被替换", last)
			}
			var n int
			if err = ro.Events.QueryRow(`SELECT count(*) FROM channel_alignment_latest WHERE account=?`, testAccount).Scan(&n); err != nil || n > 2 {
				t.Fatal("无限任务历史", n, err)
			}
			archive.Desktop().Close()
			archive.Close()
			if tc.active {
				receive, err := Open(context.Background(), e.options())
				if err != nil {
					t.Fatal(err)
				}
				defer receive.Close()
				db, _ := receive.set.For("qq")
				active, last, err = db.ReadAlignment(context.Background(), testAccount)
				if err != nil || active != nil || last.State != "canceled" || last.Reason != "interrupted" || last.EndedMS != e.now.UnixMilli() {
					t.Fatal("receive未结算断电任务", active, last, err)
				}
				var media api.AlignmentMedia
				if err = json.Unmarshal([]byte(last.MediaJSON), &media); err != nil || media.State != "unchecked" || media.Queued != 0 || media.Running != 0 || media.Unchecked != 2 {
					t.Fatal("重启持久快照仍在下载", media, err)
				}
			}
		})
	}
	t.Run("损坏媒体快照不冒充未检查成功", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		a := e.open()
		defer a.Close()
		st, err := a.set.For("qq")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		r := store.AlignmentRecord{JobID: "编造损坏快照任务", Scope: "account", Trigger: "manual", State: "running", Messages: "running", MediaJSON: `{"State":"waiting",`}
		if err = st.SaveAlignment(ctx, testAccount, r); err != nil {
			t.Fatal(err)
		}
		if _, err = a.alignment(ctx); err == nil {
			t.Fatal("损坏快照被当作成功读取")
		}
		if err = a.interruptAlignment(ctx, st); err == nil {
			t.Fatal("重启结算吞掉损坏快照")
		}
		active, _, err := st.ReadAlignment(ctx, testAccount)
		if err != nil || active == nil || active.State != "running" || active.MediaJSON != r.MediaJSON {
			t.Fatal("解析失败修写原始快照", active, err)
		}
	})
}
