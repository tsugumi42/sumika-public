package lifetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"sumika-native/internal/channel"
	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
)

// 天气（规格-免费接口）：先把地名编码成一个完整的地点（名字、省、市、国家、坐标、时区），
// 再让注册表里的天气提供方各取自己要的字段：国内腾讯按省、市名，Open-Meteo 和 MET Norway 按坐标。
// 不做 IP 定位：主人开着代理，定位出来多半是代理节点。地点要发给第三方，所以所在地默认空，填了才发；
// 腾讯永远只发省、市两级，坐标精度不超过调用方给的。

// 天气工具回给模型的几种"没结果"，文字固定，不带任何设置里的值
const (
	// UnknownPlace 是没有地点可查时的回话：配置为空、场合不是主人，都是这同一句，让模型看不出区别
	UnknownPlace = "不知道在哪，可以翻记忆或开口问，问到了把地名填进 location 再查。"
	// QueryFailed 是联网这一步没成
	QueryFailed = "这次取不到天气（网络不通或对方没回），过一会儿再查，或者先照记忆里的大概情况说。"
	// NoProvider 是没有任何能用的接口（全关了、需处理、今天上限到了或都在退避）
	NoProvider = "天气接口现在都不可用，这次没有查，过一会儿再试，或者先照记忆里的大概情况说。"
)

// Place 是一个查天气的地点。Query 非空且没有坐标时先编码；有坐标（比如设置里存好的所在地）就不再编码，
// 腾讯要用的省、市没有时，由名字再编码一次补上
type Place struct {
	Display                                              string // 非空就原样当结果里写的地名（设置里的所在地已经是整理好的完整名字）；空则按编码结果拼
	Query                                                string
	Name, Admin1, Admin2, Country, CountryCode, Timezone string
	Lat, Lon                                             float64
	HasCoord                                             bool
}

func (p Place) region() string {
	if strings.EqualFold(p.CountryCode, "CN") {
		return freeapi.RegionCN
	}
	return freeapi.RegionGlobal
}

// notFoundError 是地点查不到
type notFoundError struct{}

func (notFoundError) Error() string { return "地点查不到" }

// 取数的体积上限
const weatherBodyMax = 256 << 10

// ---- 报告：各家的结果统一成这个 ----

type weatherReport struct {
	Source string
	Temp   *float64
	Feels  *float64
	Word   string
	Days   []weatherDay
}

type weatherDay struct {
	Date     string
	Min, Max *float64
	Word     string
	Precip   *float64
}

// ---- 编码 ----

// geocodeFetcher 编码一个地名：先按原文查，没有能用的结果再去行政后缀查一次
// （实测 2026-11-09：Open-Meteo 的中文索引查不到“北京市”“南山区”这类带后缀的名字，第一个结果还可能是山）。
// 全都没有是查无此地。工具和设置页的所在地候选共用这一个
func geocodeFetcher(query string) freeapi.Fetcher {
	return func(ctx context.Context, env freeapi.Env) (any, error) {
		for _, name := range placeQueries(query) {
			q := url.Values{"name": {name}, "count": {strconv.Itoa(placeCount)}, "language": {"zh"}, "format": {"json"}}
			body, err := freeapi.Get(ctx, env.Client, env.URL+"?"+q.Encode(), nil, weatherBodyMax)
			if err != nil {
				return nil, err
			}
			hits, err := parseGeo(body)
			if err != nil {
				return nil, err
			}
			if len(hits) > 0 {
				return hits, nil
			}
		}
		return nil, freeapi.ErrNotFound
	}
}

// parseGeo 解析编码结果，只留能用的（坐标合理、是居民点或行政区）；坐标不合理的和山、岛、河一样不算，都没有就是查无此地
func parseGeo(body []byte) ([]geoHit, error) {
	var g geoResp
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, freeapi.Schema("编码接口回的不是合法数据")
	}
	return slices.DeleteFunc(g.Results, func(h geoHit) bool { return !h.usable() }), nil
}

// geocodeHits 编码一个地名，返回能用的结果（走注册表，缓存 1 小时，不落盘）。查无此地是 freeapi.ErrNotFound
func (p *Plugin) geocodeHits(ctx context.Context, query string) ([]geoHit, error) {
	res, err := p.eng.Do(ctx, freeapi.Request{Category: "geocode", Region: freeapi.RegionGlobal,
		Fetch: map[string]freeapi.Fetcher{"open-meteo-geo": geocodeFetcher(query)}, CacheKey: "geo:" + strings.ToLower(query), CacheTTL: time.Hour})
	if err != nil {
		return nil, err
	}
	return res.Value.([]geoHit), nil
}

// geocode 编码一个地名，取第一个能用的结果当要查天气的地点
func (p *Plugin) geocode(ctx context.Context, query string) (Place, error) {
	hits, err := p.geocodeHits(ctx, query)
	if err != nil {
		return Place{}, err
	}
	h := hits[0]
	return Place{Query: query, Name: h.Name, Admin1: h.Admin1, Admin2: h.Admin2, Country: h.Country, CountryCode: h.CountryCode, Timezone: h.Timezone,
		Lat: *h.Latitude, Lon: *h.Longitude, HasCoord: true}, nil
}

// ---- 查天气 ----

// queryWeather 查一个地点的天气，返回给模型看的文字。地点查不到返回 notFoundError
func (p *Plugin) queryWeather(ctx context.Context, pl Place) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.o.WeatherTimeout)
	defer cancel()
	if !pl.HasCoord {
		g, err := p.geocode(ctx, pl.Query)
		if err != nil {
			return "", mapCallErr(err)
		}
		pl = g
	}
	region := pl.region()
	fetch := map[string]freeapi.Fetcher{
		"open-meteo": openMeteoFetcher(pl),
		"met-norway": metFetcher(pl),
	}
	if prov, city, ok := tencentNames(pl); ok {
		fetch["tencent-weather"] = tencentFetcher(prov, city, p.o.Now)
	}
	res, err := p.eng.Do(ctx, freeapi.Request{Category: "weather", Region: region, Fetch: fetch,
		CacheKey: fmt.Sprintf("w:%s:%.2f,%.2f", region, pl.Lat, pl.Lon), CacheTTL: 10 * time.Minute})
	if err != nil {
		return "", mapCallErr(err)
	}
	rep := res.Value.(*weatherReport)
	name := cleanName(pl.Display, config.LocationMaxRunes)
	if name == "" {
		name = placeName(pl.Query, pl.Name, pl.Admin1, pl.Country)
	}
	return formatWeather(name, *rep), nil
}

// 调用失败的几种：查无此地、没有可用接口、别的失败
type noProviderError struct{}

func (noProviderError) Error() string { return "没有可用的接口" }

func mapCallErr(err error) error {
	switch {
	case errors.Is(err, freeapi.ErrNotFound):
		return notFoundError{}
	case errors.Is(err, freeapi.ErrNoProvider):
		return noProviderError{}
	}
	return err
}

// ---- Open-Meteo 预报 ----

type forecastResp struct {
	Current struct {
		Temp     *float64 `json:"temperature_2m"`
		Apparent *float64 `json:"apparent_temperature"`
		Code     *int     `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Time   []string   `json:"time"`
		Max    []*float64 `json:"temperature_2m_max"`
		Min    []*float64 `json:"temperature_2m_min"`
		Precip []*float64 `json:"precipitation_probability_max"`
		Code   []*int     `json:"weather_code"`
	} `json:"daily"`
}

func openMeteoFetcher(pl Place) freeapi.Fetcher {
	return func(ctx context.Context, env freeapi.Env) (any, error) {
		fq := url.Values{
			"latitude": {strconv.FormatFloat(pl.Lat, 'f', 4, 64)}, "longitude": {strconv.FormatFloat(pl.Lon, 'f', 4, 64)},
			"current":       {"temperature_2m,apparent_temperature,weather_code"},
			"daily":         {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max"},
			"timezone":      {"auto"},
			"forecast_days": {"2"},
		}
		body, err := freeapi.Get(ctx, env.Client, env.URL+"?"+fq.Encode(), nil, weatherBodyMax)
		if err != nil {
			return nil, err
		}
		return parseOpenMeteo(body)
	}
}

func parseOpenMeteo(body []byte) (*weatherReport, error) {
	var f forecastResp
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, freeapi.Schema("预报接口回的不是合法数据")
	}
	if f.Current.Temp == nil || f.Current.Code == nil {
		return nil, freeapi.Schema("预报接口没给当前天气")
	}
	r := &weatherReport{Source: "Open-Meteo", Temp: f.Current.Temp, Feels: f.Current.Apparent, Word: weatherWord(*f.Current.Code)}
	for i, d := range f.Daily.Time {
		if i >= len(f.Daily.Max) || i >= len(f.Daily.Min) || f.Daily.Max[i] == nil || f.Daily.Min[i] == nil {
			continue
		}
		day := weatherDay{Date: d, Min: f.Daily.Min[i], Max: f.Daily.Max[i]}
		if i < len(f.Daily.Code) && f.Daily.Code[i] != nil {
			day.Word = weatherWord(*f.Daily.Code[i])
		}
		if i < len(f.Daily.Precip) {
			day.Precip = f.Daily.Precip[i]
		}
		r.Days = append(r.Days, day)
	}
	return r, nil
}

// ---- 腾讯新闻天气 ----

// tencentNames 取腾讯要的省、市。编码给的 admin1 是省（不带“省”也行，腾讯都认），admin2 是地级市；
// 只发到市，区县一律不发。没有省或市就不参与（交给坐标的两家）
func tencentNames(pl Place) (province, city string, ok bool) {
	if !strings.EqualFold(pl.CountryCode, "CN") {
		return "", "", false
	}
	province, city = cleanQueryName(pl.Admin1), cleanQueryName(pl.Admin2)
	if province == "" || city == "" {
		return "", "", false
	}
	return province, city, true
}

func cleanQueryName(s string) string {
	s = strings.TrimSpace(s)
	if n := len([]rune(s)); n == 0 || n > 20 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return ""
	}
	return s
}

type tencentResp struct {
	Status int `json:"status"`
	Data   struct {
		Observe *struct {
			Degree  string `json:"degree"`
			Weather string `json:"weather"`
		} `json:"observe"`
		Forecast []struct {
			Time         string `json:"time"`
			Min          string `json:"min_degree"`
			Max          string `json:"max_degree"`
			DayWeather   string `json:"day_weather"`
			NightWeather string `json:"night_weather"`
		} `json:"forecast_24h"`
	} `json:"data"`
}

func tencentFetcher(province, city string, now func() time.Time) freeapi.Fetcher {
	return func(ctx context.Context, env freeapi.Env) (any, error) {
		q := url.Values{"source": {"pc"}, "weather_type": {"observe|forecast_24h"}, "province": {province}, "city": {city}, "county": {""}}
		body, err := freeapi.Get(ctx, env.Client, env.URL+"?"+q.Encode(), map[string]string{"Referer": "https://news.qq.com/"}, weatherBodyMax)
		if err != nil {
			return nil, err
		}
		return parseTencent(body, now())
	}
}

var shanghai = time.FixedZone("CST", 8*3600)

// parseTencent：查无此地时腾讯回 200 加空的 observe，当“查无结果”；forecast_24h 第 0 项是昨天，按 time 字段对日期
func parseTencent(body []byte, now time.Time) (*weatherReport, error) {
	var t tencentResp
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, freeapi.Schema("天气接口回的不是合法数据")
	}
	if t.Status != 200 {
		return nil, freeapi.Schema("天气接口状态不对")
	}
	if t.Data.Observe == nil || strings.TrimSpace(t.Data.Observe.Degree) == "" {
		return nil, freeapi.ErrNotFound
	}
	deg, err := strconv.ParseFloat(strings.TrimSpace(t.Data.Observe.Degree), 64)
	if err != nil || math.IsNaN(deg) || math.Abs(deg) > 90 {
		return nil, freeapi.Schema("当前温度不是数字")
	}
	r := &weatherReport{Source: "腾讯新闻天气", Temp: &deg, Word: cleanName(t.Data.Observe.Weather, 20)}
	today := now.In(shanghai)
	for i, off := range []int{0, 1} {
		date := today.AddDate(0, 0, off).Format("2006-01-02")
		for _, f := range t.Data.Forecast {
			if f.Time != date {
				continue
			}
			mn, e1 := strconv.ParseFloat(strings.TrimSpace(f.Min), 64)
			mx, e2 := strconv.ParseFloat(strings.TrimSpace(f.Max), 64)
			if e1 != nil || e2 != nil {
				break
			}
			word := cleanName(f.DayWeather, 20)
			if n := cleanName(f.NightWeather, 20); n != "" && n != word {
				word += "转" + n
			}
			r.Days = append(r.Days, weatherDay{Date: date, Min: &mn, Max: &mx, Word: word})
			break
		}
		if len(r.Days) <= i && i == 0 {
			return nil, freeapi.Schema("预报里没有今天")
		}
	}
	return r, nil
}

// ---- MET Norway ----

// 公开仓库地址，未定时先用这个（MET Norway 要求 User-Agent 带联系方式）
const metUA = "sumika-life-tools/1.0 (+https://github.com/tsugumi42/sumika)"

type metResp struct {
	Properties struct {
		Timeseries []struct {
			Time string `json:"time"`
			Data struct {
				Instant struct {
					Details struct {
						Temp *float64 `json:"air_temperature"`
					} `json:"details"`
				} `json:"instant"`
				Next1 *metNext `json:"next_1_hours"`
				Next6 *metNext `json:"next_6_hours"`
			} `json:"data"`
		} `json:"timeseries"`
	} `json:"properties"`
}

type metNext struct {
	Summary struct {
		Symbol string `json:"symbol_code"`
	} `json:"summary"`
}

func metFetcher(pl Place) freeapi.Fetcher {
	return func(ctx context.Context, env freeapi.Env) (any, error) {
		q := url.Values{"lat": {strconv.FormatFloat(pl.Lat, 'f', 4, 64)}, "lon": {strconv.FormatFloat(pl.Lon, 'f', 4, 64)}}
		body, err := freeapi.Get(ctx, env.Client, env.URL+"?"+q.Encode(), map[string]string{"User-Agent": metUA}, 2<<20)
		if err != nil {
			return nil, err
		}
		return parseMET(body, placeLoc(pl))
	}
}

// placeLoc 取地点的时区；编码没给就按经度粗算整点偏移
func placeLoc(pl Place) *time.Location {
	if pl.Timezone != "" {
		if l, err := time.LoadLocation(pl.Timezone); err == nil {
			return l
		}
	}
	return time.FixedZone("local", int(math.Round(pl.Lon/15))*3600)
}

// parseMET：compact 没有日最高最低，自己按地点的本地日聚合时序里的气温；今天只有“现在以后”的部分
func parseMET(body []byte, loc *time.Location) (*weatherReport, error) {
	var m metResp
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, freeapi.Schema("天气接口回的不是合法数据")
	}
	ts := m.Properties.Timeseries
	if len(ts) == 0 || ts[0].Data.Instant.Details.Temp == nil {
		return nil, freeapi.Schema("天气接口没给当前气温")
	}
	r := &weatherReport{Source: "MET Norway", Temp: ts[0].Data.Instant.Details.Temp}
	if n := ts[0].Data.Next1; n != nil {
		r.Word = metWord(n.Summary.Symbol)
	}
	type agg struct {
		min, max float64
		n        int
		noon     string
		noonDist int
	}
	days := map[string]*agg{}
	var order []string
	for _, e := range ts {
		t, err := time.Parse(time.RFC3339, e.Time)
		if err != nil || e.Data.Instant.Details.Temp == nil {
			continue
		}
		lt := t.In(loc)
		d := lt.Format("2006-01-02")
		a := days[d]
		if a == nil {
			a = &agg{min: math.Inf(1), max: math.Inf(-1), noonDist: 99}
			days[d] = a
			order = append(order, d)
		}
		v := *e.Data.Instant.Details.Temp
		a.min, a.max, a.n = math.Min(a.min, v), math.Max(a.max, v), a.n+1
		if dist := absInt(lt.Hour() - 12); dist < a.noonDist {
			sym := ""
			if e.Data.Next6 != nil {
				sym = e.Data.Next6.Summary.Symbol
			} else if e.Data.Next1 != nil {
				sym = e.Data.Next1.Summary.Symbol
			}
			if sym != "" {
				a.noon, a.noonDist = metWord(sym), dist
			}
		}
	}
	for _, d := range order {
		if len(r.Days) == 2 {
			break
		}
		a := days[d]
		mn, mx := a.min, a.max
		r.Days = append(r.Days, weatherDay{Date: d, Min: &mn, Max: &mx, Word: a.noon})
	}
	return r, nil
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// metWord 把 MET 的 symbol_code（去掉 _day/_night/_polartwilight）翻成中文
func metWord(code string) string {
	for _, suf := range []string{"_day", "_night", "_polartwilight"} {
		code = strings.TrimSuffix(code, suf)
	}
	thunder := strings.Contains(code, "thunder")
	base := strings.ReplaceAll(strings.ReplaceAll(code, "andthunder", ""), "showersandthunder", "showers")
	words := map[string]string{
		"clearsky": "晴", "fair": "大致晴朗", "partlycloudy": "多云", "cloudy": "阴", "fog": "有雾",
		"lightrain": "小雨", "rain": "中雨", "heavyrain": "大雨",
		"lightrainshowers": "小阵雨", "rainshowers": "阵雨", "heavyrainshowers": "强阵雨",
		"lightsleet": "小雨夹雪", "sleet": "雨夹雪", "heavysleet": "大雨夹雪",
		"lightsleetshowers": "小阵性雨夹雪", "sleetshowers": "阵性雨夹雪", "heavysleetshowers": "强阵性雨夹雪",
		"lightsnow": "小雪", "snow": "中雪", "heavysnow": "大雪",
		"lightsnowshowers": "小阵雪", "snowshowers": "阵雪", "heavysnowshowers": "大阵雪",
	}
	w, ok := words[base]
	switch {
	case thunder && ok:
		return w + "伴雷暴"
	case thunder:
		return "雷暴"
	case ok:
		return w
	}
	return "天气 " + cleanName(code, 24)
}

// ---- 拼结果 ----

// placeName：编码接口给的名字、省、国家（都当外部文字清理），和问的不一样时都写上
func placeName(asked, name, admin, country string) string {
	n := cleanName(name, 30)
	if n == "" {
		n = cleanName(asked, 30)
	}
	var extra []string
	for _, s := range []string{admin, country} {
		if s = cleanName(s, 30); s != "" && s != n && !containsString(extra, s) {
			extra = append(extra, s)
		}
	}
	if len(extra) > 0 {
		return n + "（" + strings.Join(extra, "，") + "）"
	}
	return n
}

func containsString(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func temp(v float64) string {
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + "°C"
}

func formatWeather(place string, r weatherReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s 现在 %s", place, temp(*r.Temp))
	if r.Feels != nil {
		fmt.Fprintf(&b, "，体感 %s", temp(*r.Feels))
	}
	if r.Word != "" {
		fmt.Fprintf(&b, "，%s", r.Word)
	}
	b.WriteString("。")
	for i, label := range []string{"今天", "明天"} {
		if i >= len(r.Days) {
			break
		}
		d := r.Days[i]
		fmt.Fprintf(&b, "\n%s（%s）：%s ～ %s", label, cleanName(d.Date, 10), temp(*d.Min), temp(*d.Max))
		if d.Word != "" {
			fmt.Fprintf(&b, "，%s", d.Word)
		}
		if d.Precip != nil {
			fmt.Fprintf(&b, "，降水概率 %d%%", int(math.Round(*d.Precip)))
		}
	}
	b.WriteString("\n数据来自 " + r.Source + "。")
	return b.String()
}

// weatherWord 是 WMO 天气代码的中文（Open-Meteo 文档的 weather_code 表）
func weatherWord(code int) string {
	switch code {
	case 0:
		return "晴"
	case 1:
		return "大致晴朗"
	case 2:
		return "多云"
	case 3:
		return "阴"
	case 45, 48:
		return "有雾"
	case 51:
		return "小毛毛雨"
	case 53:
		return "毛毛雨"
	case 55:
		return "大毛毛雨"
	case 56, 57:
		return "冻毛毛雨"
	case 61:
		return "小雨"
	case 63:
		return "中雨"
	case 65:
		return "大雨"
	case 66, 67:
		return "冻雨"
	case 71:
		return "小雪"
	case 73:
		return "中雪"
	case 75:
		return "大雪"
	case 77:
		return "雪粒"
	case 80:
		return "小阵雨"
	case 81:
		return "阵雨"
	case 82:
		return "强阵雨"
	case 85:
		return "小阵雪"
	case 86:
		return "大阵雪"
	case 95:
		return "雷暴"
	case 96, 99:
		return "雷暴伴冰雹"
	}
	return "天气代码 " + strconv.Itoa(code)
}

// cleanName 把外部来的名字当数据清理：去控制字符和换行、压空白、限长、转义（和其他插件拼系统行的规矩一样）
func cleanName(s string, maxRunes int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '　' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes]) + "…"
	}
	return channel.EscapeUserText(s)
}
