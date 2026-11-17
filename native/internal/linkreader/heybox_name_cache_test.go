package linkreader

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// 复核：小黑盒问 Steam 游戏名时 Steam 临时失败（503、超时这类），整条结果照常发但不进缓存，下次再问就补上名字；
// Steam 明确回 success:false 的照常缓存，缓存期内不再重复问。
func TestHeyboxGameNameTransientNotCached(t *testing.T) {
	for _, c := range []struct {
		name   string
		first  func() *http.Response
		cached bool
		named  bool // 第二次能不能补上名字：429 之后 Steam 这一档在冷却，第二次也问不到，但照样不该缓存
	}{
		{"steam_503", func() *http.Response { return response(503, "") }, false, true},
		{"steam_429", func() *http.Response { return response(429, "") }, false, false}, // 限流也是临时的（transientKind 不管 429）
		{"steam_success_false", func() *http.Response { return response(200, `{"90001":{"success":false}}`) }, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var steamCalls, heyboxCalls atomic.Int32
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Hostname() == "store.steampowered.com" {
					if steamCalls.Add(1) == 1 {
						return c.first(), nil
					}
					return response(200, `{"90001":{"success":true,"data":{"type":"game","name":"编的放松时光","steam_appid":90001}}}`), nil
				}
				heyboxCalls.Add(1)
				return response(200, fixture(t, "cn-heybox-game")), nil
			})
			st, _ := cacheStores(t)
			s := cacheService(t, st, f, time.Now)
			const raw = "https://www.xiaoheihe.cn/app/topic/game/pc/90001"
			r, err := s.Get(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if r.Title.State == "available" {
				t.Fatalf("第一次不该有名字：%+v", r.Title)
			}
			r, err = s.Get(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if c.cached {
				if heyboxCalls.Load() != 1 || steamCalls.Load() != 1 || r.Title.State == "available" {
					t.Fatalf("应当命中缓存：小黑盒 %d 次、Steam %d 次、标题 %+v", heyboxCalls.Load(), steamCalls.Load(), r.Title)
				}
				return
			}
			if heyboxCalls.Load() != 2 || (c.named && r.Title.Value != "编的放松时光") {
				t.Fatalf("临时失败不该缓存：小黑盒 %d 次、标题 %+v", heyboxCalls.Load(), r.Title)
			}
		})
	}
}
