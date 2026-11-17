package linkreader

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const boothBookURL = "https://booth.pm/ja/items/900002"
const boothThreeDURL = "https://booth.pm/ja/items/900001"

type boothCall struct{ url, accept, cookie, auth, referer string }

func boothFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// boothJSON 在真实裁剪夹具上改字段；DELETE 表示删除该键，与 mall 测试同一约定。
func boothJSON(t *testing.T, changes map[string]any) string {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(boothFile(t, "booth-item-3d.json")), &root); err != nil {
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

func boothResponse(status int, body string, h http.Header) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// boothFetcher 按路径回放：.json 走商品接口，其它走商品页 HTML；所有请求记进 calls。
func boothFetcher(t *testing.T, calls *[]boothCall, jsonStatus int, jsonBody string, jsonHeader http.Header, pageStatus int, pageBody string) *Fetcher {
	t.Helper()
	return testFetcher(t, func(req *http.Request) (*http.Response, error) {
		*calls = append(*calls, boothCall{url: req.URL.String(), accept: req.Header.Get("Accept"), cookie: req.Header.Get("Cookie"), auth: req.Header.Get("Authorization"), referer: req.Header.Get("Referer")})
		if req.Method != http.MethodGet {
			t.Fatalf("非 GET 请求：%s", req.Method)
		}
		if strings.HasSuffix(req.URL.Path, ".json") {
			return boothResponse(jsonStatus, jsonBody, jsonHeader), nil
		}
		return boothResponse(pageStatus, pageBody, nil), nil
	})
}

// boothShopUUID 从裁剪夹具读店铺 uuid，测试源码里不写死这串数字。
func boothShopUUID(t *testing.T) string {
	t.Helper()
	var j struct {
		Shop struct {
			UUID string `json:"uuid"`
		} `json:"shop"`
	}
	if err := json.Unmarshal([]byte(boothFile(t, "booth-item-3d.json")), &j); err != nil {
		t.Fatal(err)
	}
	return j.Shop.UUID
}

var boothOriginTag = regexp.MustCompile(`data-origin="([^"]+)"`)

func TestBoothReplayThreeD(t *testing.T) {
	var calls []boothCall
	page := boothFile(t, "booth-item-3d-page.html")
	f := boothFetcher(t, &calls, 200, boothFile(t, "booth-item-3d.json"), nil, 200, page)
	r, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].url != boothThreeDURL+".json" || calls[1].url != boothThreeDURL {
		t.Fatalf("请求序列应为 JSON 再商品页各一次：%+v", calls)
	}
	for _, c := range calls {
		if c.cookie != "" || c.auth != "" || c.referer != "https://booth.pm/" {
			t.Fatalf("匿名请求带了凭据或缺 Referer：%+v", c)
		}
	}
	if calls[0].accept != "application/json" || calls[1].accept != "text/html" {
		t.Fatalf("Accept 应按端点区分：%+v", calls)
	}
	if r.Platform != "booth" || r.Kind != "item" || r.ID != "900001" || r.CleanURL != boothThreeDURL {
		t.Fatalf("身份：%+v", r)
	}
	if r.Title.Value != "编造的3D商品" || r.Booth == nil || r.Booth.Price.Value != "¥ 500" || r.Category.Value != "编造的3D分类" {
		t.Fatalf("标题、价格或分类：%+v", r)
	}
	if r.Author.Name.Value != "编造店铺甲" || r.Author.Account.Value != "example-shop-a" || r.Author.ID.Value != boothShopUUID(t) {
		t.Fatalf("作者应以 uuid 为稳定 ID：%+v", r.Author)
	}
	if got := r.Published.Value.Format(time.RFC3339); got != "2026-01-05T08:52:24Z" {
		t.Fatalf("发布时间：%s", got)
	}
	if r.Stats["wish_lists"].Value != 1038 || len(r.Tags.Value) != 16 {
		t.Fatalf("收藏或标签：%+v %+v", r.Stats, r.Tags)
	}
	if r.Flags["original_images"].Value != true || r.Flags["sold_out"].Value != false {
		t.Fatalf("原图或状态标记：%+v", r.Flags)
	}
	// 原图必须正好是页面 data-origin 的集合，且按 JSON 的图序排列。
	want := map[string]bool{}
	for _, m := range boothOriginTag.FindAllStringSubmatch(page, -1) {
		want[m[1]] = true
	}
	if len(r.Media.Value) != 5 || len(want) != 5 {
		t.Fatalf("图数：%d 页面 %d", len(r.Media.Value), len(want))
	}
	for _, m := range r.Media.Value {
		if !want[m.URL.Value] || m.Referer != "https://booth.pm/" || !strings.HasSuffix(m.URL.Value, ".png") {
			t.Fatalf("图片未对应页面原图：%+v", m)
		}
	}
	if len(r.Blocks) != 6 || r.Blocks[0].Type != "text" || r.Blocks[1].Type != "image" {
		t.Fatalf("有序块应为正文后接图：%+v", r.Blocks)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "cookie") || strings.Contains(string(b), "adult") {
		t.Fatal("结果不应带 Cookie 或年龄字段")
	}
}

func TestBoothOriginDegradesWithoutPage(t *testing.T) {
	var calls []boothCall
	f := boothFetcher(t, &calls, 200, boothFile(t, "booth-item-book.json"), nil, 500, "<html>error</html>")
	r, err := f.Fetch(context.Background(), link(t, boothBookURL))
	if err != nil {
		t.Fatalf("商品页失败不该拖垮主体：%v", err)
	}
	if len(calls) != 2 || r.Title.Value == "" || len(r.Media.Value) != 1 {
		t.Fatalf("降级结果：%d 次请求 %+v", len(calls), r.Media)
	}
	if !strings.HasSuffix(r.Media.Value[0].URL.Value, "_base_resized.jpg") || r.Flags["original_images"].Value != false {
		t.Fatalf("应保留缩略版并标明未取到原图：%+v %+v", r.Media.Value[0].URL, r.Flags)
	}
}

func TestBoothOriginMappingIsByIdentityNotPosition(t *testing.T) {
	page := boothFile(t, "booth-item-3d-page.html")
	// 第一张改成 http（必须拒绝），第二张换成一个 JSON 里没有的 UUID（不能按位置顶上）。
	uuid := boothShopUUID(t)
	page = strings.Replace(page, "https://booth.pximg.net/"+uuid+"/i/900001/6bc91ded-1b35-4489-95e0-8f8807f03b34.png", "http://booth.pximg.net/"+uuid+"/i/900001/6bc91ded-1b35-4489-95e0-8f8807f03b34.png", 1)
	page = strings.Replace(page, "6bb1155f-7137-49ac-84c0-5449c86c0692.png", "ffffffff-0000-4000-8000-000000000000.png", -1)
	var calls []boothCall
	f := boothFetcher(t, &calls, 200, boothFile(t, "booth-item-3d.json"), nil, 200, page)
	r, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
	if err != nil {
		t.Fatal(err)
	}
	if r.Flags["original_images"].Value != false || len(r.Media.Value) != 5 {
		t.Fatalf("有一张没对上时不能标为全部原图：%+v", r.Flags)
	}
	if !strings.HasSuffix(r.Media.Value[0].URL.Value, "_base_resized.jpg") || !strings.HasSuffix(r.Media.Value[1].URL.Value, "_base_resized.jpg") {
		t.Fatalf("http 和未匹配的应退回缩略版：%+v", r.Media.Value)
	}
	if !strings.HasSuffix(r.Media.Value[2].URL.Value, ".png") {
		t.Fatalf("其余仍应使用原图：%+v", r.Media.Value[2])
	}
}

func TestBoothFailureShapes(t *testing.T) {
	retry := http.Header{"Retry-After": []string{"120"}}
	cases := []struct {
		name      string
		status    int
		body      string
		header    http.Header
		kind      string
		wantCalls int
	}{
		{name: "not_found", status: 404, body: "{}", kind: "not_found", wantCalls: 1},
		{name: "gone", status: 410, body: "{}", kind: "not_found", wantCalls: 1},
		{name: "forbidden", status: 403, body: "{}", kind: "login_required", wantCalls: 1},
		{name: "server", status: 500, body: "{}", kind: "interface_error", wantCalls: 1},
		{name: "rate_limited", status: 429, body: "{}", header: retry, kind: "rate_limited", wantCalls: 1},
		{name: "age_redirect", status: 302, body: "", header: http.Header{"Location": []string{"https://booth.pm/adult_confirm?return=/ja/items/900001"}}, kind: "age_restricted", wantCalls: 1},
		{name: "age_flag", status: 200, body: boothJSON(t, map[string]any{"is_adult": true}), kind: "age_restricted", wantCalls: 1},
		{name: "malformed", status: 200, body: "{", kind: "invalid_response", wantCalls: 1},
		{name: "null", status: 200, body: "null", kind: "invalid_response", wantCalls: 1},
		{name: "html", status: 200, body: "<html>not json</html>", kind: "invalid_response", wantCalls: 1},
		{name: "missing_id", status: 200, body: boothJSON(t, map[string]any{"id": "DELETE"}), kind: "invalid_response", wantCalls: 1},
		{name: "wrong_id", status: 200, body: boothJSON(t, map[string]any{"id": 900002}), kind: "identity_mismatch", wantCalls: 1},
		{name: "missing_name", status: 200, body: boothJSON(t, map[string]any{"name": "DELETE"}), kind: "invalid_response", wantCalls: 1},
		{name: "blank_name", status: 200, body: boothJSON(t, map[string]any{"name": "  "}), kind: "invalid_response", wantCalls: 1},
		{name: "bad_time", status: 200, body: boothJSON(t, map[string]any{"published_at": "yesterday"}), kind: "invalid_response", wantCalls: 1},
		{name: "bad_tags", status: 200, body: boothJSON(t, map[string]any{"tags": "x"}), kind: "invalid_response", wantCalls: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []boothCall
			f := boothFetcher(t, &calls, c.status, c.body, c.header, 200, boothFile(t, "booth-item-3d-page.html"))
			_, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
			var fe *FetchError
			if !errors.As(err, &fe) || fe.Kind != c.kind {
				t.Fatalf("kind = %v，想要 %s", err, c.kind)
			}
			if len(calls) != c.wantCalls {
				t.Fatalf("请求次数 %d，想要 %d：%+v", len(calls), c.wantCalls, calls)
			}
			if c.kind == "rate_limited" && fe.RetryAfter != "120" {
				t.Fatalf("Retry-After 丢失：%+v", fe)
			}
		})
	}
}

func TestBoothRateCooldownIsSharedWithinFetcher(t *testing.T) {
	var calls []boothCall
	f := boothFetcher(t, &calls, 429, "{}", http.Header{"Retry-After": []string{"120"}}, 200, "")
	if _, err := f.Fetch(context.Background(), link(t, boothThreeDURL)); err == nil {
		t.Fatal("429 应失败")
	}
	before := len(calls)
	_, err := f.Fetch(context.Background(), link(t, boothBookURL))
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != "rate_limited" || len(calls) != before {
		t.Fatalf("冷却期内不应再发请求：%v %d 次", err, len(calls)-before)
	}
}

func TestBoothOptionalFieldsAndLimits(t *testing.T) {
	var calls []boothCall
	body := boothJSON(t, map[string]any{
		"category":         "DELETE",
		"tags":             nil,
		"shop":             "DELETE",
		"published_at":     nil,
		"wish_lists_count": "DELETE",
		"price":            nil,
		"description":      nil,
		"is_sold_out":      true,
		"is_end_of_sale":   true,
		"images":           []any{},
	})
	f := boothFetcher(t, &calls, 200, body, nil, 200, "")
	r, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("零图不应请求商品页：%d 次", len(calls))
	}
	if r.Category.State != "not_returned" || r.Tags.State != "not_returned" || r.Author.Name.State != "not_returned" || r.Author.ID.State != "not_returned" {
		t.Fatalf("缺失字段应标 not_returned：%+v", r.Author)
	}
	if r.Published.State != "not_returned" || r.Text.State != "not_returned" || r.Booth.Price.State != "not_returned" || r.Stats["wish_lists"].State != "not_returned" {
		t.Fatalf("缺失字段应标 not_returned：%+v", r.Booth)
	}
	if r.Media.State != "available" || len(r.Media.Value) != 0 || r.Flags["sold_out"].Value != true || r.Flags["end_of_sale"].Value != true {
		t.Fatalf("零图应是 available 空列表，状态位保留：%+v %+v", r.Media, r.Flags)
	}
	if _, ok := r.Flags["original_images"]; ok {
		t.Fatal("没有图片时不应声称原图状态")
	}
}

func TestBoothLongTextAndZeroTags(t *testing.T) {
	var calls []boothCall
	long := strings.Repeat("長", 5000)
	f := boothFetcher(t, &calls, 200, boothJSON(t, map[string]any{"description": long, "tags": []any{}}), nil, 200, "")
	r, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Text.Truncated || len([]rune(r.Text.Value)) != 800 {
		t.Fatalf("长正文应截到 800 rune 并标记：%d %v", len([]rune(r.Text.Value)), r.Text.Truncated)
	}
	if r.Tags.State != "available" || len(r.Tags.Value) != 0 {
		t.Fatalf("空标签数组应是 available 空列表：%+v", r.Tags)
	}
}

func TestBoothLanguagePathIsKept(t *testing.T) {
	var calls []boothCall
	f := boothFetcher(t, &calls, 200, boothFile(t, "booth-item-book.json"), nil, 500, "")
	if _, err := f.Fetch(context.Background(), link(t, "https://example-shop-b.booth.pm/en/items/900002")); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 1 || calls[0].url != "https://booth.pm/en/items/900002.json" {
		t.Fatalf("语言段应保留在请求路径里：%+v", calls)
	}
}

// TestBoothFixturesUseInventedIdentities 夹具里的店铺 uuid 和创作者数字 ID 必须是虚构的，
// 真实值能对应到具体创作者，不能进 git。约定：uuid 用 0badc0de- 或 feedface- 前缀，用户 ID 不超过 6 位。
func TestBoothFixturesUseInventedIdentities(t *testing.T) {
	usersID := regexp.MustCompile(`/users/(\d+)/`)
	for _, name := range []string{"booth-item-3d.json", "booth-item-book.json"} {
		var j struct {
			Images []struct {
				Original string `json:"original"`
			} `json:"images"`
			Shop struct {
				UUID      string `json:"uuid"`
				Thumbnail string `json:"thumbnail_url"`
			} `json:"shop"`
		}
		if err := json.Unmarshal([]byte(boothFile(t, name)), &j); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(j.Shop.UUID, "0badc0de-") && !strings.HasPrefix(j.Shop.UUID, "feedface-") {
			t.Fatalf("%s：店铺 uuid 不是虚构值：%s", name, j.Shop.UUID)
		}
		if m := usersID.FindStringSubmatch(j.Shop.Thumbnail); m == nil || len(m[1]) > 6 {
			t.Fatalf("%s：缩略图里的用户 ID 不是虚构的短数字：%s", name, j.Shop.Thumbnail)
		}
		for _, img := range j.Images {
			if !strings.Contains(img.Original, "/"+j.Shop.UUID+"/") {
				t.Fatalf("%s：图片路径应与店铺 uuid 一致：%s", name, img.Original)
			}
		}
	}
}

// TestBoothBadTimeReportsBoothSource 发布时间解析失败时，错误来源仍应是 BOOTH 的商品接口，不是通用的 date。
func TestBoothBadTimeReportsBoothSource(t *testing.T) {
	var calls []boothCall
	f := boothFetcher(t, &calls, 200, boothJSON(t, map[string]any{"published_at": "yesterday"}), nil, 200, "")
	_, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != "invalid_response" || fe.Source != boothSource {
		t.Fatalf("时间字段坏了应报 %s 来源：%v", boothSource, err)
	}
}

// TestBoothFallbackRejectsForeignImageHost 页面取不到原图时退回 JSON 的缩略版，但 JSON 给的地址也要过 BOOTH 图床的 https 检查。
func TestBoothFallbackRejectsForeignImageHost(t *testing.T) {
	var calls []boothCall
	uuid := boothShopUUID(t)
	body := boothJSON(t, map[string]any{"images": []any{
		map[string]any{"original": "https://evil.example/x_base_resized.jpg"},
		map[string]any{"original": "http://booth.pximg.net/" + uuid + "/i/900001/y_base_resized.jpg"},
		map[string]any{"original": "https://booth.pximg.net/" + uuid + "/i/900001/z_base_resized.jpg"},
	}})
	f := boothFetcher(t, &calls, 200, body, nil, 500, "")
	r, err := f.Fetch(context.Background(), link(t, boothThreeDURL))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Media.Value) != 1 || !strings.HasSuffix(r.Media.Value[0].URL.Value, "/z_base_resized.jpg") {
		t.Fatalf("非 BOOTH 图床的地址不应进入结果：%+v", r.Media.Value)
	}
}
