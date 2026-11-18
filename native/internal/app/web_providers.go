package app

import (
	"fmt"
	"net/http"

	"sumika-native/internal/config"
	"sumika-native/internal/web"
	"sumika-native/internal/web/brave"
	"sumika-native/internal/web/deepseek"
	"sumika-native/internal/web/duckduckgo"
	"sumika-native/internal/web/exa"
	"sumika-native/internal/web/firecrawl"
	"sumika-native/internal/web/httpfetch"
	"sumika-native/internal/web/searxng"
	"sumika-native/internal/web/tavily"
)

// chooseWebProviders 按配置选搜索和读取提供方。keys 的键是服务名（tavily、exa、brave、firecrawl、deepseek），
// keyErrs 记哪家的 keyRef 解析失败：显式选了这家就报错，auto 只当它不可用。
//
// auto 时先后由 order 决定（设置页拖动排序后即时改它，不用重启）：搜索取第一个可用（有 key、没用完）的，
// 抓网页按顺序试、前一家失败换下一家。order 为 nil 时按配置里的顺序建一份。
// balances 记各家的真实余额（可为 nil）：搜索按它跳过用完的家，显式指定的这家用完时报“额度用完了”。
func chooseWebProviders(c config.Web, keys map[string]string, keyErrs map[string]error, client *http.Client, balances *web.Balances, order *web.Order) (web.SearchProvider, web.FetchProvider, error) {
	if order == nil {
		order = web.NewOrder(orderOrDefault(c.SearchOrder, config.DefaultSearchOrder()), orderOrDefault(c.FetchOrder, config.DefaultFetchOrder()))
	}
	tv := tavily.New(keys["tavily"], client)
	fc := firecrawl.New(keys["firecrawl"], client)
	ex := exa.New(keys["exa"], client)
	br := brave.New(keys["brave"], client)
	ds := deepseek.New(keys["deepseek"], client)
	for _, p := range []interface{ SetBalances(*web.Balances) }{tv, fc, ex, br, ds} {
		p.SetBalances(balances)
	}
	searches := map[string]web.SearchProvider{
		"deepseek":   ds,
		"tavily":     tv,
		"exa":        ex,
		"brave":      br,
		"firecrawl":  fc,
		"searxng":    searxng.New(c.SearXNG.URL, client),
		"duckduckgo": duckduckgo.New(client),
	}
	fetches := map[string]web.FetchProvider{
		"http":      &httpfetch.Provider{Policy: web.URLPolicy{}, Version: "kernel-k4"},
		"deepseek":  ds,
		"tavily":    tv,
		"firecrawl": fc,
	}
	var search *web.BalancedSearch
	switch name := c.SearchProvider; name {
	case "", "auto":
		// key 解析失败的家当作不存在；其余每次搜索前按余额和顺序现挑：已知用完的家跳过，换下一家。
		all := map[string]web.SearchProvider{}
		for id, p := range searches {
			if keyErrs[id] == nil {
				all[id] = p
			}
		}
		search = web.NewOrderedSearch(all, order, balances)
		if err := search.Available(); err != nil {
			return nil, nil, fmt.Errorf("没有可用的 web 搜索提供方")
		}
	default:
		p := searches[name]
		if p == nil {
			return nil, nil, fmt.Errorf("未知 web search_provider")
		}
		if err := keyErrs[name]; err != nil {
			return nil, nil, err
		}
		search = web.NewBalancedSearch([]web.SearchProvider{p}, balances, false)
	}
	var fetch web.FetchProvider
	switch name := c.FetchProvider; name {
	case "", "auto":
		all := map[string]web.FetchProvider{}
		for id, p := range fetches {
			if keyErrs[id] == nil {
				all[id] = p
			}
		}
		chain := web.NewFetchChain(all, order, balances)
		if err := chain.Available(); err != nil {
			return nil, nil, err
		}
		fetch = chain
	default:
		p := fetches[name]
		if p == nil {
			return nil, nil, fmt.Errorf("未知 web fetch_provider")
		}
		if err := keyErrs[name]; err != nil {
			return nil, nil, err
		}
		fetch = p
	}
	return search, fetch, nil
}

func orderOrDefault(list, fallback []string) []string {
	if len(list) == 0 {
		return fallback
	}
	return list
}
