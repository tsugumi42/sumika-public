package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

type Skills struct {
	Enabled         bool        `yaml:"enabled"`
	IndexBytes      int         `yaml:"indexBytes"`
	PageItems       int         `yaml:"pageItems"`
	ReadBytes       int         `yaml:"readBytes"`
	FileMaxBytes    int         `yaml:"fileMaxBytes"`
	ContextMaxBytes int         `yaml:"contextMaxBytes"`
	ScanTimeoutMs   int         `yaml:"scanTimeoutMs"`
	Roots           []SkillRoot `yaml:"roots"`
}
type SkillRoot struct {
	ID    string `yaml:"id"`
	Path  string `yaml:"path"`
	Scope string `yaml:"scope"`
}
type Web struct {
	Enabled         bool   `yaml:"enabled"`
	SearchProvider  string `yaml:"search_provider"`
	FetchProvider   string `yaml:"fetch_provider"`
	TimeoutMs       int    `yaml:"timeoutMs"`
	KeyRef          string `yaml:"keyRef"` // Tavily 的 key 引用
	ExaKeyRef       string `yaml:"exaKeyRef"`
	BraveKeyRef     string `yaml:"braveKeyRef"`
	FirecrawlKeyRef string `yaml:"firecrawlKeyRef"`
	DeepSeekKeyRef  string `yaml:"deepseekKeyRef"`
	// SearchOrder、FetchOrder 是 auto 时各家的先后：搜索取第一个可用的（有 key、没用完），
	// 抓网页按顺序试，前一家失败就换下一家。设置页可拖动排序，改了立即生效。
	SearchOrder []string `yaml:"searchOrder"`
	FetchOrder  []string `yaml:"fetchOrder"`
	Wiki        WebWiki  `yaml:"wiki"`
	SearXNG     struct {
		URL string `yaml:"url"`
	} `yaml:"searxng"`
	DailySearch          *int64 `yaml:"dailySearch,omitempty"`
	DailyFetch           *int64 `yaml:"dailyFetch,omitempty"`
	MonthlyMicroCredits  *int64 `yaml:"monthlyMicroCredits,omitempty"`
	Timezone             string `yaml:"timezone"`
	ResponseMaxBytes     int    `yaml:"responseMaxBytes"`
	MaxInflight          int    `yaml:"maxInflight"`
	MaxInflightPerTask   int    `yaml:"maxInflightPerTask"`
	MaxQueriesPerTask    int    `yaml:"maxQueriesPerTask"`
	MaxFetchesPerTask    int    `yaml:"maxFetchesPerTask"`
	MaxDispatchesPerTask int    `yaml:"maxDispatchesPerTask"`
	SearchCacheSeconds   int    `yaml:"searchCacheSeconds"`
	FetchCacheSeconds    int    `yaml:"fetchCacheSeconds"`
	CacheBytes           int    `yaml:"cacheBytes"`
	CacheEntries         int    `yaml:"cacheEntries"`
}
type WebWiki struct {
	// MoegirlHosts 是萌娘百科的来源，依次试：主站拒绝 API 或出校验页就换下一个（镜像）。
	MoegirlHosts []string `yaml:"moegirlHosts"`
}

// SearchProviderIDs、FetchProviderIDs 是 web.searchOrder、web.fetchOrder 里允许出现的服务名，也是默认顺序。
var (
	searchProviderIDs = []string{"deepseek", "tavily", "exa", "brave", "firecrawl", "searxng", "duckduckgo"}
	fetchProviderIDs  = []string{"http", "deepseek", "tavily", "firecrawl"}
)

func DefaultSearchOrder() []string { return append([]string(nil), searchProviderIDs...) }
func DefaultFetchOrder() []string  { return append([]string(nil), fetchProviderIDs...) }

// ValidWebOrder 检查顺序列表：每项必须是已知服务名、不重复。key 是 web.searchOrder 或 web.fetchOrder。
func ValidWebOrder(key string, order []string) error {
	known := searchProviderIDs
	if key == "web.fetchOrder" {
		known = fetchProviderIDs
	}
	seen := map[string]bool{}
	for _, id := range order {
		if !slices.Contains(known, id) {
			return fmt.Errorf("%s 里有不认识的服务名", key)
		}
		if seen[id] {
			return fmt.Errorf("%s 里有重复的服务名", key)
		}
		seen[id] = true
	}
	return nil
}

type Subagents struct {
	Enabled               bool   `yaml:"enabled"`
	Model                 string `yaml:"model"`
	MaxDepth              int    `yaml:"maxDepth"`
	MaxActivePerRoot      int    `yaml:"maxActivePerRoot"`
	MaxActiveGlobal       int    `yaml:"maxActiveGlobal"`
	MaxActivePerRequester int    `yaml:"maxActivePerRequester"`
	ResearchTimeoutMs     int    `yaml:"researchTimeoutMs"`
	LocalTimeoutMs        int    `yaml:"localTimeoutMs"`
	// SoftRequestBudget 照 omp 的 task.softRequestBudget：一个子代理请求到这个次数时给它注入一句收尾提示，到 1.5 倍强停；0 = 不设
	SoftRequestBudget int `yaml:"softRequestBudget"`
	CreateTimeoutMs   int `yaml:"createTimeoutMs"`
	ResultTimeoutMs   int `yaml:"resultTimeoutMs"`
}
type Ecosystem struct {
	Skills    Skills    `yaml:"skills"`
	Web       Web       `yaml:"web"`
	Subagents Subagents `yaml:"subagents"`
}

func EcosystemDefaults() Ecosystem {
	return Ecosystem{Skills: Skills{IndexBytes: 8192, PageItems: 20, ReadBytes: 32768, FileMaxBytes: 1 << 20, ContextMaxBytes: 128 << 10, ScanTimeoutMs: 5000}, Web: Web{SearchProvider: "auto", FetchProvider: "auto", SearchOrder: DefaultSearchOrder(), FetchOrder: DefaultFetchOrder(), Wiki: WebWiki{MoegirlHosts: []string{"zh.moegirl.org.cn", "moegirl.uk"}}, TimeoutMs: 30000, Timezone: "Asia/Shanghai", ResponseMaxBytes: 5 << 20, MaxInflight: 4, MaxInflightPerTask: 2, MaxQueriesPerTask: 4, MaxFetchesPerTask: 8, MaxDispatchesPerTask: 12, SearchCacheSeconds: 300, FetchCacheSeconds: 600, CacheBytes: 16 << 20, CacheEntries: 256}, Subagents: Subagents{MaxDepth: 1, MaxActivePerRoot: 4, MaxActiveGlobal: 8, MaxActivePerRequester: 2, ResearchTimeoutMs: 120000, LocalTimeoutMs: 900000, SoftRequestBudget: 200, CreateTimeoutMs: 5000, ResultTimeoutMs: 5000}}
}

// ParseEcosystem 供配置组合器读取同一份完整 YAML；不读取或覆盖真实配置。
func ParseEcosystem(b []byte) (Ecosystem, error) {
	c := EcosystemDefaults()
	var full map[string]any
	if err := yaml.Unmarshal(b, &full); err != nil {
		return c, err
	}
	sections := map[string]any{}
	for _, key := range []string{"skills", "web", "subagents"} {
		if value, ok := full[key]; ok {
			sections[key] = value
		}
	}
	// YAML 解码器会把浮点数截成整数；额度和字节边界必须在原始值处拒绝这种收窄。
	for _, fields := range [][]string{{"skills", "indexBytes", "pageItems", "readBytes", "fileMaxBytes", "contextMaxBytes", "scanTimeoutMs"}, {"web", "timeoutMs", "dailySearch", "dailyFetch", "monthlyMicroCredits", "responseMaxBytes", "maxInflight", "maxInflightPerTask", "maxQueriesPerTask", "maxFetchesPerTask", "maxDispatchesPerTask", "searchCacheSeconds", "fetchCacheSeconds", "cacheBytes", "cacheEntries"}, {"subagents", "maxDepth", "maxActivePerRoot", "maxActiveGlobal", "maxActivePerRequester", "researchTimeoutMs", "localTimeoutMs", "softRequestBudget", "createTimeoutMs", "resultTimeoutMs"}} {
		values, _ := sections[fields[0]].(map[string]any)
		for _, key := range fields[1:] {
			if value, ok := values[key]; ok {
				switch value.(type) {
				case int, int64, uint64:
				default:
					return c, fmt.Errorf("%s.%s 必须是整数", fields[0], key)
				}
			}
		}
	}
	selected, err := yaml.Marshal(sections)
	if err != nil {
		return c, err
	}
	if err := yaml.UnmarshalWithOptions(selected, &c, yaml.Strict()); err != nil {
		return c, err
	}
	s := c.Skills
	for _, limit := range []struct {
		name           string
		value, maximum int
	}{{"web.responseMaxBytes", c.Web.ResponseMaxBytes, 5 << 20}, {"web.maxInflight", c.Web.MaxInflight, 8}, {"web.maxInflightPerTask", c.Web.MaxInflightPerTask, 2}, {"web.maxQueriesPerTask", c.Web.MaxQueriesPerTask, 4}, {"web.maxFetchesPerTask", c.Web.MaxFetchesPerTask, 8}, {"web.maxDispatchesPerTask", c.Web.MaxDispatchesPerTask, 12}, {"web.searchCacheSeconds", c.Web.SearchCacheSeconds, 300}, {"web.fetchCacheSeconds", c.Web.FetchCacheSeconds, 600}, {"web.cacheBytes", c.Web.CacheBytes, 16 << 20}, {"web.cacheEntries", c.Web.CacheEntries, 256}, {"subagents.maxDepth", c.Subagents.MaxDepth, 1}, {"subagents.maxActivePerRoot", c.Subagents.MaxActivePerRoot, 4}, {"subagents.maxActiveGlobal", c.Subagents.MaxActiveGlobal, 8}, {"subagents.maxActivePerRequester", c.Subagents.MaxActivePerRequester, 2}, {"subagents.researchTimeoutMs", c.Subagents.ResearchTimeoutMs, 120000}, {"subagents.localTimeoutMs", c.Subagents.LocalTimeoutMs, 900000}, {"subagents.createTimeoutMs", c.Subagents.CreateTimeoutMs, 5000}, {"subagents.resultTimeoutMs", c.Subagents.ResultTimeoutMs, 5000}} {
		if limit.value < 1 || limit.value > limit.maximum {
			return c, fmt.Errorf("%s 越界", limit.name)
		}
	}
	if s.IndexBytes < 1024 || s.IndexBytes > 8192 || s.PageItems < 1 || s.PageItems > 20 || s.ReadBytes < 1024 || s.ReadBytes > 32768 || s.FileMaxBytes < 1 || s.FileMaxBytes > 1<<20 || s.ContextMaxBytes < 1 || s.ContextMaxBytes > 128<<10 || s.ScanTimeoutMs < 1 || s.ScanTimeoutMs > 5000 {
		return c, fmt.Errorf("skills 限额越界")
	}
	if c.Web.TimeoutMs < 1 || c.Web.TimeoutMs > 60000 {
		return c, fmt.Errorf("web timeoutMs 越界")
	}
	// dailySearch、dailyFetch、monthlyMicroCredits 可以不写，不写表示本地不限（2026-11-10 改为可选，用户定）；写了照旧生效，0 表示禁止。
	for _, p := range []*int64{c.Web.DailySearch, c.Web.DailyFetch, c.Web.MonthlyMicroCredits} {
		if p != nil && *p < 0 {
			return c, fmt.Errorf("web 额度不能为负")
		}
	}
	for _, o := range []struct {
		key   string
		order []string
	}{{"web.searchOrder", c.Web.SearchOrder}, {"web.fetchOrder", c.Web.FetchOrder}} {
		if err := ValidWebOrder(o.key, o.order); err != nil {
			return c, err
		}
	}
	for _, h := range c.Web.Wiki.MoegirlHosts {
		if h == "" || strings.ContainsAny(h, "/:?#@ ") {
			return c, fmt.Errorf("web.wiki.moegirlHosts 只写主机名，不带协议和路径")
		}
	}
	if c.Subagents.SoftRequestBudget < 0 {
		return c, fmt.Errorf("subagents.softRequestBudget 不能是负数（0 = 不设）")
	}
	if c.Subagents.Enabled && c.Subagents.Model == "" {
		return c, fmt.Errorf("subagents 缺 model")
	}
	return c, nil
}
