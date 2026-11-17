package lifetools

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"sumika-native/internal/freeapi"
)

// 假日数据的三个提供方（规格-免费接口）：holiday-cn 经 jsDelivr、holiday-cn 的 GitHub raw、timor.tech。
// 三家的结果都整理成 holiday-cn 的 JSON 形状（year、days[{name,date,isOffDay}]），再进缓存和 parseYear 校验，
// 所以缓存文件的格式不随提供方变。404 = 这一年还没公布。

// holidayFetcher 按提供方 id 造取某年假日数据的做法，返回整理好的 JSON 字节
func holidayFetcher(id string, year int) freeapi.Fetcher {
	return func(ctx context.Context, env freeapi.Env) (any, error) {
		u := strings.ReplaceAll(env.URL, "{year}", strconv.Itoa(year))
		b, err := freeapi.Get(ctx, env.Client, u, nil, holidayBodyMax)
		if err != nil {
			return nil, notFoundOn404(err)
		}
		if id == "holiday-timor" {
			b, err = timorToCN(b, year)
			if err != nil {
				return nil, err
			}
		}
		if _, err := parseYear(b, year); err != nil {
			return nil, freeapi.Schema("%s", err.Error())
		}
		return b, nil
	}
}

// timorToCN 把 timor.tech 的 {code, holiday:{"MM-DD":{holiday,name,date,target,...}}} 转成 holiday-cn 的形状。
// holiday 为真是放假；为假是调休上班，它的 name 是“元旦后补班”，target 才是节日名，和 holiday-cn 一致取 target。
// 没公布的年份它回 200 加空表：当“查无这一年”，不记它的错
func timorToCN(b []byte, year int) ([]byte, error) {
	var t struct {
		Code    *int `json:"code"`
		Holiday map[string]struct {
			Holiday *bool  `json:"holiday"`
			Name    string `json:"name"`
			Date    string `json:"date"`
			Target  string `json:"target"`
		} `json:"holiday"`
	}
	if err := json.Unmarshal(b, &t); err != nil || t.Code == nil || *t.Code != 0 || t.Holiday == nil {
		return nil, freeapi.Schema("节假日接口回的不是合法数据")
	}
	if len(t.Holiday) == 0 {
		return nil, freeapi.ErrNotFound
	}
	type day struct {
		Name     string `json:"name"`
		Date     string `json:"date"`
		IsOffDay bool   `json:"isOffDay"`
	}
	days := make([]day, 0, len(t.Holiday))
	for _, v := range t.Holiday {
		if v.Holiday == nil {
			return nil, freeapi.Schema("节假日接口缺字段")
		}
		name := v.Name
		if !*v.Holiday && v.Target != "" {
			name = v.Target
		}
		days = append(days, day{Name: name, Date: v.Date, IsOffDay: *v.Holiday})
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	return json.Marshal(struct {
		Year int   `json:"year"`
		Days []day `json:"days"`
	}{year, days})
}
