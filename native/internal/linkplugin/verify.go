package linkplugin

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"strings"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/taskrun"
)

// 等不到回声以后可选的翻历史核实（ADR-333；规格-链接发送改版 A1 第 4 条起默认关，开着时只查一次、不再等 4 秒 + 6 秒）。
//
// 背景：SnowLuma 发群消息时 QQ 回了 result=0 却没有 sequence，就报 “delivery was not confirmed”。2026-11-09 实测：
// 同一张被卡的图普通发和合并转发都是这个回包、消息确实没进群；转 180° 以后同样的回包却不出现、进群了。
// 所以这个回包既不能一律当拒绝（可能其实到了，重发就是重复），也不能一律当结果不明（被卡的图永远走不到转图）。
// 做法：向协议端取这个会话里源消息之后的消息，找“就是这一条”。结论三种：
//   - 到了：按成功算，不补发；
//   - 没到：按“确实没到”算，和明确拒绝同类，走下一种发法，并记进按图的结论；
//   - 查不了：仍是结果不明，不换形式、不补发，日志写原因。
//
// 认出“就是这一条”的依据（全部成立才算到了）：
//  1. 发言人是自己的号，在这个会话里，平台时间不早于这一次发送开始前 8 秒（QQ 时间精度整秒，本机时钟偏快几秒很常见）；
//  2. 不是这次展开里已经确认的别的段（按平台消息号排除）；
//  3. 形状对得上：直接发的，图片段数一样、文字（去掉首尾空白）一样、不是转发；合并转发的，恰有一个转发段、没有图；
//  4. 图的身份：发出去的图的 md5 是协议端自己记的图片标识（SnowLuma 上传日志写的就是它，收到的图片 file 是“<md5>.扩展名”，
//     旧式图片地址里也有它）。候选里读得出 32 位十六进制标识的，必须有一个等于我们发的 md5（这是“强对上”）；
//     标识全都读不出（NT 的图片 file 可能是不透明的 fileId）就只凭 1～3，这是“形状对上”；标识读得出却对不上，说明多半是别的图，不算到了；但页里若有这样形状一致的自己号消息，也不断言“没到”，按查不了。
//
// 为什么这样可靠：同一个号、同一个会话、发送开始之后、形状一样、图的标识一致（或读不出），这个窗口里同时出现另一条无关消息的可能极小；
// 判断偏向“到了/查不了”，只有在翻页翻到了尽头、窗口里确实没有任何对得上的消息时才判“没到”，所以宁可少补发也不重复发。
// 另有几条偏向“查不了”的保护：时间早于窗口但形状一致的自己号消息（时钟误差的可能）、图标识对不上但形状一致的消息，都不判“没到”；
// 历史页里有翻译不了的消息时，页只证明“有”（适配器把 ReachedLatest 置假）。
// 残余风险写在规格里：SnowLuma 向服务器取历史失败时会回退到本地库，Go 端分不出，本地库里没有的也会被判成“没到”。

// 每次查询的时限：适配器自己的查询时限是 45 秒（历史动作在协议端要十几次往返），核实不等那么久，到点就按查不了
const defaultVerifyTimeout = 10 * time.Second

// probe 是要找的那一条的特征。必须在发送之前从备份算好：发出去以后出口会释放媒体。
type probe struct {
	forward bool
	images  []string // 每张图内容的 md5（小写十六进制），按出现顺序
	text    string   // 直接发的正文文字（规范化后）；转发不比
}

// probeOf 取一段的特征；有媒体段没有字节（视频、超大、已释放）就取不了，返回 false（这样的段只会是“查不了”）。
func probeOf(p types.Part) (probe, bool) {
	pr := probe{forward: p.Kind == types.PartForward}
	segs := mediaSegs(p)
	if len(segs) == 0 {
		return probe{}, false
	}
	for _, s := range segs {
		if s.Kind != types.BodyImage && s.Kind != types.BodyGIF {
			return probe{}, false
		}
		if s.Media == nil {
			return probe{}, false
		}
		h := md5.New()
		if err := s.Media.Read(func(r io.Reader) error { _, e := io.Copy(h, r); return e }); err != nil {
			return probe{}, false
		}
		pr.images = append(pr.images, hex.EncodeToString(h.Sum(nil)))
	}
	if !pr.forward {
		pr.text = normText(types.BodyTextOf(p.Body))
	}
	return pr, true
}

func normText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// verifyKind 是核实的三种结论
type verifyKind int

const (
	verifyUnknown verifyKind = iota // 查不了
	verifyArrived                   // 到了
	verifyAbsent                    // 没到
)

// judge 在一页“源消息之后的消息”里找这一条。account 是发送用的号；since 是这一次发送开始的时刻；exclude 是已经确认的别的段的平台消息号。
// 返回结论和一句不含正文的依据（写日志用）。
func judge(page types.AfterPage, pr probe, account string, since time.Time, exclude map[string]bool) (verifyKind, string) {
	cut := since.Unix() - verifyClockSlack
	weak, doubt := "", ""
	for _, m := range page.Messages {
		if m.From.ID != account || exclude[m.PlatformID] {
			continue
		}
		ok, strong := shapeMatches(m.Segs, pr)
		if m.Time.Unix() < cut {
			// 比发送开始早得多的同形状消息多半是更早发的；但本机时钟比 QQ 快时真到了的也会落在这里，所以只记疑点，不当到了
			if ok && doubt == "" && m.Time.Unix() >= cut-verifyClockDoubt {
				doubt = "有自己号发的同形状消息，但时间早于发送开始，可能是时钟误差"
			}
			continue
		}
		if !ok {
			if nearMiss(m.Segs, pr) && doubt == "" {
				doubt = "有自己号发的同形状消息，但图的标识对不上"
			}
			continue
		}
		if strong {
			return verifyArrived, "找到自己号发的同形状消息，图的 md5 对上"
		}
		if weak == "" {
			weak = "找到自己号发的同形状消息（图的标识读不出，凭形状和时间对上）"
		}
	}
	if weak != "" {
		return verifyArrived, weak
	}
	if doubt != "" {
		// 宁可少补发也不重复：有任何可能就是这一条的消息，都不能断言没到
		return verifyUnknown, doubt + "，不能断言没到"
	}
	if page.ReachedLatest {
		return verifyAbsent, "翻到了最新，发送开始之后自己号在这个会话里没有对得上的消息"
	}
	return verifyUnknown, "历史没翻到最新，没找到不能说明没到"
}

// verifyClockSlack 是本机时钟和 QQ 服务器时钟允许的误差（秒）：QQ 时间是整秒，本机时钟偏快几秒很常见
const verifyClockSlack = 8

// verifyClockDoubt 是窗口之外再往前多少秒内的同形状消息仍算“可能是时钟误差”的疑点；更早的当作更早发的无关消息
const verifyClockDoubt = 30

// nearMiss 说一条消息是不是“形状和要找的直发图一样（图数、文字、非转发），只是图的标识读得出却对不上”
func nearMiss(segs []types.Seg, pr probe) bool {
	if pr.forward {
		return false
	}
	imgs := 0
	for _, s := range segs {
		switch s.Kind {
		case types.SegImage:
			imgs++
		case types.SegForward:
			return false
		}
	}
	return imgs == len(pr.images) && normText(channel.PlainTextNoMedia(segs)) == pr.text
}

// shapeMatches 说一条历史消息的段和要找的特征对不对得上；strong 为真表示图的 md5 也对上了（图的身份规则和回声认领共用 channel.MatchImageIDs）
func shapeMatches(segs []types.Seg, pr probe) (ok, strong bool) {
	var imgs []types.Seg
	forwards := 0
	for _, s := range segs {
		switch s.Kind {
		case types.SegImage:
			imgs = append(imgs, s)
		case types.SegForward:
			forwards++
		}
	}
	if pr.forward {
		return forwards == 1 && len(imgs) == 0, false
	}
	if forwards != 0 || len(imgs) != len(pr.images) || normText(channel.PlainTextNoMedia(segs)) != pr.text {
		return false, false
	}
	return channel.MatchImageIDs(imgs, pr.images)
}

// historyCheck 是等不到回声以后的可选核实（linkReader.expand.send.historyCheck，默认关）：立刻翻一次历史，
// 把“没确认送达”改写成到了 / 没到，查不了就原样返回（结果不明，不换形式、不补发）。只翻一次，不再等。
// 大图（≥ 2 MiB）以前在这里“历史里没找到只算结果不明”（B12 第 4 条）；B15 取消：等回声已经按体积放宽到 30 秒上下，
// 还没有就当没送到换形式，晚到的回声由 late.go 的看守撤回后补发的那份。
func (d *delivery) historyCheck(o PartOutcome, pr probe, hasProbe bool, label string) PartOutcome {
	ex := d.p.opt.Expand
	if ex.After == nil || ex.Account == "" {
		d.logf("%s：没等到回声，这个渠道查不了历史，按结果不明处理", label)
		d.verifyStep("查不了")
		return o
	}
	if !hasProbe {
		d.logf("%s：没等到回声，但这一段取不到可对照的图（视频、超大或已释放），按结果不明处理", label)
		d.verifyStep("查不了")
		return o
	}
	queryTimeout := ex.VerifyTimeout
	if queryTimeout <= 0 {
		queryTimeout = defaultVerifyTimeout
	}
	since := time.UnixMilli(o.SentAtMS)
	if o.SentAtMS <= 0 {
		since = d.p.opt.Now()
	}
	qctx, cancel := context.WithTimeout(d.run.ctx, queryTimeout)
	page, err := ex.After(qctx, d.conv, d.quote)
	cancel()
	if err != nil {
		d.logf("%s：核实取历史失败（%s），按结果不明处理，不换形式、不补发", label, reason(err))
		d.verifyStep("查不了")
		return o
	}
	kind, why := judge(page, pr, ex.Account, since, d.excluded)
	switch kind {
	case verifyArrived:
		d.logf("%s：核实到了（%s），按成功算，不补发", label, why)
		d.verifyStep("到了")
		o.State = OutcomeConfirmed
	case verifyAbsent:
		d.logf("%s：核实没到（%s），按确实没到处理，换形式", label, why)
		d.verifyStep("没到")
		o.State, o.Category, o.noEcho = OutcomeFailed, CategoryRejected, true
	default:
		d.logf("%s：核实查不了（%s），按结果不明处理，不换形式、不补发", label, why)
		d.verifyStep("查不了")
	}
	return o
}

// verifyStep 记一行运行页（link · form_degrade）：到了算成功，没到（含等不到回声）算失败，查不了算降级
func (d *delivery) verifyStep(result string) {
	outcome, tag := taskrun.OutcomeDegraded, "核实："+result
	switch result {
	case "到了":
		outcome = taskrun.OutcomeOK
	case "没到":
		outcome = taskrun.OutcomeFailed
	case "没回声":
		outcome, tag = taskrun.OutcomeFailed, "回声：没到"
	}
	d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: outcome, Tag: tag})
}
