package app

import (
	"context"
	"errors"
	"reflect"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/whisperlog"
)

// 对话日志的装配（规格-群聊引用和路由 第十五节）：界面和 sumika-ctl 读的是同一个 whisperlog.Reader。
// 只读：不往任何库写，也没有发 QQ 的出口。

var _ api.WhisperLogReader = (*DesktopReader)(nil)

// ErrLogSource 说这个会话不是悄悄话的来源（本地私聊、悄悄话会话自己、别的格式）。
var ErrLogSource = errors.New("这个会话没有对话日志")

// whisperLogPoll 是日志里有没到终态的批时，没有唤醒也隔多久重读一次：steps、kernel_tasks 的写入还没有失效通知，先用定时拉。
const whisperLogPoll = time.Second

// logReader 建读日志用的 Reader；本地库或悄悄话服务没装上时报不支持。
func (a *App) logReader() (*whisperlog.Reader, error) {
	st, ok := a.set.Platform("local")
	if !ok || a.whisper == nil {
		return nil, api.ErrLocalChatUnsupported
	}
	return &whisperlog.Reader{St: st, Position: a.logPosition}, nil
}

func (a *App) logPosition(batch, character string) (int, bool) {
	for _, e := range a.whisper.Status(batch) {
		if e.Character == character {
			return e.Position, true
		}
	}
	return 0, false
}

// logSource 只认界面里真有悄悄话入口的来源：本地群聊、QQ 私聊、QQ 群。
func logSource(k api.ConversationKey) (string, bool) {
	if _, ok := whisperSourceKey(k.Conversation); !ok {
		return "", false
	}
	return k.Conversation, true
}

func (r *DesktopReader) ReadWhisperLog(ctx context.Context, k api.ConversationKey, cursor string, limit int) (ctlproto.LogPage, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return ctlproto.LogPage{}, err
	}
	defer done()
	src, ok := logSource(k)
	if !ok {
		return ctlproto.LogPage{}, ErrLogSource
	}
	lr, err := r.desk.app.logReader()
	if err != nil {
		return ctlproto.LogPage{}, err
	}
	return lr.Read(ctx, whisperlog.Request{Source: src, Cursor: cursor, Limit: limit})
}

func (r *DesktopReader) ReadWhisperLogEntry(ctx context.Context, k api.ConversationKey, id string, withThinking bool) (ctlproto.LogDetail, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return ctlproto.LogDetail{}, err
	}
	defer done()
	src, ok := logSource(k)
	if !ok {
		return ctlproto.LogDetail{}, ErrLogSource
	}
	lr, err := r.desk.app.logReader()
	if err != nil {
		return ctlproto.LogDetail{}, err
	}
	return lr.Detail(ctx, src, id, withThinking)
}

type logSub struct {
	out    chan api.Snapshot[ctlproto.LogPage]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *logSub) Updates() <-chan api.Snapshot[ctlproto.LogPage] { return s.out }
func (s *logSub) Close()                                         { s.cancel(); <-s.done }

// WatchWhisperLog 先登记唤醒再读第一页；悄悄话服务的状态变了就重读，页里有没到终态的批时再加一个定时重读。
// 内容没变不发快照；后到的快照总是重读的结果，旧页不会把新状态改回去。
func (r *DesktopReader) WatchWhisperLog(ctx context.Context, k api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return nil, err
	}
	src, ok := logSource(k)
	if !ok {
		done()
		return nil, ErrLogSource
	}
	a := r.desk.app
	lr, err := a.logReader()
	if err != nil {
		done()
		return nil, err
	}
	wake, unwatch := a.whisper.Watch() // 先登记，再读第一页
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	s := &logSub{out: make(chan api.Snapshot[ctlproto.LogPage], 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer done()
		defer close(s.done)
		defer close(s.out)
		defer stop()
		defer cancel()
		defer unwatch()
		var last ctlproto.LogPage
		sent := false
		version := uint64(1)
		for {
			page, err := lr.Read(ctx, whisperlog.Request{Source: src})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				snap := api.Snapshot[ctlproto.LogPage]{Version: version, Result: api.Result[ctlproto.LogPage]{State: api.Failed, Err: err, Value: last, Stale: sent}}
				select {
				case s.out <- snap:
					version++
				case <-ctx.Done():
					return
				}
			} else if !sent || !reflect.DeepEqual(page, last) {
				select {
				case s.out <- api.Snapshot[ctlproto.LogPage]{Version: version, Result: api.Result[ctlproto.LogPage]{State: api.Ready, Value: page}}:
					last, sent = page, true
					version++
				case <-ctx.Done():
					return
				}
			}
			var tick <-chan time.Time
			var timer *time.Timer
			if last.Running || err != nil {
				timer = time.NewTimer(whisperLogPoll)
				tick = timer.C
			}
			select {
			case <-wake:
			case <-tick:
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			}
			if timer != nil {
				timer.Stop()
			}
		}
	}()
	return s, nil
}

func (r *DesktopReader) CancelWhisper(ctx context.Context, k api.ConversationKey, batch, character string) (int, error) {
	if err := r.bunOwned(); err != nil {
		return 0, err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return 0, err
	}
	defer done()
	src, ok := logSource(k)
	if !ok || !ctlproto.ValidBatch(batch) || !ctlproto.ValidCharacter(character) {
		return 0, ErrLogSource
	}
	a := r.desk.app
	lr, err := a.logReader()
	if err != nil {
		return 0, err
	}
	links, err := lr.St.WhisperLinksOf(ctx, batch)
	if err != nil {
		return 0, err
	}
	if len(links) == 0 {
		return 0, ErrWhisperBatch
	}
	mine := false
	for _, l := range links {
		if l.Source != src { // 批号属于别的会话：不取消，也不透露它存在
			return 0, ErrWhisperBatch
		}
		mine = mine || l.Character == character
	}
	if !mine {
		return 0, ErrWhisperBatch
	}
	return a.whisper.CancelOf(batch, character), nil
}

// WhisperLog 是 sumika-ctl whisper log 的入口：只认自检来源，读不到任何真实会话的日志。
func (r *DesktopReader) WhisperLog(ctx context.Context, args ctlproto.WhisperLogArgs) (ctlproto.WhisperLogResult, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	defer done()
	lr, err := r.desk.app.logReader()
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	src := args.Conversation
	if src == "" && args.Batch != "" { // 只给批号：按它的来源找，来源不是自检就当没有
		links, err := lr.St.WhisperLinksOf(ctx, args.Batch)
		if err != nil {
			return ctlproto.WhisperLogResult{}, err
		}
		if len(links) > 0 {
			src = links[0].Source
		}
	}
	if !audience.IsSelftest(src) {
		return ctlproto.WhisperLogResult{}, ErrWhisperBatch
	}
	if args.Entry != "" {
		d, err := lr.Detail(ctx, src, args.Entry, args.WithThinking)
		if err != nil {
			return ctlproto.WhisperLogResult{}, err
		}
		return ctlproto.WhisperLogResult{Detail: &d}, nil
	}
	p, err := lr.Read(ctx, whisperlog.Request{Source: src, Cursor: args.Cursor, Limit: args.Limit, Batch: args.Batch})
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	if !args.WithThinking { // 列表摘要里的思考第一行也算思考原文：默认抹掉，只留字数
		for i := range p.Entries {
			if p.Entries[i].Kind == ctlproto.LogThink {
				p.Entries[i].Summary = ""
			}
		}
	}
	return ctlproto.WhisperLogResult{Page: &p}, nil
}

// WhisperList 是 sumika-ctl whisper list 的入口：只给批号、角色、状态和时间，不给任何正文。
func (r *DesktopReader) WhisperList(ctx context.Context, selftest bool) (ctlproto.WhisperLogResult, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	defer done()
	lr, err := r.desk.app.logReader()
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	links, err := lr.St.WhisperLinksRecent(ctx, selftest, 50)
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	res := ctlproto.WhisperLogResult{Items: []ctlproto.WhisperListItem{}}
	for _, l := range links {
		res.Items = append(res.Items, ctlproto.WhisperListItem{Batch: l.Batch, Character: l.Character, State: l.State, At: l.CreatedAt})
	}
	return res, nil
}
