package freeapi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

//go:embed catalog.json
var embeddedCatalog []byte

// 区域
const (
	RegionCN     = "cn"
	RegionGlobal = "global"
)

// 路线：direct 不走代理（国内站）；proxy 有代理就走，没有就直连（海外站，和以前一致）
const (
	NetDirect = "direct"
	NetProxy  = "proxy"
)

// 没有公布值时我们自己的起步上限
const (
	DefaultDailyCap  = 100
	DefaultPerMinute = 10
)

// Category 是一类问题
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Published 是提供方公布的限额，0 表示没公布
type Published struct {
	PerMinute int    `json:"perMinute"`
	PerDay    int    `json:"perDay"`
	Source    string `json:"source"`
	Checked   string `json:"checked"`
}

// Provider 是注册表里的一条
type Provider struct {
	ID        string            `json:"id"`
	Category  string            `json:"category"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Regions   []string          `json:"regions"` // 空 = 不限；写了只服务这些区域
	Tier      map[string]int    `json:"tier"`    // 区域 → 静态档位，1 最优先；"*" 是默认
	Network   string            `json:"network"`
	Auth      string            `json:"auth"`
	Published Published         `json:"published"`
	DailyCap  int               `json:"dailyCap"`     // 0 = 取公布每天上限的 50%，没公布取 DefaultDailyCap
	PerMinute int               `json:"perMinuteCap"` // 0 = 取公布每分钟上限的 50%，没公布取 DefaultPerMinute
	Grey      bool              `json:"grey"`         // 非公开接口
	Probe     map[string]string `json:"probe"`        // 探测用的固定参数
	Notes     string            `json:"notes"`
}

// Catalog 是整张注册表
type Catalog struct {
	Categories []Category `json:"categories"`
	Providers  []Provider `json:"providers"`
}

// Serves 这个提供方服务不服务这个区域
func (p Provider) Serves(region string) bool {
	return len(p.Regions) == 0 || slices.Contains(p.Regions, region)
}

// TierFor 是这个区域下的静态档位（越小越优先）；没写的按 5
func (p Provider) TierFor(region string) int {
	if t, ok := p.Tier[region]; ok {
		return t
	}
	if t, ok := p.Tier["*"]; ok {
		return t
	}
	return 5
}

// RegionMatch：regions 里明写了这个区域得 1，不限得 0.6
func (p Provider) RegionMatch(region string) float64 {
	if slices.Contains(p.Regions, region) {
		return 1
	}
	return 0.6
}

// DefaultCaps 是不看配置覆盖时的每天、每分钟上限
func (p Provider) DefaultCaps() (day, minute int) {
	day, minute = p.DailyCap, p.PerMinute
	if day <= 0 {
		if day = p.Published.PerDay / 2; day <= 0 {
			day = DefaultDailyCap
		}
	}
	if minute <= 0 {
		if minute = p.Published.PerMinute / 2; minute <= 0 {
			minute = DefaultPerMinute
		}
	}
	return day, minute
}

// ParseCatalog 解析并校验。未知字段报错
func ParseCatalog(b []byte) (Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("注册表解析失败：%w", err)
	}
	return c, c.validate()
}

// DefaultCatalog 是内嵌的注册表
func DefaultCatalog() Catalog {
	c, err := ParseCatalog(embeddedCatalog)
	if err != nil {
		panic(err) // 内嵌数据坏了是开发期的错，测试会先红
	}
	return c
}

func (c Catalog) validate() error {
	cats := map[string]bool{}
	for _, k := range c.Categories {
		if k.ID == "" || cats[k.ID] {
			return fmt.Errorf("注册表：类别 id 为空或重复：%q", k.ID)
		}
		cats[k.ID] = true
	}
	ids := map[string]bool{}
	for _, p := range c.Providers {
		switch {
		case p.ID == "" || ids[p.ID]:
			return fmt.Errorf("注册表：提供方 id 为空或重复：%q", p.ID)
		case !cats[p.Category]:
			return fmt.Errorf("注册表：%s 的类别 %q 不存在", p.ID, p.Category)
		case p.Auth != "none":
			return fmt.Errorf("注册表：%s 的 auth 只能是 none（要密钥的接口默认不进注册表）", p.ID)
		case p.Network != NetDirect && p.Network != NetProxy:
			return fmt.Errorf("注册表：%s 的 network 要写 direct 或 proxy", p.ID)
		case p.Name == "":
			return fmt.Errorf("注册表：%s 缺少 name", p.ID)
		case p.DailyCap < 0 || p.PerMinute < 0 || p.Published.PerDay < 0 || p.Published.PerMinute < 0:
			return fmt.Errorf("注册表：%s 的上限不能为负", p.ID)
		case len(p.Tier) == 0:
			return fmt.Errorf("注册表：%s 缺少 tier", p.ID)
		}
		if u, err := url.Parse(strings.NewReplacer("{year}", "2000", "{base}", "usd").Replace(p.URL)); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("注册表：%s 的 url 要写 https 地址", p.ID)
		}
		for r, t := range p.Tier {
			if (r != "*" && r != RegionCN && r != RegionGlobal) || t < 1 {
				return fmt.Errorf("注册表：%s 的 tier 写法不对", p.ID)
			}
		}
		for _, r := range p.Regions {
			if r != RegionCN && r != RegionGlobal {
				return fmt.Errorf("注册表：%s 的区域 %q 不认识", p.ID, r)
			}
		}
		ids[p.ID] = true
	}
	return nil
}

// Provider 按 id 取
func (c Catalog) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}
