package main

// status 里的连接和没恢复的提醒（ctl.go 的 runtimeOf）：提醒标题和正文里的长数字要打码（规格 §二.8、§三第四条），
// 取不到时回空连接和空数组（不是 null）。用一个只实现 WatchRuntime 的假读取器，不起宿主。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/api"
)

type fakeRuntimeReader struct {
	api.Reader // 其余方法不会被调到：调到就是空指针，测试直接红
	view       api.RuntimeView
	err        error
	closeFirst bool // 订阅一上来就关掉（通道关闭，没有快照）
	noSnapshot bool // 订阅有，但不发快照
}

type fakeRuntimeSub struct {
	ch chan api.Snapshot[api.RuntimeView]
}

func (s fakeRuntimeSub) Updates() <-chan api.Snapshot[api.RuntimeView] { return s.ch }
func (s fakeRuntimeSub) Close()                                        {}

func (f fakeRuntimeReader) WatchRuntime(context.Context) (api.Subscription[api.RuntimeView], error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan api.Snapshot[api.RuntimeView], 1)
	switch {
	case f.closeFirst:
		close(ch)
	case f.noSnapshot:
	default:
		ch <- api.Snapshot[api.RuntimeView]{Version: 1, Result: api.Result[api.RuntimeView]{State: api.Ready, Value: f.view}}
	}
	return fakeRuntimeSub{ch}, nil
}

func TestRuntimeOfMasksLongDigitsInAlerts(t *testing.T) {
	t.Parallel()
	r := fakeRuntimeReader{view: api.RuntimeView{
		Connection: "已连接",
		Alerts: api.List[api.RuntimeAlert]{Items: []api.RuntimeAlert{
			{Key: "a", Title: "QQ 90001 掉线了", Body: "号 123456789 的连接断开，群 300 和 12345 都收不到"},
			{Key: "b", Title: "没有数字的提醒", Body: "短数字 1234 不打码"},
		}},
	}}
	conn, alerts := runtimeOf(context.Background(), r)
	if conn != "已连接" || len(alerts) != 2 {
		t.Fatalf("%q %+v", conn, alerts)
	}
	if alerts[0].Title != "QQ ***01 掉线了" || alerts[0].Body != "号 ***89 的连接断开，群 300 和 ***45 都收不到" {
		t.Errorf("长数字（5 位及以上）要打码：%+v", alerts[0])
	}
	if alerts[1].Title != "没有数字的提醒" || alerts[1].Body != "短数字 1234 不打码" {
		t.Errorf("不该动的被动了：%+v", alerts[1])
	}
	for _, a := range alerts {
		for _, leak := range []string{"90001", "123456789", "12345"} {
			if strings.Contains(a.Title+a.Body, leak) {
				t.Errorf("提醒里还有 %q：%+v", leak, a)
			}
		}
	}
}

// 取不到：空连接、空数组（--json 里是 [] 不是 null）
func TestRuntimeOfWhenNothingToRead(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		r   fakeRuntimeReader
		ctx context.Context
	}{
		"订阅报错":     {fakeRuntimeReader{err: errors.New("读不了")}, context.Background()},
		"通道关了没有快照": {fakeRuntimeReader{closeFirst: true}, context.Background()},
		"上下文已取消":   {fakeRuntimeReader{noSnapshot: true}, cancelled},
	} {
		conn, alerts := runtimeOf(c.ctx, c.r)
		if conn != "" || alerts == nil || len(alerts) != 0 {
			t.Errorf("%s：应是空连接和非 nil 的空数组：%q %#v", name, conn, alerts)
		}
	}
}
