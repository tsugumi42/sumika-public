package lifetools

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
)

// 地点编码（Open-Meteo，language=zh）。天气工具查显式地名、设置页和设置工具的所在地候选共用这一份。
//
// 实测（2026-11-09，规格-客户端自知 11.4）：它的中文索引很挑。带后缀的“北京市”“南山区”“广东省”“东京都”什么都查不到，
// 去掉后缀才有；第一个结果可能是山（“南山”是台湾的一座山）。所以先按原文查，空了去后缀再查一次，滤掉不是居民点的。

// placeCount 是问编码接口要几个结果：过滤以后还要够候选用
const placeCount = 10

type geoHit struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Feature     string   `json:"feature_code"`
	Admin1      string   `json:"admin1"`
	Admin2      string   `json:"admin2"`
	Country     string   `json:"country"`
	CountryCode string   `json:"country_code"`
	Timezone    string   `json:"timezone"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
}

type geoResp struct {
	Results []geoHit `json:"results"`
}

// usable：坐标合理，而且是居民点或行政区（没写种类的也算，山、岛、河这些不算）
func (h geoHit) usable() bool {
	if h.Latitude == nil || h.Longitude == nil {
		return false
	}
	if !validCoords(*h.Latitude, *h.Longitude) {
		return false
	}
	return h.Feature == "" || strings.HasPrefix(h.Feature, "PPL") || strings.HasPrefix(h.Feature, "ADM") || strings.HasPrefix(h.Feature, "PCL")
}

// validCoords：坐标合理（不是 NaN，纬度 -90～90，经度 -180～180）
func validCoords(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

// cityOf 是这个地点所在的城市，说不出来就是空（群里当不知道）。
// 中国的数据里 admin2 是地级市（海淀是北京市，南山是深圳），可能没有；没有且本身不是城市级就不猜，也不拿省顶。
// 国外的 admin2 多是县，不能当城市：居民点本身就算城市，城区（PPLX）才往上取。
func (h geoHit) cityOf() string {
	if h.Feature == "ADM1" || strings.HasPrefix(h.Feature, "PCL") {
		return ""
	}
	cityLevel := h.Feature == "PPLC" || h.Feature == "PPLA" || h.Feature == "PPLA2" || h.Feature == "ADM2" || h.Feature == ""
	if h.CountryCode == "CN" || h.CountryCode == "TW" {
		if h.Admin2 != "" {
			return h.Admin2
		}
		if cityLevel {
			return h.Name
		}
		return ""
	}
	if h.Feature == "PPLX" {
		return h.Admin2
	}
	return h.Name
}

// plainName 把外部来的名字收拾成能存进配置的文字：去控制字符、压空白、限长。不转义（显示时再按用途处理）
func plainName(s string, maxRunes int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes])
	}
	return s
}

// label 是候选上显示的名字：海淀，北京市，中国。同名的层级去重
func (h geoHit) label() string {
	var parts []string
	for _, s := range []string{h.Name, h.Admin2, h.Admin1, h.Country} {
		if s = plainName(s, 30); s != "" && !slices.Contains(parts, s) {
			parts = append(parts, s)
		}
	}
	return plainName(strings.Join(parts, "，"), config.LocationMaxRunes)
}

// PlaceCandidate 是一个所在地候选（设置页的搜索下拉、preview_setting_change 用）；查天气用的地点是 Place
type PlaceCandidate struct {
	ID       string          // 编码接口的编号；同一个查询里唯一
	Label    string          // 海淀，北京市，中国
	Location config.Location // 选中以后存的规范结果
}

func (h geoHit) candidate() PlaceCandidate {
	lat, lon := *h.Latitude, *h.Longitude
	tz := plainName(h.Timezone, config.LocationMaxRunes)
	l := config.Location{
		Name: h.label(), CityName: plainName(h.cityOf(), config.LocationMaxRunes), Region: plainName(h.Admin1, config.LocationMaxRunes),
		Country: plainName(h.Country, config.LocationMaxRunes), CountryCode: countryCodeOf(h.CountryCode), Latitude: &lat, Longitude: &lon, Timezone: tz,
	}
	if c := h.cityOf(); c != "" && c == h.Name {
		l.CityLatitude, l.CityLongitude = &lat, &lon // 这个地点本身就是城市：城市坐标就是它的坐标
	}
	return PlaceCandidate{ID: strconv.FormatInt(h.ID, 10), Label: h.label(), Location: l}
}

// countryCodeOf 只认两位字母的国家代码（存进配置前要过校验），别的当没有
func countryCodeOf(s string) string {
	if s = strings.ToUpper(strings.TrimSpace(s)); len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z' {
		return s
	}
	return ""
}

// cityCoords 给“区县”一类候选补城市本身的坐标：再按城市名查一次，取同国家、同省的第一个居民点。
// 查不到不报错，城市坐标留空，群里当不知道所在地，不拿区县的坐标顶（规格-客户端自知 11.4）
func (p *Plugin) cityCoords(ctx context.Context, hit geoHit, l *config.Location, memo map[string]*geoHit) {
	if l.CityLatitude != nil || l.CityName == "" {
		return
	}
	key := hit.CountryCode + "|" + hit.Admin1 + "|" + l.CityName
	best, seen := memo[key]
	if !seen {
		if hits, err := p.geocodeHits(ctx, l.CityName); err == nil {
			for i, c := range hits {
				if c.CountryCode == hit.CountryCode && c.Admin1 == hit.Admin1 && (c.Name == hit.cityOf() || strings.HasPrefix(hit.cityOf(), c.Name)) && c.Feature != "ADM1" && !strings.HasPrefix(c.Feature, "PCL") {
					best = &hits[i]
					break
				}
			}
		}
		memo[key] = best
	}
	if best != nil {
		lat, lon := *best.Latitude, *best.Longitude
		l.CityLatitude, l.CityLongitude = &lat, &lon
	}
}

// placeSuffixes 是能去掉再查一次的行政后缀，长的在前
var placeSuffixes = []string{"特别行政区", "自治区", "自治州", "市", "省", "区", "县", "都"}

// placeQueries 是一个地名要查的几种写法：原文，查不到时再去后缀
func placeQueries(q string) []string {
	out := []string{q}
	for _, suf := range placeSuffixes {
		if base := strings.TrimSuffix(q, suf); base != q && len([]rune(base)) >= 2 {
			return append(out, base)
		}
	}
	return out
}

// SearchPlaces 查地点候选：设置页的搜索下拉和 preview_setting_change 共用。最多 limit 个；查不到回空。
// 这里不算天气的每日次数：只有主人本人能调（设置页、主人私聊的工具），停手才查一次。
func (p *Plugin) SearchPlaces(ctx context.Context, query string, limit int) ([]PlaceCandidate, error) {
	query = plainName(query, config.LocationMaxRunes)
	if query == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.o.WeatherTimeout)
	defer cancel()
	hits, err := p.geocodeHits(ctx, query)
	if err != nil {
		if errors.Is(err, freeapi.ErrNotFound) {
			return nil, nil
		}
		return nil, mapCallErr(err)
	}
	seen := map[string]bool{}
	memo := map[string]*geoHit{}
	var out []PlaceCandidate
	for _, h := range hits {
		pl := h.candidate()
		if seen[pl.Label] || pl.Label == "" {
			continue
		}
		seen[pl.Label] = true
		p.cityCoords(ctx, h, &pl.Location, memo)
		out = append(out, pl)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
