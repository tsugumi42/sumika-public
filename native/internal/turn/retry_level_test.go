package turn

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// 规格-日志分级 5.1：每次（非最后一次）重试记 WARN；最后一次请求仍失败、不再重试的那一条记 ERROR，
// 并且用和重试同一句的模板（“回合 … 第 N 步的模型请求出错：”），装配处的打码才认得出它是模型错误
func TestRetryGivesUpLogsErrorOnce(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var warns, errs []string
	collect := func(dst *[]string) func(string, ...any) {
		return func(f string, a ...any) {
			mu.Lock()
			defer mu.Unlock()
			*dst = append(*dst, fmt.Sprintf(f, a...))
		}
	}
	h := newHarnessOpts(t, harnessOpts{
		intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
			http.Error(w, "挂了", http.StatusServiceUnavailable)
			return true
		},
		mutate: func(c *Config) {
			c.Warnf, c.Errorf = collect(&warns), collect(&errs)
		},
	})
	o := <-h.hub.Send(h.c.CharacterID, "你好")
	if o.Err == nil {
		t.Fatal("一直 503 该报错")
	}
	mu.Lock()
	defer mu.Unlock()
	retries := 0
	for _, w := range warns {
		if strings.Contains(w, "以后再请求") {
			retries++
		}
	}
	if retries != 4 {
		t.Errorf("前四次各记一条 WARN，实际 %d：%q", retries, warns)
	}
	if len(errs) != 1 || !strings.HasPrefix(errs[0], "回合 ") || !strings.Contains(errs[0], "步的模型请求出错：") || !strings.Contains(errs[0], "不再重试") {
		t.Errorf("最后一次仍失败要记一条 ERROR（同模板、写明不再重试）：%q", errs)
	}
}
