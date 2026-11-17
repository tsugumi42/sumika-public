package linkreader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// B 站评论：取前三条热评（置顶的排最前），给她看，也能在展开里写出来。
//
//	x/v2/reply?oid=<视频 aid 或专栏 cv 号>&type=<1 视频，12 专栏>&pn=1&ps=3&sort=2：匿名 code 0；WBI 版不签名回 -403，所以走旧接口
//
// 评论取不到不影响主结果，也不当成失败：Comments 字段写明白原因（评论不公开、评论区关闭、暂无评论、这次没取到），
// 文案由 CommentsText 统一给。充电专属视频未充电时评论区只显示 UP 主精选，接口回 0 条但 control 里写着“精选后对所有人可见”，
// 这种按“评论不公开”处理，不说成“暂无评论”。
const commentsSource = "bili_comments"

// Comment 是一条评论的展示字段；回复预览不取。
type Comment struct {
	Author  string `json:"author"`
	Text    string `json:"text"`
	Likes   int64  `json:"likes"`
	Replies int64  `json:"replies"` // 楼中楼条数
	Top     bool   `json:"top"`     // UP 主置顶
	Time    int64  `json:"time_unix,omitempty"`
}

const (
	commentsMax     = 3
	commentTextRune = 200
)

type biliReply struct {
	RPID   decimalID `json:"rpid"`
	Like   *int64    `json:"like"`
	RCount *int64    `json:"rcount"`
	CTime  *int64    `json:"ctime"`
	Member struct {
		Name *string `json:"uname"`
	} `json:"member"`
	Content struct {
		Message *string `json:"message"`
	} `json:"content"`
}

// optionalKeep 是可有可无的 B 站请求（评论、专栏统计）要给别的链接留的预算：
// 这一分钟剩余请求数不多于它就不取。3 是一条视频完整展开要的次数，连发几条链接时主请求不会被评论挤掉。
const optionalKeep = 3

// biliComments 取评论；type 1 是视频（oid 是 aid），12 是专栏（oid 是 cv 号）。
func (f *Fetcher) biliComments(ctx context.Context, oid string, typ int) Field[[]Comment] {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Page struct {
				Count *int64 `json:"count"`
			} `json:"page"`
			Replies []biliReply `json:"replies"`
			Upper   struct {
				Top *biliReply `json:"top"`
			} `json:"upper"`
			Control struct {
				RootInput string `json:"root_input_text"`
			} `json:"control"`
		} `json:"data"`
	}
	if !f.rate.spare("bilibili", optionalKeep) {
		return Field[[]Comment]{State: "not_returned", Reason: "budget_saved"}
	}
	q := url.Values{"oid": {oid}, "type": {strconv.Itoa(typ)}, "pn": {"1"}, "ps": {strconv.Itoa(commentsMax)}, "sort": {"2"}}
	err := f.getJSON(ctx, "https://api.bilibili.com/x/v2/reply?"+q.Encode(), commentsSource, &e)
	if err == nil && e.Code == nil {
		err = failure("invalid_response", commentsSource)
	}
	if err == nil && *e.Code != 0 {
		if *e.Code == 12002 { // 评论区已关闭
			return Field[[]Comment]{State: "not_returned", Reason: "comments_closed"}
		}
		err = biliFailure(*e.Code, commentsSource)
	}
	if err == nil && e.Data == nil {
		err = failure("invalid_response", commentsSource)
	}
	if err != nil {
		reason := "interface_error"
		var fe *FetchError
		if errors.As(err, &fe) {
			reason = fe.Kind
		}
		return Field[[]Comment]{State: "failed", Reason: reason}
	}
	var out []Comment
	add := func(r *biliReply, top bool) {
		if r == nil || len(out) >= commentsMax {
			return
		}
		text := strings.TrimSpace(deref(r.Content.Message))
		if text == "" {
			return
		}
		c := Comment{Author: strings.TrimSpace(deref(r.Member.Name)), Text: text, Top: top}
		if head, cut := CutRunes(c.Text, commentTextRune); cut {
			c.Text = head + "…"
		}
		if r.Like != nil {
			c.Likes = *r.Like
		}
		if r.RCount != nil {
			c.Replies = *r.RCount
		}
		if r.CTime != nil && *r.CTime > 0 {
			c.Time = *r.CTime
		}
		out = append(out, c)
	}
	add(e.Data.Upper.Top, true)
	for i := range e.Data.Replies {
		if e.Data.Upper.Top != nil && e.Data.Replies[i].RPID == e.Data.Upper.Top.RPID {
			continue // 置顶那条已经放在最前面
		}
		add(&e.Data.Replies[i], false)
	}
	if len(out) > 0 {
		return available(out)
	}
	if strings.Contains(e.Data.Control.RootInput, "精选") {
		return Field[[]Comment]{State: "not_returned", Reason: "comments_selected_only"}
	}
	return Field[[]Comment]{State: "not_returned", Reason: "no_comments"}
}

// CommentsText 把评论字段写成给人看的文字：有评论时是“热评：”加几行，没有就是一句明确的说明。
// 行内的作者名、评论正文都是外部文字，调用方按需转义；这里只管排版。限制每条字数 limit（0 不限）。
func CommentsText(c Field[[]Comment], limit int) string {
	if c.State == "available" && len(c.Value) > 0 {
		var b strings.Builder
		b.WriteString("热评：")
		for i, one := range c.Value {
			text := strings.ReplaceAll(one.Text, "\n", " ")
			if limit > 0 {
				if head, cut := CutRunes(text, limit); cut {
					text = head + "…"
				}
			}
			who := one.Author
			if who == "" {
				who = "匿名"
			}
			mark := ""
			if one.Top {
				mark = "［置顶］"
			}
			fmt.Fprintf(&b, "\n%d. %s%s：%s（赞 %d）", i+1, mark, who, text, one.Likes)
		}
		return b.String()
	}
	switch c.Reason {
	case "comments_selected_only":
		return "评论不公开（UP 主精选后才可见）"
	case "comments_closed":
		return "评论区已关闭"
	case "no_comments":
		return "暂无评论"
	case "risk_control":
		return "评论这次没取到（被平台拦了）"
	case "rate_limited":
		return "评论这次没取到（请求太频繁）"
	case "budget_saved":
		return "评论这次没取（这一分钟的请求额度留给别的链接）"
	case "":
		return ""
	}
	if c.State == "failed" {
		return "评论这次没取到"
	}
	return ""
}
