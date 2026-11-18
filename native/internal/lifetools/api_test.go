package lifetools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/freeapi"
	"sumika-native/internal/kernel"
	"sumika-native/internal/turn"
)

// 免费接口接入的测试：一台假服务按提供方 id 的路径分发，其余提供方指向连不上的地址，不会打到真实接口。

const (
	geoCN = `{"results":[{"name":"编的市","admin1":"编的省","admin2":"编的地级市","country":"中国","country_code":"CN","timezone":"Asia/Shanghai","latitude":30.2,"longitude":120.1}]}`
	geoUS = `{"results":[{"name":"外的城","admin1":"外的州","country":"外的国","country_code":"ZZ","timezone":"Europe/Oslo","latitude":59.9139,"longitude":10.7522}]}`
	tqOK  = `{"status":200,"data":{"observe":{"degree":"8","weather":"晴"},"forecast_24h":[` +
		`{"time":"2026-11-08","min_degree":"0","max_degree":"9","day_weather":"阴","night_weather":"阴"},` +
		`{"time":"2026-11-09","min_degree":"1","max_degree":"12","day_weather":"晴","night_weather":"多云"},` +
		`{"time":"2026-11-10","min_degree":"2","max_degree":"14","day_weather":"阴","night_weather":"阴"}]}}`
	tqEmpty = `{"status":200,"message":"ok","data":{"air":null,"observe":null,"forecast_24h":[]}}`
	metOK   = `{"properties":{"timeseries":[` +
		`{"time":"2026-11-09T11:00:00Z","data":{"instant":{"details":{"air_temperature":4.5}},"next_1_hours":{"summary":{"symbol_code":"partlycloudy_day"}},"next_6_hours":{"summary":{"symbol_code":"rain"}}}},` +
		`{"time":"2026-11-09T18:00:00Z","data":{"instant":{"details":{"air_temperature":1.5}},"next_6_hours":{"summary":{"symbol_code":"cloudy"}}}},` +
		`{"time":"2026-11-10T00:00:00Z","data":{"instant":{"details":{"air_temperature":-0.5}},"next_6_hours":{"summary":{"symbol_code":"snow"}}}},` +
		`{"time":"2026-11-10T12:00:00Z","data":{"instant":{"details":{"air_temperature":3.0}},"next_6_hours":{"summary":{"symbol_code":"lightrainandthunder"}}}}]}}`
)

type apiRig struct {
	srv  *httptest.Server
	p    *Plugin
	mu   sync.Mutex
	hits map[string]int
	reqs map[string][]*http.Request
	h    map[string]http.HandlerFunc
	set  Settings
	now  time.Time
	own  atomic.Bool
}

func fixedNow() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.FixedZone("CST", 8*3600)) }

func newAPIRig(t *testing.T, handlers map[string]http.HandlerFunc) *apiRig {
	t.Helper()
	r := &apiRig{hits: map[string]int{}, reqs: map[string][]*http.Request{}, h: handlers, now: fixedNow()}
	r.set.FreeAPI = freeapi.Settings{ProbeInterval: 12 * time.Hour, Limits: map[string]freeapi.Limit{"weather": {PerActorDaily: 20, PerConversationHourly: 15}, "exchange_rate": {PerActorDaily: 20, PerConversationHourly: 15}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		id := strings.Split(strings.TrimPrefix(req.URL.Path, "/"), "/")[0]
		r.mu.Lock()
		r.hits[id]++
		r.reqs[id] = append(r.reqs[id], req.Clone(context.Background()))
		h := r.h[id]
		r.mu.Unlock()
		if h == nil {
			http.NotFound(w, req)
			return
		}
		h(w, req)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	eps := map[string]string{}
	for id := range handlers {
		eps[id] = r.srv.URL + "/" + id + "/{year}/{base}"
	}
	// 注册表里的地址带占位符：编码、预报这类不带占位符的，路径尾巴多出来无所谓
	r.p = New(Options{
		CacheDir: t.TempDir(), Settings: func() Settings { return r.set }, Client: r.srv.Client(), Endpoints: eps, WeatherTimeout: 3 * time.Second,
		Now:        func() time.Time { return r.now },
		QuotaOwner: func(context.Context, kernel.Invocation, audience.Audience) bool { return r.own.Load() },
	})
	t.Cleanup(func() { r.p.Engine().Close(); r.p.Holidays().Close() })
	return r
}

func (r *apiRig) count(id string) int { r.mu.Lock(); defer r.mu.Unlock(); return r.hits[id] }

func (r *apiRig) lastQuery(id string) map[string][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.reqs[id]
	if len(q) == 0 {
		return nil
	}
	return q[len(q)-1].URL.Query()
}

func (r *apiRig) lastReq(id string) *http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.reqs[id]
	if len(q) == 0 {
		return nil
	}
	return q[len(q)-1]
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, s) }
}
func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", code) }
}

func weatherText(t *testing.T, r *apiRig, loc string) string {
	t.Helper()
	res, err := r.p.weather(context.Background(), callOf(`{"location":"`+loc+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return res.Text
}

func TestWeatherChinaPrefersTencentWithProvinceCityOnly(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoCN), "tencent-weather": body(tqOK), "open-meteo": body(fcOK)})
	text := weatherText(t, r, "编的市")
	for _, want := range []string{"现在 8°C，晴。", "今天（2026-11-09）：1°C ～ 12°C，晴转多云", "明天（2026-11-10）：2°C ～ 14°C，阴", "数据来自 腾讯新闻天气。"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "昨天") || strings.Contains(text, "2026-11-08") {
		t.Errorf("腾讯 forecast_24h 第 0 项是昨天，不该出现：\n%s", text)
	}
	q := r.lastQuery("tencent-weather")
	if q["province"][0] != "编的省" || q["city"][0] != "编的地级市" || q["county"][0] != "" {
		t.Errorf("腾讯只发省、市，区县留空：%v", q)
	}
	for k := range q {
		if strings.Contains(strings.ToLower(k), "lat") || strings.Contains(strings.ToLower(k), "lon") {
			t.Errorf("腾讯请求里不该有坐标：%v", q)
		}
	}
	if r.lastReq("tencent-weather").Header.Get("Referer") != "https://news.qq.com/" {
		t.Error("腾讯要带 Referer")
	}
	if r.count("open-meteo") != 0 {
		t.Error("腾讯成功了就不该再打 Open-Meteo")
	}
}

func TestWeatherChinaFallsBackWhenTencentUnknownOrDown(t *testing.T) {
	for name, tq := range map[string]http.HandlerFunc{"查无此地": body(tqEmpty), "挂了": status(503), "格式变了": body(`{"status":200,"data":{"observe":{"degree":"abc"}}}`)} {
		t.Run(name, func(t *testing.T) {
			r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoCN), "tencent-weather": tq, "open-meteo": body(fcOK)})
			text := weatherText(t, r, "编的市")
			if !strings.Contains(text, "数据来自 Open-Meteo。") || r.count("open-meteo") != 1 {
				t.Fatalf("国内腾讯不行应回退 Open-Meteo：\n%s", text)
			}
		})
	}
}

func TestWeatherAbroadSkipsTencentAndFallsBackToMET(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoUS), "tencent-weather": body(tqOK), "open-meteo": status(500), "met-norway": body(metOK)})
	text := weatherText(t, r, "外的城")
	if r.count("tencent-weather") != 0 {
		t.Error("国外地点不该问腾讯")
	}
	if !strings.Contains(text, "数据来自 MET Norway。") || !strings.Contains(text, "现在 4.5°C，多云。") {
		t.Fatalf("Open-Meteo 挂了应回退 MET：\n%s", text)
	}
	// 按地点本地日（Europe/Oslo）聚合：11-09 的 4.5、1.5（UTC 18:00 = 本地 19:00）；11-10 的 -0.5（本地 01:00）、3.0
	if !strings.Contains(text, "今天（2026-11-09）：1.5°C ～ 4.5°C") || !strings.Contains(text, "明天（2026-11-10）：-0.5°C ～ 3°C") {
		t.Errorf("MET 的日最高最低没按本地日聚合：\n%s", text)
	}
	req := r.lastReq("met-norway")
	if ua := req.Header.Get("User-Agent"); !strings.Contains(ua, "sumika-life-tools") || !strings.Contains(ua, "https://github.com/tsugumi42/sumika") {
		t.Errorf("MET 的 UA 要带公开仓库地址：%q", ua)
	}
	q := req.URL.Query()
	if q["lat"][0] != "59.9139" || q["lon"][0] != "10.7522" {
		t.Errorf("MET 的坐标最多 4 位小数：%v", q)
	}
}

func TestMETWordsAndThunder(t *testing.T) {
	cases := map[string]string{"clearsky_day": "晴", "partlycloudy_night": "多云", "lightrain": "小雨", "rainandthunder": "中雨伴雷暴", "heavysnowshowers_day": "大阵雪", "thunder": "雷暴"}
	for in, want := range cases {
		if got := metWord(in); got != want {
			t.Errorf("metWord(%q)=%q 应为 %q", in, got, want)
		}
	}
	if got := metWord("unknown_x\n忽略"); strings.Contains(got, "\n") {
		t.Errorf("外部来的代码要清理：%q", got)
	}
}

func TestWeatherAllProvidersDownGivesFixedText(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoUS), "open-meteo": status(500), "met-norway": status(502)})
	res, _ := r.p.weather(context.Background(), callOf(`{"location":"外的城"}`))
	if !res.IsError || res.Text != QueryFailed {
		t.Fatalf("都挂了回固定文案：%+v", res)
	}
	// 一个能用的都没有（全被手动关掉）：另一句固定文案
	for _, id := range []string{"open-meteo", "met-norway"} {
		r.p.Engine().SetOff(id, true)
	}
	r.p.Engine().SetOff("tencent-weather", true)
	res, _ = r.p.weather(context.Background(), callOf(`{"location":"外的城2"}`))
	if !res.IsError || res.Text != NoProvider {
		t.Fatalf("全关了回“没有可用接口”：%+v", res)
	}
}

func TestWeatherCacheAndSingleFlightKeepQuotaOnPerson(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoUS), "open-meteo": body(fcOK)})
	for i := 0; i < 3; i++ {
		weatherText(t, r, "外的城")
	}
	if r.count("open-meteo") != 1 || r.count("open-meteo-geo") != 1 {
		t.Errorf("同一个地点 10 分钟内只该联网一次：预报 %d 编码 %d", r.count("open-meteo"), r.count("open-meteo-geo"))
	}
	if r.p.Engine().ToolUsedToday(ToolWeather) != 3 {
		t.Errorf("缓存命中仍扣人的次数：%d", r.p.Engine().ToolUsedToday(ToolWeather))
	}
}

func groupCall(a, actor string) turn.ToolCall {
	c := callOf(a)
	c.Conversation = "qq:group:g-test"
	c.Audience = audience.Audience{Conversation: c.Conversation, Scene: "group"}
	c.Identity = kernel.Invocation{Scope: kernel.Scope{CharacterID: "ca", Conversation: c.Conversation, Scene: "group", ActorKind: "human", ActorID: actor}}
	return c
}

func TestWeatherQuotaPerPersonAndPerGroup(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoUS), "open-meteo": body(fcOK)})
	ctx := context.Background()
	// 同一个人每天 20 次，第 21 次被拒，且不联网
	for i := 0; i < 20; i++ {
		res, _ := r.p.weather(ctx, groupCall(`{"location":"外的城"}`, "alice"))
		if strings.Contains(res.Text, "查得太多") {
			t.Fatalf("第 %d 次不该被拒：%s", i+1, res.Text)
		}
		if i == 14 { // 群每小时 15 次：alice 一个人就用满了群的额度
			break
		}
	}
	// 群额度（每小时 15）：第 16 次起任何人都被拒
	res, _ := r.p.weather(ctx, groupCall(`{"location":"外的城"}`, "bob"))
	if !strings.Contains(res.Text, "这个群这个小时") || !strings.Contains(res.Text, "15 次") || res.IsError {
		t.Fatalf("群每小时到了应给明确文字：%+v", res)
	}
	if !strings.Contains(res.Text, "别再重试") {
		t.Errorf("要告诉模型这一轮别重试：%s", res.Text)
	}
	// 过了这个小时：群恢复，但 alice 今天已用 15 次
	r.now = r.now.Add(61 * time.Minute)
	for i := 0; i < 5; i++ {
		if res, _ = r.p.weather(ctx, groupCall(`{"location":"外的城"}`, "alice")); strings.Contains(res.Text, "查得太多") {
			t.Fatalf("alice 今天第 %d 次不该被拒：%s", 15+i+1, res.Text)
		}
	}
	res, _ = r.p.weather(ctx, groupCall(`{"location":"外的城"}`, "alice"))
	if !strings.Contains(res.Text, "每人每天 20 次") {
		t.Fatalf("alice 第 21 次应被每人每天拒：%+v", res)
	}
	// 主人不限：不看人也不看群
	r.own.Store(true)
	for i := 0; i < 40; i++ {
		if res, _ = r.p.weather(ctx, groupCall(`{"location":"外的城"}`, "owner")); strings.Contains(res.Text, "查得太多") {
			t.Fatalf("主人不该被限：%s", res.Text)
		}
	}
	// 被拒的时候不联网
	r.own.Store(false)
	before := r.count("open-meteo") + r.count("open-meteo-geo")
	r.p.weather(ctx, groupCall(`{"location":"外的城"}`, "alice"))
	if r.count("open-meteo")+r.count("open-meteo-geo") != before {
		t.Error("被拒时不该联网")
	}
}

func TestWeatherQuotaNotChargedForUnknownPlaceOrBadArgs(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoUS), "open-meteo": body(fcOK)})
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		r.p.weather(ctx, callOf(`{}`))                 // 没设所在地：不知道在哪
		r.p.weather(ctx, callOf(`{"location":12345}`)) // 类型错
		r.p.weather(ctx, callOf(`{"location":"`+strings.Repeat("长", 70)+`"}`))
	}
	if n := r.p.Engine().ToolUsedToday(ToolWeather); n != 0 {
		t.Errorf("不知道在哪、参数不对都不该扣次数：%d", n)
	}
}

func TestWeatherQuotaUnknownIdentityIsOrdinary(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoUS), "open-meteo": body(fcOK)})
	r.own.Store(true) // 宿主说是主人，但身份和会话对不上时不能当主人
	c := callOf(`{"location":"外的城"}`)
	c.CharacterID = "cb" // 和 Identity 里的角色对不上
	for i := 0; i < 20; i++ {
		r.p.weather(context.Background(), c)
	}
	res, _ := r.p.weather(context.Background(), c)
	if !strings.Contains(res.Text, "每人每天 20 次") {
		t.Fatalf("对不上的调用不能当主人，应按普通人限：%+v", res)
	}
}

func TestProbesRunAgainstFixedParamsAndCheckContent(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{
		"open-meteo-geo": body(geoCN), "tencent-weather": body(tqOK), "open-meteo": body(fcOK), "met-norway": body(metOK),
		"holiday-jsdelivr": body(holidayJSON(2026)), "holiday-raw": body(holidayJSON(2026)), "holiday-timor": body(timorJSON),
		"fx-fawaz-jsdelivr": body(fawazJSON), "fx-fawaz-pages": body(fawazJSON), "fx-frankfurter": body(frankJSON), "fx-exchangerate-api": body(erJSON),
	})
	res := r.p.Engine().ProbeNow(context.Background(), "")
	if len(res) != 11 {
		t.Fatalf("应有 11 个提供方的结果：%d", len(res))
	}
	for _, x := range res {
		if x.Skipped != "" || x.Kind != freeapi.KOK {
			t.Errorf("%s 检测应通过：%+v", x.ID, x)
		}
	}
	// 固定参数：北京的省市、奥斯陆的坐标，不带任何用户数据
	if q := r.lastQuery("tencent-weather"); q["province"][0] != "北京市" || q["city"][0] != "北京市" {
		t.Errorf("腾讯探测的固定参数：%v", q)
	}
	if q := r.lastQuery("met-norway"); q["lat"][0] != "59.9139" {
		t.Errorf("MET 探测的固定坐标：%v", q)
	}
	if q := r.lastQuery("open-meteo"); q["latitude"][0] != "39.9042" {
		t.Errorf("Open-Meteo 探测的固定坐标：%v", q)
	}
	if q := r.lastReq("holiday-raw"); !strings.Contains(q.URL.Path, "/2026/") {
		t.Errorf("节假日探测用当前年份：%s", q.URL.Path)
	}
}

func TestProbeCatchesWrongContentNotJustStatus(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"tencent-weather": body(`{"status":200,"data":{"observe":{"degree":"abc","weather":"晴"}}}`), "fx-frankfurter": body(`{"date":"2026-11-06","rates":{"EUR":0.9}}`)})
	for _, id := range []string{"tencent-weather", "fx-frankfurter"} {
		res := r.p.Engine().ProbeNow(context.Background(), id)
		if res[0].Kind == freeapi.KOK {
			t.Errorf("%s 回 200 但内容不对，不该算通：%+v", id, res[0])
		}
	}
}

func TestEveryCatalogProviderHasProbe(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{})
	for _, pr := range r.p.Engine().Catalog().Providers {
		res := r.p.Engine().ProbeNow(context.Background(), pr.ID)
		if len(res) != 1 || res[0].Skipped == "没有探测做法" {
			t.Errorf("%s 没有探测做法", pr.ID)
		}
	}
}

// ---- 假日 ----

func holidayJSON(year int) string {
	return fmt.Sprintf(`{"year":%d,"papers":["x"],"days":[{"name":"编的节","date":"%d-03-02","isOffDay":true},{"name":"编的节","date":"%d-03-07","isOffDay":false}]}`, year, year, year)
}

const timorJSON = `{"code":0,"holiday":{"03-02":{"holiday":true,"name":"编的节","date":"2026-03-02","rest":1},"03-07":{"holiday":false,"name":"编的节后补班","after":true,"target":"编的节","date":"2026-03-07","rest":1}}}`

func lookupVia(t *testing.T, r *apiRig, y int, m time.Month, d int) Status {
	t.Helper()
	return r.p.Holidays().Lookup(context.Background(), y, m, d)
}

func TestHolidayFallbackChain(t *testing.T) {
	// jsDelivr 挂了 → raw
	r := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": status(502), "holiday-raw": body(holidayJSON(2026)), "holiday-timor": body(timorJSON)})
	if st := lookupVia(t, r, 2026, time.March, 2); st.Kind != KindOff || st.Name != "编的节" {
		t.Fatalf("jsDelivr 挂了应回退 raw：%+v", st)
	}
	if r.count("holiday-timor") != 0 {
		t.Error("raw 成功了不该问 timor")
	}
	// 前两家都挂 → timor（转换成 holiday-cn 的形状，补班日的名字取 target）
	r2 := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": status(502), "holiday-raw": status(500), "holiday-timor": body(timorJSON)})
	if st := lookupVia(t, r2, 2026, time.March, 7); st.Kind != KindMakeupWork || st.Name != "编的节" {
		t.Fatalf("前两家挂了应回退 timor：%+v", st)
	}
	// jsDelivr 对 @master 有缓存：它说没有这一年，raw 有 → 用 raw
	r3 := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": status(404), "holiday-raw": body(holidayJSON(2026))})
	if st := lookupVia(t, r3, 2026, time.March, 2); st.Kind != KindOff {
		t.Fatalf("jsDelivr 404 应再问 raw：%+v", st)
	}
	if r3.p.Engine().Snapshot()[2].Providers[0].State != freeapi.StateOK {
		t.Error("404 不是提供方的错")
	}
}

func TestHolidayNotPublishedAndUnsureNeverGuess(t *testing.T) {
	// 两家都 404：没公布
	r := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": status(404), "holiday-raw": status(404)})
	if st := lookupVia(t, r, 2099, time.March, 2); st.Kind != KindNotPublished {
		t.Fatalf("两家都 404 应是没公布：%+v", st)
	}
	// timor 对没公布的年份回 200 加空表：当查无这一年，不是格式错，不记它的错
	r2 := newAPIRig(t, map[string]http.HandlerFunc{"holiday-timor": body(`{"code":0,"holiday":{}}`)})
	// 另外两家关掉：夹着失败（没配地址的会连不上）就不再算“查无”，这里要的是只问 timor 一家
	for _, id := range []string{"holiday-jsdelivr", "holiday-raw"} {
		if err := r2.p.Engine().SetOff(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if st := lookupVia(t, r2, 2099, time.March, 2); st.Kind != KindNotPublished {
		t.Fatalf("timor 空表应当没公布：%+v", st)
	}
	for _, p := range r2.p.Engine().Snapshot()[2].Providers {
		if p.ID == "holiday-timor" && p.State != freeapi.StateOK {
			t.Errorf("timor 空表不该让它进入 %s", p.State)
		}
	}
	// 都取不到：不猜
	r3 := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": status(500), "holiday-raw": status(500), "holiday-timor": status(500)})
	if st := lookupVia(t, r3, 2026, time.March, 3); st.Kind != KindUnavailable {
		t.Fatalf("取不到不能猜：%+v", st)
	}
	// 数据坏了（日期乱写）：不进缓存，不猜
	bad := `{"year":2026,"days":[{"name":"x","date":"2026-13-45","isOffDay":true}]}`
	r4 := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": body(bad), "holiday-raw": body(bad), "holiday-timor": status(500)})
	if st := lookupVia(t, r4, 2026, time.March, 3); st.Kind != KindUnavailable {
		t.Fatalf("坏数据不能用：%+v", st)
	}
}

func TestHolidayCustomURLBypassesRegistry(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"holiday-jsdelivr": body(holidayJSON(2026)), "custom": body(holidayJSON(2026))})
	r.set.HolidayURL = r.srv.URL + "/custom/{year}"
	if st := lookupVia(t, r, 2026, time.March, 2); st.Kind != KindOff {
		t.Fatalf("用户自己配的地址：%+v", st)
	}
	if r.count("holiday-jsdelivr") != 0 || r.count("custom") != 1 {
		t.Errorf("配了地址就只用他的：jsdelivr=%d custom=%d", r.count("holiday-jsdelivr"), r.count("custom"))
	}
}

// ---- 汇率 ----

const (
	fawazJSON = `{"date":"2026-11-08","usd":{"cny":7.2,"jpy":150.5,"eur":0.9}}`
	frankJSON = `{"amount":1.0,"base":"USD","date":"2026-11-06","rates":{"CNY":7.1}}`
	erJSON    = `{"result":"success","provider":"x","time_last_update_unix":1794182551,"time_next_update_unix":1794270061,"base_code":"USD","rates":{"CNY":7.0}}`
)

func fxText(t *testing.T, r *apiRig, a string) (string, bool) {
	t.Helper()
	res, err := r.p.exchangeRate(context.Background(), callOf(a))
	if err != nil {
		t.Fatal(err)
	}
	return res.Text, res.IsError
}

func allFx(over map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	m := map[string]http.HandlerFunc{"fx-fawaz-jsdelivr": body(fawazJSON), "fx-fawaz-pages": body(fawazJSON), "fx-frankfurter": body(frankJSON), "fx-exchangerate-api": body(erJSON)}
	for k, v := range over {
		m[k] = v
	}
	return m
}

func TestExchangeRateChinaLineUsesFawazThenPages(t *testing.T) {
	r := newAPIRig(t, allFx(nil))
	text, isErr := fxText(t, r, `{"from":"usd","to":"cny","amount":100}`)
	if isErr || !strings.Contains(text, "1 USD = 7.2 CNY") || !strings.Contains(text, "100 USD ≈ 720 CNY") || !strings.Contains(text, "2026-11-08") || !strings.Contains(text, "数据来自 fawazahmed0 汇率（jsDelivr）") {
		t.Fatalf("%q", text)
	}
	if r.count("fx-frankfurter") != 0 {
		t.Error("国内线路先 fawazahmed0")
	}
	if p := r.lastReq("fx-fawaz-jsdelivr").URL.Path; !strings.HasSuffix(p, "/usd") {
		t.Errorf("fawazahmed0 的币种代码小写：%s", p)
	}
	// jsDelivr 挂了 → pages.dev
	r2 := newAPIRig(t, allFx(map[string]http.HandlerFunc{"fx-fawaz-jsdelivr": status(502)}))
	if text, _ = fxText(t, r2, `{"from":"USD","to":"CNY"}`); !strings.Contains(text, "pages.dev") {
		t.Fatalf("jsDelivr 挂了应回退 pages.dev：%s", text)
	}
}

func TestExchangeRateGlobalLineUsesFrankfurterThenERAPI(t *testing.T) {
	r := newAPIRig(t, allFx(nil))
	r.set.FreeAPI.NetworkRegion = freeapi.RegionGlobal
	text, _ := fxText(t, r, `{"from":"USD","to":"CNY"}`)
	if !strings.Contains(text, "1 USD = 7.1 CNY") || !strings.Contains(text, "Frankfurter") {
		t.Fatalf("国外线路先 Frankfurter：%s", text)
	}
	q := r.lastQuery("fx-frankfurter")
	if q["base"][0] != "USD" || q["symbols"][0] != "CNY" {
		t.Errorf("Frankfurter 的币种代码大写：%v", q)
	}
	r2 := newAPIRig(t, allFx(map[string]http.HandlerFunc{"fx-frankfurter": status(500)}))
	r2.set.FreeAPI.NetworkRegion = freeapi.RegionGlobal
	if text, _ = fxText(t, r2, `{"from":"USD","to":"CNY"}`); !strings.Contains(text, "ExchangeRate-API") || !strings.Contains(text, "1 USD = 7 CNY") {
		t.Fatalf("Frankfurter 挂了应回退 ExchangeRate-API：%s", text)
	}
}

func TestExchangeRateUnknownCurrency(t *testing.T) {
	notFound := map[string]http.HandlerFunc{
		"fx-fawaz-jsdelivr": status(404), "fx-fawaz-pages": status(404), "fx-frankfurter": status(404),
		"fx-exchangerate-api": body(`{"result":"error","error-type":"unsupported-code"}`),
	}
	r := newAPIRig(t, notFound)
	text, isErr := fxText(t, r, `{"from":"USD","to":"ZZZ"}`)
	if isErr || !strings.Contains(text, "查不到 USD 到 ZZZ 的汇率") {
		t.Fatalf("没有这个币种不是接口坏了：%q %v", text, isErr)
	}
	// 目标币种缺失（接口有这个基准币但没有目标）：换一家再问一次
	r2 := newAPIRig(t, allFx(map[string]http.HandlerFunc{"fx-fawaz-jsdelivr": body(`{"date":"2026-11-08","usd":{"eur":0.9}}`)}))
	if text, _ = fxText(t, r2, `{"from":"USD","to":"CNY"}`); !strings.Contains(text, "7.2") {
		t.Fatalf("第一家缺目标币种应换第二家：%s", text)
	}
	for _, p := range r.p.Engine().Snapshot()[3].Providers {
		if p.State != freeapi.StateOK {
			t.Errorf("没有这个币种不该让 %s 进入 %s", p.ID, p.State)
		}
	}
}

func TestExchangeRateFailuresAndArgs(t *testing.T) {
	r := newAPIRig(t, allFx(map[string]http.HandlerFunc{"fx-fawaz-jsdelivr": status(500), "fx-fawaz-pages": status(500), "fx-frankfurter": status(500), "fx-exchangerate-api": status(500)}))
	if text, isErr := fxText(t, r, `{"from":"USD","to":"CNY"}`); !isErr || text != FxQueryFailed {
		t.Fatalf("都挂了回固定文案：%q", text)
	}
	cases := map[string]string{
		"不是对象":   `[]`,
		"缺 to":   `{"from":"USD"}`,
		"空 from": `{"from":"  ","to":"CNY"}`,
		"数字币种":   `{"from":123,"to":"CNY"}`,
		"太长":     `{"from":"ABCDEFG","to":"CNY"}`,
		"带符号":    `{"from":"US$","to":"CNY"}`,
		"金额文字":   `{"from":"USD","to":"CNY","amount":"多"}`,
		"金额负数":   `{"from":"USD","to":"CNY","amount":-5}`,
		"金额为零":   `{"from":"USD","to":"CNY","amount":0}`,
		"金额过大":   `{"from":"USD","to":"CNY","amount":1e300}`,
	}
	hits := r.count("fx-fawaz-jsdelivr")
	for name, a := range cases {
		if text, isErr := fxText(t, r, a); !isErr {
			t.Errorf("%s：应报参数错误：%q", name, text)
		}
	}
	if r.count("fx-fawaz-jsdelivr") != hits || r.p.Engine().ToolUsedToday(ToolExchangeRate) != 1 {
		t.Errorf("参数错误不该联网也不该扣次数：%d %d", r.count("fx-fawaz-jsdelivr")-hits, r.p.Engine().ToolUsedToday(ToolExchangeRate))
	}
	if text, isErr := fxText(t, r, `{"from":"usd","to":"USD"}`); isErr || !strings.Contains(text, "汇率就是 1") {
		t.Errorf("同币种：%q", text)
	}
}

func TestExchangeRateQuota(t *testing.T) {
	r := newAPIRig(t, allFx(nil))
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if res, _ := r.p.exchangeRate(ctx, groupCall(`{"from":"USD","to":"CNY"}`, "alice")); strings.Contains(res.Text, "查得太多") {
			t.Fatalf("第 %d 次不该被拒：%s", i+1, res.Text)
		}
		if i == 14 {
			break
		}
	}
	res, _ := r.p.exchangeRate(ctx, groupCall(`{"from":"USD","to":"CNY"}`, "bob"))
	if !strings.Contains(res.Text, "这个群这个小时汇率查得太多了") {
		t.Fatalf("汇率也按群每小时限：%+v", res)
	}
	// 汇率和天气各算各的
	r.set.FreeAPI.Limits["weather"] = freeapi.Limit{PerActorDaily: 1}
	if r.p.Engine().Allow(ToolWeather, freeapi.Who{ActorKind: "human", ActorID: "bob"}).Denied {
		t.Error("天气的次数不受汇率影响")
	}
}

func TestFmtRate(t *testing.T) {
	cases := map[float64]string{7.2: "7.2", 7.1234567: "7.1235", 1: "1", 12345.678: "12345.68", 0.1498: "0.1498", 0.00001234: "0.00001234", 150.5: "150.5"}
	for in, want := range cases {
		if got := fmtRate(in); got != want {
			t.Errorf("fmtRate(%v)=%q 应为 %q", in, got, want)
		}
	}
}

func TestParseTencentDatesAndNotFound(t *testing.T) {
	now := time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC) // UTC 深夜 = 北京已经是 11-10
	rep, err := parseTencent([]byte(tqOK), now)
	if err != nil || rep.Days[0].Date != "2026-11-10" {
		t.Fatalf("今天按北京时间算，该是 11-10：%+v %v", rep, err)
	}
	if _, err := parseTencent([]byte(tqEmpty), now); err == nil || !strings.Contains(err.Error(), "查无") {
		t.Errorf("observe 为空是查无此地：%v", err)
	}
	if _, err := parseTencent([]byte(`{"status":200,"data":{"observe":{"degree":"5","weather":"晴"},"forecast_24h":[]}}`), now); err == nil {
		t.Error("预报里没有今天是格式不对")
	}
	if _, err := parseTencent([]byte(`{`), now); err == nil {
		t.Error("坏 JSON")
	}
}

// 注册表里的每个提供方都有对应的做法：加条目忘了写解析会在这里红
func TestEveryProviderIsImplemented(t *testing.T) {
	p := New(Options{CacheDir: t.TempDir()})
	defer p.Holidays().Close()
	for _, pr := range p.Engine().Catalog().Providers {
		if p.probeFetcher(pr) == nil {
			t.Errorf("提供方 %s 没有实现", pr.ID)
		}
	}
}
