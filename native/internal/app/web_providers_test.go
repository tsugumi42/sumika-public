package app

import (
	"errors"
	"testing"

	"sumika-native/internal/config"
)

func TestChooseWebProvidersAutoOrder(t *testing.T) {
	t.Parallel()
	url := func(u string) config.Web {
		c := config.EcosystemDefaults().Web
		c.SearXNG.URL = u
		return c
	}
	all := map[string]string{"tavily": "a", "exa": "b", "brave": "c", "firecrawl": "d"}
	allDS := map[string]string{"tavily": "a", "exa": "b", "brave": "c", "firecrawl": "d", "deepseek": "e"}
	cases := []struct {
		name string
		c    config.Web
		keys map[string]string
		errs map[string]error
		want string
	}{
		{"什么都没配落到 duckduckgo", url(""), nil, nil, "duckduckgo"},
		{"只配了地址用 searxng", url("https://s.example"), nil, nil, "searxng"},
		{"有 deepseek key 它排第一", url("https://s.example"), allDS, nil, "deepseek"},
		{"deepseek 的 key 引用失败就跳过", url(""), allDS, map[string]error{"deepseek": errors.New("坏了")}, "tavily"},
		{"全配了先 tavily", url("https://s.example"), all, nil, "tavily"},
		{"没有 tavily 用 exa", url("https://s.example"), map[string]string{"exa": "b", "brave": "c", "firecrawl": "d"}, nil, "exa"},
		{"只有 brave 和 firecrawl 用 brave", url(""), map[string]string{"brave": "c", "firecrawl": "d"}, nil, "brave"},
		{"只有 firecrawl", url(""), map[string]string{"firecrawl": "d"}, nil, "firecrawl"},
		{"firecrawl 优先于 searxng", url("https://s.example"), map[string]string{"firecrawl": "d"}, nil, "firecrawl"},
		{"key 引用失败的跳过", url(""), map[string]string{"exa": "b", "brave": "c"}, map[string]error{"exa": errors.New("坏了")}, "brave"},
		{"空白 key 当没配", url(""), map[string]string{"tavily": "  "}, nil, "duckduckgo"},
	}
	for _, tc := range cases {
		search, fetch, err := chooseWebProviders(tc.c, tc.keys, tc.errs, nil, nil, nil)
		if err != nil {
			t.Errorf("%s：%v", tc.name, err)
			continue
		}
		if search.ID() != tc.want {
			t.Errorf("%s：选了 %s，应为 %s", tc.name, search.ID(), tc.want)
		}
		if fetch.ID() != "http" {
			t.Errorf("%s：读取默认应为 http，得到 %s", tc.name, fetch.ID())
		}
	}
}

func TestChooseWebProvidersExplicit(t *testing.T) {
	t.Parallel()
	c := config.EcosystemDefaults().Web
	if c.SearchProvider != "auto" || c.FetchProvider != "auto" {
		t.Fatalf("默认值不对：%s %s", c.SearchProvider, c.FetchProvider)
	}
	c.SearchProvider, c.FetchProvider = "exa", "firecrawl"
	search, fetch, err := chooseWebProviders(c, map[string]string{"exa": "b", "tavily": "a", "firecrawl": "d"}, nil, nil, nil, nil)
	if err != nil || search.ID() != "exa" || fetch.ID() != "firecrawl" {
		t.Fatalf("显式选择不对：%v %v %v", search, fetch, err)
	}
	// 显式选了的服务没 key：Available 在 web.New 里报错，这里应原样返回该提供方
	c.SearchProvider = "brave"
	if s, _, err := chooseWebProviders(c, nil, nil, nil, nil, nil); err != nil || s.Available() == nil {
		t.Fatalf("没 key 的显式提供方应不可用：%v %v", s, err)
	}
	// 显式选了的服务 key 引用坏了：直接报错
	boom := errors.New("引用失败")
	if _, _, err := chooseWebProviders(c, nil, map[string]error{"brave": boom}, nil, nil, nil); !errors.Is(err, boom) {
		t.Fatalf("应报 key 引用错误：%v", err)
	}
	c.SearchProvider = "duckduckgo"
	c.FetchProvider = "tavily"
	if _, _, err := chooseWebProviders(c, nil, map[string]error{"tavily": boom}, nil, nil, nil); !errors.Is(err, boom) {
		t.Fatalf("读取服务 key 引用错误应报错：%v", err)
	}
	for _, bad := range [][2]string{{"nope", "http"}, {"auto", "nope"}, {"auto", "exa"}} {
		c.SearchProvider, c.FetchProvider = bad[0], bad[1]
		if _, _, err := chooseWebProviders(c, nil, nil, nil, nil, nil); err == nil {
			t.Errorf("%v 应报未知名字", bad)
		}
	}
}

func TestChooseWebProvidersCustomOrder(t *testing.T) {
	t.Parallel()
	c := config.EcosystemDefaults().Web
	c.SearchOrder = []string{"firecrawl", "tavily", "duckduckgo"}
	c.FetchOrder = []string{"tavily", "http"}
	keys := map[string]string{"tavily": "a", "firecrawl": "d", "deepseek": "e"}
	search, fetch, err := chooseWebProviders(c, keys, nil, nil, nil, nil)
	if err != nil || search.ID() != "firecrawl" || fetch.ID() != "tavily" {
		t.Fatalf("按自定义顺序选：%v %v %v", search, fetch, err)
	}
	// 顺序列表里没有的家不会被用
	c.SearchOrder = []string{"duckduckgo"}
	if search, _, err = chooseWebProviders(c, keys, nil, nil, nil, nil); err != nil || search.ID() != "duckduckgo" {
		t.Fatalf("不在列表里的家不该被选：%v %v", search, err)
	}
}
