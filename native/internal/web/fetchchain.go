package web

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FetchChain 是 fetch_provider: auto：按 order 依次试各家，前一家失败（报错、内容为空、人机校验页）就换下一家。
// 本机 http 在默认顺序里排第一（免费）。没填 key 的家、已知用完的家直接跳过。
// 全部失败时返回最后一个错误，让模型知道为什么读不到。
type FetchChain struct {
	all      map[string]FetchProvider
	order    *Order
	balances *Balances
	// QueryTimeout 是一次余额查询的时限。
	QueryTimeout time.Duration
}

func NewFetchChain(all map[string]FetchProvider, order *Order, balances *Balances) *FetchChain {
	return &FetchChain{all: all, order: order, balances: balances, QueryTimeout: 5 * time.Second}
}

func (c *FetchChain) usable() []FetchProvider {
	var out []FetchProvider
	for _, id := range c.order.Fetch() {
		if p := c.all[id]; p != nil && p.Available() == nil {
			out = append(out, p)
		}
	}
	return out
}

func (c *FetchChain) Available() error {
	if len(c.usable()) > 0 {
		return nil
	}
	return fmt.Errorf("没有可用的 web 读取提供方")
}

// ID 不联网：给缓存键和日志用，取此刻排第一的可用家。
func (c *FetchChain) ID() string {
	for _, p := range c.usable() {
		_, refreshable := p.(BalanceSource)
		if !c.balances.Exhausted(p.ID(), refreshable) {
			return p.ID()
		}
	}
	return ""
}

func (c *FetchChain) Fetch(ctx context.Context, q FetchRequest) (ProviderFetchResult, error) {
	var lastErr error
	var last ProviderFetchResult
	upstream := 0
	for _, p := range c.usable() {
		_, refreshable := p.(BalanceSource)
		refreshBalance(ctx, c.balances, p.ID(), p, c.QueryTimeout)
		if c.balances.Exhausted(p.ID(), refreshable) {
			lastErr = &ExhaustedError{Provider: displayName(p.ID())}
			continue
		}
		SetDispatchProvider(ctx, p.ID())
		res, err := p.Fetch(ctx, q)
		upstream += res.Result.Usage.UpstreamRequests
		if err == nil {
			switch {
			case strings.TrimSpace(res.Result.Content) == "":
				err = fmt.Errorf("%s 没读到内容", displayName(p.ID()))
			case LooksBlocked(res.Result.Content):
				err = fmt.Errorf("%s 读到的是人机校验页", displayName(p.ID()))
			}
		}
		if err == nil {
			if _, ok := p.(BalanceSource); ok {
				c.balances.Spend(p.ID(), 1)
			}
			res.Result.Usage.UpstreamRequests = max(upstream, res.Result.Usage.UpstreamRequests)
			return res, nil
		}
		last, lastErr = res, err
		if ctx.Err() != nil {
			return res, err
		}
		// 失败的这一家按它自己的用量结算（没给用量就是失败不计），再试下一家。
		if settleErr := SettleAttempt(ctx, res.Result.Usage); settleErr != nil {
			return res, settleErr
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的 web 读取提供方")
	}
	return last, lastErr
}
