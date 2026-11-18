package linkreader

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

type opusPicture struct {
	URL    *string `json:"url"`
	Width  *int    `json:"width"`
	Height *int    `json:"height"`
}
type opusTextNode struct {
	Word *struct {
		Words *string `json:"words"`
	} `json:"word"`
	Rich *struct {
		Text     *string `json:"text"`
		OrigText *string `json:"orig_text"`
	} `json:"rich"`
	Formula *struct {
		Latex *string `json:"latex_content"`
	} `json:"formula"`
}
type opusText struct {
	Nodes *[]opusTextNode `json:"nodes"`
}
type opusParagraph struct {
	Type *int      `json:"para_type"`
	Text *opusText `json:"text"`
	List *struct {
		Items    *[]opusText `json:"items"`
		Children []struct{}  `json:"children"`
	} `json:"list"`
	Pic *struct {
		Pics *[]opusPicture `json:"pics"`
	} `json:"pic"`
}
type opusModule struct {
	Type  string `json:"module_type"`
	Title *struct {
		Text *string `json:"text"`
	} `json:"module_title"`
	Author *struct {
		Name      *string    `json:"name"`
		ID        decimalID  `json:"mid"`
		Face      *string    `json:"face"`
		Time      *string    `json:"pub_time"`
		Timestamp *decimalID `json:"pub_ts"`
	} `json:"module_author"`
	Content *struct {
		Paragraphs *[]opusParagraph `json:"paragraphs"`
	} `json:"module_content"`
	Top *struct {
		Display struct {
			Album *struct {
				Pics []opusPicture `json:"pics"`
			} `json:"album"`
		} `json:"display"`
	} `json:"module_top"`
	Stat *struct {
		Like *struct {
			Count *int64 `json:"count"`
		} `json:"like"`
		Comment *struct {
			Count *int64 `json:"count"`
		} `json:"comment"`
		Forward *struct {
			Count *int64 `json:"count"`
		} `json:"forward"`
	} `json:"module_stat"`
}
type opusItem struct {
	ID    string `json:"id_str"`
	Type  *int   `json:"type"`
	Basic struct {
		CommentID   string `json:"comment_id_str"` // 专栏的 cv 号；普通动态是动态自己的号
		CommentType int    `json:"comment_type"`   // 12 是专栏
		RID         string `json:"rid_str"`
	} `json:"basic"`
	Modules []opusModule `json:"modules"`
}

// opusFeatures 是老专栏（opus 号 38 开头）要带的参数；不带时 item 是 null、只给 fallback（实测）。
const opusFeatures = "htmlNewStyle,onlyfansVote,onlyfansAssetsV2,decorationCard,ugcDelete,editable"

// opusFallback 是 opus/detail 在 item 为 null 时给的退路：type 2 是专栏（文档写“已知 2 为专栏”，要带 features 再取），
// type 1 是 opus 渲染不了的普通动态（2026-11-13 实测一条转发动态，文档没写），改走 x/polymer/web-dynamic/v1/detail。
type opusFallback struct {
	Type lv[int]       `json:"type"`
	ID   lv[decimalID] `json:"id"`
}

// opusDetail 请求 opus/detail；features 为真时带上老专栏要的参数。
// item 为 null 但给了 fallback 时返回 (nil, fb, nil)，由调用方按 fb.Type 决定怎么退。
func (f *Fetcher) opusDetail(ctx context.Context, id string, features bool) (item *opusItem, fallback *opusFallback, err error) {
	var envelope struct {
		Code *int `json:"code"`
		Data *struct {
			Item     *opusItem        `json:"item"`
			Fallback *json.RawMessage `json:"fallback"`
		} `json:"data"`
	}
	q := url.Values{"id": {id}}
	if features {
		q.Set("features", opusFeatures)
	}
	const source = "bili_opus"
	// 遇风控补匿名访客标识重试一次（和专栏 detail 一样），否则专栏正文会在 detail 能过时仍丢
	if err := f.articleJSON(ctx, "https://api.bilibili.com/x/polymer/web-dynamic/v1/opus/detail?"+q.Encode(), source, &envelope, func() *int { return envelope.Code }); err != nil {
		return nil, nil, err
	}
	if envelope.Code == nil {
		return nil, nil, failure("invalid_response", source)
	}
	if *envelope.Code != 0 {
		return nil, nil, biliFailure(*envelope.Code, source)
	}
	if envelope.Data == nil {
		return nil, nil, failure("invalid_response", source)
	}
	if envelope.Data.Item == nil {
		var fb opusFallback
		if envelope.Data.Fallback != nil && string(*envelope.Data.Fallback) != "null" {
			_ = json.Unmarshal(*envelope.Data.Fallback, &fb) // 类型对不上也算“给了 fallback”，只是不知道 type
			return nil, &fb, nil
		}
		return nil, nil, nil
	}
	return envelope.Data.Item, nil, nil
}

func (f *Fetcher) fetchOpus(ctx context.Context, l Link) (Result, error) {
	const source = "bili_opus"
	item, fallback, err := f.opusDetail(ctx, l.ID, false)
	if err == nil && item == nil && fallback != nil {
		// type 1：opus 渲染不了的普通动态（转发等），直接换普通动态详情，不白费一次带 features 的请求。
		if fallback.Type.V != nil && *fallback.Type.V == 1 {
			return f.fetchDynamic(ctx, l)
		}
		item, fallback, err = f.opusDetail(ctx, l.ID, true) // 老专栏（type 2）：不带 features 取不到，带上再取一次
	}
	if err != nil {
		return Result{}, err
	}
	if item == nil && fallback != nil {
		return f.fetchDynamic(ctx, l) // 带了 features 还是 fallback（type 不认识）：最后试一次普通动态详情
	}
	if item == nil {
		return Result{}, failure("invalid_response", source)
	}
	if item.ID != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	r, err := opusResult(l, item)
	if err == nil {
		f.opusArticleCover(ctx, &r, item)
	}
	return r, err
}

// opusArticleCover 给“专栏转成的 opus”补封面。用专栏链接（read/cv）打开同一篇时，正文没有图就用专栏封面当图（fetchArticle）；
// opus/detail 自己不给封面，用 opus 链接打开就一张图都没有（样张 B-19，2026-11-13 复核）。
// 只在正文没有图、comment_type 是专栏、cv 号和 opus 号对得上时补；封面取不到不影响结果，预算紧时不取。
func (f *Fetcher) opusArticleCover(ctx context.Context, r *Result, item *opusItem) {
	if len(r.Media.Value) > 0 || item.Basic.CommentType != 12 || !digitsOnly(item.Basic.CommentID) || item.Basic.CommentID == item.ID {
		return
	}
	if !f.rate.spare("bilibili", optionalKeep) {
		return
	}
	brief, err := f.articleDetail(ctx, item.Basic.CommentID)
	if err != nil || brief == nil || brief.OpusID != item.ID || len(brief.Covers) == 0 {
		return
	}
	r.Media = available([]Media{})
	appendMedia(r, imageMetadata(available(httpsURL(brief.Covers[0]))))
}

// opusResult 把 opus/detail 的 item 排成统一结果；动态和专栏共用，身份核对由调用方做。
func opusResult(l Link, item *opusItem) (Result, error) {
	const source = "bili_opus"
	r := newResult(l, source)
	r.DynamicType = intField(item.Type)
	for _, key := range []string{"like", "comment", "forward"} {
		stat(&r, key, nil)
	}
	var body strings.Builder
	var paragraphsFound bool
	skippedNodes := 0
	var album []opusPicture
	media := make([]Media, 0)
	for _, module := range item.Modules {
		switch module.Type {
		case "MODULE_TYPE_TITLE":
			if module.Title != nil {
				r.Title = stringField(module.Title.Text)
			}
		case "MODULE_TYPE_AUTHOR":
			if a := module.Author; a != nil {
				r.Author.Name, r.Author.Avatar = stringField(a.Name), stringField(a.Face)
				if a.ID != "" {
					r.Author.ID = available(string(a.ID))
				}
				r.PublishedText = stringField(a.Time)
				if a.Timestamp != nil {
					ts, err := strconv.ParseInt(string(*a.Timestamp), 10, 64)
					if err != nil {
						return Result{}, failure("invalid_response", source)
					}
					r.Published = unixField(&ts)
				}
			}
		case "MODULE_TYPE_CONTENT":
			if module.Content == nil || module.Content.Paragraphs == nil {
				return Result{}, failure("invalid_response", source)
			}
			paragraphsFound = true
			for _, paragraph := range *module.Content.Paragraphs {
				if paragraph.Type == nil {
					return Result{}, failure("invalid_response", source)
				}
				switch *paragraph.Type {
				case 1:
					if paragraph.Text == nil || paragraph.Text.Nodes == nil {
						return Result{}, failure("invalid_response", source)
					}
					var text strings.Builder
					if err := writeOpusText(&text, paragraph.Text.Nodes, &skippedNodes); err != nil {
						return Result{}, err
					}
					appendOpusText(&r, &body, text.String(), paragraph.Type)
				case 2:
					if paragraph.Pic == nil || paragraph.Pic.Pics == nil {
						return Result{}, failure("invalid_response", source)
					}
					for _, pic := range *paragraph.Pic.Pics {
						appendOpusImage(&r, &media, pic, paragraph.Type)
					}
				case 5:
					if paragraph.List == nil || paragraph.List.Items == nil {
						return Result{}, failure("invalid_response", source)
					}
					var text strings.Builder
					for i, item := range *paragraph.List.Items {
						if i > 0 {
							text.WriteByte('\n')
						}
						if err := writeOpusText(&text, item.Nodes, &skippedNodes); err != nil {
							return Result{}, err
						}
					}
					appendOpusText(&r, &body, text.String(), paragraph.Type)
					// 本轮样本的 children 都为空；未知嵌套保留类型号并标不完整。
					if len(paragraph.List.Children) > 0 {
						r.Blocks = append(r.Blocks, Block{Type: "unsupported", ParagraphType: paragraph.Type})
						r.Text.Truncated = true
					}
				default:
					// 未覆盖的段落仍占原位置，不能把缺了一段的正文标成完整。
					r.Blocks = append(r.Blocks, Block{Type: "unsupported", ParagraphType: paragraph.Type})
					r.Text.Truncated = true
				}
			}
		case "MODULE_TYPE_TOP":
			if module.Top != nil && module.Top.Display.Album != nil {
				album = append(album, module.Top.Display.Album.Pics...)
			}
		case "MODULE_TYPE_STAT":
			if s := module.Stat; s != nil {
				if s.Like != nil {
					stat(&r, "like", s.Like.Count)
				}
				if s.Comment != nil {
					stat(&r, "comment", s.Comment.Count)
				}
				if s.Forward != nil {
					stat(&r, "forward", s.Forward.Count)
				}
			}
		}
	}
	if !paragraphsFound {
		return Result{}, failure("invalid_response", source)
	}
	// 部分普通动态的图放在顶部相簿；正文已有图时不再重复加相簿。
	if len(media) == 0 {
		for _, pic := range album {
			appendOpusImage(&r, &media, pic, nil)
		}
	}
	r.Media = available(media)
	r.Text.State, r.Text.Value, r.Text.Reason = "available", body.String(), ""
	if skippedNodes > 0 {
		r.Flags["skipped_text_nodes"] = available[jsonValue](skippedNodes)
	}
	return r, nil
}

func appendOpusImage(r *Result, media *[]Media, pic opusPicture, paragraphType *int) {
	image := Media{Type: "image", URL: stringField(pic.URL), Width: intField(pic.Width), Height: intField(pic.Height), Thumbnail: missing[string]("not_applicable"), Duration: missing[float64]("not_applicable"), Size: missing[int64]("interface_omitted")}
	*media = append(*media, image)
	r.Blocks = append(r.Blocks, Block{Type: "image", Media: &(*media)[len(*media)-1], ParagraphType: paragraphType})
}

// writeOpusText 把一段文字的节点拼起来：普通文字、富文本（表情、@、话题、链接）、公式。认不出的节点跳过、数进 skipped，
// 不让一个没见过的节点把整条动态判成失败（调用方把计数写进 Flags["skipped_text_nodes"]）。
func writeOpusText(out *strings.Builder, nodes *[]opusTextNode, skipped *int) error {
	if nodes == nil {
		return failure("invalid_response", "bili_opus")
	}
	for _, node := range *nodes {
		switch {
		case node.Word != nil && node.Word.Words != nil:
			out.WriteString(*node.Word.Words)
		case node.Rich != nil && node.Rich.Text != nil:
			out.WriteString(*node.Rich.Text)
		case node.Rich != nil && node.Rich.OrigText != nil:
			out.WriteString(*node.Rich.OrigText)
		case node.Formula != nil && node.Formula.Latex != nil:
			out.WriteString(*node.Formula.Latex)
		default:
			*skipped++
		}
	}
	return nil
}

func appendOpusText(r *Result, body *strings.Builder, text string, paragraphType *int) {
	if len(r.Blocks) > 0 {
		body.WriteByte('\n')
	}
	body.WriteString(text)
	r.Blocks = append(r.Blocks, Block{Type: "text", Text: text, ParagraphType: paragraphType})
}
