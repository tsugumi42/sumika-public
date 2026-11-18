package linkexpand

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/taskrun"
)

type MediaLoader interface {
	Image(context.Context, string, linkreader.Media) (Loaded, error)
	Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error)
	Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error)
}
type fileImageLoader interface {
	FileImage(context.Context, Loaded) (Loaded, error)
}
type Preparer struct {
	Media           MediaLoader
	Cards           *linkcard.Renderer
	Guard           func(context.Context, string) error
	Check           func(context.Context) error
	Direct, Forward types.MediaCaps
	Limits          VideoLimits
	// ImageMaxBytes 是发送前的单图体积上限（配置 linkReader.expand.imageMaxMB）；0 用默认的 20 MB，见 image_limit.go
	ImageMaxBytes int64
	// Degrade 记图片、卡片、动图、视频退回文字的原因（taskrun.ModuleMedia）；nil 就不记
	Degrade *taskrun.Degrader
	// PanoramaTimeout 是 X 拆图拼回（panorama.go）的限时；0 用 panoramaTimeout
	PanoramaTimeout time.Duration
	// Notice 收拆图拼回的判断结果、原图退档这类说明（一行中文，不含链接），日志用；nil 不记
	Notice func(string)
}
type Prepared struct {
	Parts     []types.Part
	Resources []*types.MediaResource
}

func (p *Prepared) Release() error {
	var err error
	for _, r := range p.Resources {
		err = errors.Join(err, r.Release())
	}
	p.Resources = nil
	return err
}
func (d *Draft) Semantic() string {
	var b strings.Builder
	add := func(items []Item) {
		for _, item := range items {
			if item.Kind == "card" {
				b.WriteString(item.Card.Semantic())
			} else {
				b.WriteString(item.Text)
			}
			b.WriteByte('\n')
		}
	}
	for _, part := range d.Parts {
		add(part.Body)
		for _, n := range part.Nodes {
			b.WriteString(n.Name)
			b.WriteByte('\n')
			add(n.Body)
		}
	}
	return b.String()
}

// Prepare 在任何封面、原图和 ZIP 请求之前执行文字守卫和最新许可检查。
func (p *Preparer) Prepare(ctx context.Context, r *linkreader.Result, d *Draft) (prepared Prepared, err error) {
	if err = p.Check(ctx); err != nil {
		return prepared, err
	}
	if err = p.Guard(ctx, d.Semantic()); err != nil {
		return prepared, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, prepared.Release())
		}
	}()
	var standalone []types.Part
	// lost 记下因我们自己的限制、下载范围这类原因悄悄没发出去的媒体（quietMediaFailure）；最后整条一个媒体都没有时才补一句说明（lostMediaNote）
	lost := map[string]bool{}
	seenImages := map[string]bool{}
	// 拆图拼回：整组图先下载放在 preloaded（按地址），拼不成时普通路径从这里取，不重复下载；拼成且不用附原图时释放。
	preloaded := map[string]Loaded{}
	stitched := false
	defer func() {
		for _, l := range preloaded {
			if l.Resource != nil {
				err = errors.Join(err, l.Resource.Release())
			}
		}
	}()
	add := func(loaded Loaded, semantic string) types.BodySegment {
		prepared.Resources = append(prepared.Resources, loaded.Resource)
		return types.BodySegment{Kind: loaded.Kind, Media: loaded.Resource, Semantic: semantic}
	}
	// tryPanorama 处理 X 拆图候选组：下载整组、判断、拼接；返回拼好的图片段。
	// 拿不准、失败、超时都返回 ok=false（下载好的图留在 preloaded，调用方按普通多图发）；只有取消这类错误才返回 err。
	tryPanorama := func(ctx context.Context, group []Item, keep bool, caps types.MediaCaps) (types.BodySegment, bool, error) {
		loaded := make([]Loaded, 0, len(group))
		for _, it := range group {
			if it.Kind != types.BodyImage && it.Kind != "photo" {
				return types.BodySegment{}, false, nil
			}
		}
		for _, it := range group {
			l, e := p.loadImage(ctx, r.Platform, it.Media)
			if e != nil {
				if ctx.Err() != nil {
					return types.BodySegment{}, false, ctx.Err()
				}
				break // 这张没下到：已下载的交给普通路径，缺的那张它会自己再试并写原因
			}
			loaded = append(loaded, l)
			preloaded[it.Media.URL.Value] = l
		}
		if len(loaded) != len(group) {
			return types.BodySegment{}, false, nil
		}
		res := p.stitchLoaded(ctx, loaded)
		if p.Notice != nil {
			p.Notice(res.verdict.String())
		}
		if res.res == nil {
			return types.BodySegment{}, false, ctx.Err()
		}
		moved, e := p.imageTransport(ctx, Loaded{Kind: types.BodyImage, Resource: res.res}, caps)
		if e != nil {
			return types.BodySegment{}, false, nil
		}
		if !keep {
			for _, it := range group {
				if l, ok := preloaded[it.Media.URL.Value]; ok {
					delete(preloaded, it.Media.URL.Value)
					if e := l.Resource.Release(); e != nil {
						return types.BodySegment{}, false, e
					}
				}
			}
		}
		stitched = true
		seg := add(moved, "")
		return seg, true, nil
	}
	var body func([]Item, bool) ([]types.BodySegment, error)
	body = func(items []Item, forward bool) ([]types.BodySegment, error) {
		var out []types.BodySegment
		appendText := func(s string) {
			if strings.TrimSpace(s) == "" {
				return
			}
			if len(out) > 0 && out[len(out)-1].Kind == types.BodyText {
				out[len(out)-1].Text += "\n" + s
			} else {
				out = append(out, types.BodySegment{Kind: types.BodyText, Text: s})
			}
		}
		caps := p.Direct
		if forward {
			caps = p.Forward
		}
		skip := 0
		for i, item := range items {
			if skip > 0 {
				skip--
				continue
			}
			if e := p.Check(ctx); e != nil {
				return nil, e
			}
			isImage := item.Kind == types.BodyImage || item.Kind == "photo"
			if isImage && item.PanoramaOrig && !stitched {
				continue // 没拼成：原图已经按普通多图发过了
			}
			if isImage && item.PanoramaN > 1 && caps.Image && i+item.PanoramaN <= len(items) {
				seg, ok, e := tryPanorama(ctx, items[i:i+item.PanoramaN], item.PanoramaKeep, caps)
				if e != nil {
					return nil, e
				}
				if ok {
					out = append(out, seg)
					skip = item.PanoramaN - 1
					continue
				}
			}
			if item.Kind == types.BodyText {
				appendText(item.Text)
				continue
			}
			if item.Kind == "card" {
				if !caps.Image {
					p.degraded("卡片", errImageCapability)
					appendText(item.Card.Semantic() + "\n卡片未生成：这种图片在这里发不出去")
					continue
				}
				// X 帖子画推特卡片（prepare_tweet.go）；画不成往下画通用卡片
				if tweet, semantic, ok, e := p.tweetCard(ctx, r, caps, item); e != nil {
					return nil, e
				} else if ok {
					out = append(out, add(tweet, semantic))
					continue
				}
				data := *item.Card
				var heldCover Loaded
				if item.Media.URL.State == "available" {
					cover, e := p.loadImage(ctx, r.Platform, item.Media)
					if e == nil {
						e = cover.Resource.Read(func(reader io.Reader) error {
							var decodeErr error
							data.Cover, decodeErr = linkcard.DecodeCoverReader(reader.(io.ReadSeeker))
							return decodeErr
						})
						if e != nil {
							// 封面没取到就画成没有封面的卡片：不画“封面不可用”的空白块，也不在卡片上写原因（原因只进运行记录）
							p.degraded("封面", e)
							if releaseErr := cover.Resource.Release(); releaseErr != nil {
								return nil, releaseErr
							}
						} else {
							heldCover = cover
						}
					} else {
						p.degraded("封面", e)
					}
				}
				rendered, e := p.Cards.Render(ctx, data)
				if e != nil {
					if heldCover.Resource != nil {
						heldCover, e = p.imageTransport(ctx, heldCover, caps)
						if e == nil {
							out = append(out, add(heldCover, ""))
							seenImages[item.Media.URL.Value] = true
						}
					}
					p.degraded("卡片", e)
					appendText(data.Semantic()) // 卡片画不成就发卡片上的字；原因只进运行记录
					continue
				}
				if heldCover.Resource != nil {
					if e := heldCover.Resource.Release(); e != nil {
						return nil, e
					}
					seenImages[item.Media.URL.Value] = true
				}
				// 卡片是渲染出来的静态图，封面已经画在里面；被拒后的旋转由发送端对整张图做（linkplugin，linkexpand.Rotate180）
				card, e := p.imageTransport(ctx, Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia(rand.Text(), rendered.MIME, rendered.Width, rendered.Height, rendered.Bytes)}, caps)
				if e != nil {
					p.degraded("卡片", e)
					appendText(data.Semantic())
					continue
				}
				out = append(out, add(card, data.Semantic()))
				continue
			}
			if item.Kind == types.BodyImage || item.Kind == "photo" {
				if !caps.Image {
					p.degraded("图片", errImageCapability)
					if !item.Optional {
						appendText("图片未下载：这种图片在这里发不出去")
					}
					continue
				}
				var loaded Loaded
				var e error
				if held, ok := preloaded[item.Media.URL.Value]; ok {
					loaded = held
					delete(preloaded, item.Media.URL.Value)
				} else {
					loaded, e = p.loadImage(ctx, r.Platform, item.Media)
				}
				if e != nil {
					p.degraded("图片", e)
					switch {
					case item.Optional: // 个人主页的头像：下不来就当没有，不写说明
					case !quietMediaFailure(e):
						appendText("图片未下载：" + MediaReason(e))
					default:
						lost["图片"] = true
					}
					continue
				}
				if loaded.Kind == types.BodyGIF && !caps.GIF {
					if e := loaded.Resource.Release(); e != nil {
						return nil, e
					}
					p.degraded("动图", ErrGIFCapability)
					appendText("动图未下载：" + ErrGIFCapability.Error())
					continue
				}
				loaded, e = p.imageTransport(ctx, loaded, caps)
				if e != nil {
					p.degraded("图片", e)
					switch {
					case item.Optional:
					case !quietMediaFailure(e):
						appendText("图片未准备：" + MediaReason(e))
					default:
						lost["图片"] = true
					}
					continue
				}
				out = append(out, add(loaded, ""))
				seenImages[item.Media.URL.Value] = true
				// 退档之类的说明（“原图超过20 MB，使用regular”）只进日志，不进群
				if loaded.Note != "" && p.Notice != nil {
					p.Notice(loaded.Note)
				}
				continue
			}
			// 缩略图先准备；视频失败不会抹掉同一条的正文或其他媒体。
			var thumb Loaded
			if caps.Image && item.Media.Thumbnail.State == "available" && !seenImages[item.Media.Thumbnail.Value] {
				src := item.Media
				src.Type = "image"
				src.URL = src.Thumbnail
				src.Size = linkreader.Field[int64]{}
				thumb, _ = p.loadImage(ctx, r.Platform, src)
				if thumb.Resource != nil {
					thumb, _ = p.imageTransport(ctx, thumb, caps)
				}
			}
			var loaded Loaded
			var e error
			switch item.Kind {
			case "ugoira":
				if caps.GIF {
					loaded, e = p.Media.Ugoira(ctx, item.Ugoira)
				} else {
					e = ErrGIFCapability
				}
			case types.BodyGIF:
				u, _ := url.Parse(item.Media.URL.Value)
				if caps.GIF && u != nil && strings.EqualFold(path.Ext(u.Path), ".gif") {
					loaded, e = p.loadImage(ctx, r.Platform, item.Media)
				} else if caps.GIF {
					loaded, e = p.Media.Video(ctx, r.Platform, r, item.Media, p.Limits, true)
				} else {
					e = ErrGIFCapability
				}
			default:
				if caps.Video {
					loaded, e = p.Media.Video(ctx, r.Platform, r, item.Media, p.Limits, false)
				} else {
					e = ErrVideoCapability
				}
			}
			if e == nil && loaded.Kind != types.BodyVideo {
				loaded, e = p.imageTransport(ctx, loaded, caps)
			}
			if e == nil {
				if thumb.Resource != nil {
					if err := thumb.Resource.Release(); err != nil {
						return nil, errors.Join(err, loaded.Resource.Release())
					}
				}
				segment := add(loaded, "")
				if item.Standalone && !forward {
					standalone = append(standalone, types.Part{Kind: types.PartMessage, Body: []types.BodySegment{segment}})
				} else {
					out = append(out, segment)
				}
			} else {
				if ctx.Err() != nil {
					var releaseErr error
					if thumb.Resource != nil {
						releaseErr = thumb.Resource.Release()
					}
					return nil, errors.Join(ctx.Err(), releaseErr)
				}
				if thumb.Resource != nil {
					out = append(out, add(thumb, ""))
				}
				label := "视频未下载："
				if item.Kind == "ugoira" || item.Kind == types.BodyGIF {
					label = "动图未下载："
				}
				p.degraded(strings.TrimSuffix(label, "未下载："), e)
				// 主人自己关的不算失败，不往消息里写；只留运行记录
				// 我们自己的限制（大小上限、下载范围、本机工具）只进运行记录，不往群里写
				if !errors.Is(e, ErrVideoDisabled) && !quietMediaFailure(e) {
					appendText(label + MediaReason(e))
				} else if !errors.Is(e, ErrVideoDisabled) {
					lost[strings.TrimSuffix(label, "未下载：")] = true
				}
			}
		}
		return out, nil
	}
	for _, part := range d.Parts {
		result := types.Part{Kind: part.Kind, Quote: part.Quote, Card: part.Card}
		if len(part.Body) > 0 {
			result.Body, err = body(part.Body, false)
			if err != nil {
				return prepared, err
			}
		}
		contents := make([][]types.BodySegment, len(part.Nodes))
		for i, node := range part.Nodes {
			contents[i], err = body(node.Body, true)
			if err != nil {
				return prepared, err
			}
		}
		// 准备完是空的节点（图全没下下来）不发；引用（QuoteOf → ReplyTo，规格-链接发送改版 A7）按留下的节点重新对下标
		kept := KeepNodes(part.Nodes, func(i int) bool { return len(contents[i]) > 0 })
		j := 0
		for i := range part.Nodes {
			if len(contents[i]) == 0 {
				continue
			}
			node := kept[j]
			j++
			result.Nodes = append(result.Nodes, types.ForwardNode{Name: node.Name, Self: !node.Fixed, Body: contents[i], ReplyTo: node.QuoteOf})
		}
		if part.Kind == types.PartMessage && part.imageTotal > 0 {
			result.Body = markTruncated(result.Body, countVisual(result.Body), part.imageTotal)
		}
		// 形状按准备好的媒体再定一次（shape.go 第 3、4 条）：拆图没拼成超了上限的消息改成转发或拆开；只因图多进的转发图没下下来就收回成一条
		if part.Kind == types.PartMessage && part.imageCap > 0 {
			if n := countVisual(result.Body); n > part.imageCap {
				prepared.Parts = append(prepared.Parts, overCap(part, result)...)
				continue
			}
		}
		if part.fitDirect && len(result.Nodes) > 0 {
			result, _ = collapseForward(result, part.Quote, part.imageCap)
		}
		if len(result.Body) > 0 || len(result.Nodes) > 0 {
			prepared.Parts = append(prepared.Parts, result)
		}
	}
	prepared.Parts = append(prepared.Parts, standalone...)
	if note := lostMediaNote(lost, prepared.Parts, incomplete(r)); note != "" {
		quote := ""
		if len(d.Parts) > 0 {
			quote = d.Parts[0].Quote
		}
		addLeadNote(&prepared, note, quote)
	}
	if err = p.Check(ctx); err != nil {
		return prepared, err
	}
	// 普通消息也要守住平台的单条文字上限（ADR-391）：never、画师推文、文字版路径都从 Prepare 出去，
	// 超了就按节点同一套切法拆成几条依次发，引用只挂第一条。
	maxBytes := d.nodeBytes
	if maxBytes <= 0 {
		maxBytes = linktemplate.DefaultForwardOverBytes
	}
	var split []types.Part
	for _, part := range prepared.Parts {
		pieces := splitLongMessage(part, maxBytes)
		markSplitPieces(pieces)
		split = append(split, pieces...)
	}
	prepared.Parts = split
	return prepared, nil
}

func (p *Preparer) imageTransport(ctx context.Context, loaded Loaded, caps types.MediaCaps) (Loaded, error) {
	if caps.ImageBase64 || loaded.Resource.FileAvailable() {
		return loaded, nil
	}
	if fileLoader, ok := p.Media.(fileImageLoader); ok {
		return fileLoader.FileImage(ctx, loaded)
	}
	return Loaded{}, errors.Join(errors.New("宿主未提供文件图片准备"), loaded.Resource.Release())
}

// degraded 记一次媒体退回文字：类别照 MediaClass，tag 是"图片、动图、视频、卡片"这类固定词，日志只写"模块 · 类别"加它。
// 取数层给了网络底层原因（Detail：已剥掉网址、限长，不含聊天原文）时一起附上，查“下载中断”不用再猜。
func (p *Preparer) degraded(tag string, err error) {
	class, outcome := MediaClass(err)
	var fe *linkreader.FetchError
	if errors.As(err, &fe) && fe.Detail != "" {
		tag += "，底层 " + fe.Detail
		err = fmt.Errorf("%w：%s", err, fe.Detail)
	}
	p.Degrade.Degrade(taskrun.ModuleMedia, class, err, taskrun.Opts{Outcome: outcome, Tag: tag})
}
