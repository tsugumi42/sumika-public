package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/llm"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// imageWait 是组本轮请求前最多等一条消息里的图下载完、算出哈希的时间。QQ 图片下载是异步的，消息一到回复就开始，
// 图常常还没落盘；等是为了让她第一次回话就看得到图。日志里下载多在几秒内完成，超过 8 秒回复就像卡住了，所以到点不再等：
// 照常回复、文字里写明图还没下完，图随后下完且这一轮还没结束的话，作为补充送给她（turn.LateImages）。
const imageWait = 8 * time.Second

// imageGap 是"这条消息里的图没能给她看"的原因，写进给她的文字占位，让她能如实说"还没下完""太大没下"，不假装看见了。
type imageGap struct {
	why     string // 给她看的短原因
	pending bool   // 只是还没下完（超时）；下完了还能补图
}

func (g imageGap) Error() string { return g.why }

// mediaGap 把 media_fetch 的终态翻成给她看的原因；还在下（pending）或已下完（done）返回 false。
func mediaGap(state string) (imageGap, bool) {
	switch state {
	case store.MediaPending, store.MediaDone, "":
		return imageGap{}, false
	case store.MediaTooBig:
		return imageGap{why: "图片太大，没有下载"}, true
	case store.MediaExpired:
		return imageGap{why: "图片链接已过期，没能下载"}, true
	case store.MediaBlocked:
		return imageGap{why: "图片地址不在允许范围，没有下载"}, true
	case store.MediaNotImage:
		return imageGap{why: "下载到的不是图片"}, true
	case store.MediaFailed:
		return imageGap{why: "图片下载失败了"}, true
	default:
		return imageGap{why: "图片没有下载（" + state + "）"}, true
	}
}

// prepareImages 只看这一轮自己的、她看得到的主人事件（QQ 按段存图，本地私聊按消息块的附件存图）；历史不改。
func (a *App) prepareImages(ctx context.Context, in turn.Input, model llm.Model) (string, []llm.Image) {
	if !directScene(in) {
		return in.Text, nil
	}
	st, err := a.set.ForConversation(in.Conversation)
	if err != nil {
		a.logf("真图取库失败：%v", err)
		return in.Text, nil
	}
	text := in.Text
	var images []llm.Image
	for _, seq := range in.Seqs {
		row, ok, err := st.ReadImageInput(ctx, in.CharacterID, in.Conversation, seq)
		if err != nil || !ok {
			if err != nil {
				a.logf("真图读事件失败 seq=%d：%v", seq, err)
			}
			continue
		}
		count, reason := a.directReason(row, model)
		if count == 0 {
			continue
		}
		old := row.Line
		if old == "" {
			old = row.Text
		}
		fallback := row.Seen // 事件里已有的识图描述（Bun 时代留下的）；没有就沿用原来的文字占位
		var prepared []llm.Image
		var gap imageGap
		// A cached description needs no file or model request on the fallback path.
		if reason == "" {
			loadCtx, cancel := context.WithTimeout(ctx, a.imageWait)
			prepared, err = a.loadInputImages(loadCtx, st, in, row)
			cancel()
			if err != nil {
				gap = a.gapOf(err, a.imageWait)
				reason = "原图准备失败：" + gap.why
				if gap.pending {
					a.lateImages(ctx, st, in, row, model)
				}
			}
		}
		if reason == "" {
			images = append(images, prepared...)
			a.logf("真图直给 conversation=%s seq=%d model=%s images=%d", in.Conversation, seq, model.ID, len(prepared))
			continue
		}
		a.logf("真图未直给 conversation=%s seq=%d model=%s reason=%s", in.Conversation, seq, model.ID, reason)
		if fallback != "" && old != "" && strings.Contains(text, old) {
			text = strings.Replace(text, old, fallback, 1)
		} else if gap.why != "" {
			text += "\n[图片没给你看：" + gap.why + "]"
		}
	}
	return text, images
}

// directScene：只有本地私聊和 QQ 私聊里主人自己的话能直给图；两个群场合、别人的话都不行。
func directScene(in turn.Input) bool {
	s := in.Identity.Scope
	return s.ActorKind == "owner" && s.Scene == "private" && in.Audience.Scene == "private" && !strings.Contains(in.Conversation, ":group:") && (s.Channel == "console" || s.Channel == "qq")
}

// directReason 数这条消息带几张图，并判断能不能直给：返回空串就是可以，否则是不直给的原因（写进日志）。
func (a *App) directReason(row store.ImageInput, model llm.Model) (count int, reason string) {
	count, forward := len(row.Local), false
	for _, seg := range row.Segs {
		if seg.Kind == types.SegImage || seg.Kind == types.SegEmoji {
			count++
		}
		if seg.Kind == types.SegForward {
			forward = true
		}
	}
	switch {
	case count == 0:
	case !a.cfg.Vision.Direct:
		reason = "vision.direct 关闭"
	case !model.SupportsImages():
		reason = "模型未声明 image 输入"
	case count > a.cfg.Vision.DirectMax:
		reason = "超过 vision.directMax"
	case forward:
		reason = "消息含合并转发"
	}
	return count, reason
}

// restoreImages 实例重建后给历史里第 seq 条主人消息取回它当初带的图：条件和第一次直给相同，取不到就留文字占位并写一行原因。
func (a *App) restoreImages(ctx context.Context, in turn.Input, seq int64, model llm.Model) []llm.Image {
	if !directScene(in) {
		return nil
	}
	st, err := a.set.ForConversation(in.Conversation)
	if err != nil {
		a.logf("真图重建取库失败：%v", err)
		return nil
	}
	row, ok, err := st.ReadImageInput(ctx, in.CharacterID, in.Conversation, seq)
	if err != nil {
		a.logf("真图重建读事件失败 seq=%d：%v", seq, err)
		return nil
	}
	if !ok {
		return nil
	}
	count, reason := a.directReason(row, model)
	if count == 0 {
		return nil
	}
	if reason == "" {
		loadCtx, cancel := context.WithTimeout(ctx, a.imageWait)
		images, err := a.loadInputImages(loadCtx, st, in, row)
		cancel()
		if err == nil {
			return images
		}
		reason = "原图准备失败：" + err.Error()
	}
	a.logf("真图重建未带 conversation=%s seq=%d model=%s reason=%s", in.Conversation, seq, model.ID, reason)
	return nil
}

func (a *App) loadInputImages(ctx context.Context, st *store.Store, in turn.Input, row store.ImageInput) ([]llm.Image, error) {
	sub := st.SubscribeChanges()
	defer sub.Close()
	var ok bool
	var err error
	row, ok, err = st.ReadImageInput(ctx, in.CharacterID, in.Conversation, row.Seq)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("图片事件已不可见")
	}
	waited := false
	startedAt := time.Now()
	for {
		pending := 0
		for _, seg := range row.Segs {
			if (seg.Kind == types.SegImage || seg.Kind == types.SegEmoji) && seg.Hash == "" {
				pending++
			}
		}
		if pending == 0 {
			if waited {
				a.logf("等到图了 conversation=%s seq=%d 用时 %s", in.Conversation, row.Seq, time.Since(startedAt).Round(time.Millisecond))
			}
			break
		}
		if in.Identity.Scope.Channel != "qq" || a.media == nil {
			return nil, imageGap{why: "图片原件不在本机（没有开图片下载）"}
		}
		states, err := st.MediaFetchRows(row.Seq)
		if err != nil {
			return nil, err
		}
		for _, state := range states {
			if state.Kind != types.SegImage && state.Kind != types.SegEmoji {
				continue
			}
			if gap, bad := mediaGap(state.State); bad {
				return nil, gap
			}
		}
		if !waited {
			waited = true
			a.logf("等图 conversation=%s seq=%d 还有 %d 张没下完", in.Conversation, row.Seq, pending)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-sub.Wake():
			sub.Take()
			var ok bool
			row, ok, err = st.ReadImageInput(ctx, in.CharacterID, in.Conversation, row.Seq)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("图片事件已不可见")
			}
		}
	}
	platform := in.Identity.Scope.Platform
	var images []llm.Image
	add := func(f *os.File, err error) error {
		if err != nil {
			return err
		}
		data, mime, prepErr := mediastore.PrepareImage(f)
		closeErr := f.Close()
		if err := errors.Join(prepErr, closeErr); err != nil {
			return err
		}
		images = append(images, llm.Image{DataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)})
		return nil
	}
	for _, img := range row.Local {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if img.Path != "" {
			f, err := os.Open(img.Path)
			if err = add(f, err); err != nil {
				return nil, err
			}
			continue
		}
		p := platform
		if img.Conversation != "" {
			var err error
			if p, err = store.PlatformOfConversation(img.Conversation); err != nil {
				return nil, err
			}
		}
		f, err := mediastore.OpenMedia(a.paths.Root, p, img.Hash)
		if err = add(f, err); err != nil {
			return nil, err
		}
	}
	for _, seg := range row.Segs {
		if seg.Kind != types.SegImage && seg.Kind != types.SegEmoji {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := mediastore.OpenMedia(a.paths.Root, platform, seg.Hash)
		if err = add(f, err); err != nil {
			return nil, err
		}
	}
	return images, nil
}

// gapOf 把准备图片时的错误翻成给她看的原因：超时是"还没下完"（可以补图），其余照 imageGap 或错误原文。
func (a *App) gapOf(err error, waited time.Duration) imageGap {
	var gap imageGap
	switch {
	case errors.As(err, &gap):
		return gap
	case errors.Is(err, context.DeadlineExceeded):
		return imageGap{why: fmt.Sprintf("图片还没下载完（等了 %d 秒）", int(waited.Seconds()+0.5)), pending: true}
	default:
		return imageGap{why: "图片文件读不出来：" + err.Error()}
	}
}

// lateImages 在等图超时后继续等：图下完而这一轮还没结束，就作为补充送给她；这一轮结束（ctx 取消）就放弃。
// 下载本身是异步的，不受这里影响，下完的图以后重建历史时仍会按事件取回。
func (a *App) lateImages(turnCtx context.Context, st *store.Store, in turn.Input, row store.ImageInput, model llm.Model) {
	go func() {
		images, err := a.loadInputImages(turnCtx, st, in, row)
		if err != nil {
			a.logf("补图放弃 conversation=%s seq=%d：%v", in.Conversation, row.Seq, err)
			return
		}
		if a.hub.LateImages(in.Identity.RunID, images, "（刚才你没看到的那条消息，图片现在下载完了，补给你看：）") {
			a.logf("补图已排队 conversation=%s seq=%d images=%d", in.Conversation, row.Seq, len(images))
			return
		}
		a.logf("补图没送上 conversation=%s seq=%d：这一轮已经结束", in.Conversation, row.Seq)
	}()
}
