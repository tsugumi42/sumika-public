package linkreader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

// 干净链接不带 page=detailuniversal_detail（路径已经说明是详情页，参数对群友没用）
const mallURL = "https://mall.bilibili.com/neul-next/detailuniversal/detail.html?itemsId=70001"
const mallTrace = "share_mid=fictional-sharer&from=fictional-from&channel=fictional-channel&msource=fictional-source&from_spmid=fictional-spm"

func TestMallRecognitionShapes(t *testing.T) {
	cases := []struct {
		name, raw, id string
	}{
		{"current", mallURL + "&" + mallTrace, "70001"},
		{"bare", "mall.bilibili.com/detail.html?itemsId=70001", "70001"},
		{"legacy", "https://mall.bilibili.com/neul/index.html?page=detailuniversal_detail&itemsId=70001", "70001"},
		{"index", "https://mall.bilibili.com/neul-next/index.html?itemsId=70001&page=detailuniversal_detail", "70001"},
		{"html", "https://mall.bilibili.com/detail.html?from=fictional&amp;itemsId=70001&amp;share_mid=fictional", "70001"},
		{"encoded_key", "https://mall.bilibili.com/detail.html?items%49d=70001&%73hare_mid=fictional&share_mid=other", "70001"},
		{"long_valid", "https://mall.bilibili.com/detail.html?itemsId=" + strings.Repeat("9", 128), strings.Repeat("9", 128)},
		{"missing", "https://mall.bilibili.com/detail.html", ""},
		{"empty", "https://mall.bilibili.com/detail.html?itemsId=", ""},
		{"zero", "https://mall.bilibili.com/detail.html?itemsId=0", ""},
		{"leading_zero", "https://mall.bilibili.com/detail.html?itemsId=070001", ""},
		{"letters", "https://mall.bilibili.com/detail.html?itemsId=abc", ""},
		{"negative", "https://mall.bilibili.com/detail.html?itemsId=-1", ""},
		{"fraction", "https://mall.bilibili.com/detail.html?itemsId=1.5", ""},
		{"too_long", "https://mall.bilibili.com/detail.html?itemsId=" + strings.Repeat("9", 129), ""},
		{"duplicate", mallURL + "&itemsId=70002", ""},
		{"duplicate_page", mallURL + "&page=detailuniversal_detail&page=detailuniversal_detail", ""},
		{"bad_encoding", mallURL + "&from=%zz", ""},
		{"market", "https://mall.bilibili.com/neul-next/index.html?page=magic-market_detail&itemsId=70001", ""},
		{"lottery", "https://mall.bilibili.com/neul-next/index.html?page=magic-detail_detail&itemsId=70001", ""},
		{"ticket", "https://mall.bilibili.com/neul-next/ticket-renovation/detail.html?id=70001&itemsId=70001", ""},
		{"conflicting_route", mallURL + "&page=magic-market_detail", ""},
		{"unknown_route", "https://mall.bilibili.com/other/detail.html?itemsId=70001", ""},
		{"fake_host", "https://mall.bilibili.com.evil.example/detail.html?itemsId=70001", ""},
		{"credentials", "https://fictional@mall.bilibili.com/detail.html?itemsId=70001", ""},
		{"port", "https://mall.bilibili.com:3001/detail.html?itemsId=70001", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if ok != (c.id != "") {
				t.Fatalf("recognized=%v: %+v", ok, l)
			}
			want := ""
			if ok {
				want = "https://mall.bilibili.com" + mallPath + "?itemsId=" + c.id
				if l.ID != c.id || l.Kind != "mall" || l.Platform != "bilibili" || l.Clean != want || Clean(want) != want {
					t.Fatalf("identity/clean: %+v", l)
				}
				if clean, err := CleanAny(c.raw); strings.HasPrefix(c.raw, "https://") && (err != nil || clean != want) {
					t.Fatalf("generic clean: %q %v", clean, err)
				}
			}
			for _, field := range []string{"jumpUrl", "qqdocurl"} {
				card, err := json.Marshal(map[string]any{"meta": map[string]any{"detail_1": map[string]any{field: c.raw}}})
				if err != nil {
					t.Fatal(err)
				}
				links, _ := FromCard(string(card))
				if len(links) != boolCount(ok) || (ok && links[0].Clean != want) {
					t.Fatalf("%s: %+v", field, links)
				}
			}
			links := Extract("看看 " + c.raw)
			if len(links) != boolCount(ok) || (ok && links[0].Clean != want) {
				t.Fatalf("text: %+v", links)
			}
		})
	}
	for _, raw := range []string{"https://b23.tv/fictional?" + mallTrace, "https://www.bilibili.com/video/av70001?" + mallTrace} {
		if got := Clean(raw); strings.Contains(got, "fictional-") || strings.Contains(got, "share_mid") {
			t.Fatal(got)
		}
		if got, err := CleanAny(raw); err != nil || strings.Contains(got, "fictional-") || strings.Contains(got, "share_mid") {
			t.Fatalf("通用清洗泄漏：%q %v", got, err)
		}
	}
	links, _ := FromCard(`{"meta":{"detail_1":{"title":"哔哩哔哩 商品70001","preview":"https://mall.bilibili.com/detail.html?itemsId=70001"},"news":{"jumpUrl":7}}}`)
	if len(links) != 0 {
		t.Fatal("标题或封面不能恢复商品号")
	}
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func mallBody(t *testing.T, changes map[string]any) string {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "mall-item")), &root); err != nil {
		t.Fatal(err)
	}
	for path, value := range changes {
		keys := strings.Split(path, ".")
		node := root
		for _, k := range keys[:len(keys)-1] {
			node = node[k].(map[string]any)
		}
		if marker, ok := value.(string); ok && marker == "DELETE" {
			delete(node, keys[len(keys)-1])
		} else {
			node[keys[len(keys)-1]] = value
		}
	}
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMallReplay(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.String() != "https://mall.bilibili.com/mall-search-items/items/merchant/info?itemsId=70001" || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatalf("非匿名商品请求：%s", req.URL)
		}
		return response(200, fixture(t, "mall-item")), nil
	})
	l := link(t, mallURL)
	// Fetch 也是外部入口，不能信任调用者已经清过 Clean。
	l.Clean += "&" + mallTrace
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "mall" || r.Title.Value != "纸舟工房 星灯摆件" || r.Mall.Price.Value != "325.50～499.90" || r.Mall.Deposit.Value != "48.75～74.98" || r.Mall.Status.Value != "预售" || r.Mall.Shop.Value != "纸舟商店" || r.CleanURL != mallURL {
		t.Fatalf("商品字段：%+v", r)
	}
	if len(r.Blocks) != 2 || r.Blocks[0].Type != "text" || r.Blocks[1].Type != "image" || len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/mall/fictional-first.png" {
		t.Fatalf("首图/原序：%+v", r.Blocks)
	}
	if r.Mall.PresaleStart.Value.Unix() != 1792670100 || r.Mall.PresaleEnd.Value.Unix() != 1795622340 || r.Mall.OnSale.State != "not_returned" || !strings.Contains(r.Text.Value, "10-22 19:55") || strings.Contains(r.Text.Value, "+08:00") || !strings.Contains(r.Text.Value, "预计到货补款：2027-05") || strings.Contains(r.Text.Value, "1970") || r.Author.ID.State != "not_returned" {
		t.Fatalf("时间/账号：%+v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "fictional-shop-account") || strings.Contains(string(b), "share_mid") {
		t.Fatal("无关账号进入结果")
	}
	for _, id := range []any{strings.Repeat("9", 128), json.RawMessage(strings.Repeat("9", 128))} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, mallBody(t, map[string]any{"data.itemsId": id})), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://mall.bilibili.com/detail.html?itemsId="+strings.Repeat("9", 128)))
		if err != nil || r.ID != strings.Repeat("9", 128) {
			t.Fatal("响应长编号丢精度", err, r.ID)
		}
	}
}

func TestMallResponseShapes(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		changes   map[string]any
		raw, kind string
	}{
		{name: "empty", raw: "", kind: "invalid_response"},
		{name: "html", raw: "<html>", kind: "invalid_response"},
		{name: "null", raw: "null", kind: "invalid_response"},
		{name: "business", changes: map[string]any{"code": 81102001, "success": false, "data": nil}, kind: "interface_error"},
		{name: "success_false", changes: map[string]any{"success": false}, kind: "invalid_response"},
		{name: "missing_success", changes: map[string]any{"success": "DELETE"}, kind: "invalid_response"},
		{name: "missing_code", changes: map[string]any{"code": "DELETE"}, kind: "invalid_response"},
		{name: "bad_code", changes: map[string]any{"code": "0"}, kind: "invalid_response"},
		{name: "missing_data", changes: map[string]any{"data": "DELETE"}, kind: "invalid_response"},
		{name: "empty_data", changes: map[string]any{"data": map[string]any{}}, kind: "invalid_response"},
		{name: "missing_id", changes: map[string]any{"data.itemsId": "DELETE"}, kind: "invalid_response"},
		{name: "null_id", changes: map[string]any{"data.itemsId": nil}, kind: "invalid_response"},
		{name: "empty_id", changes: map[string]any{"data.itemsId": ""}, kind: "invalid_response"},
		{name: "wrong_id", changes: map[string]any{"data.itemsId": 70002}, kind: "identity_mismatch"},
		{name: "missing_name", changes: map[string]any{"data.name": "DELETE"}, kind: "invalid_response"},
		{name: "empty_name", changes: map[string]any{"data.name": " "}, kind: "invalid_response"},
		{name: "bad_name", changes: map[string]any{"data.name": []any{}}, kind: "invalid_response"},
		{name: "numeric_price", changes: map[string]any{"data.price": 325}, kind: "invalid_response"},
		{name: "object_price", changes: map[string]any{"data.price": map[string]any{}}, kind: "invalid_response"},
		{name: "empty_price", changes: map[string]any{"data.price": ""}, kind: "invalid_response"},
		{name: "bad_price", changes: map[string]any{"data.price": "1e2"}, kind: "invalid_response"},
		{name: "negative_price", changes: map[string]any{"data.maxPrice": "-1"}, kind: "invalid_response"},
		{name: "bad_time", changes: map[string]any{"data.autoOnSaleTime": "soon"}, kind: "invalid_response"},
		{name: "bad_shop", changes: map[string]any{"data.shopInfoVO.shopName": 7}, kind: "invalid_response"},
		{name: "bad_type", changes: map[string]any{"data.itemsType": true}, kind: "invalid_response"},
		{name: "bad_images", changes: map[string]any{"data.img": 7}, kind: "invalid_response"},
		{name: "bad_skus", changes: map[string]any{"data.itemsSkuListVO.itemsSkuList": "bad"}, kind: "invalid_response"},
		{name: "forbidden", status: 403, raw: "{}", kind: "login_required"},
		{name: "risk", status: 412, raw: "{}", kind: "risk_control"}, // 412 是 B 站的风控（IP 级），不是普通接口错误；平台随即冷却
		{name: "limit", status: 429, raw: "{}", kind: "rate_limited"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := c.raw
			if c.changes != nil {
				body = mallBody(t, c.changes)
			}
			status := c.status
			if status == 0 {
				status = 200
			}
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(status, body), nil })
			_, err := f.Fetch(context.Background(), link(t, mallURL))
			wantError(t, err, c.kind)
			if f.Requests() != 1 {
				t.Fatal("出错后改路或重试")
			}
		})
	}
}

func TestMallFieldsAndStates(t *testing.T) {
	stock := func(level int, count int) any { return []any{map[string]any{"stockLevel": level, "stock": count}} }
	cases := []struct {
		name    string
		changes map[string]any
		status  string
	}{
		{"on_sale", map[string]any{"data.itemsType": "0"}, "在售"},
		{"sold", map[string]any{"data.itemsSkuListVO.itemsSkuList": stock(4, 0)}, "售罄"},
		{"temporarily_empty", map[string]any{"data.itemsType": "0", "data.itemsSkuListVO.itemsSkuList": stock(3, 0)}, "状态未知"},
		{"locked", map[string]any{"data.saleStatus": 14, "data.itemsSkuListVO.itemsSkuList": stock(4, 0)}, "状态未知"},
		{"unknown", map[string]any{"data.itemsType": "9", "data.itemsSkuListVO.itemsSkuList": stock(4, 0)}, "状态未知"},
		{"no_stock", map[string]any{"data.itemsType": "0", "data.itemsSkuListVO.itemsSkuList": stock(1, 0)}, "状态未知"},
		{"missing_skus", map[string]any{"data.itemsType": "0", "data.itemsSkuListVO": nil}, "状态未知"},
		{"empty_skus", map[string]any{"data.itemsType": "0", "data.itemsSkuListVO.itemsSkuList": []any{}}, "状态未知"},
		{"mixed_skus", map[string]any{"data.itemsType": "0", "data.itemsSkuListVO.itemsSkuList": []any{map[string]any{"stockLevel": 4, "stock": 0}, map[string]any{"stockLevel": 2, "stock": 1}}}, "在售"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, mallBody(t, c.changes)), nil })
			r, err := f.Fetch(context.Background(), link(t, mallURL))
			if err != nil {
				t.Fatal(err)
			}
			if r.Mall.Status.Value != c.status {
				t.Fatalf("状态：%+v", r.Mall.Status)
			}
		})
	}
	for _, absent := range []any{"DELETE", nil} {
		t.Run("optional", func(t *testing.T) {
			changes := map[string]any{}
			for _, key := range []string{"price", "maxPrice", "itemsType", "itemsStatus", "saleStatus", "shopInfoVO", "shopVO", "advState", "autoOnSaleTime", "presaleDeliveryTimeStr", "img", "itemsSkuListVO"} {
				changes["data."+key] = absent
			}
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, mallBody(t, changes)), nil })
			r, err := f.Fetch(context.Background(), link(t, mallURL))
			if err != nil {
				t.Fatal(err)
			}
			if r.Mall.Price.State != "not_returned" || r.Mall.Shop.State != "not_returned" || r.Media.State != "not_returned" || r.Mall.PresaleStart.State != "not_returned" || r.Mall.Status.State != "not_returned" || !strings.Contains(r.Text.Value, "价格：未返回") {
				t.Fatalf("缺项：%+v", r)
			}
		})
	}
	t.Run("zero_and_fallback", func(t *testing.T) {
		changes := map[string]any{"data.price": "0", "data.maxPrice": "0", "data.shopInfoVO": nil, "data.autoOnSaleTime": 1792670100, "data.img": []any{}}
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, mallBody(t, changes)), nil })
		r, err := f.Fetch(context.Background(), link(t, mallURL))
		if err != nil {
			t.Fatal(err)
		}
		if r.Mall.Price.Value != "0" || r.Mall.Shop.Value != "备用商店" || r.Mall.OnSale.State != "available" || r.Media.State != "available" || len(r.Media.Value) != 0 {
			t.Fatalf("零值/备用：%+v", r)
		}
	})
}

func TestMallShortCachePrivacy(t *testing.T) {
	ctx := context.Background()
	st, _ := cacheStores(t)
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "fictional-sharer") {
			t.Error("追踪参数进入 HTTP")
		}
		if req.URL.Host == "b23.tv" {
			r := response(302, "")
			r.Header.Set("Location", mallURL+"&"+mallTrace)
			return r, nil
		}
		if req.URL.Query().Get("itemsId") == "70002" {
			return response(200, `{"success":false,"code":81102001,"data":null}`), nil
		}
		return response(200, fixture(t, "mall-item")), nil
	})
	s := cacheService(t, st, f, nil)
	r, err := s.Get(ctx, "https://b23.tv/fictional?"+mallTrace)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Get(ctx, mallURL+"&"+mallTrace)
	if err != nil {
		t.Fatal(err)
	}
	cached, found, err := st.TraceStore().LinkResult(ctx, mallURL)
	if err != nil || !found || f.Requests() != 2 || r.Mall.Price.Value != r2.Mall.Price.Value {
		t.Fatalf("缓存：found=%v requests=%d err=%v", found, f.Requests(), err)
	}
	for _, token := range []string{"share_mid", "fictional-sharer", "fictional-from", "fictional-shop-account"} {
		if strings.Contains(string(cached.JSON), token) {
			t.Fatal("缓存泄漏", token)
		}
	}
	target, _, found, err := st.TraceStore().LinkAlias(ctx, "https://b23.tv/fictional")
	if err != nil || !found || target != mallURL {
		t.Fatal("别名不干净", target, err)
	}
	bad := link(t, mallURL)
	bad.ID = "70002"
	_, err = f.Fetch(ctx, bad)
	wantError(t, err, "invalid_target")
	if f.Requests() != 2 {
		t.Fatal("伪造 Link 联网")
	}
	failedURL := strings.Replace(mallURL, "70001", "70002", 1)
	_, err = s.Get(ctx, failedURL)
	wantError(t, err, "interface_error")
	_, found, err = st.TraceStore().LinkResult(ctx, failedURL)
	if err != nil || found {
		t.Fatal("失败写进成功缓存", err)
	}
}

func TestMallNetworkBoundary(t *testing.T) {
	fake := netip.MustParseAddr("198.18.0.1")
	if !allowedWithProxy("mall.bilibili.com", fake) || allowedWithProxy("evil.mall.bilibili.com", fake) || allowedWithProxy("mall.bilibili.com", netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("商城 fake-ip 边界错误")
	}
	for _, image := range []string{"http://127.0.0.1/fictional.png", "https://i0.hdslb.com.evil.example/fictional.png", "https://fictional@i0.hdslb.com/fictional.png"} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, mallBody(t, map[string]any{"data.img": []any{image}})), nil
		})
		r, err := f.Fetch(context.Background(), link(t, mallURL))
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.Value[0].URL.State != "failed" || r.Blocks[1].Media.URL.State != "failed" {
			t.Fatal("不安全首图进入结果", r.Media)
		}
	}
	for _, target := range []string{"http://127.0.0.1/", "https://mall.bilibili.com.evil.example/detail.html?itemsId=70001", "https://mall.bilibili.com/neul-next/index.html?page=magic-market_detail&itemsId=70001"} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			r := response(302, "")
			r.Header.Set("Location", target)
			return r, nil
		})
		_, err := f.Resolve(context.Background(), link(t, "https://b23.tv/fictional"))
		wantError(t, err, "unsafe_target")
		if f.Requests() != 1 {
			t.Fatal("访问了恶意跳转终点")
		}
	}
}
