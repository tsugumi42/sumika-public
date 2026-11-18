package linkreader

import (
	"context"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 小黑盒游戏页只取匿名 game_introduction；其余形状不发请求，直接返回明确状态（规格 4.15、4.16）。
var (
	heyboxTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	heyboxBlankRu = regexp.MustCompile(`\n{3,}`)
	heyboxBreak   = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>`)
	cstZone       = time.FixedZone("CST", 8*3600)
)

type heyboxValue struct {
	Value lv[string] `json:"value"`
}

// 状态和结果分两步解码：验证码、令牌等非 ok 响应的 result 可能是数组，不能先按对象解。
type heyboxEnvelope struct {
	Status *string         `json:"status"`
	Result json.RawMessage `json:"result"`
}
type heyboxIntro struct {
	SteamAppID  json.RawMessage   `json:"steam_appid"`
	Developers  lv[[]heyboxValue] `json:"developers"`
	Publishers  lv[[]heyboxValue] `json:"publishers"`
	ReleaseDate lv[string]        `json:"release_date"`
	GameType    lv[string]        `json:"game_type"`
	About       lv[string]        `json:"about_the_game"`
}

func (f *Fetcher) fetchCN(ctx context.Context, l Link) (Result, error) {
	if l.Platform == "xiaoheihe" && l.Kind == "game" {
		return f.fetchHeyboxGame(ctx, l)
	}
	if l.Platform == "xiaohongshu" {
		// 笔记和用户页都需要登录；不请求页面，也不尝试 SSR 状态。
		return Result{}, &FetchError{Kind: "login_required", Source: "xiaohongshu", Reason: "note_requires_login"}
	}
	// 帖子和用户页需要 hkey 签名与设备验证，匿名会触发验证码；不做签名。
	return Result{}, &FetchError{Kind: "unsupported", Source: "xiaoheihe", Reason: "signed_device_verification"}
}

func (f *Fetcher) fetchHeyboxGame(ctx context.Context, l Link) (Result, error) {
	const source = "xiaoheihe_intro"
	endpoint := "https://api.xiaoheihe.cn/game/game_introduction/?steam_appid=" + url.QueryEscape(l.ID) + "&return_json=1"
	b, status, h, err := f.request(ctx, endpoint, source)
	if err != nil {
		return Result{}, err
	}
	if err := httpFailure(status, h, source); err != nil {
		return Result{}, err
	}
	var env heyboxEnvelope
	if json.Unmarshal(b, &env) != nil || env.Status == nil {
		return Result{}, failure("invalid_response", source)
	}
	switch *env.Status {
	case "ok":
	case "show_captcha":
		f.rate.cooldown("xiaoheihe", "")
		return Result{}, &FetchError{Kind: "rate_limited", Source: source, Reason: "show_captcha"}
	case "lack_token":
		return Result{}, &FetchError{Kind: "login_required", Source: source, Reason: "lack_token"}
	default:
		return Result{}, &FetchError{Kind: "interface_error", Source: source, Reason: *env.Status}
	}
	raw := strings.TrimSpace(string(env.Result))
	if raw == "" || raw == "null" {
		return Result{}, failure("not_returned", source)
	}
	var p heyboxIntro
	if json.Unmarshal(env.Result, &p) != nil {
		return Result{}, failure("invalid_response", source)
	}
	steam := strings.Trim(string(p.SteamAppID), `"`)
	if steam == "" || steam == "null" {
		return Result{}, failure("invalid_response", source)
	}
	if steam != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	about := ""
	if p.About.V != nil {
		about = heyboxPlain(*p.About.V)
	}
	developer := ""
	if p.Developers.V != nil && len(*p.Developers.V) > 0 && (*p.Developers.V)[0].Value.V != nil {
		developer = strings.TrimSpace(*(*p.Developers.V)[0].Value.V)
	}
	// 发行日期为空白串与缺失同样处理，不能当成"有值"。
	release := ""
	if p.ReleaseDate.V != nil {
		release = strings.TrimSpace(*p.ReleaseDate.V)
	}
	// 空壳响应（不存在的游戏 ID 实测返回 ok 但只有 steam_appid 和空简介）不算成功，也不进缓存。
	if about == "" && developer == "" && release == "" {
		return Result{}, failure("not_returned", source)
	}
	r := newResult(l, source)
	r.Title, r.SkipCache = f.heyboxGameName(ctx, l.ID)
	if about != "" {
		r.Text = previewField(&about)
	} else {
		r.Text = missing[string]("not_returned")
	}
	if developer != "" {
		r.Author.Name = available(developer)
	}
	r.Author.ID = missing[string]("not_returned")
	r.Category = stringField(p.GameType.V)
	switch {
	case p.ReleaseDate.V == nil:
		r.PublishedText = missing[string]("interface_omitted")
	case release == "":
		r.PublishedText = missing[string]("not_returned")
	default:
		r.PublishedText = available(isoDateText(*p.ReleaseDate.V))
	}
	if release != "" {
		// "即将推出" 等非日期文字不猜；只在完整日期格式时填 Published。
		if t, err := time.ParseInLocation("2006年1月2日", release, cstZone); err == nil {
			r.Published = available(t.UTC())
		} else {
			r.Published = missing[time.Time]("not_returned")
		}
	}
	// Steam 数据留给 steam 分支：同一 appid 另行附加，这里只记录身份。
	r.Flags["steam_appid"] = available(jsonValue(l.ID))
	r.Media = available([]Media{})
	appendBody(&r)
	return r, nil
}

// heyboxPlain 去掉简介里的 HTML 标签并反转义实体，作者用的 Markdown 粗体、链接也换成纯文字。
func heyboxPlain(s string) string {
	// 段落和换行标签先换成换行，再去标签，避免相邻段落粘在一起。
	s = heyboxBreak.ReplaceAllString(s, "\n")
	s = heyboxTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = heyboxBlankRu.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(markdownPlain(s))
}
