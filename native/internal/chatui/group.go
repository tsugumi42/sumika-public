package chatui

import (
	"sumika-native/internal/segview"
	"time"
)

// State 是一条消息的状态。
type State int

const (
	Sent State = iota
	Sending
	Failed
	Streaming
	Recalled
	Interrupted
)

// Quote 是引用回复里被引的那条。
type Quote struct {
	Name, Text  string
	Owner, Gone bool
	Target      string
	Preview     bool
	Thumb       ThumbKind // 被引消息有图、贴纸、视频时的缩略图种类（取图走 Host.Media 的 QuoteMedia）
}

// Msg 是气泡族要画的一条消息；和后台的数据结构无关，由宿主（正式界面或工作台）转换过来。
type Msg struct {
	ID        string
	Service   string // 不空时这行是通知条（进群、禁言这类），不是气泡，见 Host.ServiceRow
	Icon      string // 通知条文字前面的小图标名（chatui 图标表里的）；空就不画。宿主写的系统行按来源给
	Sender    string // 发送人身份（角色 id、QQ 号……），合组只看它，不看名字
	Self      bool   // 在当前会话里画在右侧
	Name      string // 显示名
	Char      OKLCH  // 发送人的角色色；零值时按 NameID 取 Telegram 七色
	NameID    uint64 // 不是本地角色时取名字色用
	At        time.Time
	Text      string
	Blocks    []segview.Block // 宿主的不可变排版缓存
	Parts     []Part          // 不空时是一条分块消息（规格-引用）：按块画，不画 Blocks 和 Quote
	Markdown  bool
	Notes     []string
	State     State
	Quote     *Quote
	Reactions []Reaction
	Steps     []*Step // 不空时这一条是她这一轮的过程（思考、读搜、命令、改文件），画过程不画气泡
	Tip       string  // 时间悬停提示里在完整日期时间后面补的一行（主动联系：她为什么找你）；气泡时间旁的小标记走 Notes
}

// Reaction 是一个表情回应：表情、人数、主人自己点过没有、点了的人（显示头像用，可以不全）。
type Reaction struct {
	Emoji string
	Face  string
	Count int
	Mine  bool
	By    []Reactor
}

// Reactor 是点了表情回应的一个人。
type Reactor struct {
	ID, Name string
	Avatar   string // 头像的缓存键；空就画首字（Host.ReactorImage 取图）
	Char     OKLCH  // 角色色；零值按 NameID 取 Telegram 七色
	NameID   uint64
}

// ShowUserpics 照 Telegram（history_view_reactions.cpp:953-968）：整条消息所有表情加起来不超过 3 个人，
// 并且每个表情点了的人都知道是谁，就在表情后面显示头像；否则一律显示数字。
func ShowUserpics(rs []Reaction) bool {
	sum := 0
	for _, r := range rs {
		sum += r.Count
		if sum > 3 || len(r.By) != r.Count {
			return false
		}
	}
	return len(rs) > 0
}

// Joined 判断 cur 是否和 prev 连成一组：同一发送人、都不是撤回、间隔小于 window（Telegram 900 秒）。
func Joined(prev, cur *Msg, window time.Duration) bool {
	if prev == nil || cur == nil {
		return false
	}
	if prev.Self != cur.Self || prev.Sender != cur.Sender {
		return false
	}
	// 撤回的照样合组：气泡还在原处、原文还显示（只换底色、时间前写"已撤回"），名字和头像不该再出一次（主人 2026-11-06）
	d := cur.At.Sub(prev.At)
	if d < 0 {
		d = -d
	}
	return d < window
}

// Corners 是气泡四角半径，顺序同 mygo Radius：左上、右上、右下、左下。
type Corners [4]float32

// BubbleCorners 按 Telegram 的相贴规则算四角：组内相邻的两条，靠发送人一侧（自己的在右、别人的在左）
// 上一条的下角、下一条的上角改成小角；单行是胶囊（高度一半），多行不超过 r1 和高度的四分之一。
func BubbleCorners(t Tokens, self, joinPrev, joinNext, singleLine bool, height float32) Corners {
	r := min(t.R1, height/4)
	if singleLine {
		r = height / 2
	}
	r = max(r, t.RInnerMin)
	join := min(t.RJoin, r)
	c := Corners{r, r, r, r}
	top, bottom := 0, 3 // 别人：左上、左下
	if self {
		top, bottom = 1, 2 // 自己：右上、右下
	}
	if joinPrev {
		c[top] = join
	}
	if joinNext {
		c[bottom] = join
	}
	return c
}

// Inner 是嵌在半径 outer 的块里、离边 gap 的内层块的圆角（同心），不小于 RInnerMin。
func (t Tokens) Inner(outer, gap float32) float32 { return max(t.RInnerMin, outer-gap) }

// QuoteCorners 是气泡里引用块的四角：只有挨着气泡圆角的角才跟着同心放大（外角 − 竖内边距）——
// 引用块在气泡最上面时的左上、右上两角，各自对着气泡同侧的角；其余的角（下边两角、上面有名字时的全部）
// 不挨着气泡的圆角，一律用最小内层圆角（主人 2026-11-06："本来只需要右上角调大就行"）。
func QuoteCorners(t Tokens, bubble Corners, belowName bool) Corners {
	std := t.RInnerMin
	q := Corners{std, std, std, std}
	if !belowName {
		q[0] = t.Inner(bubble[0], t.BubblePadV)
		q[1] = t.Inner(bubble[1], t.BubblePadV)
	}
	return q
}
