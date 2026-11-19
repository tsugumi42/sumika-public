package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/history"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
	"sumika-native/internal/web"
)

// DesktopReader 保留现有读投影，只在桌面上额外交出可选写接口。
type DesktopReader struct {
	desktopread.Reader
	desk *Desktop
}

// RunningTurns 是现在正在跑的回合数；sumika-ctl status 用它让人换 exe 前看得出有没有回合在跑。后台还没建好是 0。
func (r *DesktopReader) RunningTurns() int {
	if r == nil || r.desk == nil || r.desk.app == nil || r.desk.app.hub == nil {
		return 0
	}
	return r.desk.app.hub.RunningTurns()
}

// SelfcheckLine 是运行自检的一句话状态（sumika-ctl status 的"自检："）；后台还没建好是空，不显示这一行。
func (r *DesktopReader) SelfcheckLine() string {
	if r == nil || r.desk == nil || r.desk.app == nil {
		return ""
	}
	return r.desk.app.SelfcheckLine()
}

// WebBalances 是联网搜索各家的剩余额度，每家一行（"tavily 剩余 123"）；联网没开或还没查到是空。sumika-ctl status 显示它。
func (r *DesktopReader) WebBalances() []string {
	if r == nil || r.desk == nil || r.desk.app == nil || r.desk.app.ecosystem == nil {
		return nil
	}
	snap := r.desk.app.ecosystem.WebBalances.Snapshot()
	ids := make([]string, 0, len(snap))
	for id := range snap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, web.BalanceLine(id, snap[id]))
	}
	return lines
}

// WebBalanceTexts 是各家已知余额的短说法（服务名 -> "剩余 123"），设置页的联网顺序列表用；没查到的家不在里面。
func (r *DesktopReader) WebBalanceTexts() map[string]string {
	if r == nil || r.desk == nil || r.desk.app == nil || r.desk.app.ecosystem == nil {
		return nil
	}
	out := map[string]string{}
	for id, b := range r.desk.app.ecosystem.WebBalances.Snapshot() {
		if b.Unlimited {
			out[id] = "剩余 不限"
		} else {
			out[id] = "剩余 " + strconv.FormatInt(b.Remaining, 10)
		}
	}
	return out
}

var _ api.LocalChat = (*DesktopReader)(nil)
var _ api.LocalImageChat = (*DesktopReader)(nil)
var _ api.LocalPartsChat = (*DesktopReader)(nil)

func (r *DesktopReader) localCharacter(ctx context.Context, k api.ConversationKey) (string, error) {
	if k.Platform != "local" || k.Channel != "console" || k.Scene != "private" {
		return "", store.ErrDisplayScope
	}
	// 与读投影共用角色和话题所有权校验，不从显示名或 UI 传来的角色字段判断。
	if _, err := r.ReadMessages(ctx, api.ConversationRequest{Key: k, Limit: 1}, ""); err != nil {
		return "", err
	}
	parts := strings.Split(k.Conversation, ":")
	return parts[1], nil
}

func (d *Desktop) enterChat() (context.Context, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil, errors.New("桌面入口已经关闭")
	}
	if d.app.opt.Mode == Archive {
		return nil, nil, api.ErrLocalChatUnsupported
	}
	d.chatWork.Add(1)
	return d.chatCtx, d.chatWork.Done, nil
}

func (r *DesktopReader) SendLocal(ctx context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	_, err := r.sendLocal(ctx, k, text, draft)
	return err
}

// sendLocal 是 SendLocal 的本体，多交出这一轮的结局（自检对话要回复和回合 id）；准入失败时结局是零值
func (r *DesktopReader) sendLocal(ctx context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) (turn.Outcome, error) {
	return r.sendLocalParts(ctx, k, []api.LocalPart{{Text: text}}, nil, draft)
}

// SendLocalImages 收本机上传的图片字节：每消息最多 16 张、每张 20 MiB、合计 80 MiB。图存进媒体库，作为这条消息的附件，
// 之后和引用块里的图走同一条给模型的路。
func (r *DesktopReader) SendLocalImages(ctx context.Context, k api.ConversationKey, text string, images [][]byte, draft func(api.LocalDraft)) error {
	_, err := r.sendLocalParts(ctx, k, []api.LocalPart{{Text: text}}, images, draft)
	return err
}

// SendLocalParts 发一条由有序几块组成的消息（规格-引用）；只有一个普通文字块、没有附件时与旧的 SendLocal 完全一样。
func (r *DesktopReader) SendLocalParts(ctx context.Context, k api.ConversationKey, in []api.LocalPart, draft func(api.LocalDraft)) error {
	_, err := r.sendLocalParts(ctx, k, in, nil, draft)
	return err
}

// sendLocalParts 是 SendLocalParts 的本体，同样交出这一轮的结局；自检话题的标记由 turn 在存主人事件时打（selftestTags），分块不影响。
// uploads 是本机上传的图片字节（SendLocalImages），挂在第一块上。
func (r *DesktopReader) sendLocalParts(ctx context.Context, k api.ConversationKey, in []api.LocalPart, uploads [][]byte, draft func(api.LocalDraft)) (turn.Outcome, error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return turn.Outcome{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return turn.Outcome{}, err
	}
	character, err := r.localCharacter(ctx, k)
	if err != nil {
		return turn.Outcome{}, err
	}
	// 斜杠命令先于"正在生成"的检查：/goal clear、/loop stop 在她说话时也要能用。
	// 只有整条消息是一个没有引用、没有附件（含本机上传的图）的普通文字块才当命令；/goal 设目标时把目标原文当这一条消息发出
	if cmd, plain := plainText("", in); plain && len(uploads) == 0 {
		send, handled, noticeDone, err := r.desk.app.localSlash(ctx, k, character, cmd)
		if handled || err != nil {
			return turn.Outcome{}, err
		}
		defer noticeDone()
		in = []api.LocalPart{{Text: send}}
	}
	// 目录用不了就在落库前原样退回：草稿不丢，这句话也不进库
	if err := r.desk.app.precheckWorkspace(ctx, k.Conversation); err != nil {
		return turn.Outcome{}, err
	}
	var parts []history.Part
	var text string
	if len(uploads) > 0 && len(in) == 1 && in[0].Quote == nil && len(in[0].Attachments) == 0 {
		// 带图的普通消息可以没有文字
		text = in[0].Text
		parts = []history.Part{{Text: text}}
	} else if parts, text, err = r.resolveLocalParts(ctx, k, in); err != nil {
		return turn.Outcome{}, err
	}
	if d := r.desk; d.app.auto.has(k.Conversation) {
		return turn.Outcome{}, api.ErrLocalChatBusy // 自动续轮或定期任务正在跑：先点停止
	}
	// 主动联系：主人在本地私聊里回了她主动发来的话，由话题线认领（宿主替他写库，线里决定怎么回）；
	// 没被认领的照旧往下走，旁听到的只是记一笔"他回过"（规格-主动联系 第五节）
	if len(uploads) == 0 {
		if r.desk.app.claimLocal(character, k.Conversation, parts, text) {
			return turn.Outcome{}, nil
		}
	} else {
		// 带上传图片的不认领（认领要替主人写库，附件还没存），但他确实回了：只记"他回过"
		r.desk.app.overhearLocal(character, k.Conversation)
	}
	runCtx, cancel := context.WithCancel(base)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	d := r.desk
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return turn.Outcome{}, errors.New("桌面入口已经关闭")
	}
	if _, busy := d.runs[k.Conversation]; busy {
		d.mu.Unlock()
		return turn.Outcome{}, api.ErrLocalChatBusy
	}
	d.runs[k.Conversation] = cancel
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.runs, k.Conversation); d.mu.Unlock() }()
	if len(uploads) > 0 {
		attachments, err := r.storeUploads(runCtx, uploads)
		if err != nil {
			return turn.Outcome{}, err
		}
		if parts == nil {
			parts = []history.Part{{Text: text}}
		}
		parts[0].Attachments = append(parts[0].Attachments, attachments...)
	}
	outcome := <-d.app.hub.SendLocalParts(runCtx, character, k.Conversation, parts, text, func(v turn.LocalDraft) {
		if draft != nil {
			draft(api.LocalDraft{TurnID: v.TurnID, Step: v.Step, Text: v.Text})
		}
	})
	return outcome, outcome.Err
}

// storeUploads 核对大小和格式后把上传的图存进本地媒体库，返回挂在消息上的附件。
func (r *DesktopReader) storeUploads(ctx context.Context, images [][]byte) ([]history.Attachment, error) {
	if len(images) > 16 {
		return nil, errors.New("一条消息最多 16 张图片")
	}
	var total int
	out := make([]history.Attachment, 0, len(images))
	for _, image := range images {
		total += len(image)
		if len(image) > 20<<20 || total > 80<<20 {
			return nil, errors.New("图片超过单张 20 MiB 或合计 80 MiB")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, _, err := mediastore.PrepareImage(bytes.NewReader(image)); err != nil {
			return nil, err
		}
		hash, _, err := mediastore.Put(r.desk.app.paths.Root, "local", bytes.NewReader(image), 20<<20)
		if err != nil {
			return nil, err
		}
		out = append(out, history.Attachment{Kind: "image", Hash: hash})
	}
	return out, nil
}

func (r *DesktopReader) StopLocal(ctx context.Context, k api.ConversationKey) error {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	_, err = r.localCharacter(ctx, k)
	if err != nil {
		return err
	}
	// 目标的裁判、自动续轮和定期任务的一轮：停止同时暂停目标
	r.desk.app.autoStop(k.Conversation, "主人停止")
	d := r.desk
	d.mu.Lock()
	cancel := d.runs[k.Conversation]
	// 只取消登记的原请求，不在持锁时等待内核退出，避免与准入前屏障互等。
	if cancel != nil {
		cancel()
	}
	d.mu.Unlock()
	return nil
}

func (r *DesktopReader) DecideLocal(ctx context.Context, k api.ConversationKey, id string, choice api.ApprovalChoice) error {
	switch choice {
	case api.ApproveOnce, api.ApproveConversation, api.ApproveAlways, api.ApproveDeny:
	default:
		return fmt.Errorf("不认识的审批选择：%q", choice)
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	character, err := r.approvalCharacter(ctx, k)
	if err != nil {
		return err
	}
	found := false
	scoped := false
	for _, s := range r.desk.app.approvals.Snapshot() {
		if s.Request.ID == id && s.Request.CharacterID == character && r.desk.app.cardConversation(s.Request) == k.Conversation && s.Decision == nil {
			found, scoped = true, cardOffers(s.Request.Options, choice)
			break
		}
	}
	if !found {
		return api.ErrApprovalGone
	}
	if (choice == api.ApproveConversation || choice == api.ApproveAlways) && !scoped {
		return fmt.Errorf("这张卡没有这个选择：%q", choice)
	}
	ok, err := r.desk.app.approvals.Decide(id, string(choice), "")
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrApprovalGone
	}
	return nil
}

// cardOffers：这张卡有没有给这个回答。范围档给不给由审批门按同一类、受保护路径、灾难性命令、
// 是不是主人发起来定（卡上 Options 就是结果），桌面只照卡办，不再按工具名另设一道。
func cardOffers(options []string, choice api.ApprovalChoice) bool {
	for _, o := range options {
		if o == string(choice) {
			return true
		}
	}
	return false
}

// ListApprovalRules 是设置页的放行规则：记住的在前，这个对话的在后，各按记下的先后。
func (r *DesktopReader) ListApprovalRules(ctx context.Context) ([]api.ApprovalRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return nil, err
	}
	defer done()
	g := r.desk.app.approvalGate
	var out []api.ApprovalRule
	for _, x := range append(g.Grants.List(), g.Conv.List()...) {
		out = append(out, api.ApprovalRule{Scope: x.Scope, Key: x.Key, Label: x.Label, Tool: x.Tool, Effect: x.Effect, Pattern: x.Pattern, Project: x.Project, Session: x.Session, Conversation: x.Conversation, Dirs: x.Dirs, At: x.At, LastUsed: x.LastUsed})
	}
	return out, nil
}

// AddApprovalRule 加一条主人自己写的放行、每次都问或拒绝规则（和"记住"同一份存储），放行、每次都问可以限定项目目录或会话。只有桌面（主人 × 主窗口）有这个入口；
// 受保护路径、灾难性命令、不是主人发起的不被放行规则越过（判定里排在它们后面），拒绝规则先于一切放行。
func (r *DesktopReader) AddApprovalRule(ctx context.Context, raw, effect, project, session string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	_, err = r.desk.app.approvalGate.Grants.PutScopedRule(raw, effect, project, session, r.desk.app.now().UnixMilli())
	return err
}

// RevokeApprovalRule 撤销一条，下一次判定就要问；已经放行、还没开始执行的那一步在执行前也会作废。
func (r *DesktopReader) RevokeApprovalRule(ctx context.Context, rule api.ApprovalRule) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	g := r.desk.app.approvalGate
	switch rule.Scope {
	case string(api.ApproveAlways):
		if _, err := g.Grants.Remove(rule.Key); err != nil {
			return err
		}
	case string(api.ApproveConversation):
		g.Conv.Remove(rule.Conversation, rule.Key)
	default:
		return fmt.Errorf("不认识的放行规则：%q", rule.Scope)
	}
	return nil
}

// approvalChanged 只唤醒读快照，不在审批回调里等界面，也不在这里代答。
func (d *Desktop) approvalChanged() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for ch := range d.approvalWakes {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// watchingApprovals：现在有没有界面在看审批卡（没人看就别发要等回答的问题）。
func (d *Desktop) watchingApprovals() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.approvalWakes) > 0
}

func (d *Desktop) localApprovals() []api.LocalApproval {
	var out []api.LocalApproval
	for _, s := range d.app.approvals.Snapshot() {
		r := s.Request
		if s.Decision != nil {
			continue
		}
		conv := d.app.cardConversation(r)
		// 悄悄话处理那一轮的卡：会话是她的悄悄话会话，放到来源会话的窗口里，卡上写明来历（规格-群聊引用和路由 8.3）
		wh, origin, whisperCard := d.app.whisperApproval(conv)
		if whisperCard && conv != audience.WhisperConversation(r.CharacterID, wh.Source.Conversation) {
			whisperCard = false // 卡上的角色和悄悄话会话的角色对不上：不放
		}
		if conv != "console:"+r.CharacterID && !strings.HasPrefix(conv, "console:"+r.CharacterID+":topic:") && !whisperCard {
			continue
		}
		v := api.LocalApproval{ID: r.ID, CharacterID: r.CharacterID, Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}, Title: r.Title, Tool: r.Tool, Subject: r.Subject, Why: r.Why, Purpose: r.Purpose, Workdir: r.Workdir, Notes: r.Notes, ExpiresAt: r.ExpiresAt, KindLabel: r.KindLabel}
		if conv != r.Conversation {
			v.Title = "子任务：" + v.Title
		}
		if whisperCard {
			v.Whisper = wh
			v.Notes = append([]string{origin}, v.Notes...)
			v.Egress = approvalEgress(r.Egress)
		}
		// 范围档照卡上的 Options：写文件、命令、MCP、子任务都一样，给不给由审批门定（受保护路径、非主人发起的只有一次）。
		for _, o := range r.Options {
			if o == string(api.ApproveConversation) || o == string(api.ApproveAlways) {
				v.Options = append(v.Options, api.ApprovalChoice(o))
			}
		}
		if r.Preview != nil {
			v.Preview = r.Preview.Text
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type localApprovalSub struct {
	out    chan api.Snapshot[[]api.LocalApproval]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *localApprovalSub) Updates() <-chan api.Snapshot[[]api.LocalApproval] { return s.out }
func (s *localApprovalSub) Close()                                            { s.cancel(); <-s.done }

func (r *DesktopReader) WatchLocalApprovals(ctx context.Context) (api.Subscription[[]api.LocalApproval], error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	s := &localApprovalSub{out: make(chan api.Snapshot[[]api.LocalApproval], 1), cancel: cancel, done: make(chan struct{})}
	d := r.desk
	wake := make(chan struct{}, 1)
	d.mu.Lock()
	d.approvalWakes[wake] = struct{}{}
	d.mu.Unlock()
	go func() {
		defer done()
		defer close(s.done)
		defer close(s.out)
		defer stop()
		defer cancel()
		defer func() { d.mu.Lock(); delete(d.approvalWakes, wake); d.mu.Unlock() }()
		for version := uint64(1); ; version++ {
			value := d.localApprovals()
			select {
			case s.out <- api.Snapshot[[]api.LocalApproval]{Version: version, Result: api.Result[[]api.LocalApproval]{State: api.Ready, Value: value}}:
			case <-ctx.Done():
				return
			}
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, nil
}

func (d *Desktop) initChat() {
	d.chatCtx, d.chatCancel = context.WithCancel(context.Background())
	d.runs = map[string]context.CancelFunc{}
	d.approvalWakes = map[chan struct{}]struct{}{}
	d.statusWakes = map[chan struct{}]struct{}{}
	if d.app.opt.Mode != Archive {
		d.app.approvals.Subscribe(func(r approval.Request) { d.approvalChanged(); d.app.attendApproval(r) }, func(approval.Request, approval.Decision) { d.approvalChanged() })
		d.app.asks.OnChange(d.approvalChanged)
	}
}

// sealChat 只封入口；宿主取消 Hub 后才能等待调用方退出。
func (d *Desktop) sealChat() {
	d.mu.Lock()
	d.closed = true
	d.chatCancel()
	d.mu.Unlock()
}

func (d *Desktop) closeChat() { d.sealChat(); d.chatWork.Wait() }

// approvalCharacter 是审批卡所在会话的角色：本地私聊（含话题）照 localCharacter 核对；悄悄话会话（主窗口里没有这个会话、
// 读投影读不到它）按会话串的形状取角色，卡本身还要在 DecideLocal 里对上 ID、角色和会话。
func (r *DesktopReader) approvalCharacter(ctx context.Context, k api.ConversationKey) (string, error) {
	if k.Platform == "local" && k.Channel == "console" && k.Scene == "private" && audience.IsWhisper(k.Conversation) {
		return strings.Split(k.Conversation, ":")[1], nil
	}
	return r.localCharacter(ctx, k)
}
