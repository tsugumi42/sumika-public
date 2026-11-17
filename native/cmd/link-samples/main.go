// link-samples 是开发用入口：拿一份链接清单真实取数（只取数，不发送、不连 QQ、不读写 Sumika 的数据和配置），
// 把每个网站每种页面在每种样式下“会发出去的样子”存下来：文字、图片、卡片渲染图、分几条、是否合并转发，
// 再生成一个能直接双击打开的 index.html（画成 QQ 群里的样子）。取不到的如实写原因。
//
// 装配照线上默认配置：排版选项用 linkplugin.BuildOptions(config.DefaultLinks().Expand)，Preparer 的媒体能力用
// QQ 适配器声明的群聊能力，图片上限、视频上限、卡片配色、翻译开关都取默认值。和线上不同的地方写在页面顶部。
//
// 用法：link-samples [-list 清单] [-out 输出目录] [-proxy URL] [-pick 1,5,20-25] [-render]
// 清单每行一个链接，# 开头是注释；紧挨着链接上面那行注释写“平台/页面类型/变体 [来源]”，用来出目录和编号。
// 每取完一条就重写 samples.json；-render 只从 samples.json 重画 index.html，不取数。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/channel/snowluma"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktranslate"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
)

func main() { os.Exit(run()) }

type flags struct {
	list, out, proxy, pick, model, base, theme string
	render, video                              bool
	keepMB                                     int
	tweetMaxHeight                             int
	perLink                                    time.Duration
}

func run() int {
	var f flags
	flag.StringVar(&f.list, "list", "", "链接清单（必填）")
	flag.StringVar(&f.out, "out", "", "输出目录（必填）")
	flag.StringVar(&f.proxy, "proxy", "", "境外平台的 HTTP CONNECT 代理；空值读环境变量 HTTPS_PROXY / HTTP_PROXY")
	flag.StringVar(&f.pick, "pick", "", "只跑清单里的这几条（从 1 数），如 1,5,20-25；空是全部")
	flag.BoolVar(&f.render, "render", false, "只从 samples.json 重画 index.html，不取数")
	flag.BoolVar(&f.video, "video", true, "下载视频（默认配置 linkReader.expand.video.enabled 是关；这里默认开，方便看效果）")
	flag.StringVar(&f.model, "translate-model", "auto/deepseek-flash", "翻译用的 Magpie 模型；空串不翻译（线上默认开翻译，用角色的后台模型）")
	flag.StringVar(&f.base, "translate-base", "http://127.0.0.1:3425/v1", "翻译模型接口")
	flag.StringVar(&f.theme, "theme", "", "推特卡片配色 light / dark；空照默认配置 auto（北京时间 6–18 点浅色）")
	flag.IntVar(&f.tweetMaxHeight, "tweet-max-height", linkcard.DefaultTweetMaxHeight, "推特卡片高度封顶（图上像素，linkReader.expand.tweetCard.maxHeight）；0 不封顶")
	flag.IntVar(&f.keepMB, "keep-video-mb", 25, "视频不超过这么大才拷进 files/（只影响页面能不能播放，不影响发送结果）")
	flag.DurationVar(&f.perLink, "per-link", 5*time.Minute, "每条链接（取数加各样式准备）的时限")
	flag.Parse()
	if f.out == "" {
		fmt.Fprintln(os.Stderr, "必须给 -out（输出目录）")
		return 2
	}
	if f.render {
		return renderOnly(f.out)
	}
	if f.list == "" {
		fmt.Fprintln(os.Stderr, "必须给 -list（链接清单）")
		return 2
	}
	return fetchAll(f)
}

func renderOnly(out string) int {
	b, err := os.ReadFile(filepath.Join(out, "samples.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var g galleryOut
	if err = json.Unmarshal(b, &g); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err = os.WriteFile(filepath.Join(out, "index.html"), []byte(page(g)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "已重画", filepath.Join(out, "index.html"))
	return 0
}

func fetchAll(f flags) int {
	entries, err := readList(f.list)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	all := entries
	if entries, err = pick(entries, f.pick); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	files := filepath.Join(f.out, "files")
	if err = os.MkdirAll(files, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// 临时文件放输出目录下，不写共享的 %TEMP%；跑完删掉
	work := filepath.Join(f.out, ".work")
	if err = os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(work)

	defaults := config.DefaultLinks()
	expand := defaults.Expand
	info := runInfo{At: time.Now().Format("2006-01-02 15:04:05"), ListPath: f.list}
	info.Deviations = append(info.Deviations,
		"取数：照线上的 FX 超时和 syndication 默认值，另加每站至少间隔 2.1 秒、每分钟 60 次的限速（线上没有这层）；不经线上的取数缓存",
		"源消息号是假的（-1），所以“回复原消息”只画成引用条",
		"同一链接的几种样式共用一次媒体下载（结果字节相同，只省重复下载）")
	fetcher, err := linkreader.NewFetcher(linkreader.FetchOptions{Proxy: f.proxy, MinInterval: 2100 * time.Millisecond, Rate: linkreader.RateOptions{RequestsPerMinute: 60},
		FXTimeout: time.Duration(defaults.X.FXTimeoutSeconds) * time.Second, NoSyndication: !defaults.X.Syndication})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	themeSetting := expand.TweetCard.Theme
	if f.theme != "" {
		themeSetting = f.theme
		info.Deviations = append(info.Deviations, "推特卡片配色固定为 "+f.theme+"（默认配置是 auto）")
	}
	if f.tweetMaxHeight != linkcard.DefaultTweetMaxHeight {
		info.Deviations = append(info.Deviations, fmt.Sprintf("推特卡片高度封顶 %d 像素（默认 %d，0 是不封顶）", f.tweetMaxHeight, linkcard.DefaultTweetMaxHeight))
	}
	started := time.Now()
	info.Theme = fmt.Sprintf("%s：本次按 %s 画成 %s", themeSetting, started.Format("15:04"), linkcard.ThemeAt(themeSetting, started))
	cards := linkcard.NewWith(linkcard.Options{Theme: func() string { return themeSetting }, MaxHeight: func() int { return f.tweetMaxHeight }})
	defer cards.Close()
	limits := linkexpand.VideoLimits{Enabled: expand.Video.Enabled, Timeout: time.Duration(expand.Video.TimeoutSeconds) * time.Second, Bytes: int64(expand.Video.MaxMB) * 1_000_000}
	info.Video = fmt.Sprintf("默认配置 %s，上限 %d MB、%d 秒", onOff(expand.Video.Enabled), expand.Video.MaxMB, expand.Video.TimeoutSeconds)
	if f.video != expand.Video.Enabled {
		limits.Enabled = f.video
		info.Video += "；本次" + onOff(f.video) + "（-video）"
		info.Deviations = append(info.Deviations, "视频下载本次"+onOff(f.video)+"，默认配置是"+onOff(expand.Video.Enabled)+"（-video）")
	}
	// 媒体能力照 QQ 适配器在群聊里声明的（ADR-310）；线上某种组合明确失败过才会收窄，这里没有那份记录
	qqCaps := snowluma.New(nil).Caps().Media(types.SceneGroup)
	ff, _ := exec.LookPath("ffmpeg")
	prep := &linkexpand.Preparer{Media: newCachedLoader(linkexpand.NewMedia(fetcher, work), filepath.Join(work, "cache")), Cards: cards,
		Direct: qqCaps, Forward: qqCaps, ImageMaxBytes: int64(expand.ImageMaxMB) * 1_000_000, Limits: limits,
		Check: func(ctx context.Context) error { return ctx.Err() }, Guard: func(context.Context, string) error { return nil }}
	g := &gallery{fetcher: fetcher, prep: prep, expand: expand, files: files, ffmpeg: ff, keepVideo: int64(f.keepMB) << 20}
	if ff == "" {
		info.Deviations = append(info.Deviations, "本机没找到 ffmpeg：视频没有封面帧")
	}
	info.Translate = "默认配置" + onOff(expand.Translate.Enabled) + "，目标 " + expand.Translate.Target
	if f.model != "" && expand.Translate.Enabled {
		g.translate = translator(f.base, f.model, expand.Translate)
		info.Translate += "；本次用 " + f.model + "（线上用角色的后台模型）"
	} else {
		info.Translate += "；本次没接翻译，只发原文"
	}

	out := galleryOut{Run: info}
	// -pick 重跑几条时，把其余条目从上一次的 samples.json 留下来，按清单顺序合并
	var previous map[string]sampleOut
	picked := map[string]bool{}
	var codes []string
	for _, e := range entries {
		picked[e.Code] = true
		codes = append(codes, e.Code)
	}
	if f.pick != "" {
		var run *runInfo
		if previous, run = loadPrevious(filepath.Join(f.out, "samples.json")); run != nil {
			out.Run = rerunInfo(*run, info, codes)
		}
	}
	baseSeconds := out.Run.TotalSecond
	if previous == nil {
		baseSeconds = 0
	}
	save := func() {
		out.Run.TotalSecond = baseSeconds + int64(time.Since(started).Seconds())
		b, _ := json.MarshalIndent(out, "", "  ")
		if e := os.WriteFile(filepath.Join(f.out, "samples.json"), b, 0o644); e != nil {
			fmt.Fprintln(os.Stderr, e)
		}
	}
	for i, e := range entries {
		ctx, cancel := context.WithTimeout(context.Background(), f.perLink)
		s := g.sample(ctx, e)
		if ctx.Err() != nil && s.Error == "" {
			s.Error = "超过每条时限 " + f.perLink.String()
		}
		cancel()
		fmt.Fprintf(os.Stderr, "%3d/%d %-6s %-10s %-10s %s\n", i+1, len(entries), s.Code, s.Platform, s.Kind, status(s))
		out.Samples = mergeSample(out.Samples, s, previous, picked, all)
		save()
	}
	if err = os.WriteFile(filepath.Join(f.out, "index.html"), []byte(page(out)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "已写到", f.out)
	return 0
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// translator 照线上 translateLink：同一套 linktranslate.Translator（提示词、清理、缓存、时限），模型调用换成直连 Magpie、思考关掉。
func translator(base, model string, ex config.LinkTranslate) func(context.Context, string, string) linktranslate.Outcome {
	client := llm.New(llm.Magpie(base))
	tr := linktranslate.Translator{Cache: linktranslate.NewCache(linktranslate.DefaultCacheSize), Timeout: time.Duration(ex.TimeoutSeconds) * time.Second,
		Call: func(ctx context.Context, system, user string) (string, error) {
			r, err := client.StreamChat(ctx, chat.Request{Model: model, Thinking: "off", NoStore: true, Style: chat.StyleBackground,
				Messages: []chat.Message{{Role: chat.RoleSystem, Blocks: []chat.Block{chat.Text(system)}}, {Role: chat.RoleUser, Blocks: []chat.Block{chat.Text(user)}}}}, nil)
			if err != nil {
				return "", err
			}
			return r.Text, nil
		}}
	return func(ctx context.Context, cleanURL, text string) linktranslate.Outcome {
		return tr.Translate(ctx, cleanURL, text, ex.Target)
	}
}

func status(s sampleOut) string {
	switch {
	case s.Error != "":
		return "取不到：" + s.Error
	case len(s.Styles) == 0:
		return s.Support + " " + s.NotSent
	}
	bad := 0
	for _, st := range s.Styles {
		if st.Error != "" || st.NotSent != "" {
			bad++
		}
	}
	return fmt.Sprintf("%d 种样式（%d 种没发）取数 %d ms %s", len(s.Styles), bad, s.FetchMs, s.Route)
}

// pick 按 -pick 留下清单里的几条（从 1 数，可写区间）。
func pick(es []entry, spec string) ([]entry, error) {
	if strings.TrimSpace(spec) == "" {
		return es, nil
	}
	keep := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("-pick 写错了：%q", part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("-pick 写错了：%q", part)
			}
		}
		for i := a; i <= b; i++ {
			keep[i] = true
		}
	}
	var out []entry
	for i, e := range es {
		if keep[i+1] {
			out = append(out, e)
		}
	}
	return out, nil
}

func loadPrevious(path string) (map[string]sampleOut, *runInfo) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var g galleryOut
	if json.Unmarshal(b, &g) != nil {
		return nil, nil
	}
	out := map[string]sampleOut{}
	for _, s := range g.Samples {
		out[s.Code] = s
	}
	return out, &g.Run
}

// rerunInfo 是 -pick 重跑后的运行信息：沿用上一次全量的（时间、配色、总耗时），追加一条哪几条在什么时候重跑过。
func rerunInfo(prev, now runInfo, codes []string) runInfo {
	out := prev
	out.Deviations = append(append([]string(nil), prev.Deviations...), fmt.Sprintf("%s 用 -pick 重跑过：%s", now.At, strings.Join(codes, "、")))
	return out
}

// mergeSample 把新取的一条放进结果：没有上一次的结果时照取的顺序追加；有的话按整份清单的顺序排，
// 这次挑中的用新的，没挑中的沿用上一次的（上一次也没有的就不出现）。
func mergeSample(done []sampleOut, s sampleOut, previous map[string]sampleOut, picked map[string]bool, all []entry) []sampleOut {
	if previous == nil {
		return append(done, s)
	}
	fresh := map[string]sampleOut{s.Code: s}
	for _, d := range done {
		if picked[d.Code] {
			fresh[d.Code] = d
		}
	}
	var out []sampleOut
	for _, e := range all {
		if v, ok := fresh[e.Code]; ok {
			out = append(out, v)
		} else if v, ok := previous[e.Code]; ok && !picked[e.Code] {
			out = append(out, v)
		}
	}
	return out
}
