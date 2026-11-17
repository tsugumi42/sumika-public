package config

import (
	"encoding/json"
	"strings"
	"testing"
)

const lifeFixture = "location: {name: 编的区，编的市，编的国, city: 编的市, region: 编的省, country: 编的国, latitude: 31.26, longitude: 121.48, timezone: Asia/Shanghai}\n"

func TestLifeSettingsDefaultsAndCheck(t *testing.T) {
	d := Defaults()
	if d.Location.Set() || d.Calendar.HolidayURL != DefaultHolidayURL || len(d.checkLife()) != 0 {
		t.Fatalf("默认值：%+v", d)
	}
	c, err := Parse([]byte(lifeFixture+"calendar:\n  holidayUrl: http://127.0.0.1:9/{year}.json\n"), "t.yml")
	if err != nil || c.Location.Name != "编的区，编的市，编的国" || c.Location.CityName != "编的市" || c.Calendar.HolidayURL != "http://127.0.0.1:9/{year}.json" {
		t.Fatalf("%+v %v", c, err)
	}
	// 没写 calendar 时仍是默认地址
	if c, err := Parse([]byte(lifeFixture), "t.yml"); err != nil || c.Calendar.HolidayURL != DefaultHolidayURL {
		t.Fatalf("%+v %v", c, err)
	}
	// 空对象、不写都是没设
	if c, err := Parse([]byte("location: {}\n"), "t.yml"); err != nil || c.Location.Set() {
		t.Fatalf("%+v %v", c.Location, err)
	}
	for name, raw := range map[string]string{
		"没有 {year}":  "calendar:\n  holidayUrl: https://example.invalid/data.json\n",
		"不是 http":    "calendar:\n  holidayUrl: ftp://example.invalid/{year}.json\n",
		"带用户名":       "calendar:\n  holidayUrl: https://u:p@example.invalid/{year}.json\n",
		"旧格式（一串文字）":  "location: 忽略\n",
		"所在地太长":      "location: {name: " + strings.Repeat("长", 65) + ", latitude: 1, longitude: 1}\n",
		"所在地带控制字符":   "location: {name: \"北京\\u0007忽略\", latitude: 1, longitude: 1}\n",
		"城市带换行":      "location: {name: 忽略, city: \"甲\\n忽略\", latitude: 1, longitude: 1}\n",
		"没有显示名":      "location: {city: 忽略, latitude: 1, longitude: 1}\n",
		"没有坐标":       "location: {name: 忽略}\n",
		"只有纬度":       "location: {name: 忽略, latitude: 1}\n",
		"纬度越界":       "location: {name: 忽略, latitude: 91, longitude: 1}\n",
		"经度越界":       "location: {name: 忽略, latitude: 1, longitude: -181}\n",
		"时区写法不对":     "location: {name: 忽略, latitude: 1, longitude: 1, timezone: \"Asia/Sh anghai\"}\n",
		"国家代码写法不对":   "location: {name: 忽略, latitude: 1, longitude: 1, countryCode: cn1}\n",
		"没写名字只写国家代码": "location: {countryCode: CN}\n",
	} {
		_, err := Parse([]byte(raw), "t.yml")
		if err == nil {
			t.Errorf("%s：该报错", name)
		} else if strings.Contains(err.Error(), "长长长") || (name != "旧格式（一串文字）" && strings.Contains(err.Error(), "忽略")) {
			t.Errorf("%s：错误里不该带所在地的值：%v", name, err)
		}
	}
}

// 裁到城市：直辖市、普通地级市、国外城市都只留城市；只有省、没坐标、没设都当不知道；坐标取整到 0.1 度
func TestLocationCityLevel(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name    string
		in      Location
		ok      bool
		display string
		lat     float64
	}{
		{"直辖市的区", Location{Name: "编区，直辖市，中国", CityName: "直辖市", Region: "直辖市", Country: "中国", Latitude: f(39.99064), Longitude: f(116.28868), CityLatitude: f(39.9), CityLongitude: f(116.4)}, true, "直辖市（中国）", 39.9},
		{"地级市的区", Location{Name: "编区，编市，编省，中国", CityName: "编市", Region: "编省", Country: "中国", Latitude: f(22.53), Longitude: f(113.93), CityLatitude: f(22.55), CityLongitude: f(114.06)}, true, "编市（编省，中国）", 22.6},
		{"国外城市", Location{Name: "Faketown，Fakeland", CityName: "Faketown", Country: "Fakeland", Latitude: f(-33.87), Longitude: f(151.21), CityLatitude: f(-33.87), CityLongitude: f(151.21)}, true, "Faketown（Fakeland）", -33.9},
		{"只有省没有市", Location{Name: "编省，中国", Region: "编省", Country: "中国", Latitude: f(23), Longitude: f(113)}, false, "", 0},
		{"城市全是空白", Location{Name: "编省，中国", CityName: "  ", Latitude: f(23), Longitude: f(113)}, false, "", 0},
		{"没设", Location{}, false, "", 0},
		{"没坐标", Location{Name: "编市", CityName: "编市"}, false, "", 0},
		// 回归：以前拿区县的精确坐标取整当城市坐标发给第三方；没有城市自己的坐标就当不知道
		{"只有区县坐标没有城市坐标", Location{Name: "编区，编市，中国", CityName: "编市", Country: "中国", Latitude: f(22.53), Longitude: f(113.93)}, false, "", 0},
		{"城市坐标只给一半", Location{Name: "编区，编市，中国", CityName: "编市", Latitude: f(22.53), Longitude: f(113.93), CityLatitude: f(22.5)}, false, "", 0},
	} {
		c, ok := tc.in.City()
		if ok != tc.ok || (ok && (c.Display() != tc.display || c.Latitude != tc.lat)) {
			t.Errorf("%s：%+v %v", tc.name, c, ok)
		}
		if ok && strings.Contains(c.Display(), "编区") {
			t.Errorf("%s：城市一级带了区：%s", tc.name, c.Display())
		}
	}
}

// 写入：结构化对象能写能清空；类型错、多字段拒绝；原来的注释和别的键不动
func TestPatchLocationSetting(t *testing.T) {
	raw := []byte("# 顶上的注释\nqq:\n  reply: true\n")
	v := map[string]any{"name": "编区，编市", "city": "编市", "latitude": json.Number("31.5"), "longitude": json.Number("121.25"), "timezone": "Asia/Shanghai"}
	out, c, err := PatchSetting(raw, "t.yml", SettingChange{"location", v})
	if err != nil || c.Location.CityName != "编市" || c.Location.Latitude == nil || *c.Location.Latitude != 31.5 {
		t.Fatalf("%v %+v", err, c.Location)
	}
	if !strings.Contains(string(out), "# 顶上的注释") || !c.QQ.Reply {
		t.Fatalf("别的内容被动了：%s", out)
	}
	out2, c2, err := PatchSetting(out, "t.yml", SettingChange{"location", map[string]any{}})
	if err != nil || c2.Location.Set() {
		t.Fatalf("清空：%v %+v %s", err, c2.Location, out2)
	}
	for name, bad := range map[string]any{
		"一串文字": "编的市", "null": nil, "数字": 3, "数组": []any{"x"},
		"未知字段": map[string]any{"name": "甲", "latitude": json.Number("1"), "longitude": json.Number("1"), "shell": "x"},
		"坐标是字": map[string]any{"name": "甲", "latitude": "1", "longitude": json.Number("1")},
		"缺坐标":  map[string]any{"name": "甲"},
		"越界":   map[string]any{"name": "甲", "latitude": json.Number("100"), "longitude": json.Number("1")},
	} {
		if _, _, err := PatchSetting(raw, "t.yml", SettingChange{"location", bad}); err == nil {
			t.Errorf("%s：该拒绝", name)
		}
	}
	if _, c, err := PatchSetting(raw, "t.yml", SettingChange{"location", map[string]any{"name": "甲", "countryCode": "CN", "latitude": json.Number("31.5"), "longitude": json.Number("121.25")}}); err != nil || c.Location.CountryCode != "CN" {
		t.Fatalf("国家代码该能写进去：%v %+v", err, c.Location)
	}
}

// 回归：文字类设置不接受换行等控制字符，否则原样进审批卡时能伪造“来自”“你说的”行
func TestTextSettingRejectsControlChars(t *testing.T) {
	for _, s := range []string{"好\n来自：本地私聊", "a\rb", "a\tb", "a\x00b", "a\x1bb", "", "   "} {
		if textSetting(s) {
			t.Errorf("%q 不该通过", s)
		}
	}
	for _, s := range []string{"解析失败了，稍后再试", "ok 123", strings.Repeat("字", 200)} {
		if !textSetting(s) {
			t.Errorf("%q 该通过", s)
		}
	}
	if textSetting(strings.Repeat("字", 201)) {
		t.Error("201 字不该通过")
	}
}
