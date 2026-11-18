package lifetools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/freeapi"
)

// 真实接口的探测（默认跳过）：SUMIKA_LIVE_API=1 才跑，会真的联网，每个提供方请求一次，只带注册表里的固定参数。
// 改了提供方的解析、或者怀疑哪家接口变了，就手动跑一次：go test -run TestLiveProbes -v ./internal/lifetools
func TestLiveProbes(t *testing.T) {
	if os.Getenv("SUMIKA_LIVE_API") != "1" {
		t.Skip("设 SUMIKA_LIVE_API=1 才联网")
	}
	proxy := os.Getenv("SUMIKA_LIVE_PROXY") // 可选：http 代理地址
	p := New(Options{CacheDir: t.TempDir(), Settings: func() Settings { return Settings{Proxy: proxy} }, WeatherTimeout: 20 * time.Second})
	defer p.Holidays().Close()
	defer p.Engine().Close()
	for _, r := range p.Engine().ProbeNow(context.Background(), "") {
		t.Logf("%s -> %s %s", r.ID, r.Kind, r.Skipped)
		if r.Skipped == "" && r.Kind != freeapi.KOK {
			t.Errorf("%s 探测没通：%s", r.ID, r.Kind)
		}
	}
	// 真实的完整路径：中国地点和外国地点各查一次
	for _, loc := range []string{"杭州", "Oslo"} {
		res, _ := p.weather(context.Background(), callOf(`{"location":"`+loc+`"}`))
		t.Logf("天气 %s：%s", loc, strings.ReplaceAll(res.Text, "\n", " | "))
		if res.IsError {
			t.Errorf("%s 天气没查到", loc)
		}
	}
	res, _ := p.exchangeRate(context.Background(), callOf(`{"from":"USD","to":"CNY","amount":100}`))
	t.Logf("汇率：%s", strings.ReplaceAll(res.Text, "\n", " | "))
	st := p.Holidays().Lookup(context.Background(), 2026, time.October, 1)
	t.Logf("2026-10-01：%+v", st)
}
