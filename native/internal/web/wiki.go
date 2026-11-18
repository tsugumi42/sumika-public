package web

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"sumika-native/internal/ecosystem"
)

// 查百科（wiki_lookup）：萌娘百科、维基百科。和 web_search 一样走服务层：权限、额度、单飞、缓存、记账都共用。

const (
	// WikiDefaultMaxChars 是正文默认最多返回的字数，超出截断并注明后面还有多少字。
	WikiDefaultMaxChars = 6000
	wikiMaxQueryRunes   = 100
)

type WikiRequest struct {
	Query string
	Site  string // moegirl、wikipedia；空 = 先萌百后维基
	Lang  string // 维基用：zh、en、ja；空 = zh
	// MaxChars 是正文最多返回多少字；0 = WikiDefaultMaxChars。
	MaxChars int
}

// WikiCandidate 是没有同名词条时给模型挑的候选。
type WikiCandidate struct {
	Title, Snippet, Site string
}

type WikiResult struct {
	// Site 是实际取到词条的来源站名（萌娘百科、维基百科）；没取到词条时为空。
	Site string
	// Host 是实际取到词条的主机，萌百镜像换过站时能看出来。
	Host  string
	Title string
	URL   string
	// Content 是词条正文（Markdown），已按 MaxChars 截断。
	Content string
	// Remaining 是被截掉的字数。
	Remaining  int
	Candidates []WikiCandidate
	// Tried 记没取到时各来源的情况，给“没找到”的说明用。
	Tried     []string
	Cached    bool
	FetchedAt time.Time
	Usage     Usage
}

type ProviderWikiResult struct {
	Result    WikiResult
	Cacheable bool
}

type WikiProvider interface {
	Lookup(context.Context, WikiRequest) (ProviderWikiResult, error)
}

// SetWiki 装上查百科的提供方；没装时 wiki_lookup 报“未配置”。
func (s *Service) SetWiki(p WikiProvider) {
	s.mu.Lock()
	s.wiki = p
	s.mu.Unlock()
}

func (s *Service) wikiProvider() WikiProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wiki
}

func (s *Service) Wiki(ctx context.Context, a Access, q WikiRequest) (WikiResult, error) {
	q.Query = strings.TrimSpace(q.Query)
	if q.Query == "" || utf8.RuneCountInString(q.Query) > wikiMaxQueryRunes {
		return WikiResult{}, fmt.Errorf("query 为空或过长")
	}
	switch q.Site {
	case "", "moegirl", "wikipedia":
	default:
		return WikiResult{}, fmt.Errorf("site 非法")
	}
	switch q.Lang {
	case "":
		q.Lang = "zh"
	case "zh", "en", "ja":
	default:
		return WikiResult{}, fmt.Errorf("lang 非法")
	}
	if q.MaxChars == 0 {
		q.MaxChars = WikiDefaultMaxChars
	}
	if q.MaxChars < 200 || q.MaxChars > 20000 {
		return WikiResult{}, fmt.Errorf("max_chars 越界")
	}
	provider := s.wikiProvider()
	if provider == nil {
		return WikiResult{}, fmt.Errorf("查百科未配置")
	}
	// 一次查百科算一次搜索：占一个逻辑查询名额，走搜索的权限和每日额度。
	ctx, done, err := s.begin(ctx, a, "search", 1)
	if err != nil {
		return WikiResult{}, err
	}
	defer done()
	key := ecosystem.Digest([]any{a.grant.Key(), "wiki", q})
	if v, ok := s.get(key); ok {
		out := v.(WikiResult)
		out.Cached = true
		out.Usage = Usage{}
		return out, nil
	}
	value, err := s.shared(ctx, a, key, func(sharedCtx context.Context) (any, error) { return s.wikiOnce(sharedCtx, a, provider, q, key) })
	if err != nil {
		return WikiResult{}, err
	}
	return value.(WikiResult), nil
}

func (s *Service) wikiOnce(ctx context.Context, a Access, p WikiProvider, q WikiRequest, key string) (WikiResult, error) {
	d := &dispatchState{s: s, a: a, kind: "search", provider: "wiki"} // 百科免费：不按搜索服务商计费
	ctx = context.WithValue(ctx, dispatchKey{}, d)
	res, err := p.Lookup(ctx, q)
	if settleErr := d.settle(ctx, FreeUsage(0)); settleErr != nil {
		return res.Result, settleErr
	}
	if err != nil {
		return res.Result, err
	}
	if err := a.grant.Check(ctx, "web:search"); err != nil {
		return WikiResult{}, err
	}
	res.Result.FetchedAt = time.Now()
	res.Result.Usage = FreeUsage(0)
	res.Result.Usage.UpstreamRequests = d.n
	if res.Cacheable {
		s.put(key, res.Result, time.Duration(s.opts.SearchCacheSeconds)*time.Second)
	}
	return res.Result, nil
}

// RenderWiki 把查百科的结果排成 Markdown：有词条给“标题、来源、正文”，没有就给候选或没找到的说明。
func RenderWiki(r WikiResult) string {
	var b strings.Builder
	b.WriteString(untrustedHeader)
	switch {
	case r.Content != "":
		fmt.Fprintf(&b, "# %s\n", oneLine(r.Title))
		src := r.Site
		if r.Host != "" {
			src += "（" + r.Host + "）"
		}
		fmt.Fprintf(&b, "来源：%s · %s\n\n", src, r.URL)
		b.WriteString(strings.TrimSpace(r.Content))
		if r.Remaining > 0 {
			fmt.Fprintf(&b, "\n\n（正文在此截断，后面还有 %d 字；要看别的部分请用 web_fetch 读上面的词条链接。）", r.Remaining)
		}
	case len(r.Candidates) > 0:
		b.WriteString("没有同名词条，可能是下面这些。请用其中一个完整标题再查一次（query 填标题；站点用括号里的）：\n")
		for i, c := range r.Candidates {
			fmt.Fprintf(&b, "%d. %s（%s）", i+1, oneLine(c.Title), c.Site)
			if sn := oneLine(c.Snippet); sn != "" {
				fmt.Fprintf(&b, " — %s", sn)
			}
			b.WriteString("\n")
		}
	default:
		b.WriteString("没有找到相关词条。")
		if len(r.Tried) > 0 {
			b.WriteString("查过：" + strings.Join(r.Tried, "；") + "。")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
