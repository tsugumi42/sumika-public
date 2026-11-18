package app

import (
	"encoding/json"
	"slices"
	"testing"

	"sumika-native/internal/web"
)

func TestApplyWebOrderUpdatesRunningOrder(t *testing.T) {
	t.Parallel()
	order := web.NewOrder([]string{"tavily", "deepseek"}, []string{"http", "deepseek"})
	a := &App{ecosystem: &Ecosystem{WebOrder: order}}
	a.applyWebOrder("web.searchOrder", json.RawMessage(`["deepseek","tavily","duckduckgo"]`))
	a.applyWebOrder("web.fetchOrder", json.RawMessage(`["deepseek","http"]`))
	if !slices.Equal(order.Search(), []string{"deepseek", "tavily", "duckduckgo"}) || !slices.Equal(order.Fetch(), []string{"deepseek", "http"}) {
		t.Fatalf("顺序没改：%v %v", order.Search(), order.Fetch())
	}
	// 不合法的值（未知名字、重复、类型不对）不动运行中的顺序
	for _, bad := range []string{`["nope"]`, `["tavily","tavily"]`, `"tavily"`, `[1]`} {
		a.applyWebOrder("web.searchOrder", json.RawMessage(bad))
	}
	if !slices.Equal(order.Search(), []string{"deepseek", "tavily", "duckduckgo"}) {
		t.Fatalf("坏值改动了顺序：%v", order.Search())
	}
	// 联网没开（没有 Order）时不能 panic
	(&App{}).applyWebOrder("web.searchOrder", json.RawMessage(`["tavily"]`))
	(&App{ecosystem: &Ecosystem{}}).applyWebOrder("web.fetchOrder", json.RawMessage(`["http"]`))
}
