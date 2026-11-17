package linkreader

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 固定样本由真实匿名响应剪裁（只留消费的字段），appid 改成编造值；其余形状是下面内联的合成响应，标注在用例里。
func TestCNHeyboxGameReplay(t *testing.T) {
	// 游戏名照同一编号问 Steam 商店：第一条 Steam 给了名字，第二条 Steam 回 success:false，标题记“没取到”，不拿简介冒充
	for _, c := range []struct{ name, raw, id, body, developer, steam, title string }{
		{"game", "https://www.xiaoheihe.cn/app/topic/game/pc/90001", "90001", fixture(t, "cn-heybox-game"), "编造开发组甲",
			`{"90001":{"success":true,"data":{"type":"game","name":"编的放松时光","steam_appid":90001}}}`, "编的放松时光"},
		{"coming_soon", "https://www.xiaoheihe.cn/app/topic/game/pc/90002", "90002", fixture(t, "cn-heybox-soon"), "编造开发组乙",
			`{"90002":{"success":false}}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var seen string
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
					t.Fatal("non-anonymous")
				}
				if req.URL.Hostname() == "store.steampowered.com" {
					if req.URL.Query().Get("appids") != c.id {
						t.Errorf("steam appids: %s", req.URL.RawQuery)
					}
					return response(200, c.steam), nil
				}
				seen = req.URL.String()
				return response(200, c.body), nil
			})
			r, err := f.Fetch(context.Background(), link(t, c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if seen != "https://api.xiaoheihe.cn/game/game_introduction/?steam_appid="+c.id+"&return_json=1" || f.Requests() != 2 {
				t.Fatalf("endpoint %s requests=%d", seen, f.Requests())
			}
			if (c.title == "" && r.Title.State != "not_returned") || r.Title.Value != c.title || r.Text.State != "available" || r.Text.Value == "" {
				t.Fatalf("title/text: %+v", r.Title)
			}
			if r.Author.Name.Value != c.developer || r.Author.ID.State != "not_returned" || r.Category.Value != "pc" {
				t.Fatalf("author/category: %+v %+v", r.Author, r.Category)
			}
			if r.Flags["steam_appid"].Value != c.id || r.Media.State != "available" || len(r.Media.Value) != 0 {
				t.Fatalf("flags/media: %+v %+v", r.Flags, r.Media)
			}
			if c.name == "game" {
				if r.Published.Value.Format(time.RFC3339) != "2026-03-03T16:00:00Z" || r.PublishedText.Value != "2026-03-04" {
					t.Fatalf("published: %v %+v", r.Published.Value, r.PublishedText)
				}
				// 作者用的 Markdown 粗体（**《名字》**）也去掉，不把星号发到群里
				if strings.Contains(r.Text.Value, "<") || strings.Contains(r.Text.Value, "&nbsp;") || strings.Contains(r.Text.Value, "**") {
					t.Fatalf("html left in text: %.80s", r.Text.Value)
				}
			} else {
				if r.Published.State != "not_returned" || r.PublishedText.Value != "即将推出" {
					t.Fatalf("coming soon: %+v %+v", r.Published, r.PublishedText)
				}
			}
		})
	}
}

// 合成：超长简介必须截断并标记，不能冒充完整正文。
func TestCNHeyboxLongText(t *testing.T) {
	long := "<p>" + strings.Repeat("长", 3000) + "</p>"
	body, _ := json.Marshal(map[string]any{"status": "ok", "result": map[string]any{"steam_appid": 90001, "developers": []map[string]string{{"value": "编造作者"}}, "about_the_game": long, "game_type": "pc"}})
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, string(body)), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Text.Truncated || utf8.RuneCountInString(r.Text.Value) != 800 {
		t.Fatalf("truncation: truncated=%v runes=%d", r.Text.Truncated, utf8.RuneCountInString(r.Text.Value))
	}
	if r.Published.State != "not_returned" || r.PublishedText.State != "not_returned" {
		t.Fatalf("missing release date must not be zero: %+v %+v", r.Published, r.PublishedText)
	}
}

// 合成：缺开发商、缺发行日期时只留下能确认的字段，不编造作者或时间。
func TestCNHeyboxOptionalFieldsMissing(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":{"steam_appid":90001,"about_the_game":"<p>只有简介</p>"}}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Author.Name.State != "not_returned" || r.Published.State != "not_returned" || r.PublishedText.State != "not_returned" || r.Text.Value != "只有简介" {
		t.Fatalf("optional fields: %+v %+v %+v %q", r.Author.Name, r.Published, r.PublishedText, r.Text.Value)
	}
}

// 合成：段落和换行标签去掉标签后要保留分隔，不能把相邻段落粘成一句。
func TestCNHeyboxParagraphBreaks(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":{"steam_appid":90001,"about_the_game":"<p>第一段</p><p>第二段<br>第三段</p>"}}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Text.Value != "第一段\n第二段\n第三段" {
		t.Fatalf("paragraph breaks lost: %q", r.Text.Value)
	}
}

// 合成：不存在的游戏 ID 在实测中返回 ok、只有 steam_appid 与空简介，必须报 not_returned，不当成功。
func TestCNHeyboxEmptyShell(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","msg":"","version":"1.0","result":{"steam_appid":1,"about_the_game":""}}`), nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/1"))
	wantError(t, err, "not_returned")
}

// 合成：release_date 为空串时与缺失一样，不能当成"有发行日期"的成功结果。
func TestCNHeyboxEmptyReleaseDate(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":{"steam_appid":90001,"developers":[],"about_the_game":"","release_date":""}}`), nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	wantError(t, err, "not_returned")

	f = testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":{"steam_appid":90001,"about_the_game":"<p>只有简介</p>","release_date":"  "}}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.PublishedText.State != "not_returned" || r.Published.State != "not_returned" {
		t.Fatalf("blank release date must be missing: %+v %+v", r.PublishedText, r.Published)
	}
}

func TestCNHeyboxFailureShapes(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		header http.Header
		body   string
		kind   string
	}{
		{"http_404", 404, nil, "", "not_found"},
		{"http_410", 410, nil, "", "not_found"},
		{"http_403", 403, nil, "", "login_required"},
		{"http_429", 429, http.Header{"Retry-After": {"30"}}, "", "rate_limited"},
		{"show_captcha", 200, nil, `{"status":"show_captcha","result":[]}`, "rate_limited"},
		{"lack_token", 200, nil, `{"status":"lack_token","result":[]}`, "login_required"},
		{"other_status", 200, nil, `{"status":"maintenance"}`, "interface_error"},
		{"malformed", 200, nil, `{"status":"ok",`, "invalid_response"},
		{"missing_status", 200, nil, `{"result":{"steam_appid":90001}}`, "invalid_response"},
		{"identity", 200, nil, `{"status":"ok","result":{"steam_appid":90009,"about_the_game":"x"}}`, "identity_mismatch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				resp := response(c.status, c.body)
				for k, v := range c.header {
					resp.Header[k] = v
				}
				return resp, nil
			})
			_, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
			wantError(t, err, c.kind)
		})
	}
}

// show_captcha 后同一平台进入冷却：第二次不再发请求。
func TestCNHeyboxCaptchaCooldown(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"show_captcha","result":[]}`), nil
	})
	l := link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001")
	_, first := f.Fetch(context.Background(), l)
	wantError(t, first, "rate_limited")
	_, second := f.Fetch(context.Background(), l)
	wantError(t, second, "rate_limited")
	if f.Requests() != 1 {
		t.Fatalf("cooldown must stop the second request; requests=%d", f.Requests())
	}
}

// 合成：开发商与发行商不同，作者只能取开发商。
func TestCNHeyboxAuthorIsDeveloper(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":{"steam_appid":90001,"developers":[{"value":"甲开发"}],"publishers":[{"value":"乙发行"}],"about_the_game":"<p>简介</p>"}}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Author.Name.Value != "甲开发" {
		t.Fatalf("author must be the developer, got %+v", r.Author.Name)
	}
}

// 429 后同一平台进入冷却：第二次不再发请求。
func TestCNHeyboxRateLimitCooldown(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		resp := response(429, "")
		resp.Header.Set("Retry-After", "60")
		return resp, nil
	})
	l := link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001")
	_, first := f.Fetch(context.Background(), l)
	wantError(t, first, "rate_limited")
	_, second := f.Fetch(context.Background(), l)
	wantError(t, second, "rate_limited")
	if f.Requests() != 1 {
		t.Fatalf("429 cooldown must stop the second request; requests=%d", f.Requests())
	}
}

// 帖子、用户页、小红书笔记和用户页都不发请求，直接返回明确状态。
func TestCNUnsupportedShapesNoRequest(t *testing.T) {
	for _, c := range []struct{ raw, kind string }{
		{"https://www.xiaoheihe.cn/app/bbs/link/90002", "unsupported"},
		{"https://www.xiaoheihe.cn/app/user/profile/90003", "unsupported"},
		{"https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake", "login_required"},
		{"https://www.xiaohongshu.com/user/profile/6a1b2c3d4e5f6a1b2c3d4e5f", "login_required"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				t.Fatal("unsupported shape must not send a request")
				return nil, nil
			})
			_, err := f.Fetch(context.Background(), link(t, c.raw))
			wantError(t, err, c.kind)
			if f.Requests() != 0 {
				t.Fatalf("requests=%d", f.Requests())
			}
		})
	}
}

// 小红书短链：只跟随一次重定向；终点是笔记时不请求终点页，返回需要登录。
func TestCNXHSShortLinkStopsAtLogin(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "xhslink.com" {
			t.Fatalf("unexpected host %s", req.URL.Host)
		}
		// 小红书请求不带 B 站的 Referer，否则是跨站的无关来源。
		if ref := req.Header.Get("Referer"); ref != "" {
			t.Fatalf("xiaohongshu request carries referer %q", ref)
		}
		resp := response(302, "")
		resp.Header.Set("Location", "https://www.xiaohongshu.com/discovery/item/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake&xsec_source=pc_share&share_id=x")
		return resp, nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://xhslink.com/o/AbC123"))
	wantError(t, err, "login_required")
	if calls != 1 || f.Requests() != 1 {
		t.Fatalf("calls=%d requests=%d; want the redirect only", calls, f.Requests())
	}
}

// 短链终点是小红书笔记时直接返回笔记链接，不请求终点页。
func TestCNXHSShortLinkResolveNote(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		resp := response(302, "")
		resp.Header.Set("Location", "https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake")
		return resp, nil
	})
	got, err := f.Resolve(context.Background(), link(t, "https://xhslink.cn/o/AbC123"))
	if err != nil || got.Clean != "https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake" || f.Requests() != 1 {
		t.Fatalf("resolve: %+v %v requests=%d", got, err, f.Requests())
	}
}

// 短链终点是小黑盒帖子：交给 unsupported，不再请求终点页。
func TestCNXHSShortLinkResolveHeyboxPost(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		resp := response(302, "")
		resp.Header.Set("Location", "https://www.xiaoheihe.cn/app/bbs/link/90002")
		return resp, nil
	})
	_, err := f.Resolve(context.Background(), link(t, "https://xhslink.com/o/AbC123"))
	wantError(t, err, "unsupported")
	if f.Requests() != 1 {
		t.Fatalf("only the redirect may be requested; requests=%d", f.Requests())
	}
}

// 个别字段类型不对（开发商给成字符串、发行日期给成数字、类别给成对象）只让那一项缺失，
// 简介等其余内容照出，缺的如实写原因；不整页失败，也不把坏字段当成空值或 0。
func TestCNHeyboxBadFieldTypeKeepsPage(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":{"steam_appid":90001,"developers":"某开发商","publishers":[{"value":7}],"release_date":20251116,"game_type":{"x":1},"about_the_game":"<p>只有简介</p>"}}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	if err != nil {
		t.Fatalf("单个字段类型不对不该整页失败：%v", err)
	}
	if r.Text.Value != "只有简介" || r.Author.Name.State == "available" || r.Published.State == "available" || r.PublishedText.State == "available" || r.Category.State == "available" {
		t.Fatalf("坏字段应当缺失、好字段保留：%+v %+v %+v %+v %q", r.Author.Name, r.Published, r.PublishedText, r.Category, r.Text.Value)
	}
}

// 语法错误才算响应坏了；result 整个类型不对（数组）按 invalid_response。
func TestCNHeyboxResultWrongShape(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"status":"ok","result":[1,2]}`), nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://www.xiaoheihe.cn/app/topic/game/pc/90001"))
	wantError(t, err, "invalid_response")
}
