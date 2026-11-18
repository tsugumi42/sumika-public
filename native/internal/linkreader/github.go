package linkreader

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

type githubUser struct {
	Login  *string   `json:"login"`
	ID     decimalID `json:"id"`
	Avatar *string   `json:"avatar_url"`
}
type githubObject struct {
	FullName    *string         `json:"full_name"`
	HTMLURL     string          `json:"html_url"`
	Private     *bool           `json:"private"`
	Public      *bool           `json:"public"`
	Title       *string         `json:"title"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Body        *string         `json:"body"`
	State       *string         `json:"state"`
	Language    *string         `json:"language"`
	Created     *string         `json:"created_at"`
	Published   *string         `json:"published_at"`
	Updated     *string         `json:"updated_at"`
	Stars       *int64          `json:"stargazers_count"`
	Forks       *int64          `json:"forks_count"`
	Comments    *int64          `json:"comments"`
	Owner       githubUser      `json:"owner"`
	User        githubUser      `json:"user"`
	Author      githubUser      `json:"author"`
	ID          json.RawMessage `json:"id"`
	Tag         *string         `json:"tag_name"`
	Draft       *bool           `json:"draft"`
	Prerelease  *bool           `json:"prerelease"`
	Merged      *bool           `json:"merged"`
	Files       map[string]struct {
		Filename  string  `json:"filename"`
		Content   *string `json:"content"`
		Truncated *bool   `json:"truncated"`
	} `json:"files"`
}

func (f *Fetcher) fetchGitHub(ctx context.Context, l Link) (Result, error) {
	const source = "github_rest"
	endpoint := ""
	parts := strings.SplitN(l.ID, "/", 3)
	if l.Kind == "gist" {
		endpoint = "/gists/" + l.ID
	} else {
		endpoint = "/repos/" + parts[0] + "/" + parts[1]
		switch l.Kind {
		case "issue":
			endpoint += "/issues/" + strings.TrimPrefix(parts[2], "issues/")
		case "pull_request":
			endpoint += "/pulls/" + strings.TrimPrefix(parts[2], "pull/")
		case "release":
			if parts[2] == "releases/latest" {
				endpoint += "/releases/latest"
			} else {
				endpoint += "/releases/tags/" + url.PathEscape(strings.TrimPrefix(parts[2], "releases/tag/"))
			}
		}
	}
	b, status, h, err := f.request(ctx, "https://api.github.com"+endpoint, source)
	if err != nil {
		return Result{}, err
	}
	if status == 403 && (h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != "") {
		f.rate.cooldown("github", h.Get("Retry-After"))
		return Result{}, &FetchError{Kind: "rate_limited", Source: source, Status: status, RetryAfter: h.Get("Retry-After")}
	}
	if err := httpFailure(status, h, source); err != nil {
		return Result{}, err
	}
	// Repository 的 id 是 JSON 数字，gist 的 id 是十六进制字符串；只在 gist 消费该字段。
	var obj githubObject
	if json.Unmarshal(b, &obj) != nil {
		return Result{}, failure("invalid_response", source)
	}
	identity, ok := Recognize(obj.HTMLURL)
	latest := l.Kind == "release" && strings.HasSuffix(l.ID, "/releases/latest")
	matches := identity.ID == l.ID
	if latest && obj.Tag != nil {
		matches = identity.ID == parts[0]+"/"+parts[1]+"/releases/tag/"+*obj.Tag
	}
	if !ok || identity.Platform != l.Platform || identity.Kind != l.Kind || !matches {
		return Result{}, failure("identity_mismatch", source)
	}
	r := newResult(l, source)
	r.GitHub = &GitHubInfo{State: stringField(obj.State), Language: stringField(obj.Language), Updated: missing[time.Time]("interface_omitted")}
	r.GitHub.Updated, err = timestampField(obj.Updated)
	if err != nil {
		return Result{}, err
	}
	r.Published, err = timestampField(obj.Created)
	if err != nil {
		return Result{}, err
	}
	user := obj.User
	switch l.Kind {
	case "repository":
		if obj.Private == nil || *obj.Private || obj.FullName == nil || !strings.EqualFold(*obj.FullName, l.ID) {
			return Result{}, failure("invalid_response", source)
		}
		r.Title, r.Text = stringField(obj.FullName), stringField(obj.Description)
		user = obj.Owner
		stat(&r, "stars", obj.Stars)
		stat(&r, "forks", obj.Forks)
		// 官方公开 Open Graph 预览地址；REST 不返回自定义社交头图，不能声称取到了自定义图。
		appendMedia(&r, imageMetadata(available("https://opengraph.githubassets.com/1/"+l.ID)))
		r.Flags["custom_social_image"] = missing[jsonValue]("rest_api_omitted")
	case "issue", "pull_request":
		r.Title, r.Text = stringField(obj.Title), previewField(obj.Body)
		stat(&r, "comments", obj.Comments)
		if l.Kind == "pull_request" {
			flag(&r, "merged", obj.Merged)
		}
	case "release":
		r.Title, r.Text = stringField(obj.Name), previewField(obj.Body)
		if r.Title.State != "available" || r.Title.Value == "" {
			r.Title = stringField(obj.Tag)
		}
		user = obj.Author
		r.Published, err = timestampField(obj.Published)
		if err != nil {
			return Result{}, err
		}
		flag(&r, "draft", obj.Draft)
		flag(&r, "prerelease", obj.Prerelease)
	case "gist":
		var id string
		if json.Unmarshal(obj.ID, &id) != nil || id != l.ID || obj.Public == nil || !*obj.Public {
			return Result{}, failure("invalid_response", source)
		}
		r.Title = stringField(obj.Description)
		user = obj.Owner
		first := ""
		for name := range obj.Files {
			if first == "" || name < first {
				first = name
			}
		}
		if first != "" {
			file := obj.Files[first]
			if r.Title.State != "available" || r.Title.Value == "" {
				r.Title = available(first)
			}
			r.Text = previewField(file.Content)
			if file.Truncated != nil && *file.Truncated {
				r.Text.Truncated = true
			}
		}
		stat(&r, "comments", obj.Comments)
		r.Stats["files"] = available(int64(len(obj.Files)))
	}
	if r.Title.State != "available" || r.Title.Value == "" {
		return Result{}, failure("invalid_response", source)
	}
	r.Author = Author{Name: stringField(user.Login), Account: stringField(user.Login), ID: missing[string]("interface_omitted"), Avatar: stringField(user.Avatar)}
	if user.ID != "" {
		r.Author.ID = available(string(user.ID))
	}
	appendBody(&r)
	return r, nil
}
