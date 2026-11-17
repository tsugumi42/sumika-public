package lifetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/freeapi"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// exchange_rate：查两种货币之间的汇率（第二批免费接口，用来验证注册表通用）。
// 国内线路先 fawazahmed0（jsDelivr，备用它的 pages.dev），国外先 Frankfurter（备用 ExchangeRate-API）；
// 都是日更，缓存到下次更新前（最多 6 小时）。币种代码只认 3 到 5 个英文字母，金额可选。

// ToolExchangeRate 是工具名
const ToolExchangeRate = "exchange_rate"

// ExchangeRateDecl 是汇率工具的声明
var ExchangeRateDecl = tools.Declaration{
	Name:        ToolExchangeRate,
	Description: "查两种货币之间的汇率，比如 from 写 USD、to 写 CNY；amount 可选，写了就顺便算出换算后的金额。汇率每天更新一次，是参考价，不是银行的成交价。币种用英文代码（USD、CNY、JPY、EUR 等）。",
	Parameters:  `{"type":"object","additionalProperties":false,"required":["from","to"],"properties":{"from":{"type":"string","description":"原币种代码，比如 USD"},"to":{"type":"string","description":"目标币种代码，比如 CNY"},"amount":{"type":"number","description":"要换算的金额；不写就只给 1 单位的汇率"}}}`,
}

// 汇率工具回给模型的几种"没结果"，文字固定
const (
	FxQueryFailed = "这次取不到汇率（网络不通或对方没回），过一会儿再查。"
	FxNoProvider  = "汇率接口现在都不可用，这次没有查，过一会儿再试。"
	fxBodyMax     = 512 << 10
)

// fxQuote 是一次查询的结果
type fxQuote struct {
	Rate   float64
	Date   string
	Source string
	Expire time.Time
}

func (q fxQuote) ExpiresAt() time.Time { return q.Expire }

// fxFetcher 按提供方 id 造取汇率的做法。币种在这里转成各家要的大小写
func fxFetcher(id, base, quote string) freeapi.Fetcher {
	return func(ctx context.Context, env freeapi.Env) (any, error) {
		switch id {
		case "fx-fawaz-jsdelivr", "fx-fawaz-pages":
			b, err := freeapi.Get(ctx, env.Client, strings.ReplaceAll(env.URL, "{base}", strings.ToLower(base)), nil, fxBodyMax)
			if err != nil {
				return nil, notFoundOn404(err)
			}
			return parseFawaz(b, base, quote, env.Provider.Name)
		case "fx-frankfurter":
			q := url.Values{"base": {base}, "symbols": {quote}}
			b, err := freeapi.Get(ctx, env.Client, env.URL+"?"+q.Encode(), nil, fxBodyMax)
			if err != nil {
				return nil, notFoundOn404(err)
			}
			return parseFrankfurter(b, quote, env.Provider.Name)
		case "fx-exchangerate-api":
			b, err := freeapi.Get(ctx, env.Client, strings.ReplaceAll(env.URL, "{base}", base), nil, fxBodyMax)
			if err != nil {
				return nil, notFoundOn404(err)
			}
			return parseERAPI(b, quote, env.Provider.Name)
		}
		return nil, freeapi.Schema("没有这个汇率接口")
	}
}

// notFoundOn404：这几家对不认识的币种回 404，那是“没有这个币种”，不是接口坏了
func notFoundOn404(err error) error {
	var se *freeapi.StatusError
	if errors.As(err, &se) && se.Code == 404 {
		return freeapi.ErrNotFound
	}
	return err
}

func sane(rate float64) bool {
	return rate > 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate < 1e12
}

func parseFawaz(b []byte, base, quote, src string) (fxQuote, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fxQuote{}, freeapi.Schema("汇率接口回的不是合法数据")
	}
	var date string
	json.Unmarshal(raw["date"], &date)
	var rates map[string]float64
	if err := json.Unmarshal(raw[strings.ToLower(base)], &rates); err != nil || date == "" {
		return fxQuote{}, freeapi.Schema("汇率接口缺字段")
	}
	r, ok := rates[strings.ToLower(quote)]
	if !ok {
		return fxQuote{}, freeapi.ErrNotFound // 这个目标币种它没有
	}
	if !sane(r) {
		return fxQuote{}, freeapi.Schema("汇率不合理")
	}
	return fxQuote{Rate: r, Date: cleanName(date, 10), Source: src}, nil
}

func parseFrankfurter(b []byte, quote, src string) (fxQuote, error) {
	var d struct {
		Date  string             `json:"date"`
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(b, &d); err != nil || d.Date == "" || d.Rates == nil {
		return fxQuote{}, freeapi.Schema("汇率接口回的不是合法数据")
	}
	r, ok := d.Rates[strings.ToUpper(quote)]
	if !ok {
		return fxQuote{}, freeapi.ErrNotFound
	}
	if !sane(r) {
		return fxQuote{}, freeapi.Schema("汇率不合理")
	}
	return fxQuote{Rate: r, Date: cleanName(d.Date, 10), Source: src}, nil
}

func parseERAPI(b []byte, quote, src string) (fxQuote, error) {
	var d struct {
		Result   string             `json:"result"`
		ErrType  string             `json:"error-type"`
		Last     string             `json:"time_last_update_utc"`
		NextUnix int64              `json:"time_next_update_unix"`
		LastUnix int64              `json:"time_last_update_unix"`
		Rates    map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return fxQuote{}, freeapi.Schema("汇率接口回的不是合法数据")
	}
	if d.Result == "error" {
		if d.ErrType == "unsupported-code" {
			return fxQuote{}, freeapi.ErrNotFound
		}
		return fxQuote{}, freeapi.Schema("汇率接口报错")
	}
	if d.Result != "success" || d.Rates == nil {
		return fxQuote{}, freeapi.Schema("汇率接口回的不是合法数据")
	}
	r, ok := d.Rates[strings.ToUpper(quote)]
	if !ok {
		return fxQuote{}, freeapi.ErrNotFound
	}
	if !sane(r) {
		return fxQuote{}, freeapi.Schema("汇率不合理")
	}
	q := fxQuote{Rate: r, Source: src}
	if d.LastUnix > 0 {
		q.Date = time.Unix(d.LastUnix, 0).UTC().Format("2006-01-02")
	}
	if d.NextUnix > 0 {
		q.Expire = time.Unix(d.NextUnix, 0)
	}
	return q, nil
}

// currencyArg 取并校验币种代码：3 到 5 个英文字母，转大写
func currencyArg(args jsjson.Value, key string) (code, errText string) {
	s, present, bad := stringArg(args, key)
	if bad {
		return "", key + " 要写成文字（币种英文代码，比如 USD）。"
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	if !present || s == "" {
		return "", key + " 不能空，写币种英文代码，比如 USD。"
	}
	if n := len(s); n < 3 || n > 5 {
		return "", key + " 要写 3 到 5 个英文字母的币种代码，比如 USD。"
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return "", key + " 要写 3 到 5 个英文字母的币种代码，比如 USD。"
		}
	}
	return s, ""
}

func (p *Plugin) exchangeRate(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	if c.Args.Kind != jsjson.Object {
		return fail("参数要写成对象，比如 {\"from\":\"USD\",\"to\":\"CNY\"}。")
	}
	from, bad := currencyArg(c.Args, "from")
	if bad != "" {
		return fail(bad)
	}
	to, bad := currencyArg(c.Args, "to")
	if bad != "" {
		return fail(bad)
	}
	amount, hasAmount := 0.0, false
	if v, found := c.Args.Obj["amount"]; found && v.Kind != jsjson.Null {
		if v.Kind != jsjson.Number {
			return fail("amount 要写成数字。")
		}
		amount, hasAmount = v.Num, true
		if !sane(amount) {
			return fail("amount 要写成大于 0 的合理数字。")
		}
	}
	if from == to {
		return ok("同一种货币，汇率就是 1。")
	}
	if text, over := p.quota(ctx, ToolExchangeRate, "汇率", c); over {
		return ok(text)
	}
	cctx, cancel := context.WithTimeout(ctx, p.o.WeatherTimeout)
	defer cancel()
	fetch := map[string]freeapi.Fetcher{}
	for _, id := range []string{"fx-fawaz-jsdelivr", "fx-fawaz-pages", "fx-frankfurter", "fx-exchangerate-api"} {
		fetch[id] = fxFetcher(id, from, to)
	}
	res, err := p.eng.Do(cctx, freeapi.Request{Category: "fx", Region: p.networkRegion(), Fetch: fetch,
		CacheKey: "fx:" + from + ":" + to, CacheTTL: 6 * time.Hour})
	if err != nil {
		switch mapCallErr(err).(type) {
		case notFoundError:
			return ok("查不到 " + from + " 到 " + to + " 的汇率：有一个币种代码可能写错了，或者这两种货币的汇率接口里都没有。")
		case noProviderError:
			return fail(FxNoProvider)
		}
		p.o.Logf("汇率：查询没成功：%s", errKind(err))
		return fail(FxQueryFailed)
	}
	q := res.Value.(fxQuote)
	var b strings.Builder
	fmt.Fprintf(&b, "1 %s = %s %s", from, fmtRate(q.Rate), to)
	if hasAmount {
		fmt.Fprintf(&b, "；%s %s ≈ %s %s", fmtRate(amount), from, fmtRate(amount*q.Rate), to)
	}
	if q.Date != "" {
		fmt.Fprintf(&b, "\n汇率日期 %s（每天更新一次的参考价）。", q.Date)
	}
	b.WriteString("\n数据来自 " + q.Source + "。")
	return ok(b.String())
}

// fmtRate：大数取两位小数，小数保留 4 位有效数字，不用科学计数法
func fmtRate(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1000:
		return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64)
	case a >= 1:
		return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0"), ".")
	}
	s := strconv.FormatFloat(v, 'g', 4, 64)
	if strings.ContainsAny(s, "eE") {
		s = strconv.FormatFloat(v, 'f', 8, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
