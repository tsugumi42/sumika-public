package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// DefaultHolidayURL 是法定假日数据的默认地址（NateScarlet/holiday-cn，每天抓国务院公告，每年一个 JSON）；{year} 换成年份
const DefaultHolidayURL = "https://raw.githubusercontent.com/NateScarlet/holiday-cn/master/{year}.json"

// LocationMaxRunes 是所在地文字的长度上限；天气工具的地点参数用同一个上限
const LocationMaxRunes = 64

// Location 是主人所在地的规范结果（规格-客户端自知 第十一节）。工具和设置页都写这个结构，候选都来自 lifetools.SearchPlaces。
// 整个对象为空 = 没设；city 为空 = 只知道到省，群里当不知道。
type Location struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`       // 规范显示名：某区，某市，中国
	CityName string `yaml:"city,omitempty" json:"city,omitempty"`       // 城市：群里只说到这一级
	Region   string `yaml:"region,omitempty" json:"region,omitempty"`   // 省、州
	Country  string `yaml:"country,omitempty" json:"country,omitempty"` // 国家
	// 国家代码（两位大写，如 CN）：免费接口按它排先后，腾讯天气只给国内。编码接口给的；手写可以省略，国家写中国或时区是 Asia/Shanghai 时按 CN
	CountryCode string   `yaml:"countryCode,omitempty" json:"countryCode,omitempty"`
	Latitude    *float64 `yaml:"latitude,omitempty" json:"latitude,omitempty"`   // 纬度
	Longitude   *float64 `yaml:"longitude,omitempty" json:"longitude,omitempty"` // 经度
	// 城市本身的坐标：群里查天气只用这一组，不用上面的（区县级的）精确坐标。没有就当群里不知道，不拿区的坐标顶
	CityLatitude  *float64 `yaml:"cityLatitude,omitempty" json:"cityLatitude,omitempty"`
	CityLongitude *float64 `yaml:"cityLongitude,omitempty" json:"cityLongitude,omitempty"`
	Timezone      string   `yaml:"timezone,omitempty" json:"timezone,omitempty"` // IANA 时区名，比如 Asia/Shanghai
}

// Set 说设没设所在地
func (l Location) Set() bool { return l.Name != "" }

// CityLocation 是裁到城市一级的所在地：群场合只能拿到这个
type CityLocation struct {
	Name, Region, Country string
	Latitude, Longitude   float64 // 城市本身的坐标，再取整到 0.1 度（约 11 公里）
}

// Display 是给群场合看的名字：城市，后面括号里最多带省和国家；不带任何比城市细的东西
func (c CityLocation) Display() string {
	var extra []string
	for _, s := range []string{c.Region, c.Country} {
		if s != "" && s != c.Name && !slices.Contains(extra, s) {
			extra = append(extra, s)
		}
	}
	if len(extra) == 0 {
		return c.Name
	}
	return c.Name + "（" + strings.Join(extra, "，") + "）"
}

// City 裁出城市一级。没设、只知道到省（city 空）、没有城市坐标都返回 false：调用方当不知道，不拿省顶，也不拿区县的坐标顶
func (l Location) City() (CityLocation, bool) {
	if !l.Set() || strings.TrimSpace(l.CityName) == "" || !validCoord(l.CityLatitude, l.CityLongitude) {
		return CityLocation{}, false
	}
	return CityLocation{Name: l.CityName, Region: l.Region, Country: l.Country,
		Latitude: math.Round(*l.CityLatitude*10) / 10, Longitude: math.Round(*l.CityLongitude*10) / 10}, true
}

func validCoord(lat, lon *float64) bool {
	return lat != nil && lon != nil && !math.IsNaN(*lat) && !math.IsNaN(*lon) && math.Abs(*lat) <= 90 && math.Abs(*lon) <= 180
}

var countryCodeShape = regexp.MustCompile(`^[A-Z]{2}$`)

var timezoneShape = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+\-]+)*$`)

// check 查所在地。错误里不写值（它是主人的私事）。时区只查写法：打包后的 exe 不一定带时区库，LoadLocation 不能当校验
func (l Location) check() []string {
	var bad []string
	if !l.Set() {
		if l.CityName != "" || l.Region != "" || l.Country != "" || l.CountryCode != "" || l.Latitude != nil || l.Longitude != nil || l.CityLatitude != nil || l.CityLongitude != nil || l.Timezone != "" {
			bad = append(bad, "location：写了别的字段就要有 name（规范显示名）；要清空写 location: {}")
		}
		return bad
	}
	for _, f := range []struct{ name, value string }{{"name", l.Name}, {"city", l.CityName}, {"region", l.Region}, {"country", l.Country}, {"countryCode", l.CountryCode}, {"timezone", l.Timezone}} {
		if n := len([]rune(f.value)); n > LocationMaxRunes {
			bad = append(bad, fmt.Sprintf("location.%s：%d 个字，最多 %d 个字", f.name, n, LocationMaxRunes))
		}
		if strings.IndexFunc(f.value, unicode.IsControl) >= 0 {
			bad = append(bad, "location."+f.name+"：不能带控制字符或换行")
		}
	}
	if !validCoord(l.Latitude, l.Longitude) {
		bad = append(bad, "location：要有合理的 latitude、longitude（纬度 -90～90，经度 -180～180）")
	}
	if (l.CityLatitude != nil || l.CityLongitude != nil) && !validCoord(l.CityLatitude, l.CityLongitude) {
		bad = append(bad, "location：cityLatitude、cityLongitude 要成对，并且合理（纬度 -90～90，经度 -180～180）")
	}
	if l.CountryCode != "" && !countryCodeShape.MatchString(l.CountryCode) {
		bad = append(bad, "location.countryCode：要写成两位大写字母，比如 CN")
	}
	if l.Timezone != "" && !timezoneShape.MatchString(l.Timezone) {
		bad = append(bad, "location.timezone：要写成 Asia/Shanghai 这样")
	}
	return bad
}

// checkLife 查日历、所在地几个设置
func (c Config) checkLife() []string {
	var bad []string
	if u := c.Calendar.HolidayURL; u != "" {
		p, err := url.Parse(strings.ReplaceAll(u, "{year}", "2000"))
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || p.User != nil || !strings.Contains(u, "{year}") {
			bad = append(bad, "calendar.holidayUrl：要写成 http(s) 地址，并带上 {year} 代表年份")
		}
	}
	bad = append(bad, c.Location.check()...)
	return bad
}
