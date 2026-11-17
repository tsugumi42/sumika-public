package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/store"
)

// 媒体组合的种类名：场合（私聊 / 群）和种类拼起来做键。直接消息的媒体和转发节点里的媒体分开记（ADR-310）。
const (
	mediaImage     = "图片"
	mediaGIF       = "动图"
	mediaVideo     = "视频"
	mediaFwdImage  = "转发内图片"
	mediaFwdGIF    = "转发内动图"
	mediaFwdVideo  = "转发内视频"
	mediaBlockNone = ""
)

// mediaBlockTTL 是“能力缺失”降级的有效期：到期后下一条链接再试一次，成功就恢复，不用重启（2026-11-08）。
const mediaBlockTTL = 10 * time.Minute

// mediaBlocks 记下"某个场合的某种媒体，协议端明确说发不了这种段，而且同一段的文字版确认了"。只在内存里，到期自动恢复。
// 单张图被拒（内容审核等）不记：那不是这个场合的能力问题。它只收窄链接展开的媒体能力（prepareLink），不挡别的生产者。
type mediaBlocks struct {
	mu  sync.Mutex
	off map[string]time.Time // 场合|种类 -> 降级到期时间
}

// partMediaKinds 列一段里的媒体种类（去重，按出现的顺序）
func partMediaKinds(p types.Part) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		if k != mediaBlockNone && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, s := range p.Body {
		add(directMediaKind(s.Kind))
	}
	for _, n := range p.Nodes {
		for _, s := range n.Body {
			add(forwardMediaKind(s.Kind))
		}
	}
	return out
}

func directMediaKind(k string) string {
	switch k {
	case types.BodyImage:
		return mediaImage
	case types.BodyGIF:
		return mediaGIF
	case types.BodyVideo:
		return mediaVideo
	}
	return mediaBlockNone
}

func forwardMediaKind(k string) string {
	switch k {
	case types.BodyImage:
		return mediaFwdImage
	case types.BodyGIF:
		return mediaFwdGIF
	case types.BodyVideo:
		return mediaFwdVideo
	}
	return mediaBlockNone
}

// record 把一段的媒体组合记下来，返回这次新记下的种类（已经记过的不重复报）。
// 一段是一个发送单元，回执只到段；整段失败时分不出是哪一种媒体导致的，所以这段里的种类一起记。
// 多记只会让更多媒体退成文字，是收紧的方向，不违反"兜底只能收紧"（ADR-310）。
func (b *mediaBlocks) record(conv types.Conversation, p types.Part, now time.Time) []string {
	kinds := partMediaKinds(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.off == nil {
		b.off = map[string]time.Time{}
	}
	var fresh []string
	for _, k := range kinds {
		key := conv.Scene + "|" + k
		if until, ok := b.off[key]; !ok || !now.Before(until) {
			fresh = append(fresh, k)
		}
		b.off[key] = now.Add(mediaBlockTTL)
	}
	return fresh
}

// narrow 把记下失败的组合从能力里拿掉，其余原样。ImageBase64 不动：它是传输方式，不是媒体种类
func (b *mediaBlocks) narrow(scene string, c types.MediaCaps, now time.Time) (direct, forward types.MediaCaps) {
	b.mu.Lock()
	defer b.mu.Unlock()
	off := func(kind string) bool {
		until, ok := b.off[scene+"|"+kind]
		return ok && now.Before(until)
	}
	direct = types.MediaCaps{Image: c.Image && !off(mediaImage), ImageBase64: c.ImageBase64, Video: c.Video && !off(mediaVideo), GIF: c.GIF && !off(mediaGIF)}
	forward = types.MediaCaps{Image: c.ForwardImage && !off(mediaFwdImage), ImageBase64: c.ImageBase64, Video: c.ForwardVideo && !off(mediaFwdVideo), GIF: c.ForwardGIF && !off(mediaFwdGIF)}
	return direct, forward
}

// recordLinkMediaFailure 是 linkplugin 的 Block：记下组合，并把新记下的写一行日志（种类、场合、类别，不写正文、URL、令牌）
func (a *App) recordLinkMediaFailure(conv types.Conversation, p types.Part, category string) {
	fresh := a.mediaBlocks.record(conv, p, a.now())
	if len(fresh) == 0 {
		return
	}
	scene := "私聊"
	if conv.Scene == types.SceneGroup {
		scene = "群"
	}
	a.logf("链接展开：%s里的%s发送明确失败（%s），文字版已确认，之后 %d 分钟内这种媒体退成文字，到期自动再试", scene, strings.Join(fresh, "、"), category, int(mediaBlockTTL/time.Minute))
}

// linkOutcomes 是 linkplugin 的 Outcomes：从出口的计划读每段终态。失败类别按适配器的原因归成两类，不回传原文
func (a *App) linkOutcomes(conv types.Conversation, planID string) ([]linkplugin.PartOutcome, error) {
	plan, err := a.outlet.Plan(planID, conv)
	if err != nil {
		return nil, err
	}
	out := make([]linkplugin.PartOutcome, 0, len(plan.Parts))
	for _, p := range plan.Parts {
		o := linkplugin.PartOutcome{Index: p.Idx, State: p.State, SentAtMS: p.SentAt}
		switch p.State {
		case store.PartFailed:
			o.Category = failureCategory(p.Err)
		case store.PartConfirmed:
			o.PlatformID = p.PlatformID
		case store.PartUnknown:
			o.NoReceipt = types.AwaitsEcho(p.Err) // 没确认送达、没回包、超时、写出后断线：等回声（规格-链接发送改版 A1）
		}
		out = append(out, o)
	}
	return out, nil
}

func failureCategory(reason string) string {
	switch {
	case strings.Contains(reason, "不支持"):
		return linkplugin.CategoryUnsupported
	case strings.Contains(reason, "协议端拒绝"):
		return linkplugin.CategoryRejected
	}
	return linkplugin.CategoryOther
}

// linkAfter 向协议端取会话里源消息之后的消息，给链接展开核实“没拿到回执”的那一条有没有进群（ADR-333）。
// 适配器没有这个能力（不是 QQ）就查不了。
func (a *App) linkAfter(ctx context.Context, conv types.Conversation, anchor string) (types.AfterPage, error) {
	if a.adapter == nil {
		return types.AfterPage{}, errors.New("没有可查历史的适配器")
	}
	return a.adapter.HistoryAfter(ctx, a.account, conv, anchor)
}
