package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/linktranslate"
)

// 输出结构（samples.json）。页面只从这里画，-render 可以不重新取数只重画页面。

type itemOut struct {
	Type     string  `json:"type"` // text / image / gif / video / card（卡片渲染图）
	Text     string  `json:"text,omitempty"`
	File     string  `json:"file,omitempty"`  // files/ 下的相对路径；视频超过保存上限时为空
	Cover    string  `json:"cover,omitempty"` // 视频的封面帧（ffmpeg 抽的第一帧）
	W        int     `json:"w,omitempty"`
	H        int     `json:"h,omitempty"`
	Size     int64   `json:"size,omitempty"`
	Seconds  float64 `json:"seconds,omitempty"` // 视频时长
	Semantic string  `json:"semantic,omitempty"`
}
type nodeOut struct {
	Name    string    `json:"name"`
	Self    bool      `json:"self,omitempty"`
	ReplyTo int       `json:"replyTo,omitempty"` // 引用同一转发里第几个节点（从 1 数）
	Items   []itemOut `json:"items"`
}
type cardOut struct {
	Source  string   `json:"source,omitempty"`
	News    []string `json:"news,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
}
type msgOut struct {
	Kind  string    `json:"kind"` // message / forward
	Quote bool      `json:"quote,omitempty"`
	Items []itemOut `json:"items,omitempty"`
	Nodes []nodeOut `json:"nodes,omitempty"`
	Card  *cardOut  `json:"card,omitempty"`
}
type styleOut struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Hint      string   `json:"hint"`
	Template  string   `json:"template,omitempty"` // 写进 templates.<站点> 的值；空是内置默认
	Forward   string   `json:"forward,omitempty"`  // 写进 sites.<站点>.forward 的值
	Error     string   `json:"error,omitempty"`
	NotSent   string   `json:"notSent,omitempty"` // 线上这种情况不发消息时写原因
	PrepareMs int64    `json:"prepareMs"`
	Notes     []string `json:"notes,omitempty"`
	Messages  []msgOut `json:"messages,omitempty"`
}
type sampleOut struct {
	Code        string     `json:"code"`
	URL         string     `json:"url"`
	ListPlat    string     `json:"listPlatform"`
	ListKind    string     `json:"listKind"`
	Variant     string     `json:"variant"`
	From        string     `json:"from"`
	Platform    string     `json:"platform"`
	Kind        string     `json:"kind"`
	PlatLabel   string     `json:"platformLabel"`
	KindLabel   string     `json:"kindLabel"`
	Site        string     `json:"site,omitempty"`
	Support     string     `json:"support,omitempty"`
	Title       string     `json:"title,omitempty"`
	Route       string     `json:"route,omitempty"` // 取到数据的线路（Result.Source）
	FetchMs     int64      `json:"fetchMs"`
	Error       string     `json:"error,omitempty"`   // 取不到的原因
	NotSent     string     `json:"notSent,omitempty"` // 线上整条链接不发消息时写原因
	Off         string     `json:"off,omitempty"`     // 默认设置里这类关着
	Translation string     `json:"translation,omitempty"`
	Styles      []styleOut `json:"styles,omitempty"`
}

// runInfo 是这次运行的设置，写在页面顶部。
type runInfo struct {
	At          string   `json:"at"`
	Theme       string   `json:"theme"`
	Video       string   `json:"video"`
	Translate   string   `json:"translate"`
	Deviations  []string `json:"deviations"`
	ListPath    string   `json:"list"`
	TotalSecond int64    `json:"totalSeconds"`
}
type galleryOut struct {
	Run     runInfo     `json:"run"`
	Samples []sampleOut `json:"samples"`
}

// gallery 是一次运行的装配：取数器、Preparer、翻译，全部照线上的默认配置。
type gallery struct {
	fetcher   *linkreader.Fetcher
	prep      *linkexpand.Preparer
	expand    config.LinkExpand
	translate func(ctx context.Context, cleanURL, text string) linktranslate.Outcome
	files     string
	ffmpeg    string
	keepVideo int64 // 视频不超过这个字节数才拷进 files/
}

// styleOptions 是某个样式下的排版选项：以线上 BuildOptions(配置) 为底，只改这个样式要写的模板和转发设置。
// 默认样式什么都不改，等于线上默认配置真发时的选项。
func styleOptions(expand config.LinkExpand, site linktemplate.Site, st linktemplate.Style) linkexpand.Options {
	o := linkplugin.BuildOptions(expand)
	if st.Direct != "" {
		o.Templates[site.Key] = st.Direct
	}
	if st.Forward != "" {
		set := o.Sites[site.Key]
		set.Forward = st.Forward
		o.Sites[site.Key] = set
	}
	return o
}

const sourceMessageID = "-1" // 群友那条源消息的号；线上是真实消息号，只用来让第一条消息回复它

func (g *gallery) sample(ctx context.Context, e entry) sampleOut {
	s := sampleOut{Code: e.Code, URL: shown(e.URL), ListPlat: e.Platform, ListKind: e.Kind, Variant: e.Variant, From: e.From}
	l, ok := linkreader.Recognize(e.URL)
	if !ok {
		s.Error = "不认识这个链接"
		s.NotSent = "线上不当链接处理，什么都不发"
		s.fillLabels()
		return s
	}
	s.Platform, s.Kind = l.Platform, l.Kind
	began := time.Now()
	r, err := g.fetcher.Fetch(ctx, l)
	s.FetchMs = time.Since(began).Milliseconds()
	if err != nil {
		s.Error = fetchReason(l, err)
		s.NotSent = "取数失败：群里不发文字，只把开始表情换成失败表情（私聊回一句解析失败）"
		s.fillLabels()
		return s
	}
	s.Platform, s.Kind, s.Route = r.Platform, r.Kind, r.Source
	s.fillLabels()
	if r.Title.State == "available" {
		s.Title = r.Title.Value
	} else if r.Text.State == "available" {
		s.Title = firstLine(r.Text.Value)
	}
	if k, found := linktemplate.LookupKind(r.Platform, r.Kind); found {
		s.Support = k.Support.Label()
	} else {
		s.Support = "不在清单里"
	}
	if !linkexpand.Supports(r.Platform, r.Kind) {
		s.NotSent = "这种页面没有发送格式，线上不展开"
		return s
	}
	if !g.expand.KindEnabled(r.Platform, r.Kind) {
		s.Off = "默认设置里这个网站或页面类型是关的，线上不展开；下面照打开后的样子画"
	}
	s.Site = linktemplate.SiteKey(r.Platform, r.Kind)
	site, _, _ := linktemplate.SiteByKey(s.Site)
	translation := ""
	if g.translate != nil && g.expand.TranslateOn(r.Platform) {
		if src := linkexpand.TranslationSource(&r, linkplugin.BuildOptions(g.expand)); src != "" {
			out := g.translate(ctx, r.CleanURL, src)
			translation = out.Text
			s.Translation = translateNote(out)
		}
	} else if g.translate == nil && g.expand.TranslateOn(r.Platform) && linkexpand.TranslationSource(&r, linkplugin.BuildOptions(g.expand)) != "" {
		s.Translation = "翻译没接（线上默认开，这里只发原文）"
	}
	for _, st := range site.Styles() {
		s.Styles = append(s.Styles, g.style(ctx, &r, site, st, translation, e.Code))
	}
	return s
}

func (s *sampleOut) fillLabels() {
	plat, kind := s.Platform, s.Kind
	if plat == "" {
		plat, kind = s.ListPlat, s.ListKind
	}
	s.PlatLabel, s.KindLabel = plat, kind
	if p, ok := linktemplate.PlatformByID(plat); ok {
		s.PlatLabel = p.Label
	}
	if k, ok := linktemplate.LookupKind(plat, kind); ok {
		s.KindLabel = k.Label
	}
}

func translateNote(o linktranslate.Outcome) string {
	switch {
	case o.Text != "":
		return fmt.Sprintf("已翻译（%s，%d ms）", o.Status, o.Took.Milliseconds())
	case o.Status == linktranslate.StatusSkipped || o.Status == linktranslate.StatusUnneeded:
		return "不用翻（" + o.Reason + "）"
	}
	return fmt.Sprintf("翻译%s（%s），线上只发原文", o.Status, o.Reason)
}

func fetchReason(l linkreader.Link, err error) (out string) {
	var fe *linkreader.FetchError
	if !errors.As(err, &fe) {
		return err.Error()
	}
	out = fe.Kind
	if k, ok := linktemplate.LookupKind(l.Platform, l.Kind); ok && fe.Kind == "unsupported" {
		// 取数层说这一种取不了；目录里的说明另起一句，不和取数层的原因拼成一句（以前会拼出“能取数，还没有发送格式：要设备签名”）
		out = "取数层不支持这种页面"
		if k.Note != "" {
			defer func() { out += "（目录说明：" + k.Note + "）" }()
		}
	}
	if fe.Code != 0 {
		out += fmt.Sprintf("（%d）", fe.Code)
	}
	if fe.Status != 0 && fe.Status != fe.Code {
		out += fmt.Sprintf("（HTTP %d）", fe.Status)
	}
	if fe.Reason != "" {
		out += "：" + fe.Reason
	}
	if fe.Trail != "" {
		out += "；经过 " + fe.Trail
	}
	return out
}

func (g *gallery) style(ctx context.Context, r *linkreader.Result, site linktemplate.Site, st linktemplate.Style, translation, code string) styleOut {
	so := styleOut{ID: st.ID, Label: st.Label, Hint: st.Hint, Template: st.Direct, Forward: st.Forward}
	o := styleOptions(g.expand, site, st)
	o.Translation = translation
	d := linkexpand.Build(r, sourceMessageID, o, 0)
	switch {
	case d.NoContent:
		so.NotSent = "取到了但标题和正文都没有：线上当失败，只换失败表情、不发文字"
		return so
	case len(d.Parts) == 0:
		so.NotSent = "模板拼出来什么都没有：线上不发，只记一行"
		return so
	}
	began := time.Now()
	prep := *g.prep
	prep.Notice = func(line string) { so.Notes = append(so.Notes, line) }
	prepared, err := prep.Prepare(ctx, r, &d)
	so.PrepareMs = time.Since(began).Milliseconds()
	if err != nil {
		so.Error = err.Error()
		so.NotSent = "准备结果失败：线上不发，记失败"
		return so
	}
	defer prepared.Release()
	for _, p := range prepared.Parts {
		m := msgOut{Kind: "message", Quote: p.Quote != ""}
		if p.Kind == types.PartForward || len(p.Nodes) > 0 {
			m.Kind = "forward"
		}
		for _, seg := range p.Body {
			m.Items = append(m.Items, g.save(seg, code))
		}
		for _, node := range p.Nodes {
			no := nodeOut{Name: node.Name, Self: node.Self, ReplyTo: node.ReplyTo}
			for _, seg := range node.Body {
				no.Items = append(no.Items, g.save(seg, code))
			}
			m.Nodes = append(m.Nodes, no)
		}
		if p.Card != nil && !p.Card.Empty() {
			m.Card = &cardOut{Source: p.Card.Source, News: append([]string(nil), p.Card.News...), Summary: p.Card.Summary, Prompt: p.Card.Prompt}
		}
		so.Messages = append(so.Messages, m)
	}
	return so
}

var extOf = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "video/mp4": ".mp4"}

// save 把一段落盘：文字原样；图片、卡片按内容哈希存进 files/（几种样式共用一张图只存一份）；
// 视频不超过 keepVideo 才拷，另外用 ffmpeg 抽第一帧当封面、ffprobe 量时长。
func (g *gallery) save(seg types.BodySegment, code string) itemOut {
	if seg.Kind == types.BodyText {
		return itemOut{Type: "text", Text: seg.Text}
	}
	it := itemOut{Type: seg.Kind, Semantic: seg.Semantic}
	if seg.Semantic != "" && seg.Kind == types.BodyImage {
		it.Type = "card"
	}
	m := seg.Media
	if m == nil {
		return it
	}
	it.W, it.H, it.Size = m.Width, m.Height, m.Size
	ext := extOf[m.MIME]
	if ext == "" {
		ext = ".bin"
	}
	if seg.Kind == types.BodyVideo {
		path, err := m.FileName()
		if err != nil {
			return it
		}
		base := strings.ToLower(code) + "-" + fileHash(path)
		it.Seconds = g.probeSeconds(path)
		if g.ffmpeg != "" {
			cover := filepath.Join(g.files, base+"-cover.jpg")
			if _, e := os.Stat(cover); e == nil || g.run(g.ffmpeg, "-y", "-loglevel", "error", "-i", path, "-frames:v", "1", "-q:v", "4", cover) == nil {
				it.Cover = "files/" + base + "-cover.jpg"
				if it.W == 0 {
					it.W, it.H = imageSize(cover)
				}
			}
		}
		if m.Size > 0 && m.Size <= g.keepVideo {
			dst := filepath.Join(g.files, base+ext)
			if _, e := os.Stat(dst); e == nil || copyFile(path, dst) == nil {
				it.File = "files/" + base + ext
			}
		}
		return it
	}
	var data []byte
	if m.Read(func(rd io.Reader) error {
		var e error
		data, e = io.ReadAll(rd)
		return e
	}) != nil {
		return it
	}
	sum := sha1.Sum(data)
	name := hex.EncodeToString(sum[:])[:16] + ext
	dst := filepath.Join(g.files, name)
	if _, e := os.Stat(dst); e != nil {
		if os.WriteFile(dst, data, 0o644) != nil {
			return it
		}
	}
	it.File = "files/" + name
	return it
}

func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	defer f.Close()
	h := sha1.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func (g *gallery) run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd.Run()
}

func (g *gallery) probeSeconds(path string) float64 {
	if g.ffmpeg == "" {
		return 0
	}
	probe := filepath.Join(filepath.Dir(g.ffmpeg), "ffprobe"+filepath.Ext(g.ffmpeg))
	if _, err := os.Stat(probe); err != nil {
		if p, e := exec.LookPath("ffprobe"); e == nil {
			probe = p
		} else {
			return 0
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return v
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "…"
	}
	return s
}
