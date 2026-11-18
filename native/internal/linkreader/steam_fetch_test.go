package linkreader

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// steamFetcher 的离线假端：每个请求都检查匿名（无 Cookie、无 Authorization、无 Referer）。
func steamFetcher(t *testing.T, check func(*http.Request), status int, body string) *Fetcher {
	t.Helper()
	return testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Referer") != "" {
			t.Fatal("steam request must be anonymous and carry no referer")
		}
		if check != nil {
			check(req)
		}
		resp := response(status, body)
		if status == 302 {
			resp.Header.Set("Location", "https://store.steampowered.com/agecheck/")
		}
		return resp, nil
	})
}

func steamGET(t *testing.T, want string) func(*http.Request) {
	return func(req *http.Request) {
		if req.Method != http.MethodGet || req.URL.String() != want {
			t.Fatalf("request %s %s; want GET %s", req.Method, req.URL, want)
		}
	}
}

func steamFixtureXML(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".xml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// steamMutate 基于真实夹具改一处，用来构造失败形状；不手写整份 JSON。
func steamMutate(t *testing.T, name string, edit func(doc map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(fixture(t, name)), &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func steamData(doc map[string]any, key string) map[string]any {
	return doc[key].(map[string]any)["data"].(map[string]any)
}

func TestSteamAppReplay(t *testing.T) {
	t.Run("game", func(t *testing.T) {
		f := steamFetcher(t, steamGET(t, "https://store.steampowered.com/api/appdetails?appids=900001&cc=cn&l=schinese"), 200, fixture(t, "steam-app-900001"))
		r, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/app/900001/"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Platform != "steam" || r.Kind != "app" || r.ID != "900001" || r.Source != "steam_store" || r.Title.Value != "编造的猫咪对战" {
			t.Fatalf("identity: %+v", r)
		}
		if r.Author.Name.Value != "编造开发组" || r.Author.ID.State != "not_returned" || r.Author.Account.State != "not_returned" {
			t.Fatalf("author: %+v", r.Author)
		}
		if r.Published.State != "not_returned" || r.PublishedText.Value != "2026-08-21" {
			t.Fatalf("release: %+v %+v", r.Published, r.PublishedText)
		}
		if r.Steam == nil || r.Steam.Price.Value.Final.Value != 2800 || r.Steam.Price.Value.Currency.Value != "CNY" || r.Steam.Price.Value.Formatted.Value != "¥ 28.00" {
			t.Fatalf("price: %+v", r.Steam)
		}
		if got := r.Steam.Platforms.Value; len(got) != 1 || got[0] != "windows" {
			t.Fatalf("platforms: %v", got)
		}
		if r.Steam.RequiredAge.Value != 0 || r.Steam.IsFree.Value != false || r.Steam.Type.Value != "game" {
			t.Fatalf("steam fields: %+v", r.Steam)
		}
		if r.Stats["price_final"].Value != 2800 || r.Stats["recommendations"].Value != 174 || r.Stats["achievements"].Value != 30 {
			t.Fatalf("stats: %+v", r.Stats)
		}
		if got := r.Tags.Value; len(got) != 2 || got[0] != "独立" {
			t.Fatalf("tags: %v", got)
		}
		// 1 张头图、7 张截图、2 个影片（影片只给缩略图，播放地址未请求）。
		if r.Media.State != "available" || len(r.Media.Value) != 10 {
			t.Fatalf("media: %d", len(r.Media.Value))
		}
		if r.Media.Value[0].Type != "image" || r.Media.Value[8].Type != "video" || r.Media.Value[8].URL.State != "unsupported" || r.Media.Value[8].URL.Reason != "playback_not_requested" {
			t.Fatalf("media kinds: %+v %+v", r.Media.Value[0], r.Media.Value[8])
		}
		if r.Text.Truncated || r.Text.Value == "" {
			t.Fatalf("text: %+v", r.Text)
		}
	})
	t.Run("age_17_string_and_no_price", func(t *testing.T) {
		f := steamFetcher(t, steamGET(t, "https://store.steampowered.com/api/appdetails?appids=900002&cc=cn&l=schinese"), 200, fixture(t, "steam-app-900002"))
		r, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/agecheck/app/900002/"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Steam.RequiredAge.Value != 17 || r.Steam.RequiredAge.State != "available" {
			t.Fatalf("required_age string decode: %+v", r.Steam.RequiredAge)
		}
		if got := r.Steam.DescriptorIDs.Value; len(got) != 1 || got[0] != 5 {
			t.Fatalf("descriptors: %v", got)
		}
		// 没有 price_overview 时不写 0 元，只缺失。
		if r.Steam.Price.State != "not_returned" {
			t.Fatalf("price should be missing: %+v", r.Steam.Price)
		}
		if _, ok := r.Stats["price_final"]; ok {
			t.Fatal("missing price must not create a zero stat")
		}
	})
	t.Run("free", func(t *testing.T) {
		f := steamFetcher(t, steamGET(t, "https://store.steampowered.com/api/appdetails?appids=900003&cc=cn&l=schinese"), 200, fixture(t, "steam-app-900003"))
		r, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/app/900003"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Steam.IsFree.Value != true || r.Steam.RequiredAge.Value != 0 || r.Author.Name.Value != "编造开发组" {
			t.Fatalf("free game: %+v author %+v", r.Steam, r.Author)
		}
		if _, ok := r.Stats["price_final"]; ok {
			t.Fatal("free game must not get a price stat")
		}
	})
}

// 打折时原价和现价必须各取各的，对调以后这里要红。
func TestSteamDiscountedPriceKeepsInitialAndFinal(t *testing.T) {
	body := steamMutate(t, "steam-app-900001", func(doc map[string]any) {
		p := steamData(doc, "900001")["price_overview"].(map[string]any)
		p["initial"] = 3990
		p["discount_percent"] = 30
	})
	f := steamFetcher(t, steamGET(t, "https://store.steampowered.com/api/appdetails?appids=900001&cc=cn&l=schinese"), 200, body)
	r, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/app/900001/"))
	if err != nil {
		t.Fatal(err)
	}
	p := r.Steam.Price.Value
	if p.Initial.Value != 3990 || p.Final.Value != 2800 || p.DiscountPercent.Value != 30 {
		t.Fatalf("price initial/final/discount: %+v", p)
	}
	if r.Stats["price_initial"].Value != 3990 || r.Stats["price_final"].Value != 2800 {
		t.Fatalf("stats: initial %+v final %+v", r.Stats["price_initial"], r.Stats["price_final"])
	}
}

func TestSteamPackageReplay(t *testing.T) {
	f := steamFetcher(t, steamGET(t, "https://store.steampowered.com/api/packagedetails?packageids=900004&cc=cn&l=schinese"), 200, fixture(t, "steam-package-900004"))
	r, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/sub/900004/"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "package" || r.Title.Value != "编造的优先状态包" || r.Steam.Type.Value != "package" {
		t.Fatalf("package identity: %+v", r)
	}
	if len(r.Steam.Apps.Value) != 1 || r.Steam.Apps.Value[0].ID.Value != "900007" || r.Steam.Apps.Value[0].Name.Value != "编造的射击游戏" {
		t.Fatalf("apps: %+v", r.Steam.Apps)
	}
	if r.Steam.Price.Value.Final.Value != 10300 || r.Steam.Price.Value.Currency.Value != "CNY" {
		t.Fatalf("price: %+v", r.Steam.Price)
	}
	if got := r.Steam.Platforms.Value; len(got) != 2 || got[0] != "windows" || got[1] != "linux" {
		t.Fatalf("platforms: %v", got)
	}
	if r.Author.Name.Reason != "not_applicable" || r.Media.Value[0].URL.Value == "" {
		t.Fatalf("author or media: %+v %+v", r.Author, r.Media)
	}
}

func TestSteamSharedfileReplay(t *testing.T) {
	const body = "itemcount=1&publishedfileids%5B0%5D=900005"
	f := steamFetcher(t, func(req *http.Request) {
		if req.Method != http.MethodPost || req.URL.String() != "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/" {
			t.Fatalf("want POST to GetPublishedFileDetails; got %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("content type: %q", req.Header.Get("Content-Type"))
		}
		b, _ := io.ReadAll(req.Body)
		if string(b) != body {
			t.Fatalf("form body %q; want %q", b, body)
		}
	}, 200, fixture(t, "steam-sharedfile-900005"))
	r, err := f.Fetch(context.Background(), link(t, "https://steamcommunity.com/workshop/filedetails/?id=900005"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "sharedfile" || r.Title.Value != "编造的工坊模组" || r.Author.ID.Value != "76561190000000002" {
		t.Fatalf("identity: %+v author %+v", r, r.Author)
	}
	// 说明是 BBCode：发出去的是纯文字
	if strings.Contains(r.Text.Value, "[h1]") || strings.Contains(r.Text.Value, "[img]") || strings.Contains(r.Text.Value, "[/") {
		t.Fatalf("bbcode left: %.200q", r.Text.Value)
	}
	if r.Author.Name.State != "not_returned" || r.Author.Name.Reason != "needs_profile_lookup" {
		t.Fatalf("author name must not be guessed: %+v", r.Author.Name)
	}
	if r.Stats["subscriptions"].Value != 9187 || r.Stats["favorited"].Value != 1531 || r.Stats["views"].Value != 45288 {
		t.Fatalf("stats: %+v", r.Stats)
	}
	if r.Steam.FileSize.Value != 89728751 || r.Steam.ConsumerAppID.Value != "900006" || r.Published.Value.Unix() != 1790209670 {
		t.Fatalf("steam fields: %+v published %v", r.Steam, r.Published)
	}
	// time_created 与 time_updated 不同：发布时间和更新时间不能互换。
	if r.Steam.Updated.Value.Unix() != 1793578268 {
		t.Fatalf("updated must come from time_updated: %v", r.Steam.Updated)
	}
	if r.Flags["text_format"].Value != "bbcode" || !r.Text.Truncated || len([]rune(r.Text.Value)) != 800 {
		t.Fatalf("description: flags %+v text %d truncated=%v", r.Flags["text_format"], len([]rune(r.Text.Value)), r.Text.Truncated)
	}
	// 夹具里 Steam 自带的 english、japanese、koreana、schinese 是语言标记，不是作者的标签；mod、skin 是作者写的，要留下。
	if got := strings.Join(r.Tags.Value, "、"); got != "编造标签甲、编造标签乙、mod、skin" {
		t.Fatalf("tags: %v", got)
	}
	if r.Media.State != "available" || r.Media.Value[0].URL.State != "available" {
		t.Fatalf("preview media: %+v", r.Media)
	}
}

func TestSteamProfileReplay(t *testing.T) {
	for _, c := range []struct{ link, want string }{
		{"https://steamcommunity.com/id/Example_Player/", "https://steamcommunity.com/id/example_player/?xml=1"},
		{"https://steamcommunity.com/profiles/76561190000000001", "https://steamcommunity.com/profiles/76561190000000001/?xml=1"},
	} {
		t.Run(c.link, func(t *testing.T) {
			f := steamFetcher(t, steamGET(t, c.want), 200, steamFixtureXML(t, "steam-profile-private"))
			r, err := f.Fetch(context.Background(), link(t, c.link))
			if err != nil {
				t.Fatal(err)
			}
			if r.Kind != "profile" || r.Title.Value != "编造玩家" || r.Author.ID.Value != "76561190000000001" || r.Author.Name.Value != "编造玩家" {
				t.Fatalf("profile: %+v", r)
			}
			if r.Author.Account.State != "not_returned" || r.Author.Avatar.State != "available" {
				t.Fatalf("account or avatar: %+v %+v", r.Author.Account, r.Author.Avatar)
			}
			if r.Flags["privacy_state"].Value != "friendsonly" || r.Flags["visibility_state"].Value != "1" {
				t.Fatalf("flags: %+v", r.Flags)
			}
		})
	}
	t.Run("profiles_identity_mismatch", func(t *testing.T) {
		f := steamFetcher(t, nil, 200, steamFixtureXML(t, "steam-profile-private"))
		_, err := f.Fetch(context.Background(), link(t, "https://steamcommunity.com/profiles/76561190000000002"))
		wantError(t, err, "identity_mismatch")
	})
}

func TestSteamFailureShapes(t *testing.T) {
	app := "https://store.steampowered.com/app/900001"
	pkg := "https://store.steampowered.com/sub/900004"
	work := "https://steamcommunity.com/sharedfiles/filedetails/?id=900005"
	profile := "https://steamcommunity.com/profiles/76561190000000001"
	workBody := fixture(t, "steam-sharedfile-900005")
	for _, c := range []struct {
		name, url string
		status    int
		body      string
		kind      string
	}{
		{"app_404", app, 404, "", "not_found"},
		{"app_410", app, 410, "", "not_found"},
		{"app_429", app, 429, "", "rate_limited"},
		{"app_redirect_agecheck", app, 302, "", "not_returned"},
		{"app_malformed_json", app, 200, "{", "invalid_response"},
		{"app_success_false", app, 200, `{"900001":{"success":false}}`, "not_returned"},
		// 地区限制时 data 可能是 []：success:false 仍要归为 not_returned，不能因 data 形状整段判成结构错误。
		{"app_success_false_array_data", app, 200, `{"900001":{"success":false,"data":[]}}`, "not_returned"},
		{"package_success_false_array_data", pkg, 200, `{"900004":{"success":false,"data":[]}}`, "not_returned"},
		{"app_missing_key", app, 200, `{"999":{"success":true,"data":{}}}`, "invalid_response"},
		{"app_identity", app, 200, steamMutate(t, "steam-app-900001", func(doc map[string]any) { steamData(doc, "900001")["steam_appid"] = 900002 }), "identity_mismatch"},
		{"app_bad_required_age", app, 200, steamMutate(t, "steam-app-900001", func(doc map[string]any) { steamData(doc, "900001")["required_age"] = map[string]any{} }), "invalid_response"},
		{"package_success_false", pkg, 200, `{"900004":{"success":false}}`, "not_returned"},
		{"package_429", pkg, 429, "", "rate_limited"},
		{"work_404", work, 404, "", "not_found"},
		{"work_result_9", work, 200, steamMutate(t, "steam-sharedfile-900005", func(doc map[string]any) {
			doc["response"].(map[string]any)["publishedfiledetails"].([]any)[0].(map[string]any)["result"] = 9
		}), "not_returned"},
		{"work_outer_result_9", work, 200, `{"response":{"result":9,"resultcount":0,"publishedfiledetails":[]}}`, "not_returned"},
		{"work_banned", work, 200, steamMutate(t, "steam-sharedfile-900005", func(doc map[string]any) {
			doc["response"].(map[string]any)["publishedfiledetails"].([]any)[0].(map[string]any)["banned"] = 1
		}), "not_returned"},
		{"work_identity", work, 200, steamMutate(t, "steam-sharedfile-900005", func(doc map[string]any) {
			doc["response"].(map[string]any)["publishedfiledetails"].([]any)[0].(map[string]any)["publishedfileid"] = "9"
		}), "identity_mismatch"},
		{"work_bad_size", work, 200, steamMutate(t, "steam-sharedfile-900005", func(doc map[string]any) {
			doc["response"].(map[string]any)["publishedfiledetails"].([]any)[0].(map[string]any)["file_size"] = "12MB"
		}), "invalid_response"},
		{"work_malformed", work, 200, workBody[:len(workBody)/2], "invalid_response"},
		{"profile_error_body", profile, 200, `<?xml version="1.0"?><response><error><![CDATA[The specified profile could not be found.]]></error></response>`, "not_found"},
		{"profile_html", profile, 200, "<html>challenge</html>", "invalid_response"},
		{"profile_unterminated", profile, 200, "<profile><steamID64>76561190000000001", "invalid_response"},
		{"profile_bad_id64", profile, 200, "<profile><steamID64>1</steamID64><steamID>x</steamID></profile>", "invalid_response"},
		{"profile_429", profile, 429, "", "rate_limited"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := steamFetcher(t, nil, c.status, c.body)
			_, err := f.Fetch(context.Background(), link(t, c.url))
			wantError(t, err, c.kind)
		})
	}
}

func TestSteamZeroMediaAndLongText(t *testing.T) {
	body := steamMutate(t, "steam-app-900001", func(doc map[string]any) {
		d := steamData(doc, "900001")
		delete(d, "header_image")
		delete(d, "screenshots")
		delete(d, "movies")
		delete(d, "price_overview")
		d["short_description"] = strings.Repeat("长", 1000)
	})
	f := steamFetcher(t, nil, 200, body)
	r, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/app/900001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Media.State != "not_returned" || len(r.Blocks) != 1 || r.Blocks[0].Type != "text" {
		t.Fatalf("zero media should leave media missing and only the text block: media=%+v blocks=%d", r.Media, len(r.Blocks))
	}
	if r.Steam.Price.State != "not_returned" || r.Stats["price_final"].State != "" {
		t.Fatalf("price missing: %+v", r.Steam.Price)
	}
	if !r.Text.Truncated || len([]rune(r.Text.Value)) != 800 {
		t.Fatalf("long text: truncated=%v runes=%d", r.Text.Truncated, len([]rune(r.Text.Value)))
	}
}

func TestSteamAppLinkFetchesLikeOtherPlatforms(t *testing.T) {
	l, ok := SteamAppLink("900001")
	if !ok {
		t.Fatal("app link")
	}
	f := steamFetcher(t, steamGET(t, "https://store.steampowered.com/api/appdetails?appids=900001&cc=cn&l=schinese"), 200, fixture(t, "steam-app-900001"))
	r, err := f.Fetch(context.Background(), l)
	if err != nil || r.ID != "900001" || r.Steam == nil {
		t.Fatalf("app link fetch: %+v %v", r, err)
	}
}

func TestSteamRequestPlatformAndHosts(t *testing.T) {
	for raw, want := range map[string]string{
		"https://store.steampowered.com/api/appdetails?appids=1": "steam",
		"https://api.steampowered.com/ISteamRemoteStorage/":      "steam",
		"https://steamcommunity.com/id/x/?xml=1":                 "steam",
		"https://steampowered.com.evil.example/":                 "web@steampowered.com.evil.example",
	} {
		if got := requestPlatform(raw); got != want {
			t.Fatalf("%s: platform %q want %q", raw, got, want)
		}
	}
	if !moreMediaHost("shared.akamai.steamstatic.com") || !moreMediaHost("images.steamusercontent.com") || moreMediaHost("steamstatic.com.evil.example") {
		t.Fatal("steam media hosts")
	}
	if !fetchableMore(Link{Platform: "steam", Kind: "sharedfile"}) || fetchableMore(Link{Platform: "steam", Kind: "reviews"}) {
		t.Fatal("steam fetchable kinds")
	}
}

// 限速的平台桶要分开：Steam 的 429 冷却不能挡住 GitHub。
func TestSteamCooldownDoesNotLeakToOtherPlatforms(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	f := steamFetcher(t, nil, 429, "")
	f.rate.options.Now = func() time.Time { return now }
	_, err := f.Fetch(context.Background(), link(t, "https://store.steampowered.com/app/900001"))
	wantError(t, err, "rate_limited")
	if err := f.rate.allowHost("steam", "store.steampowered.com", true); err == nil {
		t.Fatal("steam cooldown should block the host that answered 429")
	}
	if err := f.rate.allow("github", true); err != nil {
		t.Fatal("steam cooldown leaked to github", err)
	}
}

// 回归：社区回 429 只冷却社区，商店和接口照常取；反过来也一样（样张里 ST-07 的 429 挡住了小黑盒查游戏名）。
func TestSteamCooldownIsPerHost(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	f := steamFetcher(t, nil, 429, "")
	f.rate.options.Now = func() time.Time { return now }
	_, err := f.Fetch(context.Background(), link(t, "https://steamcommunity.com/id/example/"))
	wantError(t, err, "rate_limited")
	if err := f.rate.allowHost("steam", "steamcommunity.com", true); err == nil {
		t.Fatal("社区应该在冷却")
	}
	for _, host := range []string{"store.steampowered.com", "api.steampowered.com"} {
		if err := f.rate.allowHost("steam", host, true); err != nil {
			t.Fatalf("%s 不该被社区的 429 挡住：%v", host, err)
		}
	}
}
