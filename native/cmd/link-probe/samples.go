package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 批量样张（ADR-290）：读一份测试链接清单，真实取数（只取数、不发送），把每个站点渲染后的
// 文字和图片按“要发出的顺序”写进本地目录，给人看格式。不连任何聊天平台，不碰真实 data/。
//
// 输出：
//
//	<out>/README.md              每个站点一行的汇总
//	<out>/<站点>.md              该站点所有样张：渲染后的文字、图片（相对路径）、取数失败的原因
//	<out>/<站点>/<序号>/draft.json、result.json   原样的段列表和取数结果（地址已去掉查询串）
//	<out>/<站点>/<序号>/image-N.<扩展名>        下载到的图片
//
// 样张里不写聊天内容；取不到的如实写原因；清单里个人账号相关的形状（小红书、小黑盒用户页）只写类别，不写链接。

var urlPattern = regexp.MustCompile(`https?://[^\s|<>"'）)]+`)

// sampleURLs 从 markdown 清单里按出现顺序取链接，按规范化后的 Clean 去重；认不出的形状只记域名。
func sampleURLs(markdown string) (links []linkreader.Link, unknown []string) {
	seen := map[string]bool{}
	for _, raw := range urlPattern.FindAllString(markdown, -1) {
		raw = strings.TrimRight(raw, ".,;`")
		l, ok := linkreader.Recognize(raw)
		if !ok {
			u, err := url.Parse(raw)
			host := "（地址无法解析）"
			if err == nil {
				host = u.Hostname()
			}
			if !seen["?"+host+shapeOf(raw)] {
				seen["?"+host+shapeOf(raw)] = true
				unknown = append(unknown, host+"　"+shapeOf(raw))
			}
			continue
		}
		if seen[l.Clean] {
			continue
		}
		seen[l.Clean] = true
		links = append(links, l)
	}
	return links, unknown
}

// shapeOf 只留路径的第一段，说明“这类形状”，不带编号和查询串。
func shapeOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) == 0 || seg[0] == "" {
		return "/"
	}
	return "/" + seg[0] + "/…"
}

type sampleOptions struct {
	Out        string
	MaxPerSite int
	Now        func() time.Time
}

type sampleRow struct {
	site, kind, status string
	requests           int64
}

// private 的形状只写类别：用户账号、个性化推荐流里的链接不进样张。
func private(l linkreader.Link) bool {
	return l.Platform == "xiaohongshu" || (l.Platform == "xiaoheihe" && l.Kind != "game")
}

func runSamples(ctx context.Context, f *linkreader.Fetcher, links []linkreader.Link, unknown []string, o sampleOptions) error {
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return err
	}
	perSite := map[string]int{}
	docs := map[string]*strings.Builder{}
	var rows []sampleRow
	order := []string{}
	for _, l := range links {
		key := linktemplate.SiteKey(l.Platform, l.Kind)
		if key == "default" {
			key = l.Platform
		}
		if perSite[key] >= o.MaxPerSite {
			continue
		}
		perSite[key]++
		n := perSite[key]
		doc := docs[key]
		if doc == nil {
			doc = &strings.Builder{}
			docs[key] = doc
			order = append(order, key)
			fmt.Fprintf(doc, "# %s 样张\n\n默认模板；真实取数、没有发送。取不到的写原因。\n\n", key)
		}
		before := f.Requests()
		row := renderSample(ctx, f, l, key, n, doc, o)
		row.requests = f.Requests() - before
		rows = append(rows, row)
	}
	var idx strings.Builder
	fmt.Fprintf(&idx, "# 链接解析样张（%s）\n\n真实取数、没有发送；按 ADR-290 输出到本地目录。每个站点一份 markdown，图片放在同名目录里。\n\n| 站点 | 类别 | 结果 | 请求数 |\n|---|---|---|---|\n", o.Now().Format("2006-01-02 15:04"))
	for _, r := range rows {
		fmt.Fprintf(&idx, "| [%s](%s.md) | %s | %s | %d |\n", r.site, r.site, r.kind, r.status, r.requests)
	}
	if len(unknown) > 0 {
		idx.WriteString("\n## 认不出的形状（不发请求）\n\n")
		for _, u := range unknown {
			fmt.Fprintf(&idx, "- %s\n", u)
		}
	}
	for _, key := range order {
		if err := os.WriteFile(filepath.Join(o.Out, key+".md"), []byte(docs[key].String()), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(o.Out, "README.md"), []byte(idx.String()), 0o644)
}

func failureLine(err error) string {
	var fe *linkreader.FetchError
	if errors.As(err, &fe) {
		s := fe.Kind
		if fe.Source != "" {
			s += "，来源 " + fe.Source
		}
		if fe.Reason != "" {
			s += "，原因 " + fe.Reason
		}
		if fe.Status != 0 {
			s += fmt.Sprintf("，HTTP %d", fe.Status)
		}
		return s
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "failed"
}

func renderSample(ctx context.Context, f *linkreader.Fetcher, l linkreader.Link, key string, n int, doc *strings.Builder, o sampleOptions) sampleRow {
	row := sampleRow{site: key, kind: l.Kind}
	title := fmt.Sprintf("%s %d：%s", key, n, l.Kind)
	if private(l) {
		fmt.Fprintf(doc, "## %s\n\n链接不写进样张（可能是个人账号或个性化推荐里的）。\n\n", title)
	} else {
		fmt.Fprintf(doc, "## %s\n\n链接：%s\n\n", title, withoutQuery(l.Clean))
	}
	supported := linkexpand.Supports(l.Platform, l.Kind)
	r, err := f.Fetch(ctx, l)
	if err != nil {
		row.status = "取数失败：" + failureLine(err)
		fmt.Fprintf(doc, "取数失败：%s。\n\n", failureLine(err))
		if !supported {
			fmt.Fprintf(doc, "这种形状（%s/%s）不进自动展开，群里不会回任何内容。\n\n", l.Platform, l.Kind)
		} else {
			fmt.Fprintf(doc, "自动展开会回失败文字。\n\n")
		}
		return row
	}
	dir := filepath.Join(o.Out, key, fmt.Sprintf("%02d", n))
	_ = os.MkdirAll(dir, 0o755)
	writeJSON(filepath.Join(dir, "result.json"), r)
	if !supported {
		row.status = "取到了，但这种形状不进自动展开"
		fmt.Fprintf(doc, "取到了，但 %s/%s 不进自动展开。\n\n", l.Platform, l.Kind)
		return row
	}
	draft := linkexpand.Build(&r, "", linkexpand.Options{ForwardVideo: true}, 0) // 视频进转发照默认配置（expand.send.forwardVideo 默认开）
	writeJSON(filepath.Join(dir, "draft.json"), draft)
	row.status = "成功"
	for i, part := range draft.Parts {
		switch part.Kind {
		case types.PartForward:
			fmt.Fprintf(doc, "**第 %d 条：合并转发（%d 个节点）**\n\n", i+1, len(part.Nodes))
			for j, node := range part.Nodes {
				fmt.Fprintf(doc, "节点 %d：\n\n", j+1)
				renderItems(ctx, f, doc, l.Platform, dir, key, n, node.Body, &row)
			}
		default:
			fmt.Fprintf(doc, "**第 %d 条消息**\n\n", i+1)
			renderItems(ctx, f, doc, l.Platform, dir, key, n, part.Body, &row)
		}
	}
	return row
}

var imageCount = map[string]int{}

func renderItems(ctx context.Context, f *linkreader.Fetcher, doc *strings.Builder, platform, dir, key string, n int, items []linkexpand.Item, row *sampleRow) {
	for _, it := range items {
		switch it.Kind {
		case types.BodyText:
			for _, line := range strings.Split(it.Text, "\n") {
				fmt.Fprintf(doc, "> %s\n", line)
			}
			doc.WriteString("\n")
		case types.BodyVideo:
			doc.WriteString("[视频，单独一条消息；视频下载先不做（ADR-313），群里不会发出]\n\n")
		case types.BodyGIF, "ugoira":
			doc.WriteString("[动图]\n\n")
		case "card":
			title := ""
			if it.Card != nil {
				title = it.Card.Title
			}
			fmt.Fprintf(doc, "[卡片：%s]\n\n", strings.ReplaceAll(title, "\n", " "))
		default:
			rel, err := downloadImage(ctx, f, platform, dir, it.Media, key+n2s(n))
			if err != nil {
				fmt.Fprintf(doc, "[图片：没有下载到（%s）]\n\n", failureLine(err))
				continue
			}
			fmt.Fprintf(doc, "![图片](%s)\n\n", rel)
		}
	}
}

func n2s(n int) string { return fmt.Sprintf("-%02d", n) }

// downloadImage 把图下到样张目录；不超过 4 MB。取不到就返回原因，不重试。
func downloadImage(ctx context.Context, f *linkreader.Fetcher, platform, dir string, m linkreader.Media, tag string) (string, error) {
	raw := ""
	if m.URL.State == "available" {
		raw = m.URL.Value
	} else if m.Thumbnail.State == "available" {
		raw = m.Thumbnail.Value
	}
	if raw == "" {
		return "", &linkreader.FetchError{Kind: "not_returned", Reason: "no_image_url"}
	}
	imageCount[tag]++
	if imageCount[tag] > 4 {
		return "", &linkreader.FetchError{Kind: "skipped", Reason: "only_first_4_images"}
	}
	var buf bytes.Buffer
	if err := f.Stream(ctx, platform, raw, &buf, &linkreader.ByteBudget{Limit: 4 << 20}); err != nil {
		return "", err
	}
	ext := ".bin"
	switch http.DetectContentType(buf.Bytes()) {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	default:
		return "", &linkreader.FetchError{Kind: "not_image", Reason: http.DetectContentType(buf.Bytes())}
	}
	name := fmt.Sprintf("image-%d%s", imageCount[tag], ext)
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	// markdown 在 <out>/<站点>.md，图在 <out>/<站点>/<序号>/ 下
	return filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(dir)), filepath.Base(dir), name)), nil
}

// 查询串里常带签名和令牌，写进 JSON 前统一打掉（取数、下载用的是没打掉的原地址）。
var queryInURL = regexp.MustCompile(`(https?://[^"?\s]+)\?[^"]*`)

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		_ = os.WriteFile(path, queryInURL.ReplaceAll(b, []byte("${1}?redacted")), 0o644)
	}
}
