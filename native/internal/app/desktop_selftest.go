package app

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 自检对话（sumika-ctl chat，规格-本机控制接口第八节）：开发助手像主人一样在正在运行的程序里和角色说话。
// 走的是和界面发消息同一条路（sendLocal：准入、角色和话题所有权、同会话只跑一轮、审批），不另开后门；
// 会话固定落在自检话题里（audience.IsSelftest），那里存下的每一条事件都带 selftest 标记。

// 自检对话的结局
const (
	SelftestDone            = "done"             // 她说完了
	SelftestWaitingApproval = "waiting_approval" // 停在审批卡上：没替主人批，回合还在等，主人在界面上批或拒
	SelftestTimeout         = "timeout"          // 到时限还没说完：这一轮已取消
	SelftestStopped         = "stopped"          // 被停下、被打断
	SelftestError           = "error"            // 这一轮出错（模型报错等）
	SelftestBusy            = "busy"             // 这个自检话题上一轮还在跑，这一句没发
)

// ErrSelftestCharacter 是指定的角色不存在
var ErrSelftestCharacter = errors.New("没有这个角色")

// MaxSelftestText 是一句话的字数上限（按字算）
const MaxSelftestText = 4000

// SelftestRequest 是一次自检对话。Character 空用主窗口当前角色，没有就用角色目录里的第一个
type SelftestRequest struct {
	Character string
	New       bool
	Text      string
	Timeout   time.Duration
}

// SelftestTool 是这一轮调用的一个工具，取自运行记录（steps），不是从模型的话里凑的
type SelftestTool struct {
	Name   string
	Args   string // 参数摘要
	Result string // 结果摘要；没出结果是空
	Status string // running waiting done …
	Error  bool
}

// SelftestApproval 是停住的审批卡
type SelftestApproval struct {
	ID, Title, Tool, Subject, Why, Purpose, Preview string
	Notes                                           []string
}

// SelftestResult 是一次自检对话的结果
type SelftestResult struct {
	Status       string
	Character    string
	Topic        string // 话题 id
	Conversation string
	TurnID       string
	Reply        string // 她这一轮最后说的那句；没说是空
	Draft        string // 超时、等审批时已经说出来的半截
	Tools        []SelftestTool
	Model        string
	Route        string
	ElapsedMS    int64
	Approval     *SelftestApproval
	Error        string
}

const (
	selftestArgsMax    = 200
	selftestPreviewMax = 300
)

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// SelftestChat 往角色的自检话题发一句话，等这一轮结束（或停在审批上、到时限）
func (r *DesktopReader) SelftestChat(ctx context.Context, req SelftestRequest) (SelftestResult, error) {
	if err := r.bunOwned(); err != nil {
		return SelftestResult{}, err
	}
	d := r.desk
	a := d.app
	if a.opt.Mode == Archive {
		return SelftestResult{}, api.ErrLocalChatUnsupported
	}
	if strings.TrimSpace(req.Text) == "" {
		return SelftestResult{}, errors.New("消息不能为空")
	}
	if utf8.RuneCountInString(req.Text) > MaxSelftestText {
		return SelftestResult{}, errors.New("消息太长")
	}
	if req.Timeout <= 0 {
		return SelftestResult{}, errors.New("时限要大于 0")
	}
	char, topic, conv, err := r.selftestConversation(ctx, req.Character, req.New)
	if err != nil {
		return SelftestResult{}, err
	}
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}
	res := SelftestResult{Character: char, Topic: topic, Conversation: conv}

	// 这一轮的寿命不跟着控制连接走：停在审批上时命令先回，回合要留着等主人点；只有到时限才由这里取消
	sendCtx, cancelSend := context.WithCancel(context.Background())
	var mu sync.Mutex
	var draft string
	done := make(chan selftestOutcome, 1)
	// 先登记审批唤醒再发，不漏掉"发出去就立刻停在审批上"
	wake := make(chan struct{}, 1)
	d.mu.Lock()
	d.approvalWakes[wake] = struct{}{}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.approvalWakes, wake); d.mu.Unlock() }()
	// 发之前就在的审批卡不算这一句引起的（上一句停在审批上、卡还在）：这一句会因为话题还在跑回 busy，不是 waiting_approval
	old := map[string]bool{}
	for _, v := range d.localApprovals() {
		if v.Key.Conversation == conv {
			old[v.ID] = true
		}
	}
	started := time.Now()
	go func() {
		o, err := r.sendLocal(sendCtx, key, req.Text, func(v api.LocalDraft) {
			if v.Text == "" { // 一步收尾时的那次回调不带字：别把已经攒到的半截冲掉
				return
			}
			mu.Lock()
			draft = v.Text
			mu.Unlock()
		})
		cancelSend()
		done <- selftestOutcome{reply: replyOf(o), turnID: o.TurnID, stopped: o.Result != nil && o.Result.Stopped, err: err}
	}()
	timer := time.NewTimer(req.Timeout)
	defer timer.Stop()
	finish := func(o selftestOutcome) (SelftestResult, error) {
		res.ElapsedMS = time.Since(started).Milliseconds()
		res.TurnID, res.Reply = o.turnID, o.reply
		switch {
		case errors.Is(o.err, api.ErrLocalChatBusy):
			res.Status = SelftestBusy
			res.Error = "这个自检话题上一轮还在跑，这一句没发"
			res.Approval = d.pendingSelftestApproval(conv, nil)
		case errors.Is(o.err, api.ErrLocalChatUnsupported):
			return SelftestResult{}, o.err
		case o.err != nil && o.turnID == "" && o.reply == "" && !errors.Is(o.err, context.Canceled):
			// 没跑起来（准入失败、入口已关）
			return SelftestResult{}, o.err
		case o.err != nil:
			res.Status = SelftestError
			res.Error = RedactLog(o.err.Error(), a.tokens)
		case o.stopped:
			res.Status = SelftestStopped
		default:
			res.Status = SelftestDone
		}
		r.fillSelftestTrace(&res)
		if res.Status == SelftestDone && res.Reply == "" && res.Error == "" {
			// 没有错也没有话：照实说，不当成功
			res.Error = "这一轮结束了，她没有说话"
		}
		return res, nil
	}
	for {
		if ap := d.pendingSelftestApproval(conv, old); ap != nil {
			// 不替主人批，也不取消这一轮：卡留在界面上等主人点
			res.Status, res.Approval = SelftestWaitingApproval, ap
			res.ElapsedMS = time.Since(started).Milliseconds()
			mu.Lock()
			res.Draft = draft
			mu.Unlock()
			r.fillSelftestTrace(&res)
			return res, nil
		}
		select {
		case o := <-done:
			return finish(o)
		case <-wake:
		case <-timer.C:
			cancelSend()
			var o selftestOutcome
			select {
			case o = <-done:
			case <-time.After(10 * time.Second):
			}
			res.ElapsedMS = time.Since(started).Milliseconds()
			res.Status, res.TurnID = SelftestTimeout, o.turnID
			res.Error = "到了时限，这一轮已取消"
			mu.Lock()
			res.Draft = draft
			mu.Unlock()
			r.fillSelftestTrace(&res)
			return res, nil
		case <-ctx.Done():
			cancelSend() // 服务端的 ctx 是进程寿命：只有程序在退出才走到这里，别留着没人要的一轮（管道断开不取消，到时限才取消）
			return SelftestResult{}, ctx.Err()
		}
	}
}

type selftestOutcome struct {
	reply, turnID string
	stopped       bool
	err           error
}

func replyOf(o turn.Outcome) string {
	if o.Result == nil {
		return ""
	}
	return o.Result.Reply
}

// pendingSelftestApproval 是这个会话里还没人决定的审批卡（最早的一张，skip 里的不算）；没有是 nil
func (d *Desktop) pendingSelftestApproval(conv string, skip map[string]bool) *SelftestApproval {
	for _, v := range d.localApprovals() { // 已按 ID 排好
		if v.Key.Conversation != conv || skip[v.ID] {
			continue
		}
		return &SelftestApproval{ID: v.ID, Title: v.Title, Tool: v.Tool, Subject: v.Subject, Why: v.Why, Purpose: v.Purpose, Preview: clipRunes(v.Preview, selftestPreviewMax), Notes: v.Notes}
	}
	return nil
}

// fillSelftestTrace 从运行记录里读这一轮真正调用过的工具和用的模型。读不到不算失败：结果里就少这一块
func (r *DesktopReader) fillSelftestTrace(res *SelftestResult) {
	if res.TurnID == "" {
		return
	}
	a := r.desk.app
	tr := a.set.TraceStore()
	if tr == nil || tr.Trace == nil {
		return
	}
	rows, err := tr.Trace.Query(`SELECT COALESCE(name,''),COALESCE(args_json,''),COALESCE(preview,''),status,is_error FROM steps
		WHERE turn_id=? AND kind='tool' ORDER BY COALESCE(started_at,0), rowid`, res.TurnID)
	if err == nil {
		for rows.Next() {
			var t SelftestTool
			var isErr int
			var args, preview string
			if rows.Scan(&t.Name, &args, &preview, &t.Status, &isErr) != nil {
				break
			}
			t.Args = RedactLog(clipRunes(args, selftestArgsMax), a.tokens)
			t.Result = RedactLog(clipRunes(preview, selftestPreviewMax), a.tokens)
			t.Error = isErr != 0
			res.Tools = append(res.Tools, t)
		}
		rows.Close()
	}
	var model, provider, route, runErr sql.NullString
	err = tr.Trace.QueryRow(`SELECT model,route_provider,route_model,error FROM model_runs WHERE turn_id=? AND stage<>'reply-retry'
		ORDER BY started_at DESC, rowid DESC LIMIT 1`, res.TurnID).Scan(&model, &provider, &route, &runErr)
	if err == nil {
		res.Model = model.String
		if route.Valid && route.String != "" {
			res.Route = strings.Trim(provider.String+"/"+route.String, "/")
		}
		if res.Error == "" && runErr.Valid && runErr.String != "" && res.Reply == "" {
			res.Error = RedactLog(clipRunes(runErr.String, selftestPreviewMax), a.tokens)
			if res.Status == SelftestDone {
				res.Status = SelftestError
			}
		}
	}
}

// selftestConversation 定下自检对话的角色和会话：character 空用主窗口当前角色（没有就取目录里第一个）；
// newTopic 开一个没用过的话题，否则接着该角色最新的自检话题（没有就建默认的）。chat 和 goal、loop 命令共用。
func (r *DesktopReader) selftestConversation(ctx context.Context, character string, newTopic bool) (char, topic, conv string, err error) {
	a := r.desk.app
	char, err = a.selftestCharacter(character)
	if err != nil {
		return "", "", "", err
	}
	st, err := a.set.For("console")
	if err != nil {
		return "", "", "", err
	}
	now := time.Now()
	var title string
	if newTopic {
		// --new 一定要开一个没用过的话题（清空上下文）：同一毫秒撞了 id 就加序号再试
		title = store.SelftestTopicTitle + " " + now.Format("01-02 15:04:05")
		stamp := strconv.FormatInt(now.UnixMilli(), 36)
		for n := 0; ; n++ {
			topic = audience.SelftestTopicID(char, stamp)
			if n > 0 {
				topic = audience.SelftestTopicID(char, stamp+"-"+strconv.Itoa(n))
			}
			created, err := st.EnsureSelftestTopic(ctx, char, topic, title, now.UnixMilli())
			if err != nil {
				return "", "", "", err
			}
			if created {
				break
			}
			if n >= 50 {
				return "", "", "", errors.New("开不出新的自检话题")
			}
		}
	} else {
		if topic, err = st.LatestSelftestTopic(ctx, char); err != nil {
			return "", "", "", err
		}
		if topic == "" {
			topic = audience.SelftestTopicID(char, "")
		}
		if _, err := st.EnsureSelftestTopic(ctx, char, topic, "", now.UnixMilli()); err != nil {
			return "", "", "", err
		}
	}
	conv = audience.SelftestConversation(char, topic)
	return char, topic, conv, nil
}

// selftestCharacter 把 sumika-ctl 给的角色定成具体的角色 id：空用主窗口当前角色（没有就取目录里第一个），写了的必须在目录里。
func (a *App) selftestCharacter(character string) (string, error) {
	ids, err := a.characterIDs()
	if err != nil {
		return "", err
	}
	switch {
	case character == "":
		if len(ids) == 0 {
			return "", ErrSelftestCharacter
		}
		if slices.Contains(ids, a.character) {
			return a.character, nil
		}
		return ids[0], nil
	case !slices.Contains(ids, character):
		return "", ErrSelftestCharacter
	}
	return character, nil
}

// SelftestCharacter 是 selftestCharacter 的对外口子，给 bun 模式下的 sumika-ctl 用：自检话题由 Bun 建，Bun 不知道主窗口选了谁，
// 空的角色由 Go 先换成具体的 id（只读角色目录，不碰任何库，不受 bunOwned 管）。
func (r *DesktopReader) SelftestCharacter(character string) (string, error) {
	if r == nil || r.desk == nil || r.desk.app == nil {
		return "", errors.New("后台还没就绪")
	}
	return r.desk.app.selftestCharacter(character)
}
