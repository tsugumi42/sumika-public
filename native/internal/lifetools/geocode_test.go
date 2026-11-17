package lifetools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 下面的响应照 2026-11-09 实测的 Open-Meteo 结果的形状写（地名是公开地名，字段原样），规格-客户端自知 11.4
const (
	geoHaidian = `{"results":[{"id":1809104,"name":"海淀","feature_code":"PPLA3","admin1":"北京市","admin2":"北京市","country":"中国","country_code":"CN","latitude":39.99064,"longitude":116.28868,"timezone":"Asia/Shanghai"},
{"id":7446509,"name":"Haidian","feature_code":"PPL","admin1":"甘肃","admin2":"临夏回族自治州","country":"中国","country_code":"CN","latitude":35.08016,"longitude":103.73918,"timezone":"Asia/Shanghai"}]}`
	geoNanshan = `{"results":[{"id":1671645,"name":"南山","feature_code":"PK","admin1":"高雄市","admin2":"高雄市","admin3":"桃源區","country":"台湾","country_code":"TW","latitude":23.2,"longitude":120.9,"timezone":"Asia/Taipei"},
{"id":1916136,"name":"南山","feature_code":"PPLA4","admin1":"广东","admin2":"深圳","country":"中国","country_code":"CN","latitude":22.53,"longitude":113.93,"timezone":"Asia/Shanghai"}]}`
	geoTokyo   = `{"results":[{"id":1850147,"name":"東京","feature_code":"PPLC","admin1":"东京都","country":"日本","country_code":"JP","latitude":35.6895,"longitude":139.69171,"timezone":"Asia/Tokyo"}]}`
	geoBeijing = `{"results":[{"id":1816670,"name":"北京","feature_code":"PPLC","admin1":"北京市","admin2":"北京市","country":"中国","country_code":"CN","latitude":39.9075,"longitude":116.39723,"timezone":"Asia/Shanghai"}]}`
	geoEmpty   = `{"generationtime_ms":0.1}`
)

func TestPlaceCityAndLabel(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name, label, city string
		hit               geoHit
	}{
		{"直辖市的区", "海淀，北京市，中国", "北京市", geoHit{Name: "海淀", Feature: "PPLA3", Admin1: "北京市", Admin2: "北京市", Country: "中国", CountryCode: "CN"}},
		{"直辖市本身", "北京，北京市，中国", "北京市", geoHit{Name: "北京", Feature: "PPLC", Admin1: "北京市", Admin2: "北京市", Country: "中国", CountryCode: "CN"}},
		{"地级市", "深圳，广东，中国", "深圳", geoHit{Name: "深圳", Feature: "PPLA2", Admin1: "广东", Admin2: "深圳", Country: "中国", CountryCode: "CN"}},
		{"地级市的区", "南山，深圳，广东，中国", "深圳", geoHit{Name: "南山", Feature: "PPLA4", Admin1: "广东", Admin2: "深圳", Country: "中国", CountryCode: "CN"}},
		{"中国没有地级市的区：不猜也不拿省顶", "朝阳，黑龙江，中国", "", geoHit{Name: "朝阳", Feature: "PPLA3", Admin1: "黑龙江", Country: "中国", CountryCode: "CN"}},
		{"中国的村子没有地级市", "某村，山西，中国", "", geoHit{Name: "某村", Feature: "PPL", Admin1: "山西", Country: "中国", CountryCode: "CN"}},
		{"省", "广东，中国", "", geoHit{Name: "广东", Feature: "ADM1", Admin1: "广东", Country: "中国", CountryCode: "CN"}},
		{"国家", "编的国", "", geoHit{Name: "编的国", Feature: "PCLI", Country: "编的国", CountryCode: "XX"}},
		{"国外首都", "東京，东京都，日本", "東京", geoHit{Name: "東京", Feature: "PPLC", Admin1: "东京都", Country: "日本", CountryCode: "JP"}},
		{"国外居民点", "紐約市，纽约州，美国", "紐約市", geoHit{Name: "紐約市", Feature: "PPL", Admin1: "纽约州", Country: "美国", CountryCode: "US"}},
		{"国外城区往上取", "某城区，某县，某州，美国", "某县", geoHit{Name: "某城区", Feature: "PPLX", Admin1: "某州", Admin2: "某县", Country: "美国", CountryCode: "US"}},
	} {
		tc.hit.Latitude, tc.hit.Longitude = f(1), f(2)
		pl := tc.hit.candidate()
		if pl.Label != tc.label || pl.Location.Name != tc.label || pl.Location.CityName != tc.city {
			t.Errorf("%s：%+v", tc.name, pl)
		}
		if _, ok := pl.Location.City(); ok != (tc.city != "" && tc.city == tc.hit.Name) { // 区县的城市坐标要另查（cityCoords）
			t.Errorf("%s：城市一级 %v", tc.name, ok)
		}
	}
}

func TestSearchPlacesFiltersAndSuffixRetry(t *testing.T) {
	r := newWeatherRig(t)
	answers := map[string]string{"海淀": geoHaidian, "haidian": geoHaidian, "南山": geoNanshan, "Tokyo": geoTokyo}
	r.geo = func(w http.ResponseWriter, req *http.Request) {
		if a, ok := answers[req.URL.Query().Get("name")]; ok {
			fmt.Fprint(w, a)
			return
		}
		fmt.Fprint(w, geoEmpty)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		query, first string
		n            int
	}{
		{"海淀", "海淀，北京市，中国", 2},      // 中文
		{"haidian", "海淀，北京市，中国", 2}, // 拼音
		{"Tokyo", "東京，东京都，日本", 1},   // 英文
		{"南山区", "南山，深圳，广东，中国", 1},   // 带后缀查不到：去后缀再查；山滤掉
		{"  南山  ", "南山，深圳，广东，中国", 1},
	} {
		got, err := r.p.SearchPlaces(ctx, tc.query, 8)
		if err != nil || len(got) != tc.n || got[0].Label != tc.first {
			t.Errorf("%q：%+v %v", tc.query, got, err)
		}
	}
	// 查不到：空，不是错
	if got, err := r.p.SearchPlaces(ctx, "没有这种地方", 8); err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// 空白不联网
	hits := r.hits.Load()
	if got, err := r.p.SearchPlaces(ctx, "  \t ", 8); err != nil || len(got) != 0 || r.hits.Load() != hits {
		t.Fatalf("%+v %v", got, err)
	}
	// 上限
	if got, _ := r.p.SearchPlaces(ctx, "海淀", 1); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// 选中后存的结构是合法配置，坐标、时区都带着
	got, _ := r.p.SearchPlaces(ctx, "南山", 8)
	l := got[0].Location
	if l.CityName != "深圳" || l.Region != "广东" || l.Country != "中国" || *l.Latitude != 22.53 || *l.Longitude != 113.93 || l.Timezone != "Asia/Shanghai" {
		t.Fatalf("%+v", l)
	}
	// 接口坏了：是错误，调用方给文字（换一个没缓存过的名字，编码结果缓存 1 小时）
	r.geo = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }
	if _, err := r.p.SearchPlaces(ctx, "没缓存的地方", 8); err == nil {
		t.Fatal("接口坏了该报错")
	}
}

// 回归：带行政后缀的地名以前直接回“没查到”，她说“查一下北京市的天气”会失败
func TestWeatherExplicitPlaceWithSuffix(t *testing.T) {
	r := newWeatherRig(t)
	r.geo = func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("name") == "编的市" {
			fmt.Fprint(w, geoEmpty)
			return
		}
		fmt.Fprint(w, `{"results":[{"name":"编的","feature_code":"PPLA","admin1":"编的省","country":"编的国","latitude":31.5,"longitude":121.25}]}`)
	}
	res, err := r.p.weather(context.Background(), callOf(`{"location":"编的市"}`))
	if err != nil || res.IsError || !strings.Contains(res.Text, "编的（编的省，编的国） 现在") {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.geoNames) != 2 || !strings.HasPrefix(r.geoNames[0], "编的市|") || !strings.HasPrefix(r.geoNames[1], "编的|") {
		t.Fatalf("%v", r.geoNames)
	}
}

// 回归：选“海淀”这类区县候选时，存的城市坐标是北京市自己的，群里查天气发给第三方的也是它，不是海淀的坐标
func TestSearchPlacesCityCoordsAndGroupWeather(t *testing.T) {
	r := newWeatherRig(t)
	answers := map[string]string{"海淀": geoHaidian, "北京": geoBeijing}
	r.geo = func(w http.ResponseWriter, req *http.Request) {
		if a, ok := answers[req.URL.Query().Get("name")]; ok {
			fmt.Fprint(w, a)
			return
		}
		fmt.Fprint(w, geoEmpty)
	}
	got, err := r.p.SearchPlaces(context.Background(), "海淀", 8)
	if err != nil || len(got) < 1 || got[0].Label != "海淀，北京市，中国" {
		t.Fatalf("%+v %v", got, err)
	}
	l := got[0].Location
	if l.CityLatitude == nil || *l.CityLatitude != 39.9075 || *l.CityLongitude != 116.39723 || *l.Latitude != 39.99064 {
		t.Fatalf("城市坐标该是北京市自己的：%+v", l)
	}
	r.set.Location = l
	r.grant = PlaceCity
	res, _ := r.p.weather(context.Background(), callOf(`{}`))
	q := r.fcQueries[len(r.fcQueries)-1]
	if res.IsError || !strings.Contains(q, "latitude=39.9000") || !strings.Contains(q, "longitude=116.4000") || strings.Contains(q, "40.0") || strings.Contains(q, "116.3000") {
		t.Fatalf("群里该用城市坐标：%+v %s", res, q)
	}
	// 城市坐标查不到：群里当不知道，不拿海淀的坐标顶
	delete(answers, "北京")
	r2 := newWeatherRig(t) // 编码结果缓存 1 小时：换一个新插件才会重新查
	r2.geo = r.geo
	r = r2
	got, _ = r.p.SearchPlaces(context.Background(), "海淀", 8)
	if _, ok := got[0].Location.City(); ok {
		t.Fatalf("查不到城市坐标就不该有城市一级：%+v", got[0].Location)
	}
	// 本身就是城市的候选（国外这类 cityOf 就是自己的名字）：直接用自己的坐标，不多查一次
	answers["Tokyo"] = geoTokyo
	hits := r.hits.Load()
	if got, _ = r.p.SearchPlaces(context.Background(), "Tokyo", 8); len(got) != 1 || *got[0].Location.CityLatitude != 35.6895 || r.hits.Load() != hits+1 {
		t.Fatalf("%+v 联网 %d 次", got, r.hits.Load()-hits)
	}
}
