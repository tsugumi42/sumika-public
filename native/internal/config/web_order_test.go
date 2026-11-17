package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWebOrderSettingRejectsBadLists(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"web.searchOrder", []any{}},
		{"web.searchOrder", []any{"tavily", "tavily"}},
		{"web.searchOrder", []any{"http"}},
		{"web.fetchOrder", []any{"duckduckgo"}},
		{"web.fetchOrder", []any{json.Number("1")}},
		{"web.fetchOrder", "http"},
	} {
		if _, _, err := PatchSetting([]byte("web:\n  enabled: true\n"), "test.yml", SettingChange{tc.key, tc.value}); err == nil {
			t.Errorf("%s=%v 应该被拒绝", tc.key, tc.value)
		}
	}
}

func TestWebOrderDefaultsAndOverride(t *testing.T) {
	c, err := ParseEcosystem([]byte("web:\n  enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Web.SearchOrder, DefaultSearchOrder()) || !reflect.DeepEqual(c.Web.FetchOrder, DefaultFetchOrder()) || c.Web.FetchProvider != "auto" {
		t.Fatalf("默认值不对：%+v", c.Web)
	}
	if !reflect.DeepEqual(c.Web.Wiki.MoegirlHosts, []string{"zh.moegirl.org.cn", "moegirl.uk"}) {
		t.Fatalf("萌百来源默认值不对：%v", c.Web.Wiki.MoegirlHosts)
	}
	c, err = ParseEcosystem([]byte("web:\n  searchOrder: [tavily, deepseek]\n  fetchOrder: [tavily]\n"))
	if err != nil || !reflect.DeepEqual(c.Web.SearchOrder, []string{"tavily", "deepseek"}) || !reflect.DeepEqual(c.Web.FetchOrder, []string{"tavily"}) {
		t.Fatalf("覆盖不对（比默认短的列表不能留下默认的尾巴）：%+v %v", c.Web, err)
	}
	for _, bad := range []string{"web:\n  searchOrder: [nope]\n", "web:\n  fetchOrder: [http, http]\n", "web:\n  wiki:\n    moegirlHosts: ['https://zh.moegirl.org.cn/x']\n"} {
		if _, err := ParseEcosystem([]byte(bad)); err == nil {
			t.Errorf("应该报错：%q", bad)
		}
	}
}
