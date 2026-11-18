package lifetools

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"sumika-native/internal/freeapi"
	"sumika-native/internal/turn"
)

// 免费接口注册表的接线（规格-免费接口）：引擎的客户端、地址覆盖、探测做法，以及按场合的次数检查。

// newEngine 造引擎。测试（给了 Client、地址覆盖）里没覆盖到的提供方指向一个连不上的本机地址，
// 免得失败的测试悄悄打到真实接口
func (p *Plugin) newEngine() *freeapi.Engine {
	o := p.o
	urls := map[string]string{}
	for k, v := range o.Endpoints {
		urls[k] = v
	}
	if o.GeocodeURL != "" {
		urls["open-meteo-geo"] = o.GeocodeURL
	}
	if o.ForecastURL != "" {
		urls["open-meteo"] = o.ForecastURL
	}
	if o.Client != nil || len(urls) > 0 {
		for _, pr := range freeapi.DefaultCatalog().Providers {
			if _, ok := urls[pr.ID]; !ok {
				urls[pr.ID] = "http://127.0.0.1:1/" + pr.ID + "/{year}/{base}"
			}
		}
	}
	return freeapi.New(freeapi.Options{
		StateFile: o.FreeAPIStateFile,
		Settings: func() freeapi.Settings {
			s := o.Settings()
			fs := s.FreeAPI
			fs.Proxy = s.Proxy
			return fs
		},
		HTTP: func(direct bool) (*http.Client, error) {
			if direct {
				return p.cl.direct()
			}
			return p.cl.get(o.Settings().Proxy)
		},
		Now:   o.Now,
		Logf:  o.Logf,
		URLs:  urls,
		Trace: p.traceAttempt,
	})
}

// networkRegion 是节假日、汇率这类看网络线路的类别的区域
func (p *Plugin) networkRegion() string {
	if p.o.Settings().FreeAPI.NetworkRegion == freeapi.RegionGlobal {
		return freeapi.RegionGlobal
	}
	return freeapi.RegionCN
}

// registerProbes 给注册表里每个提供方登记探测做法：用注册表里的固定参数造和真实调用同一个的函数，要解析出关键字段才算通
func (p *Plugin) registerProbes() {
	for _, pr := range p.eng.Catalog().Providers {
		build := p.probeFetcher(pr)
		if build == nil {
			continue
		}
		p.eng.RegisterProbe(pr.ID, func(ctx context.Context, env freeapi.Env) error {
			_, err := build(ctx, env)
			return err
		})
	}
}

func (p *Plugin) probeFetcher(pr freeapi.Provider) freeapi.Fetcher {
	q := pr.Probe
	lat, lon := parseFloat(q["lat"]), parseFloat(q["lon"])
	switch pr.ID {
	case "open-meteo-geo":
		return geocodeFetcher(q["name"])
	case "open-meteo":
		return openMeteoFetcher(Place{Lat: lat, Lon: lon})
	case "met-norway":
		return metFetcher(Place{Lat: lat, Lon: lon})
	case "tencent-weather":
		return tencentFetcher(q["province"], q["city"], p.o.Now)
	case "holiday-jsdelivr", "holiday-raw", "holiday-timor":
		return func(ctx context.Context, env freeapi.Env) (any, error) {
			return holidayFetcher(pr.ID, p.probeYear(q["year"]))(ctx, env)
		}
	case "fx-fawaz-jsdelivr", "fx-fawaz-pages", "fx-frankfurter", "fx-exchangerate-api":
		return fxFetcher(pr.ID, strings.ToUpper(q["base"]), strings.ToUpper(q["quote"]))
	}
	return nil
}

func (p *Plugin) probeYear(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return p.o.Now().Year()
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// ---- 按场合的次数 ----

// who 把这一次工具调用的身份整理成引擎要的样子。主人由宿主核对（QuotaOwner）；身份和会话对不上的按“未知”算，不是主人
func (p *Plugin) who(ctx context.Context, c turn.ToolCall) freeapi.Who {
	sc := c.Identity.Scope
	w := freeapi.Who{Conversation: c.Conversation}
	consistent := c.CharacterID == sc.CharacterID && c.Conversation == sc.Conversation && c.Audience.Conversation == c.Conversation
	if consistent {
		w.ActorKind, w.ActorID = sc.ActorKind, sc.ActorID
		w.Owner = p.o.QuotaOwner != nil && p.o.QuotaOwner(ctx, c.Identity, c.Audience)
	}
	w.Group = isGroupScene(c)
	return w
}

// isGroupScene：任何一处说是群就按群算（宁可多限，不少限）
func isGroupScene(c turn.ToolCall) bool {
	return c.Identity.Scope.Scene == "group" || c.Audience.Scene == "group" || strings.Contains(c.Conversation, ":group:") || strings.Contains(c.Identity.Scope.Conversation, ":group:")
}

// quota 联网之前占一次“人”的次数；被拒返回给模型的话（固定一句，写明限额，不带用户号），并要求这一轮别重试
func (p *Plugin) quota(ctx context.Context, tool, label string, c turn.ToolCall) (string, bool) {
	d := p.eng.Allow(tool, p.who(ctx, c))
	if !d.Denied {
		return "", false
	}
	switch d.Reason {
	case "group":
		return fmt.Sprintf("这个群这个小时%s查得太多了（每群每小时 %d 次），过一会儿再查，这一轮别再重试，先照记忆里的大概情况说。", label, d.Limit), true
	}
	return fmt.Sprintf("这个人今天%s查得太多了（每人每天 %d 次），明天再来，这一轮别再重试，先照记忆里的大概情况说。", label, d.Limit), true
}

// Engine 给装配和设置页用
func (p *Plugin) Engine() *freeapi.Engine { return p.eng }
