package chatui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// 输入区（docs/规格-新界面主线.md "输入区和引用"）：宿主按会话各持有一个 Composer，Host 无关，
// 由 ComposerHost 画。状态和动作（加卡、删卡、Esc、附件挂哪块）都是 Composer 的纯方法，不用窗口就能测。

// AttKind 是附件的种类。
type AttKind int

const (
	AttImage AttKind = iota
	AttFile
	AttFolder
	AttText // 大段粘贴折叠成的文字：正文在 Att.Text，发送时拼进这一块的文字（composer_paste.go）
)

// Att 是挂在某一块上的一个附件：图片（有缩略图）、文件或文件夹（记路径）。
type Att struct {
	ID      uint64
	Kind    AttKind
	Name    string
	Path    string
	Stamp   string         // 本地图片挂上时文件的修改时间和大小；图片缓存的键带它，文件被改写后看大图不显示旧图
	Thumb   ui.ImageSource // 缩略图，可以为空（还没解码好、解不出来）
	Pending bool           // 粘贴的图片还在存进媒体库：这时不能发送
	Text    string         // AttText 的全文
	// 图片的像素宽高（解缩略图时量的，已按 EXIF 转正）：待发气泡按它占位，和存好后的气泡同一个比例；0 是还没量出来。
	Width, Height int
}

// MaxAtts 是一块最多挂几个附件。
const MaxAtts = 16

// MaxImages 是一条消息（所有块加起来）最多几张图，和后台收图的上限（每消息 16 张）一致。
const MaxImages = 16

// Quoted 是一张引用卡引的那一段：被引的消息、发言人、选中的文字和它在原文里的位置。
type Quoted struct {
	MessageID  string
	Name       string    // 被引的人，卡头上写
	Text       string    // 选中的文字
	Start, End int       // 选区在被引消息原文里的字符（rune）偏移 [Start, End)；拿不准时都是 0，让后台按文字找
	At         time.Time // 被引消息的时间，卡头上写；取不到是零值，不写
	Thumb      ThumbKind // 被引消息有图、贴纸、视频时的缩略图种类；取图走 ComposerHost.QuoteThumb
}

// Block 是输入区里的一块：Quote 为 nil 是普通文字块（主输入框），否则是一张引用卡，Text 是针对这一段的回复。
type Block struct {
	ID     uint64
	Quote  *Quoted
	Text   string
	Atts   []Att  // 这一块的附件，按添加的先后
	Err    string // 后台拒收时标在这一块上的原因；改动这一块的文字（悄悄话卡还有对象）后清掉
	errFor string // 拒收时这一块的文字和对象（sig）：变了就清掉 Err
	// To、All 是悄悄话卡的对象（Composer.Whisper 为真时才有意义）：指给哪几位角色（角色 ID，按选的先后）、是不是"所有人"。
	To      []string
	All     bool
	Sending bool // 已交给后台、还没有回音：这一块只读
	field   ui.Handle
	// 对象勾选列表开着没有；刚勾了一位、下一帧要原地再开
	tagOpen, tagReopen bool
	// 上一帧里光标（选区）的位置和当时的文字：↑ 回忆历史要知道光标在不在第一行开头，
	// OnTextKey 里读不到光标，只能用构建时存下的；文字对不上说明这一帧里又打了字，那就不认。
	sel     [2]int
	selText string
}

// Composer 是一个会话的输入区草稿：若干张引用卡（按添加的先后）加一个主输入框，发出去的顺序就是这个顺序。
type Composer struct {
	Cards  []*Block
	Main   *Block
	Notice string // 输入区上方的一行提示（粘贴失败、路径找不到……），下一次动作清掉
	// Whisper 为真：引用卡是悄悄话（本地群聊、QQ 私聊、QQ 群，规格-群聊引用和路由第四节）——卡上可以只有引用不写字，
	// 但至少要指给一位；主输入框照旧是说话，两者分开发。宿主按会话设。
	Whisper bool
	next    uint64
	active  uint64   // 最近有焦点的块：附件挂在它上面
	focus   uint64   // 下一帧要把焦点给哪一块；0 不动
	reveal  uint64   // 下一帧要滚进卡区视野的引用卡；0 不动
	send    bool     // 在输入框里按了 Enter，下一次构建时交给宿主
	queue   bool     // 在输入框里按了 Alt+Enter（她在回复时排队，不在回复时就是发送）
	sent    []string // 本次运行里这个会话发出过的话（历史回忆用）
	recall  recall
	slash   slashState
}

// NewComposer 建一个只有主输入框的草稿。
func NewComposer() *Composer {
	p := &Composer{}
	p.Main = &Block{ID: p.nextID()}
	p.active = p.Main.ID
	return p
}

func (p *Composer) nextID() uint64 { p.next++; return p.next }

// FocusMain 下一帧把焦点给主输入框，附件也改挂它（在输入框之外粘贴时用）。
func (p *Composer) FocusMain() { p.focus, p.active = p.Main.ID, p.Main.ID }

// Focused 报告有没有哪一块的输入框正拿着焦点。
func (p *Composer) Focused(c *ui.Context) bool {
	for _, b := range p.every() {
		if b.field.Focused(c) {
			return true
		}
	}
	return false
}

func hasContent(b *Block) bool { return strings.TrimSpace(b.Text) != "" || len(b.Atts) > 0 }

// Sendable 报告有没有可发的东西：任何一块有字就行；悄悄话的卡没字也算（等于"你看看这句"）。
func (p *Composer) Sendable() bool {
	for _, b := range p.Cards {
		if hasContent(b) || p.Whisper {
			return true
		}
	}
	return hasContent(p.Main)
}

// AddQuote 加一张引用卡。同一段（同一条消息、同样的文字和位置）已经有卡时不重复加，只把焦点给它。
// focus 为真时下一帧把焦点给这张卡的输入框；为假时把焦点还给原来有焦点的那一块（点按钮时焦点会被按钮拿走）。
func (p *Composer) AddQuote(q Quoted, focus bool) *Block {
	for _, b := range p.Cards {
		if *b.Quote == q {
			p.refocus(b, focus)
			return b
		}
	}
	b := &Block{ID: p.nextID(), Quote: &q}
	p.Cards = append(p.Cards, b)
	p.refocus(b, focus)
	return b
}

// AddWhisper 加一张悄悄话卡，指给 to（或所有人）。同一段已经有卡时不重复加：把这次选的对象并进那张卡，焦点给它。
func (p *Composer) AddWhisper(q Quoted, to []string, all bool, focus bool) *Block {
	b := p.AddQuote(q, focus)
	for _, id := range to {
		if id != "" && !slices.Contains(b.To, id) {
			b.To = append(b.To, id)
		}
	}
	b.All = b.All || all
	return b
}

// SetTarget 在悄悄话卡上勾上或取消一位（id 为空表示"所有人"）。发送中的卡不动。
func (p *Composer) SetTarget(b *Block, id string, on bool) {
	if b.Sending {
		return
	}
	if id == "" {
		b.All = on
		return
	}
	i := slices.Index(b.To, id)
	switch {
	case on && i < 0:
		b.To = append(b.To, id)
	case !on && i >= 0:
		b.To = slices.Delete(slices.Clone(b.To), i, i+1)
	}
}

// NoTarget 报告这张悄悄话卡一个对象都没有（不能发）。
func NoTarget(b *Block) bool { return len(b.To) == 0 && !b.All }

// sig 是判断拒收原因还算不算数的依据：文字和对象。
func (b *Block) sig() string {
	return b.Text + "|" + strings.Join(b.To, ",") + "|" + fmt.Sprint(b.All)
}

func (p *Composer) refocus(b *Block, focus bool) {
	p.reveal = b.ID // 刚加的（或者已经有的那张）要看得见：卡区最多四张高，超过就在里面滚
	if focus {
		p.focus = b.ID
	} else {
		p.focus = p.active
	}
}

// Remove 删一张引用卡；焦点落到前一张卡，没有就回主输入框。返回有没有这张卡。
func (p *Composer) Remove(id uint64) bool {
	for i, b := range p.Cards {
		if b.ID != id {
			continue
		}
		p.Cards = append(p.Cards[:i:i], p.Cards[i+1:]...)
		p.focus, p.active = p.Main.ID, p.Main.ID
		if i > 0 {
			p.focus, p.active = p.Cards[i-1].ID, p.Cards[i-1].ID
		}
		return true
	}
	return false
}

// Esc 删掉最后一张空卡（没有字）；返回有没有删。有字的卡不删，免得误丢写好的回复。
func (p *Composer) Esc() bool {
	for i := len(p.Cards) - 1; i >= 0; i-- {
		if !hasContent(p.Cards[i]) {
			return p.Remove(p.Cards[i].ID)
		}
	}
	return false
}

// Blocks 是这次要发的几块，顺序就是发出去的顺序：引用卡按添加的先后，主输入框有字才带上。
func (p *Composer) Blocks() []*Block {
	out := append([]*Block(nil), p.Cards...)
	if hasContent(p.Main) {
		out = append(out, p.Main)
	}
	return out
}

func (p *Composer) block(id uint64) *Block {
	if p.Main.ID == id {
		return p.Main
	}
	for _, b := range p.Cards {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (p *Composer) every() []*Block { return append(slices.Clone(p.Cards), p.Main) }

// Active 是附件现在该挂的那一块：最近有焦点的输入框所在的块，没有就是主输入框。
func (p *Composer) Active() *Block {
	if b := p.block(p.active); b != nil {
		return b
	}
	return p.Main
}

// Attach 把附件挂到 to 这一块；to 为 nil 或这一块已经不在时挂在 Active。同一块里同一路径只挂一次。
// 返回附件的编号和有没有挂上（满了会在 Notice 里说）。
func (p *Composer) Attach(to *Block, a Att) (uint64, bool) {
	if to == nil || p.block(to.ID) != to {
		to = p.Active()
	}
	if to.Sending {
		p.Notice = "这一块正在发送，发完再加"
		return 0, false
	}
	for _, x := range to.Atts {
		if a.Path != "" && x.Path == a.Path {
			return x.ID, true
		}
	}
	if a.Kind == AttImage && p.imageCount() >= MaxImages {
		p.Notice = fmt.Sprintf("一条消息最多 %d 张图片", MaxImages)
		return 0, false
	}
	if len(to.Atts) >= MaxAtts {
		p.Notice = fmt.Sprintf("一块最多 %d 个附件", MaxAtts)
		return 0, false
	}
	p.Notice = ""
	a.ID = p.nextID()
	to.Atts = append(to.Atts, a)
	return a.ID, true
}

// imageCount 数这条消息现在挂了几张图（各块加起来，还在保存的也算）。
func (p *Composer) imageCount() int {
	n := 0
	for _, b := range p.every() {
		for _, a := range b.Atts {
			if a.Kind == AttImage {
				n++
			}
		}
	}
	return n
}

// Update 改某个附件（比如图存好了、缩略图解好了）；附件已经被删了返回 false，调用方丢掉结果。
func (p *Composer) Update(attID uint64, f func(a *Att)) bool {
	for _, b := range p.every() {
		for i := range b.Atts {
			if b.Atts[i].ID == attID {
				f(&b.Atts[i])
				return true
			}
		}
	}
	return false
}

// Detach 删一个附件，返回有没有。
func (p *Composer) Detach(attID uint64) bool {
	for _, b := range p.every() {
		for i := range b.Atts {
			if b.Atts[i].ID == attID {
				b.Atts = append(b.Atts[:i:i], b.Atts[i+1:]...)
				return true
			}
		}
	}
	return false
}

// AppendText 把拖进来的文字接在 to 这一块（nil 或已经不在是 Active）的末尾，和已有的字之间补一个空格或换行；
// 发送中的块不收。返回有没有放进去。
func (p *Composer) AppendText(to *Block, text string) bool {
	if to == nil || p.block(to.ID) != to {
		to = p.Active()
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if to.Sending {
		p.Notice = "这一块正在发送，发完再加"
		return false
	}
	switch {
	case to.Text == "" || strings.HasSuffix(to.Text, "\n") || strings.HasSuffix(to.Text, " "):
	case strings.Contains(text, "\n"):
		to.Text += "\n"
	default:
		to.Text += " "
	}
	to.Text += text
	p.Notice = ""
	return true
}

// DetachAll 删掉一块的全部附件，返回删了几个；发送中的块不动。
func (p *Composer) DetachAll(b *Block) int {
	if b.Sending {
		return 0
	}
	n := len(b.Atts)
	b.Atts = nil
	return n
}

// Check 在发送前查：引用卡一个字都没写，后台会拒（每块至少要有内容），所以先在这张卡上标出来、不发。
func (p *Composer) Check() bool {
	ok := true
	for _, b := range p.every() {
		for _, a := range b.Atts {
			if a.Pending {
				p.Notice = "图片还在保存，稍等一下再发"
				ok = false
			}
		}
	}
	for _, b := range p.Cards {
		switch {
		case p.Whisper && NoTarget(b):
			p.SetError(b, "至少选一位")
			ok = false
		case !p.Whisper && !hasContent(b):
			p.SetError(b, "写点针对这一段的话再发")
			ok = false
		}
	}
	return ok
}

// SetError 把原因标在这一块上；这一块的文字再改动就清掉。
func (p *Composer) SetError(b *Block, why string) { b.Err, b.errFor = why, b.sig() }

// Begin 标记这几块已交给后台：只读，直到 Accept 或 Reject。
func (p *Composer) Begin(blocks []*Block) {
	for _, b := range blocks {
		b.Sending = true
	}
}

// Accept 后台收下了：发出去的引用卡删掉，主输入框清空；发送期间新加的卡原样留着。
func (p *Composer) Accept(blocks []*Block) {
	for _, b := range blocks {
		b.Sending = false
		if b == p.Main {
			b.Text, b.Err, b.Atts = "", "", nil
			p.slash = slashState{}
			p.recall = recall{}
			continue
		}
		p.Remove(b.ID)
	}
	p.focus = 0
}

// Reject 后台拒收：所有块都留着，只解除只读。
func (p *Composer) Reject(blocks []*Block) {
	for _, b := range blocks {
		b.Sending = false
	}
}

// cardsMaxLines 是引用卡区最多占几个正文行高。
const cardsMaxLines = 14

// pressed 给没有底色的小按钮悬停、按下的样子：在文字色上叠一层淡淡的。
func (h ComposerHost) pressed(b ui.Element) ui.Color {
	switch {
	case b.Pressed():
		return h.C.Text.Alpha(0.16)
	case b.Hovered():
		return h.C.Text.Alpha(0.08)
	}
	return ui.Transparent
}

// solid 给有底色的圆键悬停、按下的样子：往文字色靠一点。
func (h ComposerHost) solid(b ui.Element, base ui.Color) ui.Color {
	switch {
	case b.Pressed():
		return base.Mix(h.C.Text, 0.22)
	case b.Hovered():
		return base.Mix(h.C.Text, 0.12)
	}
	return base
}

// ComposerColors 是宿主给输入区的语义色（来自当前 ui.Theme）。
type ComposerColors struct {
	Fill, Card, Line, Text, Muted ui.Color
	Accent, OnAccent              ui.Color
	Danger                        ui.Color
}

// ComposerHost 画输入区。
type ComposerHost struct {
	T      Tokens
	C      ComposerColors
	Font   string
	Busy   bool // 她正在回复：发送键换成停止
	Stop   bool // 停止已发出，等收尾
	Locked bool // 有一次提交还没有回音：已有的块只读
	// DropHot：有文件正拖在窗口上，外框高亮、写明松手加到哪。
	DropHot bool
	// OnPasteImage 在输入框里粘贴、剪贴板里是图片时调用（b 是粘贴所在的块）；OnFiles 在剪贴板里是文件或文件夹时调用。
	// 两个都由宿主安排后台的活，再用 Composer.Attach 挂上。
	OnPasteImage func(b *Block, png []byte)
	OnFiles      func(b *Block, paths []string)
	OnSend       func()
	OnStop       func()
	// OnSteer、OnQueue 是她在回复时的两种发送：Enter 插话（她做完这一步就看到），Alt+Enter 排队（这一轮说完再处理）。
	// OnSteer 设了才启用：输入框有字时右下角的键是"发送"（走 OnSteer），没字才是"停止"；不设就保持"回复时只能停"。
	OnSteer func()
	OnQueue func()
	// Past 给出这个会话里列表已有的、自己发过的话（旧到新）。只在开始翻历史时调用一次，不用每帧算。
	Past func() []string
	// Surface 让宿主给输入框外框上材质（玻璃、磨砂、实色，设计语言第二节第 2 层）；不设就用 C.Fill 实底。
	Surface func(e ui.Element)
	// Tools 画输入框底行左边的东西（模型、审批方式的药丸，照 Bun .su-composer-bar）。设了以后文字占上面一整行，
	// 底行是这些加右端的发送键；不设就是文字和发送键同一行。
	Tools func()
	// ToolsPad 是外框内距（药丸的外边、发送键到外框的距离）；ToolsInset 是药丸里面图标到药丸边的内距。
	// 文字和药丸是同一级：文字也缩进 ToolsInset，这样文字到外框上、左等距，也和药丸里的图标左对齐（照 Bun .su-composer）。
	ToolsPad, ToolsInset float32
	// QuoteThumb 取引用卡里被引消息的缩略图（走宿主的图片加载）；nil 时引用卡不画缩略图。
	QuoteThumb func(messageID string, wantPx int) MediaResult
	// Commands 给出能用的斜杠命令（旧到新不用管，按给的顺序列）。只在输入框以 / 开头时调用；不设就没有命令补全。
	Commands func() []SlashItem
	// OnOpenText 在点开"粘贴的内容"小药丸时调用，由宿主弹出全文。
	OnOpenText func(a Att)
	// OnOpenImage 在点输入框里的图片缩略图时调用，由宿主用看大图打开；id 是被点的那张，不设就点不开。
	OnOpenImage func(id uint64)
	// Targets 是悄悄话卡能指给的角色（Composer.Whisper 为真时用）：卡头对象标签显示她们的名字，点标签勾选。
	Targets []WhisperTarget
	// Handoff 为真：QQ 私聊、QQ 群的输入区（规格-群聊引用和路由 4、10.5）。没有往 QQ 发的主输入框，只有引用卡；
	// 键是写着"交给她"的文字键（不叫发送），Enter 在卡里只换行、不直接交出；卡下固定一行"只交给她，不会发到 QQ"。
	Handoff bool
}

// HandoffLabel、HandoffNote 是 QQ 场合输入区的键名和固定说明。
const (
	HandoffLabel = "交给她"
	HandoffNote  = "只交给她，不会发到 QQ"
)

// 输入区的尺寸（照原型 cmd/sumika-proto 的 composer：最小高 112、内距 12、发送键圆形；用户 11-08 要键不小于 32、
// 最好 36、图标 20 左右，所以键取 34 × 字号比例，图标 20 × 字号比例）。外框圆角 = 内距 + 键半径，键和外框同心。

// ComposerPad 是输入框四边的内距：字到左、上边缘，键到右、下边缘都是它。
func ComposerPad(t Tokens) float32 { return t.BubblePadH }

// ComposerSend 是发送（停止）键的直径。
func ComposerSend(t Tokens) float32 { return round1(34 * t.TS) }

// ComposerSendIcon 是发送键里图标的边长。
func ComposerSendIcon(t Tokens) float32 { return round1(20 * t.TS) }

// ComposerRadius 是外框圆角：内距 + 键半径，键的圆角 = 外框圆角 − 内距（同心）。
func ComposerRadius(t Tokens) float32 { return ComposerPad(t) + ComposerSend(t)/2 }

// ComposerMinHeight 是外框最小高：一行字时正好是胶囊，高 = 控件大档 size.ctl.lg（默认 48）；打多行往上长、圆角不变，
// 自然成了圆角矩形（ADR-351；原来是三行高的大卡片）。
func ComposerMinHeight(t Tokens) float32 { return 2*(composerPadV(t)+1) + ComposerSend(t) }

// composerPadV 是胶囊四边的内距：发送键离上、右、下一样远、和外框同心（1 是描边）。字离左边还是 ComposerPad，
// 差的那一截是文字行的左外边距（几何检查要每层四边内距同档，所以不用不对称的内距）。
func composerPadV(t Tokens) float32 { return max(t.S1/2, (t.HLg-ComposerSend(t))/2-1) }

// composerTextInset 是胶囊里文字行左边多出的那一截：外框内距加上它正好是 ComposerPad；字和发送键之间也隔这么多。
func composerTextInset(t Tokens) float32 { return max(0, ComposerPad(t)-composerPadV(t)) }

// Draw 画输入区；外框宽度由调用方的容器决定。
func (h ComposerHost) Draw(c *ui.Context, p *Composer) {
	t := h.T
	if p.send || p.queue {
		steer, queue := p.send, p.queue
		p.send, p.queue = false, false
		switch {
		case h.Locked || h.Stop:
		case h.Busy:
			if p.Sendable() {
				if queue && h.OnQueue != nil {
					h.OnQueue()
				} else if steer && h.OnSteer != nil {
					h.OnSteer()
				}
			}
		case h.OnSend != nil:
			h.OnSend()
		}
	}
	pad := ComposerPad(t)
	radius := ComposerRadius(t)
	minHeight := ComposerMinHeight(t)
	pill := h.Tools == nil && !h.Handoff && len(p.Cards) == 0 // 单纯打字：胶囊（ADR-351）
	padV := pad
	if pill {
		padV = composerPadV(t)
		radius = padV + 1 + ComposerSend(t)/2
	}
	if h.Handoff {
		minHeight = 0 // 只有卡：高度跟卡走
	}
	if h.Tools != nil {
		minHeight = round1(96 * t.TS)
		pad = h.ToolsPad
		radius = pad + 1 + ComposerSend(t)/2 // 发送键和外框同心（1 是描边）
	}
	h.slashPanel(c, p) // 输入框上方的命令列表（以 / 开头、还在打命令名时才有）
	if pill {
		pad = padV
	}
	box := ui.Column(c.Key("composer")).Label("输入区").FillWidth().Shrink(0).MinHeight(minHeight).Padding(pad).Gap(t.S1).Radius(radius).Border(1, h.C.Line)
	if h.Surface != nil {
		h.Surface(box)
	} else {
		box.Background(h.C.Fill)
	}
	if h.DropHot {
		box.Border(2, h.C.Accent)
	}
	inner := t.Inner(radius, pad)
	box.Children(func() {
		if p.Notice != "" {
			ui.Text(c.Key("notice"), p.Notice).Font(h.Font).FontSize(t.FS[1]).TextColor(h.C.Danger).Wrap().FillWidth()
		}
		if h.DropHot {
			ui.Text(c.Key("drop"), "松手加到"+h.dropTarget(p)).Font(h.Font).FontSize(t.FS[1]).TextColor(h.C.Accent).FillWidth()
		}
		if len(p.Cards) > 0 {
			// 卡很多（后台最多收 20 块）时不能把消息列表挤没：卡区有最大高度，里面滚动
			ui.Scroll(c.Key("cards")).Label("引用卡").FillWidth().MaxHeight(t.BodyLine * cardsMaxLines).Shrink(0).Children(func() {
				ui.Column(c).FillWidth().Gap(pad).Children(func() {
					for i, b := range slices.Clone(p.Cards) {
						h.card(c, p, b, i+1, inner)
					}
				})
			})
		}
		if h.Handoff {
			h.handoffRow(c, p)
			return
		}
		h.atts(c, p, p.Main, inner, "main-atts")
		// 胶囊输入框（ADR-351）：文字在左、发送键贴右下角，多行时文字往上长、键留在底下；
		// 字到左、上边缘，键到右、下边缘都是同一个内距 pad，键的圆角和外框同心（半径 = 外框圆角 − pad）
		align := ui.Stretch
		if pill {
			align = ui.Center // 一行字和发送键垂直居中；多行时字比键高，键自己贴底（AlignSelf End）
		}
		gap := pad
		if pill {
			gap = composerTextInset(t)
		}
		ui.Row(c.Key("main")).FillWidth().Grow(1).Gap(gap).AlignItems(align).Children(func() {
			f := h.field(c, p, p.Main, "发一条消息")
			if h.Tools != nil {
				f.Padding(h.ToolsInset)
			} else if pill {
				f.Margin(0, 0, 0, composerTextInset(t))
			}
			switch {
			case len(p.Cards) > 0 && p.Whisper:
				f.Placeholder("发到群里的话（引用卡另外交给被指的角色）")
			case len(p.Cards) > 0:
				f.Placeholder("另外想说的（不针对任何引用）")
			}
			if h.Tools == nil {
				h.action(c, p)
			}
		})
		if h.Tools != nil {
			ui.Row(c.Key("composer-bar")).FillWidth().Gap(t.S1).AlignItems(ui.Center).Children(func() {
				h.Tools()
				ui.Spacer(c)
				h.action(c, p)
			})
		}
		h.blockError(c, p.Main)
	})
}

// card 是一张引用卡：卡头（谁说的、删除）、被引的那一段、针对它的回复框。
// 卡的圆角是外框圆角减内距（同心），卡里面四边都是同一个内距。
func (h ComposerHost) card(c *ui.Context, p *Composer, b *Block, n int, radius float32) {
	t := h.T
	pad := t.S2
	name := b.Quote.Name
	if name == "" {
		name = "对方"
	}
	box := ui.Column(c.Key(b.ID)).Label(fmt.Sprintf("引用 %d：%s", n, name)).FillWidth().Padding(pad).Gap(t.S1).Radius(radius).Background(h.C.Card)
	if p.reveal == b.ID {
		p.reveal = 0
		box.ScrollIntoView()
	}
	box.Children(func() {
		ui.Row(c.Key("head")).FillWidth().Gap(t.S1).AlignItems(ui.Center).Children(func() {
			ui.Icon(c.Key("icon"), Icon("quote", true)).Size(t.FS[2], t.FS[2]).Shrink(0).TextColor(h.C.Accent)
			if p.Whisper {
				ui.Text(c.Key("name"), "引用 "+name).Font(h.Font).FontSize(t.FS[1]).FontWeight(t.Weight[1]).TextColor(h.C.Muted).Shrink(1).MinWidth(0).SingleLine()
				if at := clockText(b.Quote.At); at != "" {
					ui.Text(c.Key("at"), at).Font(h.Font).FontSize(t.FS[0]).FontFeatures(Tabular).TextColor(h.C.Muted).Shrink(0).SingleLine()
				}
				h.targetTag(c, p, b, n)
				ui.Spacer(c)
			} else {
				ui.Text(c.Key("name"), "回复 "+name).Font(h.Font).FontSize(t.FS[1]).FontWeight(t.Weight[1]).TextColor(h.C.Muted).Grow(1).MinWidth(0).SingleLine()
			}
			rm := ui.ButtonBase(c.Key("remove")).Label(fmt.Sprintf("删除引用 %d", n)).Size(t.HSm, t.HSm).Shrink(0).Radius(t.HSm / 2).Center().TextColor(h.C.Muted).
				Disabled(b.Sending).OnClick(func() { p.Remove(b.ID) })
			rm.Background(h.T.Fill(rm, h.pressed(rm))).Children(func() { ui.Icon(c, Icon("close", false)).Size(t.FS[2], t.FS[2]) })
		})
		ui.Row(c.Key("quoted")).FillWidth().Gap(t.S2).Children(func() {
			ui.Box(c.Key("bar")).Width(3).FillHeight().Shrink(0).Radius(1.5).Background(h.C.Accent)
			h.cardThumb(c, b)
			ui.Text(c.Key("snippet"), b.Quote.Text).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(round1(t.FS[2] * 1.45)).TextColor(h.C.Text).MaxLines(3).Wrap().Grow(1).MinWidth(0)
		})
		h.atts(c, p, b, t.Inner(radius, pad), "atts")
		hint := "针对这一段说点什么"
		if p.Whisper {
			hint = "对她说点什么，也可以不写"
		}
		h.field(c, p, b, fmt.Sprintf("回复引用 %d", n)).Placeholder(hint)
		h.blockError(c, b)
	})
}

// cardThumb 是引用卡里竖线右边的小缩略图（方形，圆角用最小内层圆角）。取不到、没实现就不画。
func (h ComposerHost) cardThumb(c *ui.Context, b *Block) {
	if b.Quote.Thumb == "" || h.QuoteThumb == nil {
		return
	}
	t := h.T
	size := t.QuoteThumbSize()
	res := h.QuoteThumb(b.Quote.MessageID, int(size))
	if res.State == MediaFailed || res.State == MediaMissing {
		return
	}
	ui.Box(c.Key("thumb")).Label("引用的图片").Size(size, size).Shrink(0).Radius(t.RInnerMin).Background(h.C.Card).Clip().Children(func() {
		if res.State == MediaReady && res.Image != nil {
			ui.Image(c.Key("img"), res.Image).Size(size, size).Fit(ui.Cover)
		}
		if b.Quote.Thumb == ThumbVideo {
			ui.Box(c.Key("play")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Children(func() {
				ui.Box(c.Key("disc")).Size(size*0.46, size*0.46).Radius(size).Background(ui.RGBA(0, 0, 0, 0.5)).Center().Children(func() {
					ui.Icon(c, Icon("play", true)).Size(size*0.26, size*0.26).TextColor(ui.RGB(255, 255, 255))
				})
			})
		}
	})
}

// blockError 把后台拒收的原因写在出错的那一块上。
func (h ComposerHost) blockError(c *ui.Context, b *Block) {
	if b.Err == "" {
		return
	}
	ui.Text(c.Key("error"), b.Err).Font(h.Font).FontSize(h.T.FS[1]).TextColor(h.C.Danger).Wrap().FillWidth()
}

// field 是一块的文字输入：Enter 发送、Shift+Enter 换行、Esc 删最后一张空卡；输入法组字时 Enter 归输入法。
func (h ComposerHost) field(c *ui.Context, p *Composer, b *Block, label string) ui.Element {
	t := h.T
	if b.Err != "" && b.sig() != b.errFor {
		b.Err = ""
	}
	f := ui.TextAreaBase(c.Key("field"), &b.Text).Label(label).Placeholder(label).FontSize(t.Body).FixedLineHeight(t.BodyLine).
		TextColor(h.C.Text).Lines(1, 8).MinHeight(0).Grow(1).MinWidth(0).ReadOnly(b.Sending).Bind(&b.field)
	if h.Font != "" {
		f.Font(h.Font)
	}
	if b == p.Main && p.slash.caretEnd { // 采纳命令后把光标放到末尾，接着打参数（SetText 不挪光标）
		p.slash.caretEnd = false
		n := utf8.RuneCountInString(b.Text)
		f.SetTextSelection(n, n)
	}
	b.sel[0], b.sel[1] = f.TextSelection()
	b.selText = b.Text
	if b.field.Focused(c) {
		p.active = b.ID
	}
	if p.focus == b.ID {
		p.focus = 0
		b.field.Focus(c)
	}
	svc := c.Services()
	f.OnPaste(func(text string) bool {
		if b.Sending {
			return false
		}
		p.active = b.ID
		if text != "" {
			// 大段文字不塞进输入框，折叠成附件；没超过的返回 false，让输入框照常插入
			return p.PasteText(b, text)
		}
		d := svc.ReadClipboardData(transfer.FileList, transfer.PNG)
		if files, err := d.Files(); err == nil && len(files) > 0 && h.OnFiles != nil {
			h.OnFiles(b, files)
			return true
		}
		if png, err := d.Read(transfer.PNG); err == nil && len(png) > 0 && h.OnPasteImage != nil {
			h.OnPasteImage(b, png)
			return true
		}
		return false
	})
	composing := f.Composing()
	f.OnTextKey(func(k ui.TextKey) bool {
		if b == p.Main && !composing && !b.Sending && h.slashKey(p, k) {
			return true // 命令列表开着：↑↓ Tab Enter Esc 归它，不触发回忆历史、不发送
		}
		switch {
		case k.Key == ui.KeyEnter && k.Mods == 0 && h.Handoff:
			return false // QQ 场合：Enter 只换行，交出去要点"交给她"
		case k.Key == ui.KeyEnter && k.Mods == 0:
			if !composing {
				b.Text = k.Text()
				p.send = true
			}
			return true
		case k.Key == ui.KeyEnter && k.Mods == ui.Alt:
			if composing {
				return false
			}
			if h.OnQueue != nil {
				b.Text = k.Text()
				p.queue = true
			}
			return true // 不能排队的会话里 Alt+Enter 什么都不做，也不插换行
		case k.Key == ui.KeyEscape && k.Mods == 0:
			if b == p.Main {
				if text, ok := p.recall.end(); ok { // 退出回忆：把正在打的草稿还回来
					k.SetText(text)
					return true
				}
			}
			return p.Esc()
		case b == p.Main && !composing && !b.Sending && k.Mods == 0 && (k.Key == ui.KeyUp || k.Key == ui.KeyDown):
			cur := k.Text()
			if cur != b.selText {
				return false // 这一帧里又打了字，光标位置不可信，让键照常移动光标
			}
			text, changed, ok := p.recall.step(k.Key == ui.KeyUp, cur, b.sel, func() []string { return p.history(h) })
			if changed {
				k.SetText(text)
			}
			return ok
		}
		return false
	})
	return f
}

// action 是右下角的圆键：平时发送，她在回复时停止。
func (h ComposerHost) action(c *ui.Context, p *Composer) {
	t := h.T
	size := ComposerSend(t)
	icon := ComposerSendIcon(t)
	if h.Busy && h.OnSteer != nil && p.Sendable() && !h.Stop {
		// 回复时输入框里有字：键是"发送"（插话）；清空了才回到"停止"
		b := ui.ButtonBase(c.Key("send")).Label("发送（插话）").Size(size, size).Shrink(0).AlignSelf(ui.End).Radius(size / 2).Center().
			TextColor(h.C.OnAccent).Disabled(h.Locked).OnClick(func() { h.OnSteer() })
		b.Background(h.T.Fill(b, h.solid(b, h.C.Accent))).Children(func() { ui.Icon(c, Icon("send", false)).Size(icon, icon) })
		return
	}
	if h.Busy {
		label := "停止生成"
		if h.Stop {
			label = "正在停止"
		}
		b := ui.ButtonBase(c.Key("stop")).Label(label).Size(size, size).Shrink(0).AlignSelf(ui.End).Radius(size / 2).Center().
			TextColor(h.C.OnAccent).Disabled(h.Stop).OnClick(func() {
			if h.OnStop != nil {
				h.OnStop()
			}
		})
		b.Background(h.T.Fill(b, h.solid(b, h.C.Accent))).Children(func() { ui.Icon(c, Icon("stop", true)).Size(icon, icon) })
		return
	}
	off := !p.Sendable() || h.Locked
	bg, ink := h.C.Accent, h.C.OnAccent
	if off {
		bg, ink = h.C.Line, h.C.Muted
	}
	b := ui.ButtonBase(c.Key("send")).Label("发送").Size(size, size).Shrink(0).AlignSelf(ui.End).Radius(size / 2).Center().
		TextColor(ink).Disabled(off).OnClick(func() {
		if h.OnSend != nil {
			h.OnSend()
		}
	})
	if !off {
		bg = h.solid(b, bg)
	}
	b.Background(h.T.Fill(b, bg)).Children(func() { ui.Icon(c, Icon("send", false)).Size(icon, icon) })
}

// handoffRow 是 QQ 场合卡下面的一行：固定说明在左，"交给她"键在右。
func (h ComposerHost) handoffRow(c *ui.Context, p *Composer) {
	t := h.T
	ui.Row(c.Key("handoff")).FillWidth().Gap(t.S2).AlignItems(ui.Center).Children(func() {
		ui.Text(c.Key("note"), HandoffNote).Font(h.Font).FontSize(t.FS[1]).TextColor(h.C.Muted).Grow(1).MinWidth(0).SingleLine()
		off := !p.Sendable() || h.Locked
		bg, ink := h.C.Accent, h.C.OnAccent
		if off {
			bg, ink = h.C.Line, h.C.Muted
		}
		size := ComposerSend(t)
		b := ui.ButtonBase(c.Key("send")).Label(HandoffLabel).Height(size).Shrink(0).Padding(0, t.S3).Radius(size / 2).Gap(t.S1).AlignItems(ui.Center).
			TextColor(ink).Disabled(off).OnClick(func() {
			if h.OnSend != nil {
				h.OnSend()
			}
		})
		if !off {
			bg = h.solid(b, bg)
		}
		b.Background(h.T.Fill(b, bg)).Children(func() {
			ui.Icon(c, Icon("send", false)).Size(t.FS[2], t.FS[2])
			ui.Text(c, HandoffLabel).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[1]).SingleLine()
		})
	})
	h.blockError(c, p.Main)
}

// dropTarget 说明拖进来的文件会挂在哪一块。
func (h ComposerHost) dropTarget(p *Composer) string {
	b := p.Active()
	for i, c := range p.Cards {
		if c == b {
			return fmt.Sprintf("引用 %d", i+1)
		}
	}
	return "输入框"
}

const (
	attName = 180 // 文件名最宽
	// 图片缩略图的边长和关闭钮：缩略图是方块，圆角和外框同心；关闭钮压在右上角、不遮主体，深色半透明底在深浅主题下都看得清。
	attTileBase   = 72
	attRemoveBase = 20
)

func attTile(t Tokens) float32 { return round1(attTileBase * t.TS) }

// ImageAtts 是现在挂着、能看大图的图片附件（还在保存和没读到图的不算），顺序同输入区里从上到下的排法。
func (p *Composer) ImageAtts() []Att {
	var out []Att
	for _, b := range p.every() {
		for _, a := range b.Atts {
			if a.Kind == AttImage && !a.Pending && a.Thumb != nil {
				out = append(out, a)
			}
		}
	}
	return out
}

// atts 画一块的附件条：图片是缩略图方块，文件和文件夹是带图标的小条，每个右上角或右边有删除键。
// radius 是附件的圆角：它所在容器的圆角减内距（同心）。
func (h ComposerHost) atts(c *ui.Context, p *Composer, b *Block, radius float32, key string) {
	if len(b.Atts) == 0 {
		return
	}
	t := h.T
	ui.Row(c.Key(key)).Label("附件").FillWidth().Gap(t.S1).Wrap().Children(func() {
		for _, a := range slices.Clone(b.Atts) {
			if a.Kind == AttImage {
				h.imageChip(c, p, a, radius)
			} else if a.Kind == AttText {
				h.textChip(c, p, a, radius)
			} else {
				h.fileChip(c, p, a, radius)
			}
		}
		if len(b.Atts) > 1 {
			// 两个以上才摆"全部移除"，一个时用它自己的删除键
			wipe := ui.ButtonBase(c.Key("clear")).Label("移除这一块的全部附件").Height(t.HSm).Shrink(0).Padding(0, t.S2).Radius(t.HSm / 2).AlignItems(ui.Center).
				TextColor(h.C.Muted).Disabled(b.Sending).OnClick(func() { p.DetachAll(b) })
			wipe.Background(h.T.Fill(wipe, h.pressed(wipe))).Children(func() {
				ui.Text(c, "全部移除").Font(h.Font).FontSize(t.FS[1]).TextColor(h.C.Muted).SingleLine()
			})
		}
	})
}

func (h ComposerHost) remove(c *ui.Context, p *Composer, a Att, over bool) ui.Element {
	t := h.T
	name := a.Name
	if name == "" {
		name = "附件"
	}
	b := ui.ButtonBase(c.Key("remove")).Label("移除附件 "+name).Size(t.HSm, t.HSm).Shrink(0).Radius(t.HSm / 2).Center().
		OnClick(func() { p.Detach(a.ID) })
	icon := t.FS[1]
	if over {
		// 压在缩略图角上的小圆钮：看得见的圆 20（深色半透明底、白图标，不跟主题走，图上总是这样才看得清），
		// 可点的范围不小于 24（WCAG 2.2 的最小目标尺寸），透明的那一圈在圆外面；圆离角一个 s1
		size := round1(attRemoveBase * t.TS)
		hit := max(24, size+4)
		fill := ui.RGBA(0, 0, 0, 0.55)
		if b.Pressed() || b.Hovered() {
			fill = ui.RGBA(0, 0, 0, 0.78)
		}
		edge := max(0, t.S1-(hit-size)/2)
		b.Absolute().Top(edge).Right(edge).Size(hit, hit).Radius(hit / 2).Center().TextColor(ui.RGB(255, 255, 255))
		icon = round1(size * 0.6)
		b.Children(func() {
			ui.Box(c.Key("disc")).Label("关闭钮").Size(size, size).Radius(size / 2).Background(fill).Center().PassThrough().Children(func() {
				ui.Icon(c, Icon("close", false)).Size(icon, icon)
			})
		})
		return b
	} else {
		b.Background(h.T.Fill(b, h.pressed(b))).TextColor(h.C.Muted)
	}
	b.Children(func() { ui.Icon(c, Icon("close", false)).Size(icon, icon) })
	return b
}

// imageChip 是图片缩略图：方块铺满（Cover）、圆角和外框同心；点一下看大图，右上角的关闭钮只移除、不打开。
// 关闭钮是缩略图的兄弟元素，不在"打开"按钮里面，点它不会同时打开。
func (h ComposerHost) imageChip(c *ui.Context, p *Composer, a Att, radius float32) {
	t := h.T
	size := attTile(t)
	name := a.Name
	if name == "" {
		name = "图片"
	}
	ui.Box(c.Key(a.ID)).Label("图片："+name).Size(size, size).Shrink(0).Children(func() {
		open := ui.ButtonBase(c.Key("open")).Label("查看图片："+name).Size(size, size).Radius(radius).Background(h.C.Card).Clip().Center()
		if a.Thumb != nil && h.OnOpenImage != nil {
			open.OnClick(func() { h.OnOpenImage(a.ID) })
		} else {
			open.Disabled(true)
		}
		open.Children(func() {
			switch {
			case a.Thumb != nil:
				ui.Image(c.Key("thumb"), a.Thumb).Size(size, size).Fit(ui.Cover)
				if open.Pressed() || open.Hovered() { // 悬停、按下压一层很淡的黑，图上也看得出能点
					shade := float32(0.08)
					if open.Pressed() {
						shade = 0.14
					}
					ui.Box(c.Key("hover")).Absolute().Left(0).Right(0).Top(0).Bottom(0).Background(ui.RGBA(0, 0, 0, shade)).PassThrough()
				}
			case a.Pending:
				ui.Spinner(c).Size(t.FS[3], t.FS[3]).TextColor(h.C.Muted)
			default:
				ui.Icon(c, Icon("pic", false)).Size(t.FS[5], t.FS[5]).TextColor(h.C.Muted)
			}
		})
		h.remove(c, p, a, true)
	})
}

func (h ComposerHost) fileChip(c *ui.Context, p *Composer, a Att, radius float32) {
	t := h.T
	icon := "file"
	if a.Kind == AttFolder {
		icon = "folder"
	}
	name := a.Name
	if name == "" {
		name = a.Path
	}
	ui.Row(c.Key(a.ID)).Label("文件："+name).Height(t.HSm).Shrink(0).Padding(0, 0, 0, t.S2).Gap(t.S1).AlignItems(ui.Center).Radius(radius).Background(h.C.Card).Children(func() {
		ui.Icon(c.Key("icon"), Icon(icon, false)).Size(t.FS[2], t.FS[2]).Shrink(0).TextColor(h.C.Muted)
		ui.Text(c.Key("name"), name).Font(h.Font).FontSize(t.FS[2]).TextColor(h.C.Text).MaxWidth(attName).SingleLine()
		h.remove(c, p, a, false)
	})
}
