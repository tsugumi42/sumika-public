package lifetools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
)

// 设置里存好的所在地接到免费接口的 Place（规格-免费接口、ADR-330 第 6 条）：
// 腾讯要的省和市从结构化所在地给，不再编码；群里只给城市级，区县的名字和坐标不会出现在任何一家的请求里。
func TestDefaultPlaceFeedsProvidersByLevel(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	loc := config.Location{Name: "某区，编的市，中国", CityName: "编的市", Region: "编的省", Country: "中国",
		Latitude: f(31.26), Longitude: f(121.48), CityLatitude: f(31.23), CityLongitude: f(121.47), Timezone: "Asia/Shanghai"}
	run := func(t *testing.T, loc config.Location, grant PlaceGrant) (*apiRig, string) {
		t.Helper()
		r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoCN), "tencent-weather": body(tqOK), "open-meteo": body(fcOK), "met-norway": body(metOK)})
		r.set.Location = loc
		r.p.o.DefaultPlace = func(context.Context, kernel.Invocation, audience.Audience) PlaceGrant { return grant }
		res, err := r.p.weather(context.Background(), callOf(`{}`))
		if err != nil || res.IsError {
			t.Fatalf("%+v %v", res, err)
		}
		return r, res.Text
	}

	t.Run("主人本人：腾讯拿到省和市，不再编码，结果里写完整的名字", func(t *testing.T) {
		r, text := run(t, loc, PlaceFull)
		if q := r.lastQuery("tencent-weather"); q["province"][0] != "编的省" || q["city"][0] != "编的市" {
			t.Fatalf("腾讯该拿到省和市：%v", q)
		}
		if n := r.count("open-meteo-geo"); n != 0 {
			t.Fatalf("存了坐标就不该再编码：%d 次", n)
		}
		if !strings.Contains(text, "某区，编的市，中国 现在") {
			t.Fatalf("结果里该写存好的完整名字：%s", text)
		}
	})

	t.Run("群：只有城市，区县的名字和坐标哪里都不出现", func(t *testing.T) {
		r, text := run(t, loc, PlaceCity)
		q := r.lastQuery("tencent-weather")
		if q["province"][0] != "编的省" || q["city"][0] != "编的市" {
			t.Fatalf("群里腾讯也只该拿到省和市：%v", q)
		}
		if strings.Contains(text, "某区") {
			t.Fatalf("群里的结果不该带区县：%s", text)
		}
		for id := range r.reqs {
			for _, req := range r.reqs[id] {
				if s := req.URL.String(); strings.Contains(s, "31.26") || strings.Contains(s, "121.48") || strings.Contains(s, "%E6%9F%90%E5%8C%BA") {
					t.Fatalf("%s 的请求里出现了区县的坐标或名字：%s", id, s)
				}
			}
		}
	})

	t.Run("国家代码：存了用存的；手写省略时国家写中国按 CN；海外不发腾讯", func(t *testing.T) {
		if got := countryOf(config.Location{CountryCode: "JP", Country: "中国"}); got != "JP" {
			t.Fatalf("存了就用存的：%q", got)
		}
		if got := countryOf(config.Location{Country: "中国"}); got != "CN" {
			t.Fatalf("国家写中国按 CN：%q", got)
		}
		if got := countryOf(config.Location{Country: "日本", Timezone: "Asia/Tokyo"}); got != "" {
			t.Fatalf("海外不该是 CN：%q", got)
		}
		over := config.Location{Name: "東京，东京都，日本", CityName: "東京", Region: "东京都", Country: "日本", CountryCode: "JP",
			Latitude: f(35.68), Longitude: f(139.69), CityLatitude: f(35.68), CityLongitude: f(139.69), Timezone: "Asia/Tokyo"}
		r, _ := run(t, over, PlaceFull)
		if n := r.count("tencent-weather"); n != 0 {
			t.Fatalf("海外所在地不该问腾讯：%d 次", n)
		}
	})

	t.Run("旧值（读配置失败）：群里当不知道，主人本人照用", func(t *testing.T) {
		s := Settings{Location: loc, Stale: true}
		if _, ok := defaultPlace(s, PlaceCity); ok {
			t.Fatal("旧值在群里不该给")
		}
		if _, ok := defaultPlace(s, PlaceFull); !ok {
			t.Fatal("主人本人旧值照用")
		}
		if _, ok := defaultPlace(Settings{Location: config.Location{Name: "只有省", Region: "编的省", Latitude: f(1), Longitude: f(1)}}, PlaceCity); ok {
			t.Fatal("没有城市自己的坐标，群里当不知道")
		}
	})
}
