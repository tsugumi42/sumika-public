package lifetools

// 生活工具自己的日志（规格-插件两层 第二点五节第 5 条，规格-Bun后台第5批 9.7“生活工具”）：
// 免费接口注册表里每家提供方的每次尝试一条，site 提供方 id、route 类别、url 真请求的地址、ms；失败的 kind 是注册表的分类，error 带状态码。
// “查无结果”是提供方的正常回答，记 ok 加 note notfound；调用方取消记 skip。用户自配假日地址取数时 site 是主机名、route 是 holiday。探测不记。

import (
	"net/url"
	"regexp"
	"time"

	"sumika-native/internal/freeapi"
	"sumika-native/internal/plugintrace"
)

func (p *Plugin) setTrace(f func(plugintrace.Event)) {
	if f == nil {
		p.tracer.Store(nil)
		return
	}
	p.tracer.Store(&f)
}

func (p *Plugin) trace(e plugintrace.Event) {
	if f := p.tracer.Load(); f != nil {
		(*f)(e)
	}
}

// urlInText 是错误文字里的网址（Go 的 http 错误会带上整个请求地址，查询串里可能有坐标）
var urlInText = regexp.MustCompile(`https?://[^\s"']+`)

// errText 是写进日志的错误：里面的网址只留协议、主机、端口和路径
func errText(err error) string {
	if err == nil {
		return ""
	}
	return urlInText.ReplaceAllStringFunc(err.Error(), plugintrace.CleanURL)
}

// attemptEvent 把一次尝试换成一行
func attemptEvent(site, route, u string, took time.Duration, k freeapi.Kind, err error) plugintrace.Event {
	e := plugintrace.Event{Site: site, Route: route, URL: u, Took: took, Timed: true}
	switch k {
	case freeapi.KOK:
		e.Result = plugintrace.OK
	case freeapi.KNotFound:
		e.Result, e.Note = plugintrace.OK, "notfound"
	case freeapi.KCanceled:
		e.Result, e.Kind = plugintrace.Skip, string(k)
	default:
		e.Result, e.Kind, e.Error = plugintrace.Fail, string(k), errText(err)
	}
	return e
}

// traceAttempt 是免费接口引擎每次尝试的回调
func (p *Plugin) traceAttempt(a freeapi.Attempt) {
	p.trace(attemptEvent(a.Provider, a.Category, a.URL, a.Took, a.Kind, a.Err))
}

// traceHolidayURL 记一次用户自配假日地址的取数
func (p *Plugin) traceHolidayURL(raw string, took time.Duration, err error) {
	host := "holiday-url"
	if u, e := url.Parse(raw); e == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	k, _ := freeapi.Classify(err)
	p.trace(attemptEvent(host, "holiday", raw, took, k, err))
}
