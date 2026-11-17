package channel

// 对话链简化版（规格-群聊闸门 第二节，规格-渠道中间件 第八节"观察"一步）：一条消息在接哪句话，只用确定的信号，不调模型。
// 从 daemon/util/chain.ts 搬来的纯函数（和 Bun 对拍过，testdata/dialog.json）：给一条事件和它前面的几十条（同一个会话，按 seq 排），
// 返回它的连接，和它让前面哪条连接失效（回错了、@ 错人）。存库、查询不在这里。
//
// 和 Bun 的差别：比较字面时按字（rune）切，Bun 按 UTF-16 码元切；只差在 emoji 这类码点大于 U+FFFF 的字上（一个字占两个码元），
// 中文、英文、数字两边一样。

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
)

// DialogLink 的 Kind（规格-群聊闸门 2.1、2.2）
const (
	LinkReply  = "reply"  // 引用回复
	LinkAt     = "at"     // @ 某人（没有引用）时那个人 5 分钟内最近的一条
	LinkMerge  = "merge"  // 同一个人 10 秒内连发（拆条发的同一句）
	LinkWeak   = "weak"   // 紧挨着、只有两个人在说、有字面重合
	LinkRecall = "recall" // 撤回通知 → 被撤回的那条
	LinkRevise = "revise" // 撤回后重发 / @ 错人后重发（新的取代旧的）
	LinkFix    = "fix"    // "打错了""*正确的字""回错了"（补在上一句后面）
	LinkRepeat = "repeat" // 复读（连到这一串的第一条）
)

// DialogLink 是一条消息连出去的一个连接。强连接是定论，弱连接只做参考
type DialogLink struct {
	Kind   string
	To     int64 // 连到的那条的 seq
	Strong bool
	Why    string
}

// DialogDowngrade 说前面某条消息的某种连接失效了：From 那条的 Kind 连接改成"错的"
type DialogDowngrade struct {
	From int64
	Kind string // LinkReply 或 LinkAt
	Why  string
}

// DialogEvent 是对话链看一条事件要用的字段
type DialogEvent struct {
	Seq      int64
	ID       string
	Kind     string // "message" 或 "notice"
	ActorID  string
	Text     string
	At       time.Time
	ReplyTo  string   // 引用的（或撤回的）那条事件的 id
	Mentions []string // @ 了谁：说话人的 id，和 ActorID 同一种写法；"all" 是全体
	Notice   string   // 通知的种类，撤回是 "recall"
}

// MentionsOfSegs 从段里取出 @ 了谁，写成发言人 id（渠道:号；全体是 "all"）。Bun 里叫 mentions
func MentionsOfSegs(channel string, segs []types.Seg) []string {
	var out []string
	for _, s := range segs {
		if s.Kind != types.SegAt || s.Target == "" {
			continue
		}
		if s.Target == types.AtAll {
			out = append(out, "all")
		} else {
			out = append(out, channel+":"+s.Target)
		}
	}
	return out
}

const dlgMin = time.Minute

var (
	dlgURL      = regexp.MustCompile(`https?://\S+`)
	dlgHolder   = regexp.MustCompile(`\[[^\]]{1,12}\]`)
	dlgAtText   = regexp.MustCompile(`@\S+\s?`)
	dlgWrongRe  = regexp.MustCompile(`(?i)^(回错了|引用错了|回复错了|不是这条|不是回你|发错了|@错了|艾特错了|at错了)`)
	dlgTypoRe   = regexp.MustCompile(`^(\*|＊)|^(打错了|打错字|错字)`)
	dlgRecallOK = map[string]bool{"recall": true, "group_recall": true, "friend_recall": true}
)

// dlgWords 只比说的字：去掉"[图片]""[表情包]"这类占位和链接（两个"[表情包]"不能算复读）
func dlgWords(s string) string { return dlgHolder.ReplaceAllString(dlgURL.ReplaceAllString(s, ""), "") }

// dlgNorm 去掉占位、链接、空白、标点、符号
func dlgNorm(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, dlgWords(s))
}

func dlgGrams(s string) map[string]bool {
	rs := []rune(dlgNorm(s))
	g := map[string]bool{}
	for i := 0; i+1 < len(rs); i++ {
		g[string(rs[i:i+2])] = true
	}
	return g
}

// DialogOverlap 说两段话字面上有多像：相邻两个字（去掉空白和标点）重合的个数
func DialogOverlap(a, b string) int {
	x, y := dlgGrams(a), dlgGrams(b)
	n := 0
	for g := range y {
		if x[g] {
			n++
		}
	}
	return n
}

// DialogSimilar 说两句话几乎一样（撤回重发、复读）：较短那句的相邻两字有一半以上在另一句里；很短的直接比去掉标点后的全文
func DialogSimilar(a, b string) bool {
	x, y := dlgNorm(a), dlgNorm(b)
	if x == "" || y == "" {
		return false
	}
	if x == y {
		return true
	}
	short := min(len(dlgGrams(a)), len(dlgGrams(b)))
	return short >= 2 && float64(DialogOverlap(a, b))/float64(short) >= 0.5
}

// DialogLinksFor 是一条事件的连接。before 是同一个会话里它前面的事件（按 seq 升序，含通知），byID 用来找引用的那条（可能早于 before）。
// 规则按规格 2.1、2.2 的顺序；一条可以有几个连接（比如引用 + 撤回重发）
func DialogLinksFor(e DialogEvent, before []DialogEvent, byID func(id string) (DialogEvent, bool)) (links []DialogLink, down []DialogDowngrade) {
	// 撤回通知：连到被撤回的那条
	if e.Kind == "notice" {
		if dlgRecallOK[e.Notice] && e.ReplyTo != "" {
			if t, ok := byID(e.ReplyTo); ok {
				links = append(links, DialogLink{LinkRecall, t.Seq, true, "撤回了这条"})
			}
		}
		return links, down
	}
	if e.Kind != "message" {
		return links, down
	}
	var msgs []DialogEvent
	recalled := map[string]bool{}
	for _, x := range before {
		switch {
		case x.Kind == "message":
			msgs = append(msgs, x)
		case x.Kind == "notice" && dlgRecallOK[x.Notice] && x.ReplyTo != "":
			recalled[x.ReplyTo] = true
		}
	}
	// 只往前看：补录回来的旧消息 seq 大、时间早，时间差是负的，不算"刚刚"；"上一句"按时间找，不按 seq
	dt := func(x DialogEvent) time.Duration { return e.At.Sub(x.At) }
	var earlier []DialogEvent
	for _, x := range msgs {
		if dt(x) >= 0 {
			earlier = append(earlier, x)
		}
	}
	slices.SortStableFunc(earlier, func(a, b DialogEvent) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		}
		return 0
	})
	last := func(xs []DialogEvent) *DialogEvent {
		if len(xs) == 0 {
			return nil
		}
		return &xs[len(xs)-1]
	}
	filter := func(xs []DialogEvent, f func(DialogEvent) bool) []DialogEvent {
		var out []DialogEvent
		for _, x := range xs {
			if f(x) {
				out = append(out, x)
			}
		}
		return out
	}
	prev := last(earlier)
	mine := filter(earlier, func(x DialogEvent) bool { return x.ActorID == e.ActorID })
	myPrev := last(mine)
	within := func(x *DialogEvent, d time.Duration) bool { return x != nil && dt(*x) >= 0 && dt(*x) <= d }
	recent := func(d time.Duration) []DialogEvent {
		return filter(msgs, func(x DialogEvent) bool { return within(&x, d) })
	}
	text := strings.TrimSpace(e.Text)

	// 打错字后补一句 / "回错了"：补在自己上一句后面
	if within(myPrev, dlgMin) && dlgTypoRe.MatchString(text) {
		links = append(links, DialogLink{LinkFix, myPrev.Seq, true, "更正上一句的错字"})
	}
	if within(myPrev, 2*dlgMin) && dlgWrongRe.MatchString(text) {
		links = append(links, DialogLink{LinkFix, myPrev.Seq, true, "说上一句回错了"})
		down = append(down, DialogDowngrade{myPrev.Seq, LinkReply, "本人说回错了"}, DialogDowngrade{myPrev.Seq, LinkAt, "本人说回错了"})
	}

	// 撤回后重发（改了几个字）：同一个人 2 分钟内撤回过的、和这句很像的
	redo := last(filter(mine, func(x DialogEvent) bool {
		return recalled[x.ID] && within(&x, 2*dlgMin) && DialogSimilar(x.Text, e.Text)
	}))
	if redo != nil {
		links = append(links, DialogLink{LinkRevise, redo.Seq, true, "撤回后重发"})
	}

	// 引用回复
	if e.ReplyTo != "" {
		if t, ok := byID(e.ReplyTo); ok {
			// 引用的是一小时前的、和内容对不上、却和最近的话对得上：多半是回错了消息，引用降成弱的，同时连到最近的
			old := dt(t) > 60*dlgMin && DialogOverlap(e.Text, t.Text) == 0
			var near *DialogEvent
			if old {
				near = last(filter(recent(10*dlgMin), func(x DialogEvent) bool { return x.ActorID != e.ActorID && DialogOverlap(e.Text, x.Text) > 0 }))
			}
			if near != nil {
				links = append(links, DialogLink{LinkReply, t.Seq, false, "引用的是一小时前的，内容对不上"}, DialogLink{LinkWeak, near.Seq, false, "和最近的话对得上"})
			} else {
				links = append(links, DialogLink{LinkReply, t.Seq, true, "引用回复"})
			}
		}
	}

	// @ 某人（没有引用）：那个人 5 分钟内最近的一条
	mentions := filter2(e.Mentions, func(m string) bool { return m != "all" && m != e.ActorID })
	if e.ReplyTo == "" {
		for _, m := range mentions {
			t := last(filter(recent(5*dlgMin), func(x DialogEvent) bool { return x.ActorID == m }))
			if t == nil {
				continue
			}
			// 那一条已经被别人接过、和这句又对不上：不硬连
			answered := slices.ContainsFunc(msgs, func(x DialogEvent) bool { return x.Seq > t.Seq && x.ActorID != m && x.ReplyTo == t.ID })
			links = append(links, DialogLink{LinkAt, t.Seq, !(answered && DialogOverlap(e.Text, t.Text) == 0), "@ 了这个人"})
		}
	}
	// @ 错人：同一个人 30 秒内 @ 了别人、说的差不多，以后一个为准
	var myLast *DialogEvent
	if within(myPrev, 30*time.Second) {
		myLast = myPrev
	}
	if myLast != nil && len(mentions) > 0 {
		was := filter2(myLast.Mentions, func(m string) bool { return m != "all" })
		if len(was) > 0 && !slices.ContainsFunc(was, func(m string) bool { return slices.Contains(mentions, m) }) &&
			DialogSimilar(dlgAtText.ReplaceAllString(myLast.Text, ""), dlgAtText.ReplaceAllString(e.Text, "")) {
			links = append(links, DialogLink{LinkRevise, myLast.Seq, true, "@ 错人后重发"})
			down = append(down, DialogDowngrade{myLast.Seq, LinkAt, "@ 错人"})
		}
	}

	// 同一个人 10 秒内连发、中间没有别人：拆条发的同一句
	if within(prev, 10*time.Second) && prev.ActorID == e.ActorID && !slices.ContainsFunc(links, func(l DialogLink) bool { return l.Kind == LinkFix || l.Kind == LinkRevise }) {
		links = append(links, DialogLink{LinkMerge, prev.Seq, true, "10 秒内连发"})
	}

	// 复读：别人 2 分钟内说过一样的话，连到这一串的第一条
	if utf8.RuneCountInString(dlgNorm(e.Text)) >= 2 {
		same := filter(recent(2*dlgMin), func(x DialogEvent) bool { return x.ActorID != e.ActorID && dlgNorm(x.Text) == dlgNorm(e.Text) })
		if len(same) > 0 {
			links = append(links, DialogLink{LinkRepeat, same[0].Seq, true, "复读"})
		}
	}

	// 弱连接：没有别的连接时，紧挨着的上一条（别人说的、2 分钟内）、最近 6 条只有两个人在说、有字面重合
	if len(links) == 0 && within(prev, 2*dlgMin) && prev.ActorID != e.ActorID {
		speakers := map[string]bool{e.ActorID: true}
		tail := earlier
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		for _, x := range tail {
			speakers[x.ActorID] = true
		}
		if len(speakers) == 2 && DialogOverlap(e.Text, prev.Text) > 0 {
			links = append(links, DialogLink{LinkWeak, prev.Seq, false, "紧挨着、只有两个人在说、有重合的字"})
		}
	}
	return links, down
}

func filter2(xs []string, f func(string) bool) []string {
	var out []string
	for _, x := range xs {
		if f(x) {
			out = append(out, x)
		}
	}
	return out
}
