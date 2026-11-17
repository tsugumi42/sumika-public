// Package wiki 是查百科（wiki_lookup）的提供方：萌娘百科和维基百科，都走 MediaWiki 的 api.php。
//
// 萌娘百科主站（zh.moegirl.org.cn）对接口有访问限制（回 action-notallowed）、网页还有 Cloudflare 校验，
// 这里不绕过：来源写成配置 web.wiki.moegirlHosts，依次试，主站拒绝或回校验页就换下一个（镜像）。
package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/web"
)

// userAgent 按维基媒体的要求带项目名和联系方式（项目主页，不写邮箱）。
const userAgent = "Sumika/0.1 (https://github.com/tsugumi42/sumika; wiki_lookup tool)"

const (
	siteMoegirl   = "萌娘百科"
	siteWikipedia = "维基百科"
	candidateMax  = 5
)

type Provider struct {
	hosts  []string
	client *http.Client
	do     func(context.Context, *http.Client, *http.Request) (*http.Response, error)
	// apiURL、wikiAPI 给测试换成假站点；默认是真实地址。
	apiURL  func(host string) string
	wikiAPI func(lang string) string
	// pageURL 拼词条的网址。
	pageURL func(host, title string) string
}

func New(moegirlHosts []string, client *http.Client) *Provider {
	return &Provider{
		hosts:  moegirlHosts,
		client: web.NoRedirectClient(client),
		do:     web.DoHTTP,
		apiURL: func(host string) string { return "https://" + host + "/api.php" },
		wikiAPI: func(lang string) string {
			return "https://" + lang + ".wikipedia.org/w/api.php"
		},
		pageURL: func(host, title string) string { return "https://" + host + "/" + escapeTitle(title) },
	}
}

func escapeTitle(title string) string {
	return url.PathEscape(strings.ReplaceAll(title, " ", "_"))
}

// errBlocked 表示这个来源拒绝了接口或给了校验页：换下一个来源，不重试、不绕过。
type errBlocked struct{ why string }

func (e *errBlocked) Error() string { return e.why }

// mwSite 是一个 MediaWiki 站的一组接口地址。
type mwSite struct {
	site, host, api, variant string
	pageURL                  func(string) string
}

func (p *Provider) Lookup(ctx context.Context, q web.WikiRequest) (web.ProviderWikiResult, error) {
	var tried []string
	var candidates []web.WikiCandidate
	try := func(res web.WikiResult, found bool, note string) (web.ProviderWikiResult, bool) {
		if found {
			res.Tried = nil
			return web.ProviderWikiResult{Result: res, Cacheable: true}, true
		}
		if note != "" {
			tried = append(tried, note)
		}
		candidates = append(candidates, res.Candidates...)
		return web.ProviderWikiResult{}, false
	}
	if q.Site == "" || q.Site == "moegirl" {
		res, found, note, err := p.moegirl(ctx, q)
		if err != nil {
			return web.ProviderWikiResult{}, err
		}
		if out, ok := try(res, found, note); ok {
			return out, nil
		}
	}
	if q.Site == "" || q.Site == "wikipedia" {
		site := mwSite{site: siteWikipedia, host: q.Lang + ".wikipedia.org", api: p.wikiAPI(q.Lang), pageURL: func(t string) string {
			return "https://" + q.Lang + ".wikipedia.org/wiki/" + escapeTitle(t)
		}}
		if q.Lang == "zh" {
			site.variant = "zh-cn"
		}
		res, found, err := p.lookupSite(ctx, site, q)
		note := ""
		if err != nil {
			if ctx.Err() != nil {
				return web.ProviderWikiResult{}, ctx.Err()
			}
			note = siteWikipedia + "：" + short(err)
		} else if !found && len(res.Candidates) == 0 {
			note = siteWikipedia + "：没有"
		}
		if out, ok := try(res, found, note); ok {
			return out, nil
		}
	}
	if len(candidates) == 0 && len(tried) > 0 && allFailed(tried) {
		return web.ProviderWikiResult{}, fmt.Errorf("查百科失败：%s", strings.Join(tried, "；"))
	}
	if len(candidates) > candidateMax {
		candidates = candidates[:candidateMax]
	}
	return web.ProviderWikiResult{Result: web.WikiResult{Candidates: candidates, Tried: tried}, Cacheable: true}, nil
}

// allFailed 说明每个来源都是出错而不是“没有这个词条”。
func allFailed(tried []string) bool {
	for _, t := range tried {
		if strings.HasSuffix(t, "：没有") {
			return false
		}
	}
	return true
}

func short(err error) string {
	s := err.Error()
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:80]) + "…"
	}
	return s
}

// moegirl 依次试各个来源。返回 found 表示取到了词条；note 是没取到时的说明。
func (p *Provider) moegirl(ctx context.Context, q web.WikiRequest) (res web.WikiResult, found bool, note string, err error) {
	var fails []string
	for _, host := range p.hosts {
		site := mwSite{site: siteMoegirl, host: host, api: p.apiURL(host), variant: "zh-cn", pageURL: func(t string) string { return p.pageURL(host, t) }}
		r, ok, e := p.lookupSite(ctx, site, q)
		if e == nil {
			if !ok && len(r.Candidates) == 0 {
				return r, false, siteMoegirl + "：没有", nil
			}
			return r, ok, "", nil
		}
		if ctx.Err() != nil {
			return web.WikiResult{}, false, "", ctx.Err()
		}
		if web.IsExhausted(e) {
			return web.WikiResult{}, false, "", e
		}
		fails = append(fails, host+" "+short(e))
	}
	if len(p.hosts) == 0 {
		return web.WikiResult{}, false, siteMoegirl + "：没有配置来源", nil
	}
	return web.WikiResult{}, false, siteMoegirl + "取不到（" + strings.Join(fails, "；") + "）", nil
}

// lookupSite 在一个站里查：先按标题前缀搜，没有再全文搜；找准了就取词条，否则给候选。
func (p *Provider) lookupSite(ctx context.Context, s mwSite, q web.WikiRequest) (web.WikiResult, bool, error) {
	titles, err := p.prefixSearch(ctx, s, q.Query)
	if err != nil {
		return web.WikiResult{}, false, err
	}
	var cands []web.WikiCandidate
	switch {
	case len(titles) > 0:
		if t, ok := pickTitle(titles, q.Query); ok {
			return p.article(ctx, s, t, q)
		}
		for _, t := range titles {
			cands = append(cands, web.WikiCandidate{Title: t, Site: s.site})
		}
	default:
		hits, err := p.fullSearch(ctx, s, q.Query)
		if err != nil {
			return web.WikiResult{}, false, err
		}
		if len(hits) == 1 {
			return p.article(ctx, s, hits[0].Title, q)
		}
		for _, h := range hits {
			h.Site = s.site
			cands = append(cands, h)
		}
	}
	return web.WikiResult{Candidates: cands}, false, nil
}

// pickTitle：标题和查询完全一致（忽略大小写和空白）就用它；前缀搜索只剩一个也用它。
func pickTitle(titles []string, query string) (string, bool) {
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "_", " ")), " "))
	}
	for _, t := range titles {
		if norm(t) == norm(query) {
			return t, true
		}
	}
	if len(titles) == 1 {
		return titles[0], true
	}
	return "", false
}

type mwError struct {
	Code string `json:"code"`
	Info string `json:"info"`
}

// get 发一次 GET，返回 JSON 正文。接口拒绝、校验页（不是 JSON）、403/429/5xx 一律算 errBlocked 之外的普通错误由调用方决定。
func (p *Provider) get(ctx context.Context, s mwSite, params url.Values, out any) error {
	params.Set("format", "json")
	params.Set("formatversion", "2")
	if s.variant != "" {
		params.Set("variant", s.variant)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := p.do(ctx, p.client, req)
	if err != nil {
		return err
	}
	data, _, err := web.ReadBody(ctx, res, s.site)
	if err != nil {
		if web.IsExhausted(err) {
			return &errBlocked{why: s.host + " 拒绝了请求"}
		}
		return &errBlocked{why: err.Error()}
	}
	var probe struct {
		Error *mwError `json:"error"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return &errBlocked{why: s.host + " 回的不是接口数据（多半是人机校验页）"}
	}
	if probe.Error != nil {
		switch probe.Error.Code {
		case "missingtitle", "invalidtitle", "nosuchpageid":
			return errMissing
		}
		return &errBlocked{why: s.host + " 接口回错误 " + probe.Error.Code}
	}
	return json.Unmarshal(data, out)
}

var errMissing = errors.New("没有这个词条")

func (p *Provider) prefixSearch(ctx context.Context, s mwSite, query string) ([]string, error) {
	var r struct {
		Query struct {
			Prefixsearch []struct {
				Title string `json:"title"`
			} `json:"prefixsearch"`
		} `json:"query"`
	}
	err := p.get(ctx, s, url.Values{"action": {"query"}, "list": {"prefixsearch"}, "pssearch": {query}, "pslimit": {"5"}}, &r)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, x := range r.Query.Prefixsearch {
		out = append(out, x.Title)
	}
	return out, nil
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func (p *Provider) fullSearch(ctx context.Context, s mwSite, query string) ([]web.WikiCandidate, error) {
	var r struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	err := p.get(ctx, s, url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {query}, "srlimit": {"5"}, "srprop": {"snippet"}}, &r)
	if err != nil {
		return nil, err
	}
	var out []web.WikiCandidate
	for _, x := range r.Query.Search {
		snippet := strings.TrimSpace(htmlUnescape(tagRE.ReplaceAllString(x.Snippet, "")))
		if rs := []rune(snippet); len(rs) > 80 {
			snippet = string(rs[:80]) + "…"
		}
		out = append(out, web.WikiCandidate{Title: x.Title, Snippet: snippet})
	}
	return out, nil
}

func (p *Provider) article(ctx context.Context, s mwSite, title string, q web.WikiRequest) (web.WikiResult, bool, error) {
	var r struct {
		Parse struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"parse"`
	}
	err := p.get(ctx, s, url.Values{"action": {"parse"}, "page": {title}, "prop": {"text"}, "redirects": {"1"}, "disableeditsection": {"1"}, "disablelimitreport": {"1"}}, &r)
	if errors.Is(err, errMissing) {
		return web.WikiResult{}, false, nil
	}
	if err != nil {
		return web.WikiResult{}, false, err
	}
	md, err := ToMarkdown(r.Parse.Text, "https://"+s.host)
	if err != nil {
		return web.WikiResult{}, false, err
	}
	if strings.TrimSpace(md) == "" {
		return web.WikiResult{}, false, nil
	}
	got := r.Parse.Title
	if got == "" {
		got = title
	}
	content, remaining := Truncate(md, q.MaxChars)
	return web.WikiResult{Site: s.site, Host: s.host, Title: got, URL: s.pageURL(got), Content: content, Remaining: remaining}, true, nil
}
