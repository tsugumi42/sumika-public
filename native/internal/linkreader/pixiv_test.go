package linkreader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPixivNovelRecognition(t *testing.T) {
	raw := "https://pixiv.net/en/novel/show.php?id=90010&utm_source=qq&x=keep#top"
	l := link(t, raw)
	if l.Kind != "novel" || l.ID != "90010" || l.Clean != "https://www.pixiv.net/novel/show.php?id=90010&x=keep#top" {
		t.Fatalf("novel: %+v", l)
	}
	again := link(t, l.Clean)
	if again.Clean != l.Clean || !fetchableMore(l) {
		t.Fatalf("unstable novel: %+v", again)
	}
	if got := Extract("小说 " + raw); len(got) != 1 || got[0].ID != l.ID {
		t.Fatalf("extract: %+v", got)
	}
	cards, _ := FromCard(`{"meta":{"news":{"jumpUrl":"` + raw + `"}}}`)
	if len(cards) != 1 || cards[0].Kind != "novel" {
		t.Fatalf("card: %+v", cards)
	}
	for _, query := range []string{"", "id=", "id=0", "id=-1", "id=90010&id=90011", "id=90010&id=90010", "id=%zz"} {
		if got, ok := Recognize("https://www.pixiv.net/novel/show.php?" + query); ok {
			t.Fatalf("accepted %s: %+v", query, got)
		}
	}
}

func TestPixivFlagUnknowns(t *testing.T) {
	for _, kind := range []string{"artwork", "novel"} {
		for _, ai := range []string{"null", "0", "1", "2", "9", `false`} {
			for _, restrict := range []string{"null", "0", "1", "2", "9"} {
				t.Run(kind+"/"+ai+"/"+restrict, func(t *testing.T) {
					source := "more-pixiv-one"
					target := "https://www.pixiv.net/artworks/100000000"
					if kind == "novel" {
						source = "pixiv-novel"
						target = "https://www.pixiv.net/novel/show.php?id=90010"
					}
					var body map[string]any
					if err := json.Unmarshal([]byte(fixture(t, source)), &body); err != nil {
						t.Fatal(err)
					}
					work := body["body"].(map[string]any)
					work["aiType"] = json.RawMessage(ai)
					work["xRestrict"] = json.RawMessage(restrict)
					b, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, string(b)), nil })
					r, err := f.Fetch(context.Background(), link(t, target))
					if ai == "false" {
						wantError(t, err, "invalid_response")
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					flag := r.Flags["ai_generated"]
					if ai == "1" || ai == "2" {
						if flag.State != "available" || flag.Value != (ai == "2") {
							t.Fatalf("AI: %+v", flag)
						}
					} else if flag.State != "not_returned" {
						t.Fatalf("unknown AI became known: %+v", flag)
					}
					age := r.Flags["age_rating"]
					want := map[string]string{"0": "all_ages", "1": "R18", "2": "R18-G"}[restrict]
					if want == "" {
						if age.State != "not_returned" || r.Flags["possibly_sensitive"].State != "not_returned" {
							t.Fatalf("unknown age: %+v", r.Flags)
						}
					} else if age.State != "available" || age.Value != want || r.Flags["possibly_sensitive"].Value != (restrict != "0") {
						t.Fatalf("age: %+v", r.Flags)
					}
				})
			}
		}
	}
}

func TestPixivOriginalPagesAndFallback(t *testing.T) {
	const count = 38
	var pages []any
	for i := range count {
		pages = append(pages, map[string]any{"urls": map[string]string{"original": fmt.Sprintf("https://i.pximg.net/img-original/sample_p%d.png", i), "regular": fmt.Sprintf("https://i.pximg.net/img-master/sample_p%d.jpg", i)}})
	}
	pageJSON, err := json.Marshal(map[string]any{"error": false, "body": pages})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(fixture(t, "more-pixiv-one"), `"pageCount": 1`, `"pageCount": 38`, 1)
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Referer") != "https://www.pixiv.net/" {
			t.Fatal("request headers")
		}
		if strings.HasSuffix(req.URL.Path, "/pages") {
			return response(200, string(pageJSON)), nil
		}
		return response(200, body), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/100000000"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Media.Value) != count || r.Media.Truncated {
		t.Fatalf("incomplete pages: %+v", r.Media)
	}
	for i, m := range r.Media.Value {
		if m.URL.Value != fmt.Sprintf("https://i.pximg.net/img-original/sample_p%d.png", i) || m.Fallback == nil || m.Fallback.URL.Value != fmt.Sprintf("https://i.pximg.net/img-master/sample_p%d.jpg", i) || m.Fallback.AboveBytes != 100*1024*1024 || m.Fallback.Reason != "original_too_large" {
			t.Fatalf("page %d: %+v", i, m)
		}
	}
	regular := "https://i.pximg.net/regular.jpg"
	for _, original := range []*string{nil, new(string)} {
		m := pixivImage(pixivURLs{Original: original, Regular: &regular}, nil, nil)
		if m.URL.Value != regular || m.URL.Reason != "original_not_returned_using_regular" {
			t.Fatalf("unmarked fallback: %+v", m)
		}
	}
	t.Run("single_original_from_pages", func(t *testing.T) {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(fixture(t, "more-pixiv-one")), &envelope); err != nil {
			t.Fatal(err)
		}
		envelope["body"].(map[string]any)["urls"].(map[string]any)["original"] = nil
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/pages") {
				return response(200, `{"error":false,"body":[{"urls":{"original":"https://i.pximg.net/recovered.png","regular":"https://i.pximg.net/regular.jpg"}}]}`), nil
			}
			return response(200, string(body)), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/100000000"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i.pximg.net/recovered.png" || r.Media.Truncated {
			t.Fatalf("lost recovered original: %+v", r.Media)
		}
	})
}

func TestPixivUgoiraFrames(t *testing.T) {
	for _, c := range []struct{ name, meta, wantZIP, wantFrames string }{
		{"complete", fixture(t, "pixiv-ugoira"), "available", "available"},
		{"business_denied", `{"error":true,"body":[]}`, "not_returned", "not_returned"},
		{"missing", `{"error":false,"body":{}}`, "not_returned", "not_returned"},
		{"empty", `{"error":false,"body":{"frames":[]}}`, "not_returned", "not_returned"},
		{"unsafe_file", strings.Replace(fixture(t, "pixiv-ugoira"), "000001.jpg", "../001.jpg", 1), "available", "not_returned"},
		{"root_path", strings.Replace(fixture(t, "pixiv-ugoira"), "000001.jpg", "/", 1), "available", "not_returned"},
		{"missing_delay", strings.Replace(fixture(t, "pixiv-ugoira"), `"delay":120`, `"delay":null`, 1), "available", "not_returned"},
		{"negative_delay", strings.Replace(fixture(t, "pixiv-ugoira"), `"delay":120`, `"delay":-1`, 1), "available", "not_returned"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/ugoira_meta") {
					return response(200, c.meta), nil
				}
				return response(200, strings.Replace(fixture(t, "more-pixiv-one"), `"illustType": 0`, `"illustType": 2`, 1)), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/100000000"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Ugoira == nil || r.Ugoira.ZIP.State != c.wantZIP || r.Ugoira.Frames.State != c.wantFrames || r.Ugoira.Referer != "https://www.pixiv.net/" {
				t.Fatalf("ugoira: %+v", r.Ugoira)
			}
			if c.name == "complete" {
				if !strings.HasSuffix(r.Ugoira.ZIP.Value, "_ugoira1920x1080.zip") || len(r.Ugoira.Frames.Value) != 3 || r.Ugoira.Frames.Value[1] != (UgoiraFrame{File: "000001.jpg", DelayMS: 120}) || r.Ugoira.Frames.Value[2].DelayMS != 0 {
					t.Fatalf("lost frames: %+v", r.Ugoira)
				}
			}
			if c.name == "business_denied" && r.Ugoira.Frames.Reason != "api_rejected_anonymous_request" {
				t.Fatalf("lost API denial reason: %+v", r.Ugoira)
			}
			if c.name == "unsafe_file" || c.name == "missing_delay" || c.name == "negative_delay" {
				if r.Ugoira.Frames.Reason != "invalid_response" {
					t.Fatalf("invalid frame: %+v", r.Ugoira)
				}
			}
		})
	}
}

func TestPixivNovelPreviewAndImages(t *testing.T) {
	body := fixture(t, "pixiv-novel")
	for _, long := range []bool{false, true} {
		input := body
		if long {
			input = strings.Replace(input, "清晨，街道还很安静。", strings.Repeat("花🌸", 500), 1)
		}
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/ajax/novel/90010" {
				t.Fatalf("unexpected request: %s", req.URL)
			}
			return response(200, input), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/novel/show.php?id=90010"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Novel == nil || r.Novel.CharacterCount.Value != 1580 || r.Novel.Description.Value != "第一行\n第二行 & 说明链接" || r.Title.Value != "编造的清晨散步" || r.Author.ID.Value != "90011" || r.Tags.Value[0] != "编造日常" {
			t.Fatalf("novel: %+v", r)
		}
		if r.Text.Truncated != long || (long && utf8.RuneCountInString(r.Text.Value) != 800) || (!long && !strings.HasPrefix(r.Text.Value, "清晨，街道")) {
			t.Fatalf("preview: %+v", r.Text)
		}
		if len(r.Media.Value) != 2 || !strings.HasSuffix(r.Media.Value[0].URL.Value, "/first.png") || !strings.HasSuffix(r.Media.Value[1].URL.Value, "/second.jpg") || r.Media.Truncated {
			t.Fatalf("illustrations: %+v", r.Media)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var restored Result
		if err = json.Unmarshal(b, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Novel.CharacterCount.Value != 1580 || restored.Media.Value[0].Fallback.URL.State != "available" || restored.Text.Truncated != long {
			t.Fatal("lost serialized metadata")
		}
	}
	for _, c := range []struct{ name, key, raw, wantError string }{
		{"wrong_id", "id", `"90099"`, "identity_mismatch"},
		{"negative_count", "characterCount", `-1`, "invalid_response"},
		{"wrong_count", "characterCount", `"many"`, "invalid_response"},
		{"missing_content", "content", `null`, ""},
		{"empty_content", "content", `""`, ""},
		{"zero_count", "characterCount", `0`, ""},
		{"missing_count", "characterCount", `null`, ""},
		{"missing_images", "textEmbeddedImages", `null`, ""},
		{"empty_images", "textEmbeddedImages", `{}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var envelope map[string]any
			if err := json.Unmarshal([]byte(body), &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["body"].(map[string]any)[c.key] = json.RawMessage(c.raw)
			b, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, string(b)), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/novel/show.php?id=90010"))
			if c.wantError != "" {
				wantError(t, err, c.wantError)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch c.name {
			case "missing_content":
				if r.Text.State != "not_returned" {
					t.Fatalf("content: %+v", r.Text)
				}
			case "empty_content":
				if r.Text.State != "available" || r.Text.Value != "" {
					t.Fatalf("content: %+v", r.Text)
				}
			case "zero_count":
				if r.Novel.CharacterCount.State != "available" || r.Novel.CharacterCount.Value != 0 {
					t.Fatalf("count: %+v", r.Novel)
				}
			case "missing_count":
				if r.Novel.CharacterCount.State != "not_returned" {
					t.Fatalf("count: %+v", r.Novel)
				}
			case "missing_images":
				if r.Media.State != "not_returned" {
					t.Fatalf("images: %+v", r.Media)
				}
			case "empty_images":
				if r.Media.State != "available" || len(r.Media.Value) != 0 || !r.Media.Truncated {
					t.Fatalf("missing referenced images: %+v", r.Media)
				}
			}
		})
	}
}

func TestPixivAnonymousRejections(t *testing.T) {
	for _, target := range []string{"https://www.pixiv.net/artworks/90010", "https://www.pixiv.net/novel/show.php?id=90010"} {
		for _, c := range []struct {
			body   string
			status int
			reason string
		}{{`{"error":true,"body":[]}`, 200, "api_rejected_anonymous_request"}, {`{"error":true,"body":null}`, 200, "api_rejected_anonymous_request"}, {`{}`, 403, "anonymous_access_denied"}} {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(c.status, c.body), nil })
			_, err := f.Fetch(context.Background(), link(t, target))
			wantError(t, err, "not_returned")
			fe := err.(*FetchError)
			if fe.Reason != c.reason || fe.Status != map[bool]int{true: 403, false: 0}[c.status == 403] {
				t.Fatalf("denial detail: %+v", fe)
			}
		}
	}
	for _, c := range []struct {
		kind, target string
		pages        int
	}{
		{"artwork", "https://www.pixiv.net/artworks/90020", 38},
		{"artwork", "https://www.pixiv.net/artworks/90020", 1},
		{"novel", "https://www.pixiv.net/novel/show.php?id=90020", 0},
	} {
		body := fixtureObject(t, "pixiv-restricted", c.kind)
		if c.kind == "artwork" {
			var envelope map[string]any
			if err := json.Unmarshal([]byte(body), &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["body"].(map[string]any)["pageCount"] = c.pages
			b, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			body = string(b)
		}
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/pages") {
				return response(200, `{"error":true,"body":[]}`), nil
			}
			return response(200, body), nil
		})
		r, err := f.Fetch(context.Background(), link(t, c.target))
		if err != nil {
			t.Fatal(err)
		}
		if r.Flags["age_rating"].Value != "R18" || r.Title.State != "available" || r.Media.State != "not_returned" {
			t.Fatalf("restricted metadata: %+v", r)
		}
		if c.kind == "artwork" && (r.Stats["pages"].Value != int64(c.pages) || r.Media.Reason != "api_rejected_anonymous_request" || r.Flags["ai_generated"].Value != true) {
			t.Fatalf("restricted artwork: %+v", r)
		}
		if c.kind == "novel" && (r.Novel.CharacterCount.Value != 16480 || r.Text.State != "not_returned") {
			t.Fatalf("metadata-only novel: %+v", r)
		}
	}
}

func TestPixivNewMediaGuards(t *testing.T) {
	for _, raw := range []string{"https://i.pximg.net.evil.example/a.zip", "http://127.0.0.1/a.zip", "https://user:password@i.pximg.net/a.zip"} {
		m := Media{URL: available("https://i.pximg.net/original.jpg"), Fallback: &MediaFallback{URL: available(raw)}}
		r := Result{Media: available([]Media{m}), Blocks: []Block{{Type: "image", Media: &m}}, Ugoira: &UgoiraInfo{ZIP: available(raw)}}
		(&Fetcher{}).inspectMedia(context.Background(), &r)
		if r.Media.Value[0].Fallback.URL.State != "failed" || m.Fallback.URL.Reason != "unsafe_target" || r.Ugoira.ZIP.State != "failed" {
			t.Fatalf("unsafe candidates: %+v %+v", r.Media, r.Ugoira)
		}
	}
}

func TestPixivProductionStagedPolicy(t *testing.T) {
	for _, kind := range []string{"artwork", "novel"} {
		for _, restriction := range []string{"null", "0", "1", "2", "9", `false`} {
			t.Run(kind+"/"+restriction, func(t *testing.T) {
				name, raw := "more-pixiv-one", "https://www.pixiv.net/artworks/100000000"
				if kind == "novel" {
					name, raw = "pixiv-novel", "https://www.pixiv.net/novel/show.php?id=90010"
				}
				var envelope map[string]any
				if err := json.Unmarshal([]byte(fixture(t, name)), &envelope); err != nil {
					t.Fatal(err)
				}
				envelope["body"].(map[string]any)["xRestrict"] = json.RawMessage(restriction)
				body, err := json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
					calls++
					if strings.HasSuffix(req.URL.Path, "/pages") || strings.HasSuffix(req.URL.Path, "/ugoira_meta") {
						t.Error("未获许可就请求正文资源", req.URL.Path)
					}
					return response(200, string(body)), nil
				})
				st, _ := cacheStores(t)
				s := cacheService(t, st, f, nil)
				checked := false
				r, err := s.GetPermitted(context.Background(), raw, func(r *Result) bool {
					checked = true
					if r.Stage != "metadata" || r.Text.State == "available" || r.Media.State == "available" || r.Ugoira != nil || r.Novel != nil {
						t.Errorf("元信息提前包含正文：%+v", r)
					}
					sensitive := r.Flags["possibly_sensitive"]
					return sensitive.State == "available" && sensitive.Value == false
				})
				if restriction == "false" {
					wantError(t, err, "invalid_response")
					if checked {
						t.Fatal("错类型进入政策回调")
					}
					return
				}
				if err != nil || !checked {
					t.Fatalf("生产分阶段接口未接通：%v", err)
				}
				cached, found, err := st.TraceStore().LinkResult(context.Background(), raw)
				if restriction != "0" {
					if calls != 1 || r.Stage != "metadata" || found || err != nil {
						t.Fatalf("受限取数或缓存越界：calls=%d stage=%s cached=%+v err=%v", calls, r.Stage, cached, err)
					}
				} else if err != nil || !found || r.Stage == "metadata" || (kind == "novel" && r.Text.State != "available") || (kind == "artwork" && r.Media.State != "available") {
					t.Fatalf("许可后丢失正文或成功缓存：%+v found=%v err=%v", r, found, err)
				}
			})
		}
	}
}
