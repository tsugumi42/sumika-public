package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/mention"
	"sumika-native/internal/whisper"
)

// 悄悄话的控制接口入口（sumika-ctl whisper，规格-群聊引用和路由 第十一节）。
// 来源只有一种：命令行给的文字，挂在第一个角色的自检话题上，不读真实 QQ 会话和本地群的事件，请求里也没有任何字段能指向它们。

// ErrWhisperBatch 说这个批号这次运行里没见过（状态只记在进程里，重启后看库里的状态事件）。
var ErrWhisperBatch = errors.New("没有这个批号：只认这次运行里发出的悄悄话")

// SelftestWhisperRequest 是一次自检悄悄话：角色们、引用的文字、引用者的名字（空用默认）、主人说的话、等结局的时限。
type SelftestWhisperRequest struct {
	Characters            []string
	SourceText, QuoteName string
	Say                   string
	Timeout               time.Duration
}

// WhisperOutcome 是一批悄悄话现在的样子：每位角色的现状，和已经完成的处理记录。
type WhisperOutcome struct {
	Batch   string
	Entries []whisper.Entry
	Notes   map[string]string // 角色 → 处理记录
	Pending bool              // 到时限还有没落定的
	Elapsed time.Duration
}

// SelftestWhisper 发一条自检悄悄话并等到结局或时限。
func (r *DesktopReader) SelftestWhisper(ctx context.Context, req SelftestWhisperRequest) (WhisperOutcome, error) {
	if err := r.bunOwned(); err != nil {
		return WhisperOutcome{}, err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return WhisperOutcome{}, err
	}
	defer done()
	a := r.desk.app
	if a.whisper == nil {
		return WhisperOutcome{}, api.ErrLocalChatUnsupported
	}
	if len(req.Characters) == 0 {
		return WhisperOutcome{}, ErrSelftestCharacter
	}
	var cands []mention.Candidate
	for _, id := range req.Characters {
		found := false
		for _, c := range a.displayCharacters() {
			if c.State == api.Ready && c.Value.ID == id {
				cands = append(cands, mention.Candidate{ID: id, Name: c.Value.Name})
				found = true
			}
		}
		if !found {
			return WhisperOutcome{}, ErrSelftestCharacter
		}
	}
	first := req.Characters[0]
	source := audience.SelftestConversation(first, audience.SelftestTopicID(first, ""))
	now := a.now()
	name := req.QuoteName
	if name == "" {
		name = "群友"
	}
	part := history.Part{
		Quote:    &history.Quote{ID: "selftest-" + strconv.FormatInt(now.UnixNano(), 36), Name: name, At: now.UnixMilli(), Clock: now.Format("15:04"), Text: req.SourceText, Whole: true},
		Text:     req.Say,
		Mentions: slices.Clone(req.Characters),
	}
	start := time.Now()
	rec, err := a.whisper.Send(ctx, whisper.Request{Source: source, Candidates: cands, Parts: []history.Part{part}})
	if err != nil {
		return WhisperOutcome{}, err
	}
	wctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	_, waitErr := a.whisper.Wait(wctx, rec.Batch)
	out := a.whisperOutcome(ctx, rec.Batch)
	out.Elapsed = time.Since(start)
	out.Pending = waitErr != nil
	return out, nil
}

// WhisperStatus 看一批现在的状态。
func (r *DesktopReader) WhisperStatus(ctx context.Context, batch string) (WhisperOutcome, error) {
	a := r.desk.app
	if a.whisper == nil {
		return WhisperOutcome{}, api.ErrLocalChatUnsupported
	}
	out := a.whisperOutcome(ctx, batch)
	if len(out.Entries) == 0 || !selftestBatch(out.Entries) {
		return WhisperOutcome{}, ErrWhisperBatch
	}
	return out, nil
}

// WhisperCancel 取消一批：排队中的移除，处理中的取消那一轮；已经结束的不动。返回取消后的状态。
func (r *DesktopReader) WhisperCancel(ctx context.Context, batch string) (WhisperOutcome, error) {
	if err := r.bunOwned(); err != nil {
		return WhisperOutcome{}, err
	}
	a := r.desk.app
	if a.whisper == nil {
		return WhisperOutcome{}, api.ErrLocalChatUnsupported
	}
	if es := a.whisper.Status(batch); len(es) == 0 || !selftestBatch(es) {
		return WhisperOutcome{}, ErrWhisperBatch
	}
	a.whisper.Cancel(batch)
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	a.whisper.Wait(wctx, batch)
	return a.whisperOutcome(ctx, batch), nil
}

// selftestBatch 说这一批的来源都是自检话题：控制接口只碰自检发的悄悄话，界面发的真实悄悄话（它的批号、处理记录）不经它看或取消。
func selftestBatch(es []whisper.Entry) bool {
	for _, e := range es {
		if !audience.IsSelftest(e.Source) {
			return false
		}
	}
	return true
}

func (a *App) whisperOutcome(ctx context.Context, batch string) WhisperOutcome {
	out := WhisperOutcome{Batch: batch, Entries: a.whisper.Status(batch), Notes: map[string]string{}}
	for _, e := range out.Entries {
		if e.State == whisper.StateDone {
			if n := a.whisper.NoteOf(ctx, batch, e.Character, e.Source); n != "" {
				out.Notes[e.Character] = n
			}
		}
		if !whisper.Terminal(e.State) {
			out.Pending = true
		}
	}
	return out
}
