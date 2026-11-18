package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"time"

	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

func main() { os.Exit(run()) }
func run() int {
	proxy := flag.String("proxy", "", "境外平台的 HTTP CONNECT 代理；空值读取 HTTPS_PROXY / HTTP_PROXY；国内平台始终直连")
	samples := flag.String("samples", "", "批量样张：测试链接清单（markdown），真实取数、不发送，写到 -out 目录（ADR-290）")
	outDir := flag.String("out", "", "样张输出目录（和 -samples 一起用）")
	perSite := flag.Int("max-per-site", 12, "批量样张时每个站点最多取几条")
	stitch := flag.String("stitch", "", "X 拆图拼回诊断：下载帖子的图，按顺序判断能不能拼回，把每道缝的数打出来；通过时把拼好的图写到这个文件（只读，不发送）")
	flag.Parse()
	if *samples != "" {
		return runSamplesCommand(*proxy, *samples, *outDir, *perSite)
	}
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法：link-probe [-proxy URL] <公开链接>\n      link-probe [-proxy URL] -samples <清单.md> -out <目录> [-max-per-site N]")
		return 2
	}
	l, ok := linkreader.Recognize(flag.Arg(0))
	if !ok {
		fmt.Fprintln(os.Stderr, "unsupported link")
		return 2
	}
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Proxy: *proxy, MinInterval: 2100 * time.Millisecond})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if *stitch != "" {
		return runStitch(ctx, f, l, *stitch)
	}
	started := time.Now()
	r, err := f.Fetch(ctx, l)
	out := struct {
		Result    *linkreader.Result     `json:"result,omitempty"`
		Error     *linkreader.FetchError `json:"error,omitempty"`
		Requests  int64                  `json:"requests"`
		ElapsedMS int64                  `json:"elapsed_ms"`
	}{Requests: f.Requests(), ElapsedMS: time.Since(started).Milliseconds()}
	code := 0
	if err != nil {
		var e *linkreader.FetchError
		if errors.As(err, &e) {
			out.Error = e
		} else {
			out.Error = &linkreader.FetchError{Kind: "failed"}
		}
		code = 1
	} else {
		redact(&r)
		out.Result = &r
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if enc.Encode(out) != nil {
		return 1
	}
	return code
}

// 诊断不输出签名地址；原始候选只留在进程内统一结果中。
func redact(r *linkreader.Result) {
	r.CleanURL = withoutQuery(r.CleanURL)
	if r.Author.Avatar.State == "available" {
		r.Author.Avatar.Value = withoutQuery(r.Author.Avatar.Value)
	}
	if r.Ugoira != nil && r.Ugoira.ZIP.State == "available" {
		r.Ugoira.ZIP.Value = withoutQuery(r.Ugoira.ZIP.Value)
	}
	for i := range r.Links.Value {
		redactLink(&r.Links.Value[i])
	}
	if r.LinkCard.State == "available" {
		if r.LinkCard.Value.URL.State == "available" {
			r.LinkCard.Value.URL.Value = withoutQuery(r.LinkCard.Value.URL.Value)
		}
		redactMedia(&r.LinkCard.Value.Image.Value)
	}
	for i := range r.Media.Value {
		redactMedia(&r.Media.Value[i])
	}
	for i := range r.Blocks {
		b := &r.Blocks[i]
		for j := range b.Links {
			redactLink(&b.Links[j])
		}
		if b.Media != nil {
			redactMedia(b.Media)
		}
		if b.Quote != nil {
			redact(b.Quote)
		}
	}
	if r.Quote.Value != nil {
		redact(r.Quote.Value)
	}
}
func redactMedia(m *linkreader.Media) {
	if m.URL.State == "available" {
		m.URL.Value = withoutQuery(m.URL.Value)
	}
	if m.Thumbnail.State == "available" {
		m.Thumbnail.Value = withoutQuery(m.Thumbnail.Value)
	}
	if m.Fallback != nil && m.Fallback.URL.State == "available" {
		m.Fallback.URL.Value = withoutQuery(m.Fallback.URL.Value)
	}
	for i := range m.Variants.Value {
		if m.Variants.Value[i].URL.State == "available" {
			m.Variants.Value[i].URL.Value = withoutQuery(m.Variants.Value[i].URL.Value)
		}
	}
}
func redactLink(l *linkreader.ExternalLink) {
	if l.URL.State == "available" {
		l.URL.Value = withoutQuery(l.URL.Value)
	}
	if l.Original.State == "available" {
		l.Original.Value = withoutQuery(l.Original.Value)
	}
}
func withoutQuery(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return "[redacted]"
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = "redacted"
	}
	u.Fragment = ""
	return u.String()
}

func runSamplesCommand(proxy, list, out string, perSite int) int {
	if out == "" || perSite < 1 {
		fmt.Fprintln(os.Stderr, "批量样张要写 -out <目录>，-max-per-site 至少 1")
		return 2
	}
	raw, err := os.ReadFile(list)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Proxy: proxy, MinInterval: 2100 * time.Millisecond})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	links, unknown := sampleURLs(string(raw))
	if err := runSamples(ctx, f, links, unknown, sampleOptions{Out: out, MaxPerSite: perSite, Now: time.Now}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "写好了：%s（取了 %d 条链接，共 %d 次请求）\n", out, len(links), f.Requests())
	return 0
}

// runStitch 下载帖子的全部图片（只在内存里，不落盘原图），打印拼回判断；通过时才写拼好的图。
func runStitch(ctx context.Context, f *linkreader.Fetcher, l linkreader.Link, out string) int {
	r, err := f.Fetch(ctx, l)
	if err != nil {
		fmt.Fprintln(os.Stderr, "取数失败：", err)
		return 1
	}
	var raws [][]byte
	for _, m := range r.Media.Value {
		if (m.Type != "image" && m.Type != "photo") || m.URL.State != "available" {
			continue
		}
		var buf bytes.Buffer
		if err = f.Stream(ctx, r.Platform, m.URL.Value, &buf, &linkreader.ByteBudget{Limit: 20 << 20}); err != nil {
			fmt.Fprintln(os.Stderr, "下载失败：", err)
			return 1
		}
		raws = append(raws, buf.Bytes())
	}
	report, data, err := linkexpand.DiagnosePanorama(raws)
	fmt.Print(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if data != nil {
		if err = os.WriteFile(out, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("已写到", out)
	}
	return 0
}
