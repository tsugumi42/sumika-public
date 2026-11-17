package linkplugin

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/taskrun"
)

// 图发不出去的降级（规格-链接发送改版 A2～A7，改写了 ADR-333 的逐张拆开和翻历史核实）。
//
// 阶梯写死三级，整段一起换形式，不拆开、不猜是哪一张：
//  1. 原样发；
//  2. 这一段里所有静态图一起转 180°，同一个容器重发一次（直接发的还是一条消息、图文照旧在一起；合并转发的还是合并转发，节点里的图全转）；
//  3. 文字版（带原因、试过哪几种、原链接），只补一次。
//
// 触发第 2 级的：协议端明确拒收这一张（failed 且是“协议端拒绝”），或没确认送达又在 echoWaitMS 内等不到回声（echo.go）。
// 能力缺失（协议端说这个场合发不了这种段）转图帮不上，照旧换不转的合并转发试一次，再不行文字版并记场合降级；
// 动图、视频不转：动图原样 → 文字版（能力缺失时仍可走不转的合并转发），视频被拒直接文字版。
// rotateOnReject 关着时第 2 级换成不转的合并转发。网站的 imageMode：rotate 从第 2 级起，forward 起步就包成合并转发并转图，不成直接文字版。
// 被出口中止的后续段（从没发出过）原样补发，不跟着变形。每一步写 link · form_degrade（转 180° 那步另写 link · rotate_retry），不写正文。

// maxSettleDepth 限制“补发后续段”的递归层数（每层至少去掉一段，不会无限）
const maxSettleDepth = 8

// errAborted：这一步被取消（展开关了、源消息撤回、关闭）或没能入队，不再继续换形式
var errAborted = errors.New("发送被中止")

// textItem 是最后要并进文字版的一块：原因类别，以及已经试过哪几种形式（都被拒）
type textItem struct {
	part     types.Part
	category string
	tried    []form
	guard    *lateGuard // 这一块是大图原段等不到回声退下来的：原段的回声晚到，文字版就是要撤的补发份（late.go）；nil 的不参与
}

// sendItem 是一个待发的段：part、res 是第一次发用的（准备好的原资源），backup 是留好的备份；
// 补发、换形式时没有 part，现从 backup 拷一份发。
type sendItem struct {
	part   types.Part
	res    []*types.MediaResource
	hasNow bool
	backup types.Part
}

// delivery 管一个结果计划从发出去到收尾的全过程
type delivery struct {
	p        *Plugin
	run      *expansionRun
	conv     types.Conversation
	snap     Snapshot
	send     config.LinkSend           // 冻结的发送流程设置（等回声、核实、两个实验）
	site     string                    // 插件日志的 site、url
	url      string                    //
	link     string                    // 原链接（文字版附上）
	quote    string                    // 源消息号，文字版引用它
	nodeName string                    // 合并转发里节点的显示名退路（配置没写、QQ 昵称又取不到时用）
	fwd      linkexpand.ForwardOptions // 配置里合并转发的节点名和卡片文字
	result   *linkreader.Result
	items    []textItem
	mode     string          // 这个网站的发图方式（auto / rotate / forward），决定阶梯从哪一级开始
	excluded map[string]bool // 这次展开里已确认的段的平台消息号，翻历史核实“就是这一条”时排除
	known    []types.Part    // 已经确认送到的段，文字版据此判断链接是不是已经出现过
	unsure   []types.Part    // 结果不明的段：不补发，只是记下
	blocks   []types.Part    // 协议端明确说发不了的形式，文字版确认后记组合（能力缺失按场合降级）
	videos   *videoKeep      // 合并转发里的视频留的硬链接（视频进转发不支持时单独补发用）
	hardFail bool            // 有段没送到又没有补救
	broken   bool            // 被取消或没能入队：不再发任何东西
	why      []string        // 没送到的原因（写进插件日志的 error），不含正文
	textOdd  bool            // 文字版结果不明
	// textLanded 是文字版确认送到了
	textLanded bool
	// guard 是正在替哪个大图原段（等不到回声、已按没送到换形式）补发：补发出去并确认的份记在它名下，原段的回声晚到就撤这些份（late.go）。
	// 只在 settle 处理那一段失败的这一小段时有值，别的段的确认不当补发份
	guard *lateGuard
	// hint 是阶梯里这一级发之前给的“按图的结论用哪个键、哪一级”：这一级没回声要开看守时用（同一个协程，发完清掉）
	hint *guardHint
}

// sendReport 是一条展开发完的结论（写插件日志用）
type sendReport struct {
	delivered bool   // 没有硬失败（全部确认，或补发的文字版确认；结果不明也不算失败，见 landed）
	landed    bool   // 至少有一段、或补发的文字版确认送到了：群里确实有东西。只有结果不明、没有一段确认时不贴“完成”表情（B15）
	unsure    bool   // 有段结果不明
	fallback  bool   // 有媒体没发出去，退了文字版
	broken    bool   // 被取消或没能入队
	why       string // 没送到的原因
}

// failWhy 记一条没送到的原因（最多留 4 条）
func (d *delivery) failWhy(format string, args ...any) {
	if len(d.why) < 4 {
		d.why = append(d.why, fmt.Sprintf(format, args...))
	}
}

func (d *delivery) logf(format string, args ...any) {
	d.p.opt.Logf(format, args...)
}

func (d *delivery) warnf(format string, args ...any)  { d.p.warnf(format, args...) }
func (d *delivery) errorf(format string, args ...any) { d.p.errorf(format, args...) }

// deliver 把准备好的段发出去并处理没成的段。返回的结论里 delivered 是内容有没有送到用户手里（全部确认，或补发的文字版确认，
// 结果不明按“可能已到”算，不当失败）；err 是第一批没能入队（调用方回失败）。
func (p *Plugin) deliver(run *expansionRun, rt retryInput, parts []types.Part, resources []*types.MediaResource) (sendReport, error) {
	d := &delivery{p: p, run: run, conv: run.source.Inbound.Conv, snap: rt.snapshot, send: rt.snapshot.Settings.Expand.Send, link: rt.result.ShareURL(), quote: run.source.Inbound.PlatformID, nodeName: nodeName(rt.result), fwd: ForwardOptions(rt.snapshot.Settings.Expand.Forward), result: rt.result, excluded: map[string]bool{}}
	if rt.result != nil {
		d.mode = rt.snapshot.Settings.Expand.ImageModeOf(rt.result.Platform)
		d.site, d.url = rt.result.Platform, rt.result.CleanURL
	}
	if !d.send.ForwardQuoteReply || p.quirks.on(quirkKey(d.conv, "reply"), p.opt.Now()) {
		stripReplies(parts) // 实验关着、或这个会话 6 小时内退回过：按旧排法发
	}
	dropForwardQuotes(parts)
	d.videos = keepVideos(parts)
	defer d.videos.release()
	items := d.firstItems(parts, resources)
	if len(items) > 0 {
		if err := d.runParts(items, 0, true); err != nil {
			return sendReport{}, err
		}
	}
	fallback := len(d.items) > 0
	d.finishText()
	return d.report(fallback), nil
}

// report 是发完以后的结论；fallback 是有没有退文字版
func (d *delivery) report(fallback bool) sendReport {
	return sendReport{delivered: !d.hardFail && !d.broken, landed: len(d.known) > 0 || d.textLanded, unsure: len(d.unsure) > 0 || d.textOdd, fallback: fallback, broken: d.broken, why: strings.Join(d.why, "；")}
}

func nodeName(r *linkreader.Result) string {
	if r != nil && r.Author.Name.State == "available" && strings.TrimSpace(r.Author.Name.Value) != "" {
		return clip(strings.TrimSpace(r.Author.Name.Value), 24)
	}
	return "链接内容"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// firstItems 给第一次要发的段留备份，并把每段用到的资源挑出来；没被任何段用到的资源当场释放
func (d *delivery) firstItems(parts []types.Part, resources []*types.MediaResource) []sendItem {
	backup := backupParts(parts)
	used := map[*types.MediaResource]bool{}
	items := make([]sendItem, len(parts))
	for i, part := range parts {
		items[i] = sendItem{part: part, hasNow: true, backup: backup[i]}
		for _, s := range mediaSegs(part) {
			if s.Media != nil {
				used[s.Media] = true
				items[i].res = append(items[i].res, s.Media)
			}
		}
	}
	for _, r := range resources {
		if !used[r] {
			if e := r.Release(); e != nil {
				d.warnf("释放没用到的媒体失败")
			}
		}
	}
	return items
}

// runParts 把一串段发出去：连续的普通段一起发成一个计划；按图记的结论说整段都已知会被拒的，直接走换形式，不再往 QQ 提交。
// first 为真时，第一个计划没能入队就返回错误（这时还没有任何东西发出去）。
func (d *delivery) runParts(items []sendItem, depth int, first bool) error {
	for i := 0; i < len(items) && !d.broken; {
		if d.moded(items[i].backup) {
			d.logf("第 %d 段按这个网站的发图方式（%s）从「%s」起发", i+1, d.mode, d.startForm(items[i].backup))
			releaseItem(items[i])
			d.ladder(items[i].backup, CategoryRejected, i, d.minRank(), true)
			first = false
			i++
			continue
		}
		k := i
		for k < len(items) && !d.preRejected(items[k].backup) && !d.moded(items[k].backup) {
			k++
		}
		if k > i {
			if err := d.sendGroup(items[i:k], depth, first); err != nil {
				return err
			}
			first = false
			i = k
			continue
		}
		d.logf("第 %d 段的图之前被这个场合明确拒收过，不再原样提交，直接换形式", i+1)
		releaseItem(items[i])
		d.failedPart(items[i].backup, CategoryRejected, i, false)
		first = false
		i++
	}
	return nil
}

func releaseItem(it sendItem) {
	for _, r := range it.res {
		_ = r.Release()
	}
}

// sendGroup 发一个计划（一组段）并按回执处理
func (d *delivery) sendGroup(group []sendItem, depth int, first bool) error {
	var res []*types.MediaResource
	sendable := make([]sendItem, 0, len(group))
	sendParts := make([]types.Part, 0, len(group))
	for _, it := range group {
		if it.hasNow {
			sendable, sendParts, res = append(sendable, it), append(sendParts, it.part), append(res, it.res...)
			continue
		}
		cp, r, err := clonePart(it.backup)
		if err != nil {
			// 没有备份的段（视频等）补发不了：并进文字版，写明原因；从计划里拿掉，回执下标按留下的段算
			d.items = append(d.items, textItem{part: it.backup, category: catAborted})
			continue
		}
		sendable, sendParts, res = append(sendable, it), append(sendParts, cp), append(res, r...)
	}
	if len(sendParts) == 0 {
		return nil
	}
	planID, err := d.submit(sendParts, res)
	if err != nil {
		if first {
			return err
		}
		d.errorf("结果未入队（%s）", reason(err))
		d.failWhy("后面的段没能入队（%s）", reason(err))
		d.hardFail, d.broken = true, true
		return nil
	}
	outcomes, err := d.p.opt.Expand.Outcomes(d.conv, planID)
	if err != nil || len(outcomes) == 0 {
		// 读不到回执：可能已经发出去了，不当作失败，也不换形式、不补发；但这几段结果不明，插件日志记 fail（kind unknown），不记成确认送到
		d.warnf("读结果计划的回执失败（%s），按结果不明处理", receiptWhy(err))
		for _, it := range sendable {
			d.unsure = append(d.unsure, it.backup)
		}
		return nil
	}
	d.settle(sendable, planID, outcomes, depth)
	return nil
}

// submit 提交一个计划，并记下计划号是这个链接的（迟到的回声要查）
func (d *delivery) submit(parts []types.Part, res []*types.MediaResource) (string, error) {
	planID, err := d.p.send(d.run, parts, res)
	if err == nil {
		d.p.plans.put(planID, planEntry{site: d.site, url: d.url, until: d.p.opt.Now().Add(planBookTTL)}, d.p.opt.Now())
	}
	return planID, err
}

// receiptWhy 是读不到回执的原因：出错写原因，没出错却一段回执都没有写“回执是空的”
func receiptWhy(err error) string {
	if err == nil {
		return "回执是空的"
	}
	return reason(err)
}

// settle 按回执处理一个计划里的每一段
func (d *delivery) settle(group []sendItem, planID string, outcomes []PartOutcome, depth int) {
	firstBad := -1 // 出口那边第一个没确认的段（明确失败或结果不明）：它后面被中止的段要原样补发，不管等回声的结论是什么
	var tail []sendItem
	for _, o := range outcomes {
		if o.Index < 0 || o.Index >= len(group) {
			continue
		}
		it := group[o.Index]
		bad := o.State == OutcomeUnknown || o.State == OutcomeFailed
		explicit := o.State == OutcomeFailed
		o = d.confirm(planID, o, it.backup, fmt.Sprintf("第 %d 段", o.Index+1))
		// 被回声认领成确认的段，出口发它时多半还是结果不明（回声比回执晚），后面的段已经被中止：同样要原样补发
		if (bad || (o.State == OutcomeConfirmed && o.echo != nil)) && firstBad < 0 {
			firstBad = o.Index
		}
		switch o.State {
		case OutcomeConfirmed:
			d.known = append(d.known, it.backup)
			d.exclude(o)
			d.noteReplacement(planID, o) // 正替大图原段补发时，这一份记在它的看守名下（late.go）
			if o.echo != nil && o.echo.MediaMissing {
				d.swallowed(it.backup, planID, o, o.Index)
			}
		case OutcomeUnknown:
			d.unsure = append(d.unsure, it.backup)
			if d.guard != nil {
				d.guard.noteUnsure()
			}
			d.logf("第 %d 段结果不明（可能已经发出去了），不换形式、不补发", o.Index+1)
			d.step(formOf(it.backup), "结果不明", taskrun.OutcomeDegraded, nil)
		case OutcomeFailed:
			outer := d.guard
			d.guard = o.guard // 大图等不到回声退下来的：补发的份记在这个看守名下；别的失败不带看守
			d.failedPart(it.backup, o.Category, o.Index, explicit)
			d.guard = outer
		case OutcomeAborted:
			// 只有前面有段没发出去（失败或不明）时的中止才是“被连累”，要原样补发；
			// 其余中止（展开关了、切档、计划被取消）是别的原因，不补发
			if firstBad >= 0 && o.Index > firstBad {
				tail = append(tail, sendItem{backup: it.backup})
			}
		default:
			// 还在发送、重试等不该出现的终态：不知道发没发出去，按结果不明办
			d.unsure = append(d.unsure, it.backup)
			d.logf("第 %d 段的回执不是终态（%s），按结果不明处理", o.Index+1, o.State)
		}
	}
	if len(tail) == 0 || d.broken {
		return
	}
	if depth >= maxSettleDepth || d.run.ctx.Err() != nil {
		for _, it := range tail {
			d.items = append(d.items, textItem{part: it.backup, category: catAborted})
		}
		return
	}
	d.logf("前面有段没发出去，后面被中止的 %d 段原样补发（它们从没发出过）", len(tail))
	_ = d.runParts(tail, depth+1, false)
}

// catAborted：这一段从没发出过，又补发不了（媒体没有备份）
const catAborted = "被中止"

func formOf(p types.Part) form {
	if p.Kind == types.PartForward {
		return formForward
	}
	return formDirect
}

// step 记换形式的一步：日志和运行页（link · form_degrade），转 180° 那一步另记 link · rotate_retry
func (d *delivery) step(f form, result, outcome string, err error) {
	d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, err, taskrun.Opts{Outcome: outcome, Tag: f.String() + "：" + result})
	if f == formTurned || f == formRotated {
		d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassRotateRetry, err, taskrun.Opts{Outcome: outcome, Tag: result})
	}
}

func hasLost(p types.Part) bool {
	for _, s := range mediaSegs(p) {
		if s.Media == nil || s.Kind == types.BodyVideo {
			return true // 视频不备份（备份里留着原资源只为认出是哪一个，发完就释放了）
		}
	}
	return false
}

// ---- 一段没发出去 ----

// failedPart 处理没发出去的一段：纯文字的没有可换的形式；媒体段按类别决定：本地原因（没连上）直接并进文字版，
// 协议端拒绝 / 不支持（或没回声）走阶梯。explicit 是回包明确失败（不是等不到回声、也不是按旧结论跳过）：只有它才触发两个实验的退回。idx 只用于日志
func (d *delivery) failedPart(orig types.Part, category string, idx int, explicit bool) {
	if explicit && category != CategoryOther && orig.Kind == types.PartForward && hasReply(orig) {
		// 先看引用：适配器对带引用的节点一律拒（snowluma 的 forward 编码），有引用时被拒不能算到视频头上；
		// 排在“纯文字没有可换的形式”前面：纯文字推文引用纯文字推文的转发没有媒体，去掉引用照样能重发
		d.replyBack(orig, idx)
		return
	}
	if !hasMedia(orig) {
		d.warnf("第 %d 段（纯文字）没发出去（%s），没有可换的形式", idx+1, category)
		d.failWhy("第 %d 段（纯文字）没发出去（%s）", idx+1, category)
		d.hardFail = true
		return
	}
	if category == CategoryOther {
		d.warnf("第 %d 段的媒体没发出去（%s，不是协议端拒收），不换形式，并进文字版", idx+1, category)
		d.items = append(d.items, textItem{part: orig, category: category, guard: d.guard})
		return
	}
	if explicit && orig.Kind == types.PartForward && hasForwardVideo(orig) {
		d.videoBack(orig, idx)
		return
	}
	d.step(formOf(orig), resultOf(category), taskrun.OutcomeFailed, nil) // 第一次发（原样）就被拒，也记一行
	if orig.Kind == types.PartForward && hasForwardVideo(orig) {
		// 没回声（不是明确失败）的带视频转发：不知道卡在哪，视频没备份、不能整段转图。
		// 视频拿出来单独补发，剩下的图走阶梯（转 180° 重发）；不记“这个会话转发里放不了视频”
		fwd, videos := splitVideos(orig)
		d.logf("第 %d 段：带视频的合并转发没送到，视频拿出来单独发，其余的图换形式重发", idx+1)
		if len(fwd.Nodes) > 0 {
			d.ladder(fwd, category, idx, 0, false)
		}
		if d.broken {
			return
		}
		_ = d.runParts(d.videoItems(videos, idx), 1, false)
		return
	}
	d.ladder(orig, category, idx, 0, false)
}

// ---- 网站的发图方式 ----

// moded 说这一段要不要按网站的发图方式起步：方式不是 auto，且整段只有静态图、都有备份（动图、视频不转，照常发）
func (d *delivery) moded(p types.Part) bool {
	return d.minRank() > 0 && hasMedia(p) && !hasLost(p) && canTurn(p)
}

// minRank 是发图方式起步的级别：auto 0（从头）、rotate 1（转图）、forward 2（包成合并转发再转图）
func (d *delivery) minRank() int {
	switch d.mode {
	case config.ImageModeRotate:
		return 1
	case config.ImageModeForward:
		return 2
	}
	return 0
}

func (d *delivery) startForm(p types.Part) form {
	if d.minRank() >= 2 || p.Kind == types.PartForward {
		return formTurned
	}
	return formRotated
}

// attempt 把一段（备份里的，发之前再拷一份资源）作为只有一段的计划发出去，读这一段的终态（没确认送达的等回声）。
// 返回 err 就是没法继续（没入队、被取消）：调用方停下，已经置 broken
func (d *delivery) attempt(part types.Part, label string) (PartOutcome, error) {
	cp, res, err := clonePart(part)
	if err != nil {
		d.warnf("要发的段没有备份（%s）", reason(err))
		d.failWhy("要发的段没有备份（%s）", reason(err))
		d.hardFail = true
		return PartOutcome{}, err
	}
	planID, err := d.submit([]types.Part{cp}, res)
	if err != nil {
		d.errorf("换形式的发送未入队（%s）", reason(err))
		d.failWhy("换形式的发送没能入队（%s）", reason(err))
		d.hardFail, d.broken = true, true
		return PartOutcome{}, err
	}
	outs, err := d.p.opt.Expand.Outcomes(d.conv, planID)
	if err != nil || len(outs) == 0 {
		// 读不到回执：可能已经到了，当结果不明，不换形式
		d.warnf("读换形式那一步的回执失败，按结果不明处理")
		return PartOutcome{Index: 0, State: OutcomeUnknown}, nil
	}
	o := outs[0]
	if o.State == OutcomeAborted {
		d.broken = true
		return o, errAborted
	}
	o = d.confirm(planID, o, part, label)
	if o.State == OutcomeConfirmed {
		d.exclude(o)
		d.noteReplacement(planID, o)
	}
	return o, nil
}

// exclude 记下已确认的段的平台消息号：之后核实别的段时，不把它当成“那一条”
func (d *delivery) exclude(o PartOutcome) {
	if o.PlatformID != "" {
		d.excluded[o.PlatformID] = true
	}
}

// ---- 阶梯 ----

// rungs 列出这一段要试的形式（第 1 级原样发不在里面，它已经发过，或按发图方式跳过了）：
//   - 转图帮得上（协议端拒绝或没回声、全是静态图、开关开着）：消息段转 180° 直接发，转发段转 180° 的合并转发；
//   - plain（能力缺失、或 rotateOnReject 关着）：消息段不转的合并转发，转发段没有可换的；
//   - 动图、视频被拒：没有可换的，直接文字版。
//
// minRank 2（imageMode forward）时消息段也包成转 180° 的合并转发。这张图以前发成过的形式排最前，以前被拒过的形式跳过。
func (d *delivery) rungs(part types.Part, key string, category string, minRank int, fresh bool) []form {
	var base []form
	rotateOff := !fresh && !d.snap.Settings.Expand.RotateOnReject
	switch {
	case category == CategoryUnsupported || (rotateOff && canTurn(part)):
		if part.Kind != types.PartForward && !hasVideo(part) {
			base = []form{formForward}
		}
	case !canTurn(part):
	case part.Kind == types.PartForward || minRank >= 2:
		base = []form{formTurned}
	default:
		base = []form{formRotated}
	}
	bad, good := d.p.verdicts.get(key, d.p.opt.Now())
	var out []form
	for _, f := range base {
		if bad[f] && good != int(f) {
			d.logf("这一段的图之前在这个场合用「%s」被拒收过，跳过这一级", f)
			continue
		}
		out = append(out, f)
	}
	return out
}

// build 造这一级要发的段
func (d *delivery) build(part types.Part, f form) (types.Part, error) {
	switch f {
	case formDirect:
		return part, nil
	case formForward:
		return d.asForward(part), nil
	case formRotated:
		body, err := rotateBody(part.Body)
		if err != nil {
			return types.Part{}, err
		}
		out := part
		out.Body = body
		return out, nil
	}
	fwd := d.asForward(part)
	nodes := make([]types.ForwardNode, len(fwd.Nodes))
	for i, n := range fwd.Nodes {
		body, err := rotateBody(n.Body)
		if err != nil {
			return types.Part{}, err
		}
		n.Body = body
		nodes[i] = n
	}
	fwd.Nodes = nodes
	return fwd, nil
}

// rotateBody 把正文里的静态图转 180°（文字原样）
func rotateBody(in []types.BodySegment) ([]types.BodySegment, error) {
	body := make([]types.BodySegment, len(in))
	for j, s := range in {
		body[j] = s
		if s.Kind == types.BodyImage {
			turned, err := linkexpand.Rotate180(s.Media)
			if err != nil {
				return nil, err
			}
			body[j].Media = turned
		}
	}
	return body, nil
}

// asForward 把消息段的内容包成一个合并转发；本来就是转发段的原样返回。
// 节点显示名按设置：写了用设置的，没写用她的 QQ 昵称，取不到才用作者名；卡片文字同正常路径。
func (d *delivery) asForward(p types.Part) types.Part {
	if p.Kind == types.PartForward {
		return p
	}
	name, self := linkexpand.ForwardNodeName(d.fwd.NodeName, d.nodeName)
	return types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: name, Self: self, Body: p.Body}}, Card: linkexpand.BuildCard(d.result, d.fwd)}
}

// canTurn 说这一段的媒体全是静态图（动图、视频不转）
func canTurn(p types.Part) bool {
	segs := mediaSegs(p)
	for _, s := range segs {
		if s.Kind != types.BodyImage {
			return false
		}
	}
	return len(segs) > 0
}

func hasVideo(p types.Part) bool {
	for _, s := range mediaSegs(p) {
		if s.Kind == types.BodyVideo {
			return true
		}
	}
	return false
}

func resultOf(category string) string {
	switch category {
	case CategoryUnsupported:
		return "能力缺失"
	case CategoryRejected:
		return "被拒"
	}
	return "没发出去"
}

// ladder 让一段走阶梯。category 是把它带到这里的那次失败的类别；fresh 为真是还没发过（按发图方式起步、撤回后重发），
// minRank 是起步的级别。原样那一级被拒（fresh 为假）就先记进按图的结论
func (d *delivery) ladder(part types.Part, category string, idx, minRank int, fresh bool) {
	label := fmt.Sprintf("第 %d 段", idx+1)
	item := textItem{part: part, category: category, guard: d.guard}
	outer := d.guard
	defer func() { d.guard = outer }() // 下面可能接过某一级自己开的看守，出去时还原
	if hasLost(part) {
		d.logf("%s的媒体没有备份（视频或超大），不换形式，并进文字版", label)
		d.items = append(d.items, item)
		return
	}
	key := ""
	if k := partKey(part); k != "" {
		key = d.conv.String() + "|" + k
	}
	var unsupported []types.Part
	if !fresh {
		item.tried = append(item.tried, formOf(part))
		if category == CategoryUnsupported {
			unsupported = append(unsupported, part)
		}
		if category == CategoryRejected {
			d.p.verdicts.mark(key, formOf(part), false, d.p.opt.Now())
		}
	}
	for _, f := range d.rungs(part, key, category, minRank, fresh) {
		if g := d.guard; g != nil && d.originalArrived(g) {
			// 等了这么久才到的大图原段，回声在换形式发出去之前到了：它到了，不再补发（B15）
			d.logf("%s：原来那份的回声晚到了，不再换形式", label)
			d.step(formOf(part), "原图晚到", taskrun.OutcomeOK, nil)
			d.known = append(d.known, part)
			return
		}
		built, err := d.build(part, f)
		if err != nil {
			d.warnf("%s：「%s」没准备成（%s），退文字", label, f, reason(err))
			d.step(f, "没准备成", taskrun.OutcomeFailed, err)
			continue
		}
		d.hint = &guardHint{key: key, form: f} // 这一级自己没回声时开的看守，按图的结论用阶梯的键和这一级的形式
		o, err := d.attempt(built, label)
		d.hint = nil
		if err != nil {
			return
		}
		switch o.State {
		case OutcomeConfirmed:
			d.logf("%s：「%s」发出去了，不再换形式", label, f)
			d.step(f, "成功", taskrun.OutcomeOK, nil)
			d.p.verdicts.mark(key, f, true, d.p.opt.Now())
			d.known = append(d.known, part)
			if o.echo != nil && o.echo.MediaMissing && f == formRotated {
				// 转过的图又被吞了：撤不撤都不再转第二次，只记下
				d.warnf("%s：转 180° 重发的图又被吞了，不再换形式", label)
				d.failWhy("%s的图转 180° 后又被吞了", label)
			}
			return
		case OutcomeUnknown:
			d.logf("%s：「%s」结果不明（可能已经发出去了），不换形式、不补发", label, f)
			d.step(f, "结果不明", taskrun.OutcomeDegraded, nil)
			d.unsure = append(d.unsure, part)
			if d.guard != nil {
				d.guard.noteUnsure()
			}
			return
		case OutcomeFailed:
			if d.guard == nil && o.guard != nil {
				// 第一次发送就在阶梯里（按图的结论说原样已被拒、或网站按转图起步）：这一级没回声，它就是被看守的原段，
				// 后面的级别和文字版是它的补发份
				d.guard, item.guard = o.guard, o.guard
			}
			item.tried = append(item.tried, f)
			item.category = o.Category
			if o.Category == CategoryUnsupported {
				unsupported = append(unsupported, built)
			}
			if o.Category == CategoryRejected {
				d.p.verdicts.mark(key, f, false, d.p.opt.Now())
			}
			d.step(f, resultOf(o.Category), taskrun.OutcomeFailed, nil)
			d.warnf("%s：「%s」也没发出去（%s），退文字", label, f, o.Category)
		default:
			d.broken = true
			return
		}
	}
	d.warnf("%s：能试的形式都没发出去，退文字版", label)
	if len(unsupported) > 0 && len(unsupported) == len(item.tried) {
		// 每一级都是能力缺失：文字版确认后，记下这个场合发不了这些形式
		d.blocks = append(d.blocks, unsupported...)
	}
	d.items = append(d.items, item)
}

// preRejected 说这一段（整段的图）在按图记的结论里已经用它本来的形式被拒过、又没有发成过的记录：不再原样提交，直接走阶梯
func (d *delivery) preRejected(p types.Part) bool {
	if d.p.verdicts.empty() || !hasMedia(p) || hasLost(p) {
		return false
	}
	key := partKey(p)
	if key == "" {
		return false
	}
	bad, good := d.p.verdicts.get(d.conv.String()+"|"+key, d.p.opt.Now())
	return bad[formOf(p)] && good != int(formOf(p))
}

// ---- 两个实验的退回（A6、A7） ----

// hasForwardVideo 说一个合并转发的节点里有没有视频
func hasForwardVideo(p types.Part) bool {
	for _, n := range p.Nodes {
		for _, s := range n.Body {
			if s.Kind == types.BodyVideo {
				return true
			}
		}
	}
	return false
}

func hasReply(p types.Part) bool {
	for _, n := range p.Nodes {
		if n.ReplyTo > 0 {
			return true
		}
	}
	return false
}

// dropForwardQuotes 去掉非消息段上的引用：QQ 的合并转发带不了回复段（Caps.Supports 会拒掉整批），
// 第一段是合并转发时不补一条文字去挂引用（那又多发一条，规格-链接发送改版 A5），进度表情已经说明这是对哪条的回应
func dropForwardQuotes(parts []types.Part) {
	for i := range parts {
		if parts[i].Kind != types.PartMessage {
			parts[i].Quote = ""
		}
	}
}

// stripReplies 去掉合并转发节点里的引用（ReplyTo）
func stripReplies(parts []types.Part) {
	for i := range parts {
		for j := range parts[i].Nodes {
			parts[i].Nodes[j].ReplyTo = 0
		}
	}
}

// videoBack：视频进合并转发被拒（能力缺失或明确失败）。把视频节点拿出来，转发（不带视频）重发一次，视频单独一条紧跟其后；
// 这个会话 6 小时内直接按旧排法发
func (d *delivery) videoBack(orig types.Part, idx int) {
	d.p.quirks.mark(quirkKey(d.conv, "video"), d.p.opt.Now())
	d.traceRow(traceEvent(RouteSend, kindFwdVideo, noteVideoBack))
	d.logf("第 %d 段：视频放进合并转发被拒，视频拿出来单独发，这个会话 6 小时内照旧排法", idx+1)
	fwd, videos := splitVideos(orig)
	var items []sendItem
	if len(fwd.Nodes) > 0 {
		items = append(items, sendItem{backup: fwd})
	}
	_ = d.runParts(append(items, d.videoItems(videos, idx)...), 1, false)
}

// splitVideos 把合并转发节点里的视频拿出来：返回不带视频的转发（只剩视频的节点去掉）和拿出来的视频
func splitVideos(orig types.Part) (types.Part, []types.BodySegment) {
	fwd := orig
	fwd.Nodes = nil
	var videos []types.BodySegment
	for _, n := range orig.Nodes {
		var body []types.BodySegment
		for _, s := range n.Body {
			if s.Kind == types.BodyVideo {
				videos = append(videos, s)
				continue
			}
			body = append(body, s)
		}
		if len(body) > 0 {
			n.Body = body
			fwd.Nodes = append(fwd.Nodes, n)
		}
	}
	return fwd, videos
}

// videoItems 把拿出来的视频各做成单独一条（用留下的硬链接重开）；没留下文件的并进文字版
func (d *delivery) videoItems(videos []types.BodySegment, idx int) []sendItem {
	var items []sendItem
	for _, v := range videos {
		part, res, ok := d.videos.reopen(v)
		if !ok {
			d.logf("第 %d 段的视频没留下能补发的文件，并进文字版", idx+1)
			d.items = append(d.items, textItem{part: types.Part{Kind: types.PartMessage, Body: []types.BodySegment{v}}, category: CategoryRejected, guard: d.guard})
			continue
		}
		items = append(items, sendItem{part: part, res: res, hasNow: true, backup: types.Part{Kind: types.PartMessage, Body: []types.BodySegment{v}}})
	}
	return items
}

// replyBack：转发里用 reply 指向别的节点被拒。去掉引用按旧排法重发一次；这个会话 6 小时内不再试
func (d *delivery) replyBack(orig types.Part, idx int) {
	d.p.quirks.mark(quirkKey(d.conv, "reply"), d.p.opt.Now())
	d.traceRow(traceEvent(RouteSend, kindFwdReply, noteReplyBack))
	d.logf("第 %d 段：合并转发里的引用被拒，去掉引用重发，这个会话 6 小时内照旧排法", idx+1)
	cp := orig
	cp.Nodes = slices.Clone(orig.Nodes)
	stripReplies([]types.Part{cp})
	if !hasForwardVideo(cp) {
		_ = d.runParts([]sendItem{{backup: cp}}, 1, false)
		return
	}
	// 视频没有备份、放不回转发：照旧排法拿出来单独一条（不记视频的退回，被拒的是引用）
	fwd, videos := splitVideos(cp)
	var items []sendItem
	if len(fwd.Nodes) > 0 {
		items = append(items, sendItem{backup: fwd})
	}
	_ = d.runParts(append(items, d.videoItems(videos, idx)...), 1, false)
}

// ---- 文字版 ----

// finishText 把所有退文字的块合成一条文字版补发；文字版确认了，才记能力缺失的组合
func (d *delivery) finishText() {
	if len(d.items) == 0 {
		return
	}
	// 大图原段的回声在文字版发出去之前晚到了：它到了，这一块不并进文字版（B15）
	kept := make([]textItem, 0, len(d.items))
	for _, it := range d.items {
		if it.guard != nil && d.originalArrived(it.guard) {
			d.logf("一块大图原来那份的回声晚到了，不并进文字版")
			d.known = append(d.known, it.part)
			continue
		}
		kept = append(kept, it)
	}
	d.items = kept
	if len(d.items) == 0 {
		return
	}
	src := make([]types.Part, len(d.items))
	var notes []string
	for i, it := range d.items {
		src[i] = it.part
		if it.category == catAborted {
			labels, _ := mediaLabels(it.part)
			if len(labels) > 0 {
				notes = append(notes, strings.Join(labels, "、")+"没发出去：前面的内容没发出去，这一段被中止，也没法补发")
			}
			continue
		}
		if note := failureNote(it, d.conv.Scene); note != "" {
			notes = append(notes, note)
		}
	}
	text, ok := linkexpand.TextFallbacks(src, d.known, d.quote, d.link, notes...)
	if !ok {
		d.failWhy("媒体没发出去，文字版也拼不出来")
		d.hardFail = true
		return
	}
	d.warnf("%d 块媒体没能以任何形式发出去，补发一条不带媒体的文字版（写明原因并附原链接）", len(d.items))
	planID, err := d.submit([]types.Part{text}, nil)
	if err != nil {
		d.errorf("文字版未入队（%s）", reason(err))
		d.failWhy("文字版没能入队（%s）", reason(err))
		d.hardFail = true
		return
	}
	outs, err := d.p.opt.Expand.Outcomes(d.conv, planID)
	if err != nil {
		// 读不到回执：文字版可能已经发出去了，不补发；按结果不明记
		d.warnf("读文字版的回执失败（%s），按结果不明处理", reason(err))
		d.textOdd = true
		return
	}
	switch {
	case allConfirmed(outs):
		d.textLanded = true
		d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: taskrun.OutcomeDegraded, Tag: "文字版：成功"})
		// 文字版也是“后发的那份”：它装着的大图原段的回声都到了才撤（有别的内容的块不撤，免得撤掉还有用的）
		needs, guarded := make([]*lateGuard, len(d.items)), false
		for i, it := range d.items {
			needs[i], guarded = it.guard, guarded || it.guard != nil
		}
		if guarded {
			d.attachReplacement(planID, outs[0], needs)
		}
		for _, part := range d.blocks {
			d.p.opt.Expand.Block(d.conv, part, CategoryUnsupported)
		}
	case len(outs) == 1 && outs[0].State == OutcomeUnknown:
		d.logf("文字版结果不明，不补发")
		d.textOdd = true
		for _, it := range d.items {
			if it.guard != nil {
				it.guard.noteUnsure()
			}
		}
	default:
		d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: taskrun.OutcomeFailed, Tag: "文字版：没发出去"})
		d.failWhy("媒体没发出去，文字版也没发出去")
		d.hardFail = true
	}
}

// retryInput 是换形式需要的东西：冻结的设置和取到的结果（取原链接、作者名）
type retryInput struct {
	snapshot Snapshot
	result   *linkreader.Result
}
