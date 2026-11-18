package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/memory"
	"sumika-native/internal/memplugin"
	"sumika-native/internal/memwrite"
	"sumika-native/internal/paths"
	"sumika-native/internal/prompts"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

type issue17Event struct {
	Character, Conversation, Actor, Text string
	Seq, At                              int64
}
type issue17World struct {
	p           paths.Paths
	root, model string
	gateway     *issue17Gateway
	set         *store.Set
	mems        map[string]*memory.Memory
	pool        *memory.Memory
	hub         *turn.Hub
	plugin      *memplugin.Plugin
	cancel      context.CancelFunc
	mu          sync.Mutex
	logs        []string
	events      []issue17Event
	failFast    bool // 故障注入要在首次失败时检查回滚，正常场景等待正式排程的退避。
}

func issue17DB(data, char string) string {
	return filepath.Join(data, "go", "characters", char, "memory.db")
}
func issue17URI(file string) string {
	return "file:" + strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
}
func issue17Exec(file, q string, args ...any) error {
	// 夹具在后台任务还开着库时改库（建触发器等）：和生产一样带 busy_timeout，等对方的写锁放掉，不立刻报 SQLITE_BUSY
	db, err := sql.Open("sqlite", issue17URI(file)+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(q, args...)
	return err
}
func issue17Name(char string) string {
	if char == issue17Other {
		return "曜"
	}
	return "岚"
}
func newIssue17World(ctx context.Context, dir, root, model string, g *issue17Gateway) (*issue17World, error) {
	p, err := paths.Init(filepath.Join(dir, "data"))
	if err != nil {
		return nil, err
	}
	w := &issue17World{p: p, root: root, model: model, gateway: g}
	for _, char := range []string{issue17Character, issue17Other} {
		file := issue17DB(p.Root, char)
		rel, relErr := filepath.Rel(p.Root, file)
		if relErr != nil {
			return nil, relErr
		}
		created, _, err := memory.CreateIfEmpty(ctx, p.Root, file, p.UpgradeSnapshotDir(), filepath.ToSlash(rel))
		if err != nil {
			return nil, err
		}
		if !created {
			return nil, fmt.Errorf("%s 不是空库，没有新建", file)
		}
		for name, text := range map[string]string{"character.yml": "name: " + issue17Name(char) + "\n", "character.md": "## 身份\n我是" + issue17Name(char) + "。只根据已知资料回答，不知道就说不知道。\n\n## 表达\n回答简短。\n"} {
			if err = issue17Write(filepath.Join(p.CardDir(char), name), []byte(text)); err != nil {
				return nil, err
			}
		}
	}
	return w, nil
}
func (w *issue17World) logf(f string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logs = append(w.logs, fmt.Sprintf(f, args...))
}
func (w *issue17World) open(ctx context.Context, cfg config.MemoryWrite, hasModel bool) error {
	var err error
	w.set, err = store.OpenGo(w.p, "local", "qq")
	if err != nil {
		return err
	}
	w.mems = map[string]*memory.Memory{}
	ownerRel := filepath.Base(w.p.OwnerMemoryDB())
	if rel, err := filepath.Rel(w.p.Root, w.p.OwnerMemoryDB()); err == nil {
		ownerRel = filepath.ToSlash(rel)
	}
	w.pool, err = memory.OpenOwnerPool(ctx, w.p.OwnerMemoryDB(), w.gateway.embedding, nil, memory.Options{
		Writable: true, SummarizeEnabled: true, AutoConfirm: cfg.AutoConfirm, ConfirmOccasions: cfg.ConfirmOccasions, ConfirmKinds: cfg.ConfirmKinds,
		SnapshotDir: w.p.UpgradeSnapshotDir(), SnapshotRel: ownerRel,
		Logf: w.logf, SourceEvents: func(char string) (memory.EventSource, error) { return memsource.NewMulti(w.set, char) },
	})
	if err != nil {
		return err
	}
	for _, char := range []string{issue17Character, issue17Other} {
		file := issue17DB(w.p.Root, char)
		in := memwriteInput(w, cfg, file, char, hasModel)
		m, err := openIssue17Memory(ctx, in, w, char)
		if err != nil {
			_ = w.close()
			return err
		}
		w.mems[char] = m
	}
	life, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	client := llm.New(llm.Magpie(w.gateway.server.URL + "/v1"))
	// NoStartRebuild：评测只靠注入的记忆答题，RecentMessages: 1 是拿来把聊天历史挡在外面的；
	// 本地私聊和主人 QQ 私聊从起点重建会绕过这个前提（题面用的是 exam 会话，也不能让上一轮的话溜进来）
	tc := turn.Config{Root: w.root, Data: w.p, Models: map[string]string{issue17Character: w.model, issue17Other: w.model}, RecentMessages: 1, NoStartRebuild: true, Workdir: w.p.Root, Now: time.Now, Logf: w.logf, Retry: turn.RetryPolicy{Max: -1}}
	// 记忆工具照生产经过 Gate；夹具拒绝所有本地执行工具，不触碰宿主文件。
	tc.Gate = approval.NewGateWith(w.p.Root, nil)
	tc.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
		return tools.Result{Text: "issue17 夹具只允许记忆工具，没有执行本地操作。", IsError: true}, nil
	}
	w.hub = turn.NewHub(life, tc, w.set, client, []llm.Model{{ID: w.model, ContextLength: 128000, MaxOutputTokens: 2048}})
	w.plugin = memplugin.New(memplugin.Options{Memories: func(char string) *memory.Memory { return w.mems[char] }, Characters: func() []string { return []string{issue17Character, issue17Other} }, Threshold: 1, Quiet: time.Hour, Parallel: 2, Logf: w.logf})
	if err = w.hub.Use(w.plugin); err != nil {
		_ = w.close()
		return err
	}
	return nil
}
func (w *issue17World) close() error {
	if w.hub != nil {
		w.hub.StopPlugin(memplugin.ID)
		w.hub = nil
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	var err error
	for _, m := range w.mems {
		err = errors.Join(err, m.Close())
	}
	w.mems = nil
	if w.pool != nil {
		err = errors.Join(err, w.pool.Close())
		w.pool = nil
	}
	if w.set != nil {
		err = errors.Join(err, w.set.Close())
		w.set = nil
	}
	return err
}
func issue17Write(file string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	return os.WriteFile(file, b, 0600)
}
func issue17Enabled() config.MemoryWrite {
	c := config.Defaults().Memory.Write
	c.Characters = []string{issue17Character, issue17Other}
	c.Summarize = append([]string(nil), c.Characters...)
	c.Places = memory.AllWritePlaces()
	c.CatchUp = "cursor"
	c.AutoConfirm = true
	return c
}
func (w *issue17World) append(char, conv, actor, text string, seen bool) (int64, error) {
	channel, _, _ := strings.Cut(conv, ":")
	st, err := w.set.For(channel)
	if err != nil {
		return 0, err
	}
	e := store.NewEvent{Channel: channel, Conversation: conv, Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}
	switch actor {
	case "owner":
		e.ActorKind, e.ActorID, e.Origin = "owner", "owner", "owner"
	case char:
		e.ActorKind, e.ActorID, e.Origin = "character", char, "character"
	default:
		e.ActorKind, e.ActorID, e.Origin = "human", actor, "peer"
	}
	observers := []string{}
	if seen {
		observers = []string{char}
	}
	seq, err := st.AppendEvent(e, observers)
	if err == nil {
		w.mu.Lock()
		w.events = append(w.events, issue17Event{char, conv, actor, text, seq, e.CreatedAt})
		w.mu.Unlock()
	}
	return seq, err
}
func (w *issue17World) deliver(ctx context.Context, char, conv string, lines []issue17Event) error {
	m := w.mems[char]
	if m.Writable() {
		if _, err := m.InitCursor(ctx, conv, 0); err != nil {
			return err
		}
	}
	var last int64
	for _, e := range lines {
		seq, err := w.append(char, conv, e.Actor, e.Text, true)
		if err != nil {
			return err
		}
		last = seq
	}
	w.hub.NotifySeen(turn.Seen{CharacterID: char, Conversation: conv, Seq: last})
	return w.wait(ctx, char, conv, last)
}
func (w *issue17World) wait(ctx context.Context, char, conv string, last int64) error {
	// 正式退避会从两分钟翻倍；等到生产 MaxFailures 停止，不能在第二次退避前截断。
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		s, ok, err := w.mems[char].SummaryState(ctx, conv)
		if err != nil {
			return err
		}
		if ok && s.LastSeq >= last {
			// 游标先提交，之后才发布共用池。先等角色写锁，再走正式交接恢复屏障；
			// 发布失败照实返回，不能把“游标已挪”误当成共用事实已经可读。
			if _, err := w.mems[char].InitCursor(ctx, conv, last); err != nil {
				return err
			}
			return w.mems[char].FlushShared(ctx)
		}
		if s.Failures >= memory.MaxFailures || (w.failFast && s.Failures > 0) {
			return fmt.Errorf("整理失败：%s %s failures=%d", char, conv, s.Failures)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func issue17Audience(conv, actor string) audience.Audience {
	channel, _, _ := strings.Cut(conv, ":")
	scene := "private"
	if strings.Contains(conv, ":group:") {
		scene = "group"
	}
	// 对齐 channel.AudienceOf：本地可信，QQ 私聊认主人；QQ 群只看 OwnerAsked。
	privateOK := channel == "console" || (scene == "private" && actor == "owner")
	return audience.Audience{Channel: channel, Conversation: conv, Scene: scene, PrivateOK: privateOK, OwnerAsked: actor == "owner", PersonIDs: []string{actor}}
}

// 把生产开写前提保留在一处，所有 A/B、重启与故障分支复用。
// 不通过 app.Open：本工具不启动真实接收、发送、模型子进程或读取用户配置。
func memwriteInput(w *issue17World, cfg config.MemoryWrite, file, char string, hasModel bool) memwrite.Input {
	return memwrite.Input{Data: w.p.Root, DB: file, Char: char, Cfg: cfg, HasModel: hasModel, BackupDir: filepath.Join(w.p.Root, "backups"), Logf: w.logf}
}
func openIssue17Memory(ctx context.Context, in memwrite.Input, w *issue17World, char string) (*memory.Memory, error) {
	events, err := memsource.NewMulti(w.set, char)
	if err != nil {
		return nil, err
	}
	return memwrite.Open(ctx, in, func(s memwrite.Settings) (*memory.Memory, error) {
		o := memory.Options{Character: char, Name: issue17Name(char), Events: events, Gen: issue17Generator{llm.New(llm.Magpie(w.gateway.server.URL + "/v1"))}, Model: func() string { return w.model }, Prompts: prompts.Store{Root: w.root}, Logf: w.logf}
		o.Shared = w.pool
		s.Apply(&o)
		return memory.Open(ctx, in.DB, w.gateway.embedding, nil, o)
	})
}
