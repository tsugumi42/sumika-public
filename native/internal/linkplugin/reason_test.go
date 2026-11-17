package linkplugin

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// 日志里的原因要看得出是哪一道，又不能带出网址和号码（ADR-311 第 2 条）
func TestReasonNamesTheGateWithoutLeaking(t *testing.T) {
	cases := []struct {
		err       error
		want, not string
	}{
		{errors.New("发送前守卫拦下"), "发送前守卫拦下", ""},
		{fmt.Errorf("准备：%w", errors.New("敏感内容许可已撤销")), "敏感内容许可已撤销", ""},
		{&linkreader.FetchError{Kind: "rate_limited", Source: "pixiv", Status: 429}, "rate_limited，来源 pixiv，HTTP 429", ""},
		{errors.New("Get \"https://i.pximg.net/img/1.png\": dial tcp"), "error *errors.errorString", "pximg"},
		{errors.New("会话 qq:private:1234567:7654321 不在名单"), "error *errors.errorString", "1234567"},
	}
	for _, c := range cases {
		got := reason(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("reason(%v) = %q，要含 %q", c.err, got, c.want)
		}
		if c.not != "" && strings.Contains(got, c.not) {
			t.Errorf("reason(%v) = %q，不该带出 %q", c.err, got, c.not)
		}
	}
}
