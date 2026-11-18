// Package localgroup 调度主人和本机角色的群聊；不经过 QQ 闸门，不自行调用模型。
package localgroup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/mention"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

const MaxReplies = 2
const AutoCooldown = 5 * time.Second

// Kernel 使用已有 Hub，保留记忆、工具与步骤记录；上下文取消后仍等 Outcome 收尾。
type Kernel interface {
	RespondContext(context.Context, turn.Input) <-chan turn.Outcome
	NotifySeen(turn.Seen)
}
type job struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type Service struct {
	// OnStored 在成员观察记录提交后、首位角色回合前调用；只许排任务，不等网络。
	OnStored   func(conversation string, seq int64, text string)
	st         *store.Store
	kernel     Kernel
	characters map[string]api.CharacterView
	now        func() time.Time
	mu         sync.Mutex
	closed     bool
	jobs       map[string]*job
	pending    sync.WaitGroup
	base       context.Context
	cancel     context.CancelFunc
}

var _ api.LocalGroupWriter = (*Service)(nil)

func New(st *store.Store, kernel Kernel, characters []api.CharacterView, now func() time.Time) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{st: st, kernel: kernel, characters: map[string]api.CharacterView{}, now: now, jobs: map[string]*job{}, base: ctx, cancel: cancel}
	for _, c := range characters {
		s.characters[c.ID] = c
	}
	return s
}
func Key(conversation string) api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: conversation}
}
func validKey(k api.ConversationKey) bool {
	_, ok := store.LocalGroupID(k.Conversation)
	return ok && k == Key(k.Conversation)
}

func (s *Service) enter(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("本地群聊服务已关闭")
	}
	s.pending.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.base, cancel)
	return ctx, func() { stop(); cancel(); s.pending.Done() }, nil
}
func (s *Service) Seal() {
	s.mu.Lock()
	s.closed = true
	for _, j := range s.jobs {
		j.cancel()
	}
	s.cancel()
	s.mu.Unlock()
}
func (s *Service) Wait()  { s.pending.Wait() }
func (s *Service) Close() { s.Seal(); s.Wait() }

func (s *Service) CreateLocalGroup(ctx context.Context, req api.LocalGroupCreate) (api.ConversationView, error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.ConversationView{}, err
	}
	defer done()
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return api.ConversationView{}, errors.New("群名需要 1 到 40 个可见字符，不能换行")
	}
	if len(req.Characters) < 2 || len(req.Characters) > 10 {
		return api.ConversationView{}, errors.New("请选择 2 到 10 个角色")
	}
	at := s.now()
	g := store.LocalGroup{ID: store.NewID(at), Name: name, CreatedAt: at.UnixMilli()}
	seen := map[string]bool{}
	for i, id := range req.Characters {
		if _, ok := s.characters[id]; !ok || seen[id] {
			return api.ConversationView{}, fmt.Errorf("角色不存在、资料读不到或重复：%q", id)
		}
		seen[id] = true
		g.Members = append(g.Members, store.LocalGroupMember{CharacterID: id, Position: i})
	}
	if err := s.st.CreateLocalGroup(ctx, g); err != nil {
		return api.ConversationView{}, err
	}
	return api.ConversationView{Key: Key(g.Conversation()), Title: g.Name, Observers: g.CharacterIDs(), CreatedAt: g.CreatedAt}, nil
}

// Mentions 只认独立的 @ID/名字；匹配名字只用于选候选，成员权限仍按 ID 校验。
func Mentions(text string, g store.LocalGroup, characters map[string]api.CharacterView) ([]string, error) {
	var found []string
	candidates := make([]mention.Candidate, 0, len(g.Members))
	for _, m := range g.Members {
		candidates = append(candidates, mention.Candidate{ID: m.CharacterID, Name: characters[m.CharacterID].Name})
	}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，。！？、,:：;；()（）[]［］\"“”", r)
	}) {
		target, at := strings.CutPrefix(word, "@")
		if !at || target == "" {
			continue
		}
		id, err := mention.ResolveToken(target, candidates)
		if err != nil {
			return nil, err
		}
		if id != "" && !slices.Contains(found, id) {
			found = append(found, id)
		}
	}
	return found, nil
}
func (s *Service) mentions(req api.LocalGroupSend, g store.LocalGroup) ([]string, error) {
	if len(req.Mentions) == 0 {
		return Mentions(req.Text, g, s.characters)
	}
	ids := g.CharacterIDs()
	var out []string
	for _, id := range req.Mentions {
		if !slices.Contains(ids, id) {
			return nil, fmt.Errorf("被 @ 的角色不在这个群：%q", id)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}
func memberAt(g store.LocalGroup, id string) int {
	for i, m := range g.Members {
		if m.CharacterID == id {
			return i
		}
	}
	return -1
}
func eligible(m store.LocalGroupMember, at int64) bool {
	return m.AutoCount < 1 && (m.LastAutoAt == 0 || at-m.LastAutoAt >= AutoCooldown.Milliseconds())
}

func (s *Service) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	if !validKey(req.Key) {
		return store.ErrDisplayScope
	}
	if strings.TrimSpace(req.Text) == "" || len(req.Text) > 32*1024 {
		return errors.New("消息不能为空，且不能超过 32 KiB")
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	j := &job{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	if s.jobs[req.Key.Conversation] != nil {
		s.mu.Unlock()
		return api.ErrLocalGroupBusy
	}
	s.jobs[req.Key.Conversation] = j
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.jobs, req.Key.Conversation); close(j.done); s.mu.Unlock() }()
	g, err := s.st.ReadLocalGroup(ctx, req.Key.Conversation)
	if err != nil {
		return err
	}
	for _, m := range g.Members {
		if _, ok := s.characters[m.CharacterID]; !ok {
			return fmt.Errorf("群成员的角色资料读不到：%s", m.CharacterID)
		}
	}
	observers := g.CharacterIDs()
	mentions, err := s.mentions(req, g)
	if err != nil {
		return err
	}
	for i := range g.Members {
		if len(mentions) == 0 || slices.Contains(mentions, g.Members[i].CharacterID) {
			g.Members[i].AutoCount = 0
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	seq, err := s.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: g.Conversation(), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: req.Text, CreatedAt: s.now().UnixMilli()}, observers)
	if err != nil {
		return err
	}
	if s.OnStored != nil {
		s.OnStored(g.Conversation(), seq, req.Text)
	}
	for _, id := range observers {
		s.kernel.NotifySeen(turn.Seen{CharacterID: id, Conversation: g.Conversation(), Seq: seq})
	}
	first := g.NextPosition % len(g.Members)
	if len(mentions) > 0 {
		first = memberAt(g, mentions[0])
	}
	var spoken []string
	triggerSeq := seq
	text := "[所有者] " + history.EscapeUserText(req.Text)
	speaker := "owner"
	roster := []string{"所有者和这些角色在本地群中聊天。每条 [角色 ID] 是对应角色说的话，只扮演你自己："}
	for _, m := range g.Members {
		roster = append(roster, m.CharacterID+"="+strconv.Quote(s.characters[m.CharacterID].Name))
	}
	relation := strings.Join(roster, "\n")
	index := first
	for n := range MaxReplies {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := g.Members[index].CharacterID
		direct := n == 0 || slices.Contains(mentions, id)
		if !direct {
			g.Members[index].AutoCount++
			g.Members[index].LastAutoAt = s.now().UnixMilli()
		}
		g.NextPosition = (index + 1) % len(g.Members)
		if err := s.st.AdvanceLocalGroup(ctx, g); err != nil {
			return err
		}
		in := turn.Input{CharacterID: id, Conversation: g.Conversation(), Seqs: []int64{triggerSeq}, Text: text,
			Audience:  audience.Audience{Channel: "console", Conversation: g.Conversation(), Scene: "group", PrivateOK: true, OwnerAsked: direct, PersonIDs: []string{speaker}},
			Addressed: direct || len(mentions) > 0, Scope: "local-group", Relation: relation, Scenes: []string{"scene-home-group"}, Observers: observers}
		in = withProgress(in, req, id, n)
		result := <-s.kernel.RespondContext(ctx, in)
		if result.Err != nil {
			return result.Err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if result.Result.Stopped || result.Result.Reply == "" {
			return nil
		}
		spoken = append(spoken, id)
		if n+1 == MaxReplies {
			break
		}
		if len(mentions) > 1 {
			index = memberAt(g, mentions[1])
			triggerSeq, err = history.MaxSeq(s.st.Events, g.Conversation())
			if err != nil {
				return err
			}
			// 触发事件是第一位的回复，主人原话已经在历史里；不把这条回复漏掉或冒充主人。
			text = "[角色 " + history.EscapeUserText(id) + "] " + history.EscapeUserText(result.Result.Reply)
			continue
		}
		replyMentions, err := Mentions(result.Result.Reply, g, s.characters)
		if err != nil {
			return err
		}
		candidates := replyMentions
		if len(mentions) == 0 && len(candidates) == 0 {
			for offset := 1; offset < len(g.Members); offset++ {
				candidates = append(candidates, g.Members[(index+offset)%len(g.Members)].CharacterID)
			}
		}
		index = -1
		for _, next := range candidates {
			i := memberAt(g, next)
			if !slices.Contains(spoken, next) && eligible(g.Members[i], s.now().UnixMilli()) {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		triggerSeq, err = history.MaxSeq(s.st.Events, g.Conversation())
		if err != nil {
			return err
		}
		text = "[角色 " + history.EscapeUserText(id) + "] " + history.EscapeUserText(result.Result.Reply)
		speaker = id
	}
	return nil
}

// withProgress 把这位成员这一轮的进度转给界面。转出去的编号用第几位发言，不用后台的运行号：界面只要分清换人。
func withProgress(in turn.Input, req api.LocalGroupSend, id string, n int) turn.Input {
	key := strconv.Itoa(n)
	var draft func(turn.LocalDraft)
	if req.Draft != nil {
		req.Draft(api.LocalDraft{TurnID: key, Step: -1, CharacterID: id})
		draft = func(v turn.LocalDraft) {
			req.Draft(api.LocalDraft{TurnID: key, Step: v.Step, Text: v.Text, CharacterID: id, Reset: v.Reset})
		}
	}
	var steps func([]turn.ProcessStep)
	if req.Process != nil {
		steps = func(list []turn.ProcessStep) {
			out := make([]api.LocalProcessStep, len(list))
			for i, s := range list {
				out[i] = api.LocalProcessStep{ID: s.ID, Kind: s.Kind, Tool: s.Tool, Target: s.Target, Thought: s.Thought, Output: s.Output, State: s.State, Rule: s.Rule, Started: s.Started, Seconds: s.Seconds, Step: s.Step, Recall: apiRecall(s.Recall)}
			}
			req.Process(key, out)
		}
	}
	return in.WithProgress(draft, steps)
}

func apiRecall(items []turn.RecallItem) []api.RecallItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.RecallItem, len(items))
	for i, v := range items {
		out[i] = api.RecallItem{Text: v.Text, Kind: v.Kind, Source: v.Source, CreatedAt: v.CreatedAt, Pinned: v.Pinned, Hearsay: v.Hearsay,
			Score: v.Score, Rerank: v.Rerank, Sim: v.Sim, Lexical: v.Lexical, Vector: v.Vector}
		for _, p := range v.Previous {
			out[i].Previous = append(out[i].Previous, api.RecallPrior{Text: p.Text, CreatedAt: p.CreatedAt})
		}
	}
	return out
}

func (s *Service) StopLocalGroup(ctx context.Context, k api.ConversationKey) error {
	if !validKey(k) {
		return store.ErrDisplayScope
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	if _, err := s.st.ReadLocalGroup(ctx, k.Conversation); err != nil {
		return err
	}
	s.mu.Lock()
	j := s.jobs[k.Conversation]
	if j != nil {
		j.cancel()
	}
	s.mu.Unlock()
	if j != nil {
		select {
		case <-j.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
