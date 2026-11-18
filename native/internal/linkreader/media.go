package linkreader

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
)

func (f *Fetcher) inspectMedia(ctx context.Context, r *Result) {
	hosts := map[string]error{}
	// 先收集要检查的主机，再并发解析（经代理时每次解析要几秒，逐个串行会拖过取数时限），最后按原样逐字段套用结果。
	collecting := true
	pending := map[string]bool{}
	resolve := func(host string) error {
		ips, err := f.lookup(ctx, host)
		if err == nil && len(ips) == 0 {
			err = failure("network", "media_dns")
		}
		if err != nil {
			return err
		}
		// 和拨号同一个裁决：fake-ip 下名单内主机要有代理才算能下载，否则这条候选现在就标 failed（候选都是 https 443）
		if _, fe := f.policy.judge(host, "443", ips, "media_dns"); fe != nil {
			return fe
		}
		return nil
	}
	visited := map[*Result]bool{}
	var visit func(*Result)
	check := func(field *Field[string]) {
		if field.State != "available" {
			return
		}
		u, err := url.Parse(field.Value)
		safe := err == nil && u.User == nil && u.Opaque == "" && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
		if safe {
			port := u.Port()
			safe = port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")
		}
		host := ""
		if safe {
			host = strings.ToLower(u.Hostname())
			safe = host == "pbs.twimg.com" || host == "video.twimg.com" || host == "mosaic.fxtwitter.com" || strings.HasSuffix(host, ".hdslb.com") || moreMediaHost(host)
		}
		if !safe {
			*field = Field[string]{State: "failed", Reason: "unsafe_target"}
			return
		}
		if f.lookup == nil {
			return
		}
		if collecting {
			pending[host] = true
			return
		}
		err = hosts[host]
		if err != nil {
			kind := "network"
			var fe *FetchError
			if errors.As(err, &fe) {
				kind = fe.Kind
			}
			*field = Field[string]{State: "failed", Reason: kind}
		}
	}
	checkMedia := func(m *Media) {
		check(&m.URL)
		check(&m.Thumbnail)
		if m.Fallback != nil {
			check(&m.Fallback.URL)
		}
		for i := range m.Variants.Value {
			check(&m.Variants.Value[i].URL)
		}
	}
	visit = func(item *Result) {
		if item == nil || visited[item] {
			return
		}
		visited[item] = true
		check(&item.Author.Avatar)
		if item.Ugoira != nil {
			check(&item.Ugoira.ZIP)
		}
		if item.Novel != nil && item.Novel.Cover.State == "available" {
			checkMedia(&item.Novel.Cover.Value)
		}
		if item.LinkCard.State == "available" {
			checkMedia(&item.LinkCard.Value.Image.Value)
		}
		for i := range item.Media.Value {
			checkMedia(&item.Media.Value[i])
		}
		if item.Mosaic.State == "available" {
			checkMedia(&item.Mosaic.Value)
		}
		for i := range item.Blocks {
			b := &item.Blocks[i]
			if b.Media != nil {
				checkMedia(b.Media)
			}
			visit(b.Quote)
		}
		visit(item.Quote.Value)
	}
	visit(r)
	if len(pending) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for host := range pending {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := resolve(host)
				mu.Lock()
				hosts[host] = err
				mu.Unlock()
			}()
		}
		wg.Wait()
	}
	collecting = false
	visited = map[*Result]bool{}
	visit(r)
}
