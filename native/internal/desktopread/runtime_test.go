package desktopread

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"sumika-native/internal/alert"
	"sumika-native/internal/api"
)

func TestOverviewRuntimeAndOpenAlerts(t *testing.T) {
	t.Parallel()
	s, _ := fixture(t)
	hub := alert.New()
	hub.Raise(alert.Alert{Key: "connection", Title: "编造断线提醒", Body: "尚未连接"})
	s.runtime = NewRuntime(api.RuntimeView{Mode: "archive", Phase: "ready"}, hub)
	a, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	runtime, e := s.WatchRuntime(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.Close()
	initialA, initialB, initialRuntime := next(t, a), next(t, b), next(t, runtime)
	for _, v := range []api.RuntimeView{initialA.Value.Runtime, initialB.Value.Runtime, initialRuntime.Value} {
		if v.Mode != "archive" || v.Phase != "ready" || len(v.Alerts.Items) != 1 || v.Alerts.Items[0].Key != "connection" {
			t.Fatalf("初始宿主或未恢复提醒缺失: %+v", v)
		}
	}
	s.runtime.Publish(api.RuntimeView{Mode: "receive", Phase: "connecting", Account: "90001", Connection: "transport_connected"})
	for _, v := range []api.RuntimeView{next(t, a).Value.Runtime, next(t, b).Value.Runtime, next(t, runtime).Value} {
		if v.Mode != "receive" || v.Phase != "connecting" || v.Connection != "transport_connected" || v.Account != "***01" {
			t.Fatalf("宿主更新丢失、冒充在线或泄露账号: %+v", v)
		}
	}
	hub.Clear("connection", alert.Note{Title: "已恢复"})
	for _, v := range []api.RuntimeView{next(t, a).Value.Runtime, next(t, b).Value.Runtime, next(t, runtime).Value} {
		if len(v.Alerts.Items) != 0 {
			t.Fatalf("提醒恢复未更新: %+v", v)
		}
	}
	a.Close()
	b.Close()
	runtime.Close()
	runtime.Close()
	s.runtime.Publish(api.RuntimeView{Mode: "archive", Phase: "ready"})
	s.runtime.mu.Lock()
	listeners := len(s.runtime.subs)
	s.runtime.mu.Unlock()
	if listeners != 0 {
		t.Fatalf("取消后仍登记 %d 个宿主监听", listeners)
	}
}

func TestRuntimeRegistrationDuringInitialRead(t *testing.T) {
	t.Parallel()
	for range 20 {
		r := NewRuntime(api.RuntimeView{Mode: "archive", Phase: "ready"}, nil)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		var calls atomic.Int64
		sub := watch(context.Background(), nil, r, func(ctx context.Context) (api.RuntimeView, error) {
			calls.Add(1)
			v := r.snapshot()
			once.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			return v, nil
		})
		defer sub.Close()
		<-entered
		r.Publish(api.RuntimeView{Mode: "receive", Phase: "connecting"})
		close(release)
		first, second := next(t, sub), next(t, sub)
		if first.Version != 1 || first.Value.Mode != "archive" || second.Version <= first.Version || second.Value.Mode != "receive" {
			sub.Close()
			t.Fatalf("宿主初始读取交错丢失: %+v %+v", first, second)
		}
		sub.Close()
		before := calls.Load()
		r.Publish(api.RuntimeView{Mode: "archive"})
		if calls.Load() != before {
			t.Fatal("关闭后仍读宿主")
		}
	}
}

func TestMissingRuntimeSourceIsExplicit(t *testing.T) {
	t.Parallel()
	s, _ := fixture(t)
	sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	v := next(t, sub)
	if v.Value.Runtime.ReasonCode != "runtime_not_attached" || v.Value.Runtime.Connection != "" {
		t.Fatal("缺宿主来源时伪造连接状态")
	}
	found := false
	for _, c := range v.Value.Capabilities {
		if c.Name == "runtime" && c.State == api.Unsupported {
			found = true
		}
	}
	if !found {
		t.Fatal("缺宿主来源未标 unsupported")
	}
	if _, e := s.WatchRuntime(context.Background()); e == nil {
		t.Fatal("缺宿主来源仍声明可订阅")
	}
}
