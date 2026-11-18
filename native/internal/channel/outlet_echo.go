package channel

// 回声认领（规格-渠道中间件 10.4"回声认领（总规则）"，第十四节"回声认领"一行）。
//
// 协议端会把我们自己发出去的消息推回来（SnowLuma 的 reportSelfMessage）。不认领的话，入库一站会把它存成一条"她的号上别人发的"
// （origin = human），她的历史里就多出一条重复的话（盘点 4.4，Bun 的 notMine，qq.ts:301）。
// 认领 = 把回声的平台消息号和我们发的那一段连起来：段转成确认（via = echo），回声的号记进 channel_msgs 指向这次回复的事件。
// 之后入库一站按（渠道、会话、平台消息号）去重，发现是已有的事件，只补"看到了"，不再存一条新的。
//
// 认领的规矩：
//   - 键是（发送用的号、会话、段类型）：文字段还比文字（去掉首尾空白、统一换行），转发段不比占位或节点正文；每个回声只认一段，每段只认一次；
//     对得上的有几段时按 echoBook.rank 挑：图 md5 强对上的、正有人等的、没人等过的、等过已放弃的依次往后，同分先发的先认；
//   - 带图的段（规格-链接发送改版 A1）：回声的图数等于段的图数是正常认领；回声的图比段少（0 张或一部分）也认，记成吞图（MediaMissing）；
//     回声的图比段多不认。回声里图的 file / 地址读得出 32 位十六进制标识的，必须等于段里某张图入队时算好的 md5，读得出却对不上的不认；
//     视频按段类型认（回声的视频数要和段一样），不比内容；带表情的回声不认（我们不发表情段）；
//   - 能认领的段：发送中（回包还没到）、明确失败等重试、结果未知——且是在这一段第一次调 Send 以后的 5 分钟内。
//     认领成功，重试作废，迟到的回包不再改它；已中止、已确认的段不认（已确认的回执本来就带着号，映射早写了）；
//   - 键里有账号：同一个群里两个号各发一段同样的字，各认各的，不串（清单⑤）；
//   - 这个平台消息号已经认识了（channel_msgs 里有）就不认领：同一个回声到第二次（同群双号各交一份、重连重推、补录和实时重叠），
//     或者是回包已确认的那一段的回声，都不是另一段同文字的段的回声；这样的消息交给入库一站按键去重，只补"看到了"。
//
// 认领分强弱（EchoNote.Strong）：只有消息段、图的 md5 都对上且唯一的才是强匹配，链接插件撤回补发份只认它。
//
// 认领到的回声在窗口内记在内存里（echoBook）：链接插件靠 AwaitPart 等某一段的回声代替翻历史（规格-链接发送改版 A1），
// 迟到的回声（有人等过、到点放弃了才来）交给按来源登记的回调。
//
// 残余风险：主人拿着号手动发同样的字，或在转发在途时手动转发，只靠段类型、文字和窗口区分不开——协议端没有别的可比的键。
// 若真机帧里有能区分的字段（sub_type 或设备），第 6 步前以真机帧为准（规格 第二十节）。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// claimText 是认领时比的文字：去掉首尾空白，换行统一成 \n
func claimText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// ClaimEcho 认领一个回声：in 是协议端推回来的、发言人是某个角色的号的消息。认成我们发的某一段返回 true。
// 入库一站在存它之前调（Config.Echo），位置在准入检查之后、入库之前。
// 对任何账号交来的角色号的消息都要问，不只问收到它的那个号：B 号的回复被 A 号先看到，A 号交来的这份也得能认领
func (o *Outlet) ClaimEcho(in types.Inbound) (bool, error) {
	var notify func() // 认领回调放开结果锁以后再调：回调会写插件日志（磁盘），不能让所有发送等它（先 defer 的后跑）
	defer func() {
		if notify != nil {
			notify()
		}
	}()
	o.resultMu.Lock()
	defer o.resultMu.Unlock()
	sh := echoShapeOf(in.Segs)
	if sh.emoji || sh.forwards > 1 || (sh.forwards == 1 && (len(sh.images) > 0 || sh.videos > 0)) {
		return false, nil
	}
	kind := types.PartMessage
	if sh.forwards == 1 {
		kind = types.PartForward
	}
	if (kind == types.PartMessage && sh.text == "" && len(sh.images) == 0 && sh.videos == 0) || !store.HasPlatformID(in.PlatformID) {
		return false, nil // 回声什么都没有，或没有号，认领不了
	}
	db, err := o.cfg.Stores.For(in.Conv.Channel)
	if err != nil {
		return false, err
	}
	var planID string
	var idx int
	var found bool
	var plan store.Plan
	gone := map[string]bool{}    // 每个对得上的候选是不是被吞了图；认领的是哪一段，库里挑完再查
	strongs := map[string]bool{} // 每个对得上的候选，回声是不是用图的 md5 强对上的（EchoNote.Strong）
	err = o.db(func() (e error) {
		planID, idx, found, e = db.ClaimEcho(store.EchoClaim{
			Channel: in.Conv.Channel, Account: in.From.ID, Conversation: in.Conv.String(), PlatformID: in.PlatformID,
			Kind:  kind,
			Since: o.clock.Now().Add(-o.cfg.EchoWindow).UnixMilli(),
			Score: func(pid string, i int, partText string) int {
				strong := false
				if kind == types.PartForward {
					var nodes []types.ForwardNode
					if json.Unmarshal([]byte(partText), &nodes) != nil {
						return 0
					}
				} else {
					var body []types.BodySegment
					if json.Unmarshal([]byte(partText), &body) != nil {
						return 0
					}
					ok, g, s := sh.matches(body)
					if !ok {
						return 0
					}
					gone[partID(pid, i)], strong = g, s
					strongs[partID(pid, i)] = s
				}
				return o.echoes.rank(partID(pid, i), strong)
			},
		}, o.nowMS())
		if e == nil && found {
			plan, e = db.GetPlan(planID)
			if e == nil {
				e = o.linkReceipt(context.Background(), planID, partID(planID, idx), in.PlatformID)
			}
		}
		return
	})
	switch {
	case errors.Is(err, errSealed):
		return false, nil // 封库以后什么也不写；入库一站也该关了
	case err != nil:
		return false, err
	case !found:
		return false, nil
	}
	o.partResultLocked(&planRun{id: planID, character: plan.CharacterID, conv: in.Conv}, idx, true, "")
	o.wakeFlight(partID(planID, idx)) // 还在等回包、等重试的 goroutine 醒来，看到已经确认
	note := EchoNote{PlatformID: in.PlatformID, Account: in.From.ID, MediaMissing: gone[partID(planID, idx)], Strong: strongClaim(kind, partID(planID, idx), gone, strongs)}
	if idx >= 0 && idx < len(plan.Parts) && plan.Parts[idx].SentAt > 0 {
		note.LatencyMS = max(0, o.nowMS()-plan.Parts[idx].SentAt)
	}
	notify = o.echoes.claimed(o.clock.Now(), o.cfg.EchoWindow, plan.Origin, planID, idx, note)
	// 带上来源：被认领说明这一段真发进了会话，读日志的人（巡检）要能分清是她开口还是链接展开这类插件发的（2026-11-16 巡检把链接展开误当成闸门漏了）
	o.logf("会话 %s 的计划 %s（来源 %s）第 %d 段已发出、被回声认领", in.Conv.String(), planID, plan.Origin, idx)
	return true, nil
}

// echoShape 是一个回声里能比的东西
type echoShape struct {
	text     string      // 规范化后的文字
	images   []types.Seg // 图片段（动图回来也是图片段）
	videos   int
	forwards int
	emoji    bool
}

func echoShapeOf(segs []types.Seg) echoShape {
	var sh echoShape
	for _, s := range segs {
		switch s.Kind {
		case types.SegImage:
			sh.images = append(sh.images, s)
		case types.SegVideo:
			sh.videos++
		case types.SegEmoji:
			sh.emoji = true
		case types.SegForward:
			sh.forwards++
		}
	}
	sh.text = claimText(PlainTextNoMedia(segs))
	return sh
}

// matches 比一个消息段（库里存的正文）和这个回声：文字一样、视频数一样、回声的图不比段多、读得出的图标识都对得上段里的 md5。
// gone 为真是段有图而回声的图少了（吞图）；strong 为真是回声里有图、每张都读得出标识且对上了段里的 md5
func (sh echoShape) matches(body []types.BodySegment) (ok, gone, strong bool) {
	var digests []string
	images, videos, unknown := 0, 0, false
	for _, s := range body {
		switch s.Kind {
		case types.BodyImage, types.BodyGIF:
			images++
			if s.Media != nil && s.Media.MD5 != "" {
				digests = append(digests, s.Media.MD5)
			} else {
				unknown = true
			}
		case types.BodyVideo:
			videos++
		}
	}
	if claimText(types.BodyTextOf(body)) != sh.text || videos != sh.videos || len(sh.images) > images {
		return false, false, false
	}
	if images == 0 && videos == 0 && sh.text == "" {
		return false, false, false
	}
	if !unknown {
		// 段里每张图都有入队时算的 md5：回声里读得出标识的图必须是其中之一（旧计划没存 md5 的只凭形状）
		ok, s := MatchImageIDs(sh.images, digests)
		if !ok {
			return false, false, false
		}
		strong = s && len(sh.images) > 0
	}
	return true, len(sh.images) < images, strong
}

// hexID 是图片 file / 地址里的 32 位十六进制标识（协议端记的图片 md5）
var hexID = regexp.MustCompile(`(?i)[0-9a-f]{32}`)

// MatchImageIDs 比一组收到的图片段和我们发的图的 md5（ADR-333 的“图的身份”规则，回声认领和链接插件翻历史核实共用）：
// 读得出 32 位十六进制标识的图，必须有一个标识等于 want 里的某个 md5，有一张读得出却对不上就 ok 为假；
// strong 为真是每张图都读得出标识且都对上了。标识全读不出（NT 的图片 file 可能是不透明的 fileId）时 ok 为真、strong 为假。
func MatchImageIDs(imgs []types.Seg, want []string) (ok, strong bool) {
	set := make(map[string]bool, len(want))
	for _, h := range want {
		set[strings.ToLower(h)] = true
	}
	matched, readable := 0, 0
	for _, im := range imgs {
		ids := hexID.FindAllString(im.File+" "+im.URL, -1)
		if len(ids) == 0 {
			continue
		}
		readable++
		for _, id := range ids {
			if set[strings.ToLower(id)] {
				matched++
				break
			}
		}
	}
	if readable > 0 && matched < readable {
		return false, false
	}
	return true, readable == len(imgs) && matched == readable
}

// PlainTextNoMedia 是一条消息里的文字段连起来（图、视频、转发等不算字），回声认领和核实比文字用
func PlainTextNoMedia(segs []types.Seg) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Kind == types.SegText {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// digestImages 给要发的段里的静态图、动图算 md5，存在资源上（随计划写进库）；视频不算（没有可比的标识，文件也可能很大）
func digestImages(parts []types.Part) {
	one := func(body []types.BodySegment) {
		for _, s := range body {
			if (s.Kind != types.BodyImage && s.Kind != types.BodyGIF) || s.Media == nil || s.Media.MD5 != "" {
				continue
			}
			h := md5.New()
			if s.Media.Read(func(r io.Reader) error { _, e := io.Copy(h, r); return e }) == nil {
				s.Media.MD5 = hex.EncodeToString(h.Sum(nil))
			}
		}
	}
	for _, p := range parts {
		one(p.Body)
		for _, n := range p.Nodes {
			one(n.Body)
		}
	}
}

// ---- 等某一段的回声 ----

// EchoNote 是一段被回声认领时的样子：回声的平台消息号和发言人、从这一段第一次调 Send 到认领的毫秒数、回声是不是少了图、认领是不是强匹配
type EchoNote struct {
	PlatformID   string
	Account      string
	LatencyMS    int64
	MediaMissing bool
	// Strong 说这次认领不是只凭形状：消息段（不是转发段），回声里每张图的标识都读得出、都对上了这一段里图入队时算的 md5，
	// 图没少，且对得上的候选里只有这一段是这样（没有第二段同样强对上，免得同一张图在两个计划里时认错）。
	// 只有强匹配的认领才能触发链接插件撤回补发的那份（规格-链接发送改版 B15，复核问题 1）：转发段不比内容、读不出图标识的只比形状，
	// 同会话同发送号的别的回声（主人手动转发、别的链接的转发）可能被认到这一段上，据此撤回会把群里的内容撤光
	Strong bool
}

// strongClaim 算认领到的这一段（id）的认领是不是强匹配：见 EchoNote.Strong。strongs 是每个对得上的消息段候选的“图 md5 强对上”
func strongClaim(kind, id string, gone, strongs map[string]bool) bool {
	if kind != types.PartMessage || gone[id] || !strongs[id] {
		return false
	}
	n := 0
	for _, s := range strongs {
		if s {
			n++
		}
	}
	return n == 1
}

// ClaimedStrong 说这一段的回声认领还记在内存里（窗口内）、回声号就是 platformID、而且是强匹配（EchoNote.Strong）。
// 撤回补发份的宿主门槛独立问它：不信插件说“强匹配”，记不到（重启过、过了窗口）就当不是
func (o *Outlet) ClaimedStrong(planID string, idx int, platformID string) bool {
	b := &o.echoes
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.notes[partID(planID, idx)]
	return ok && o.clock.Now().Before(r.until) && r.note.Strong && r.note.PlatformID == platformID
}

// EchoClaimed 是交给认领回调的一次认领。Late 为真：有人等过这一段的回声、等到点放弃了，回声这才到（迟到）
type EchoClaimed struct {
	PlanID string
	Idx    int
	Note   EchoNote
	Late   bool
}

type echoRecord struct {
	note   EchoNote
	origin string
	until  time.Time
}

// echoBook 记最近认领到的回声（窗口内）、正在等某一段回声的、等到点放弃了的，和按来源登记的回调
type echoBook struct {
	mu      sync.Mutex
	notes   map[string]echoRecord
	waiters map[string]chan EchoNote
	gaveUp  map[string]time.Time
	hooks   map[string]func(EchoClaimed)
}

func (b *echoBook) init() {
	if b.notes == nil {
		b.notes, b.waiters, b.gaveUp, b.hooks = map[string]echoRecord{}, map[string]chan EchoNote{}, map[string]time.Time{}, map[string]func(EchoClaimed){}
	}
}

// prune 丢掉过了窗口的记录（每次写入时顺手做，表不会无限长）
func (b *echoBook) prune(now time.Time) {
	for k, r := range b.notes {
		if !now.Before(r.until) {
			delete(b.notes, k)
		}
	}
	for k, t := range b.gaveUp {
		if !now.Before(t) {
			delete(b.gaveUp, k)
		}
	}
}

// rank 给一个对得上的候选段打分（store.EchoClaim.Score）：图的 md5 强对上的最先；其次是正有人等它回声的；
// 再次是没人等过的（回声可能比等待先到）；最后才是“等过、到点放弃了”的——那一段已经按没送到换了形式，
// 同形状的回声更可能是换形式重发的那一段的（转发段不比内容，读不出图标识的直接发只比形状，复核问题 1）。
// 只剩放弃了的段对得上时仍认它，当迟到的回声
func (b *echoBook) rank(id string, strong bool) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	score := 1
	if _, given := b.gaveUp[id]; !given {
		score++
	}
	if _, waiting := b.waiters[id]; waiting {
		score += 2
	}
	if strong {
		score += 4
	}
	return score
}

// claimed 记下一次认领、叫醒等它的；返回要在锁外调的认领回调（没登记回调时为 nil）
func (b *echoBook) claimed(now time.Time, window time.Duration, origin, planID string, idx int, note EchoNote) func() {
	id := partID(planID, idx)
	b.mu.Lock()
	b.init()
	b.prune(now)
	b.notes[id] = echoRecord{note: note, origin: origin, until: now.Add(window)}
	_, late := b.gaveUp[id]
	delete(b.gaveUp, id)
	if ch := b.waiters[id]; ch != nil {
		ch <- note // 在锁里送：缓冲 1、一段只有一个等待者，不会堵；等待方放弃前在锁里看一眼，不会漏掉
	}
	delete(b.waiters, id)
	hook := b.hooks[origin]
	b.mu.Unlock()
	if hook == nil {
		return nil
	}
	return func() { hook(EchoClaimed{PlanID: planID, Idx: idx, Note: note, Late: late}) }
}

// AwaitPart 等这一段被回声认领，最多等 wait（按出口的时钟计，wait 为 0 只看已经认领的）。已经认领过的（窗口内）立刻返回；
// 到点没等到返回 false，并记下“等过、放弃了”：之后这一段的回声再到就是迟到，交给认领回调时 Late 为真。
// origin 非空时只认这个来源发的段。
func (o *Outlet) AwaitPart(ctx context.Context, origin, planID string, idx int, wait time.Duration) (EchoNote, bool) {
	id := partID(planID, idx)
	// 先过一下认领的锁：认领在库里把段转成确认以后、记下回声的样子之前，读计划的人可能已经看到“确认”了；
	// 不等这一下，就会看成“回包确认、没有回声”，吞图和被中止的后续段都漏掉（2026-11-12 并行跑测试时出现过）
	o.resultMu.Lock()
	o.resultMu.Unlock() //nolint:staticcheck // 只当栅栏用
	b := &o.echoes
	b.mu.Lock()
	b.init()
	if r, ok := b.notes[id]; ok && o.clock.Now().Before(r.until) {
		b.mu.Unlock()
		return r.note, origin == "" || r.origin == origin
	}
	if _, waiting := b.waiters[id]; waiting {
		b.mu.Unlock()
		return EchoNote{}, false // 同一段只许一个等待者
	}
	ch := make(chan EchoNote, 1)
	b.waiters[id] = ch
	b.mu.Unlock()
	if wait > 0 {
		t := o.clock.NewTimer(wait)
		defer t.Stop()
		select {
		case n := <-ch:
			return n, o.ownNote(origin, id)
		case <-t.C():
		case <-ctx.Done():
		case <-o.closing:
		}
	}
	b.mu.Lock()
	delete(b.waiters, id)
	select {
	case n := <-ch: // 放弃前的一瞬间到了
		b.mu.Unlock()
		return n, o.ownNote(origin, id)
	default:
	}
	if wait > 0 {
		b.gaveUp[id] = o.clock.Now().Add(o.cfg.EchoWindow)
	}
	b.mu.Unlock()
	return EchoNote{}, false
}

func (o *Outlet) ownNote(origin, id string) bool {
	if origin == "" {
		return true
	}
	o.echoes.mu.Lock()
	defer o.echoes.mu.Unlock()
	return o.echoes.notes[id].origin == origin
}

// onClaim 给一个来源登记认领回调（迟到的回声靠它通知）；再登记一次换掉旧的，nil 撤掉
func (o *Outlet) onClaim(origin string, fn func(EchoClaimed)) {
	o.echoes.mu.Lock()
	defer o.echoes.mu.Unlock()
	o.echoes.init()
	if fn == nil {
		delete(o.echoes.hooks, origin)
		return
	}
	o.echoes.hooks[origin] = fn
}
