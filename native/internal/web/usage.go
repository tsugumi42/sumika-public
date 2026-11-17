package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"
	_ "time/tzdata"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

type Access struct {
	grant ecosystem.Access
	inv   kernel.Invocation
}

func NewAccess(grant ecosystem.Access, inv kernel.Invocation) (Access, error) {
	if inv.Scope != grant.Scope() || inv.RootID == "" || inv.RunID == "" {
		return Access{}, ecosystem.ErrDenied
	}
	return Access{grant, inv}, nil
}

type Journal interface {
	WebConsume(context.Context, []store.WebQuota) error
	WebDispatch(context.Context, string, string, string, int64, []store.WebQuota) error
	WebSettle(context.Context, string, *int64) error
}
type dispatchKey struct{}
type dispatchState struct {
	s    *Service
	a    Access
	kind string
	ids  []string
	n    int // 这一轮一共出网几次（含已结算的），给查百科这类多次出网的调用报用量
	// provider 由包装多家的提供方（BalancedSearch）在挑定后写入，计费判断以它为准。
	provider string
}

// SetDispatchProvider 告诉派发域本次实际用哪一家（只有搜索会换家）。
func SetDispatchProvider(ctx context.Context, id string) {
	if d, ok := ctx.Value(dispatchKey{}).(*dispatchState); ok {
		d.provider = id
	}
}

// SettleAttempt 结算到目前为止已派发的出网记录。FetchChain 在一家失败、换下一家之前调用，
// 失败的那家按它自己的用量结算，不跟最后成功的那家混在一起。
func SettleAttempt(ctx context.Context, usage Usage) error {
	if d, ok := ctx.Value(dispatchKey{}).(*dispatchState); ok {
		return d.settle(ctx, usage)
	}
	return nil
}

// ResponseLimit 与已校验派发域共用配置，提供方不再另藏一份字节上限。
func ResponseLimit(ctx context.Context) int64 {
	if d, ok := ctx.Value(dispatchKey{}).(*dispatchState); ok {
		return int64(d.s.opts.ResponseMaxBytes)
	}
	return 5 << 20
}

// DoHTTP 是提供方的唯一出网口；先记 dispatched 再交给 HTTP transport。
func DoHTTP(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	d, ok := ctx.Value(dispatchKey{}).(*dispatchState)
	if !ok {
		return nil, fmt.Errorf("web 缺持久化派发域")
	}
	if err := d.a.grant.Check(ctx, "web:"+d.kind); err != nil {
		return nil, err
	}
	now := time.Now().In(d.s.zone)
	day := now.Format("2006-01-02")
	month := now.Format("2006-01")
	owner := d.a.grant.Scope()
	credits := int64(0)
	providerID := d.provider // 包装多家的提供方（搜索的 BalancedSearch、抓网页的 FetchChain）挑定后写入
	if providerID == "" {
		providerID = d.s.search.ID()
		if d.kind == "fetch" {
			providerID = d.s.fetch.ID()
		}
	}
	if Paid(providerID) {
		credits = 1000000
	}
	id := ecosystem.ID()
	quotas := []store.WebQuota{
		{Dimension: "dispatch:" + d.a.inv.RootID, Window: "root", Limit: int64(d.s.opts.MaxDispatchesPerTask), Amount: 1},
		{Dimension: "daily:" + d.kind + ":" + d.a.grant.Key(), Window: day, Limit: d.s.daily(d.kind), Amount: 1},
		{Dimension: "global:" + d.kind, Window: day, Limit: d.s.daily(d.kind), Amount: 1},
		{Dimension: "actor:" + d.kind + ":" + owner.ActorID, Window: day, Limit: d.s.daily(d.kind), Amount: 1},
		{Dimension: "platform:" + d.kind + ":" + owner.Platform, Window: day, Limit: d.s.daily(d.kind), Amount: 1},
		{Dimension: "credits:global", Window: month, Limit: limitOrUnbounded(d.s.opts.MonthlyMicroCredits), Amount: credits},
		{Dimension: "credits:provider", Window: month, Limit: limitOrUnbounded(d.s.opts.MonthlyMicroCredits), Amount: credits},
	}
	if err := d.s.opts.Journal.WebDispatch(ctx, id, d.a.grant.Key(), d.a.inv.RootID, credits, quotas); err != nil {
		return nil, err
	}
	d.ids = append(d.ids, id)
	d.n++
	return client.Do(request.WithContext(ctx))
}
func (s *Service) daily(kind string) int64 {
	if kind == "search" {
		return limitOrUnbounded(s.opts.DailySearch)
	}
	return limitOrUnbounded(s.opts.DailyFetch)
}

// limitOrUnbounded 把“没配置”译成事实上不限；配了的（含 0）照原值。
func limitOrUnbounded(p *int64) int64 {
	if p == nil {
		return math.MaxInt64
	}
	return *p
}
func (d *dispatchState) settle(ctx context.Context, usage Usage) error {
	persist, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	for _, id := range d.ids {
		if err := d.s.opts.Journal.WebSettle(persist, id, usage.MicroCredits); err != nil {
			return err
		}
	}
	d.ids = nil
	return nil
}
