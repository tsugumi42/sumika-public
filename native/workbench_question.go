package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// 问题卡的宿主侧（规格-向主人提问）：订阅后台的待答问题、画卡、把点选回给后台。
// 回答不是批准：不碰 approvals；打字回答走输入区自己的发送（submitComposer）。

type questionUI struct {
	busy  bool
	err   string
	shown time.Time // 卡片第一次出现的时刻
}

func (a *app) questionStateOf(id string) *questionUI {
	if a.questionState == nil {
		a.questionState = map[string]*questionUI{}
	}
	s := a.questionState[id]
	if s == nil {
		s = &questionUI{}
		a.questionState[id] = s
	}
	return s
}

// hasQuestion 报告这个会话有没有她在等回答的问题。
func (a *app) hasQuestion(k api.ConversationKey) bool {
	for _, v := range a.questions {
		if v.Key == k {
			return true
		}
	}
	return false
}

func (a *app) watchLocalQuestions(ctx context.Context, r api.LocalQuestions, gen uint64) {
	sub, err := r.WatchLocalQuestions(ctx)
	if errors.Is(err, api.ErrLocalChatUnsupported) {
		return
	}
	if err != nil {
		a.post(func() {
			if a.srcGen == gen && ctx.Err() == nil {
				a.questionErr = "读取问题失败：" + err.Error()
			}
		})
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		value := snap.Value
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			a.questions = value
			active := map[string]bool{}
			for _, v := range value {
				active[v.ID] = true
			}
			for id := range a.questionState {
				if !active[id] {
					delete(a.questionState, id)
				}
			}
		})
	}
}

// workbenchQuestions 画这个会话的问题卡（输入区上方、审批卡下面）。
func (a *app) workbenchQuestions(c *ui.Context, k api.ConversationKey) {
	th := c.Theme()
	if a.questionErr != "" {
		ui.Text(c.Key("question-error"), a.questionErr).Font(th.Font).TextColor(th.Danger).Wrap().FillWidth()
	}
	var mine []api.LocalQuestion
	byID := map[string]api.LocalQuestion{}
	for _, v := range a.questions {
		if v.Key == k {
			mine = append(mine, v)
			byID[v.ID] = v
		}
	}
	if len(mine) == 0 {
		return
	}
	h := a.approvalHost(c)
	h.OnAnswer = func(id string, option int) {
		if v, ok := byID[id]; ok {
			a.answerLocal(v, a.questionStateOf(id), option)
		}
	}
	ui.Scroll(c.Key("local-questions")).MaxHeight(320).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(h.T.S2).Children(func() {
			for _, v := range mine {
				state := a.questionStateOf(v.ID)
				if state.shown.IsZero() {
					state.shown = c.Now()
				}
				info := chatui.QuestionInfo{ID: v.ID, Text: strings.TrimSpace(v.Text), Shown: state.shown, Busy: state.busy, Error: state.err}
				for _, o := range v.Options {
					info.Options = append(info.Options, chatui.QuestionOption{Label: o.Label, Detail: o.Detail})
				}
				h.QuestionCard(c, info, th.Border, th.Surface)
			}
		})
	})
}

func (a *app) answerLocal(v api.LocalQuestion, state *questionUI, option int) {
	qr, ok := a.reader.(api.LocalQuestions)
	if !ok || state.busy {
		return
	}
	state.busy, state.err = true, ""
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		err := qr.AnswerLocal(ctx, v.Key, v.ID, option)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			state.busy = false
			if errors.Is(err, api.ErrQuestionGone) {
				return // 已经结束了：卡片随订阅消失，不留错误
			}
			if err != nil {
				state.err = err.Error()
			}
		})
	}()
}
