package memwrite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/memory"
	"sumika-native/internal/memory/memtest"
	"sumika-native/internal/prompts"
	"sumika-native/internal/testutil/junction"
)

// 写入开关和开之前的检查（规格-记忆写入上线 W5 验收 1、4、6、9）。数据目录、库、名字都在临时目录里造，不碰真实的 data/

type fx struct {
	t      *testing.T
	data   string
	goDB   string
	bunDB  string
	backup string
	in     Input
	logs   *logs
	events *fakeEvents
	opens  []Settings
}

type logs struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.text, format+"\n", args...)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// fakeEvents：只够 catchUp 用的事件来源（会话的最新 seq、单个会话读事件）
type fakeEvents struct {
	heads []memory.ConvHead
	err   error
}

func (f *fakeEvents) Events(_ context.Context, conv string, after int64, limit int) ([]memory.Event, error) {
	var out []memory.Event
	for _, h := range f.heads {
		if h.Conversation == conv {
			for s := after + 1; s <= h.LastSeq && len(out) < limit; s++ {
				out = append(out, memory.Event{Seq: s, At: time.Date(2026, 11, 2, 3, 0, 0, 0, time.Local).UnixMilli() + s*1000, Conversation: conv, Kind: "message", Origin: "owner", ActorKind: "owner", ActorID: "owner", Text: "编造的待整理消息", Seen: true})
			}
		}
	}
	return out, nil
}
func (f *fakeEvents) Revoked(context.Context, string, []int64) (map[int64]bool, error) {
	return nil, nil
}
func (f *fakeEvents) LastSaid(_ context.Context, conv string) (memory.Event, bool, error) {
	return memory.Event{Conversation: conv, Kind: "message", Origin: "owner", ActorKind: "owner"}, true, nil
}
func (f *fakeEvents) Find(context.Context, string, []string, int) ([]memory.Event, error) {
	return nil, nil
}
func (f *fakeEvents) Conversations(context.Context) ([]memory.ConvHead, error) {
	return f.heads, f.err
}

func makeBunDB(t *testing.T, file string, migrate bool) {
	t.Helper()
	memtest.BunShaped(t, file)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES ('F1', 'fact', '编的事实', 'owner', 'private', 'active', 1000)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if migrate {
		if err := memory.Migrate(context.Background(), file); err != nil {
			t.Fatal(err)
		}
	}
}

// newFx：数据目录里有 Go 的副本 go/characters/suzu/memory.db（迁移好的）和 Bun 的原库 characters/suzu/memory.db；
// 配置只开 remember（characters: [suzu]），别的都是默认值
func newFx(t *testing.T) *fx {
	t.Helper()
	f := &fx{t: t, data: t.TempDir(), backup: filepath.Join(t.TempDir(), "备份"), logs: &logs{}, events: &fakeEvents{}}
	f.goDB = filepath.Join(f.data, "go", "characters", "suzu", "memory.db")
	f.bunDB = filepath.Join(f.data, "characters", "suzu", "memory.db")
	makeBunDB(t, f.goDB, true)
	makeBunDB(t, f.bunDB, false)
	cfg := config.Defaults().Memory.Write
	cfg.Characters = []string{"suzu"}
	f.in = Input{Data: f.data, DB: f.goDB, Char: "suzu", Cfg: cfg, BackupDir: f.backup, BackupFallback: filepath.Join(t.TempDir(), "兜底"),
		HasModel: true, Now: func() time.Time { return time.Date(2026, 11, 2, 3, 4, 5, 0, time.Local) }, Logf: f.logs.logf}
	return f
}

// run 调 Open，open 函数按 Settings 真的打开 f.in.DB 的库，并记下每次的 Settings
func (f *fx) run(wrap ...func(Settings, string) (*memory.Memory, error)) *memory.Memory {
	f.t.Helper()
	open := func(s Settings) (*memory.Memory, error) {
		f.opens = append(f.opens, s)
		if len(wrap) > 0 {
			return wrap[0](s, f.in.DB)
		}
		return f.realOpen(s, f.in.DB)
	}
	m, err := Open(context.Background(), f.in, open)
	if err != nil {
		f.t.Fatalf("Open：%v", err)
	}
	f.t.Cleanup(func() { m.Close() })
	return m
}

func (f *fx) realOpen(s Settings, db string) (*memory.Memory, error) {
	opt := memory.Options{Character: "suzu", Events: f.events, Logf: f.logs.logf}
	s.Apply(&opt)
	return memory.Open(context.Background(), db, nil, nil, opt)
}

func (f *fx) last() Settings { return f.opens[len(f.opens)-1] }

func (f *fx) backups() []string {
	es, err := os.ReadDir(f.backup)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func (f *fx) meta(key string) (int64, bool) {
	v, ok, err := memory.MetaValue(context.Background(), f.goDB, key)
	if err != nil {
		f.t.Fatal(err)
	}
	return v, ok
}

// 日志里不能有数据目录、备份目录这类本机路径
func (f *fx) noPathsInLogs() {
	f.t.Helper()
	log := f.logs.String()
	for _, p := range []string{f.data, f.backup, filepath.Base(f.data), filepath.Dir(f.data)} {
		if strings.Contains(log, p) {
			f.t.Errorf("日志里有本机路径 %q：%s", p, log)
		}
	}
}

func linkDir(t *testing.T, link, target string) {
	t.Helper()
	if err := junction.Create(link, target); err != nil {
		t.Fatalf("建编造联接：%v", err)
	}
}

func TestWriteDefaultsRememberOn(t *testing.T) {
	f := newFx(t)
	f.in.Cfg = config.Defaults().Memory.Write
	m := f.run()
	s := f.last()
	if !m.Writable() || !s.Writable || s.SummarizeEnabled || len(f.opens) != 1 {
		t.Fatalf("默认配置该只开 remember、整理仍关：%+v 打开 %d 次", s, len(f.opens))
	}
	if !strings.Contains(f.logs.String(), "写入已开") {
		t.Fatalf("默认配置该记一行写入已开：%q", f.logs)
	}
	if got := f.backups(); len(got) != 1 || !strings.Contains(got[0], "before-write") {
		t.Fatalf("第一次开写该做一份 before-write 备份：%v", got)
	}
	if _, ok := f.meta(memory.MetaWriteEnabledAt); !ok {
		t.Fatal("默认配置该记开写时间")
	}
}

func TestWritePreconditionRememberTier(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fx)
		reason string // 空 = 该开
	}{
		{"全满足：只开 remember", func(f *fx) {}, ""},
		{"角色不在名单（名单里是别人）", func(f *fx) { f.in.Cfg.Characters = []string{"kiri"} }, ReasonNotListed},
		{"整理要先开写：summarize 里有、characters 里没有", func(f *fx) { f.in.Cfg.Characters = nil; f.in.Cfg.Summarize = []string{"suzu"} }, ReasonSummarizeNoWrite},
		{"库是 Bun 的原库", func(f *fx) { f.in.DB = f.bunDB }, ReasonNotGoCopy},
		{"库路径里有指向 Bun 原库的联接", func(f *fx) {
			dir := filepath.Dir(f.goDB)
			if err := os.RemoveAll(dir); err != nil {
				f.t.Fatal(err)
			}
			linkDir(f.t, dir, filepath.Dir(f.bunDB))
		}, ReasonNotGoCopy},
		{"库在数据目录之外", func(f *fx) {
			out := filepath.Join(t.TempDir(), "go", "characters", "suzu", "memory.db")
			makeBunDB(f.t, out, true)
			f.in.DB = out
		}, ReasonNotGoCopy},
		{"库没迁移", func(f *fx) {
			_ = os.Remove(f.goDB)
			makeBunDB(f.t, f.goDB, false)
		}, ReasonNotMigrated},
		{"备份目录建不了", func(f *fx) {
			blocker := filepath.Join(t.TempDir(), "占位的文件")
			if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
				f.t.Fatal(err)
			}
			f.in.BackupDir = filepath.Join(blocker, "备份")
		}, ReasonBackupFailed},
		{"备份目录算不出来（配置写错了）", func(f *fx) {
			f.in.BackupDirErr = errors.New("备份目录在数据目录里面")
		}, ReasonBackupFailed},
		{"检查超时", func(f *fx) {
			f.in.Precheck = 20 * time.Millisecond
			f.in.precheckStarted = func(ctx context.Context) { <-ctx.Done() } // 等时限真的到了再检查，不赌计时器先触发
		}, ReasonPrecheckTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			c.setup(f)
			if c.reason != ReasonNotListed {
				f.in.Cfg.Summarize = append(f.in.Cfg.Summarize, "suzu") // 整理也被要求：remember 的前提不满足时它也不能开
			}
			f.in.Cfg.CatchUp = "cursor"
			m := f.run()
			s := f.last()
			log := f.logs.String()
			f.noPathsInLogs()
			if c.reason != "" {
				if m.Writable() || s.Writable || s.SummarizeEnabled {
					t.Fatalf("前提不满足该两档都不开：%+v", s)
				}
				if !strings.Contains(log, "写入保持只读（原因 "+c.reason+"）") {
					t.Fatalf("日志该有原因码 %s：%q", c.reason, log)
				}
				if _, ok := f.meta(memory.MetaWriteEnabledAt); ok && c.name != "库没迁移" {
					t.Fatal("前提不满足时不该记开写时间")
				}
				return
			}
			if !m.Writable() || !s.Writable || !s.SummarizeEnabled || !strings.Contains(log, "写入已开") {
				t.Fatalf("全满足该两档都开：%+v %q", s, log)
			}
		})
	}
}

// 库文件不见了：说 path_error，没得退，只读打开也失败就把错误交出去（装配处看过文件在才调，走不到这里）
func TestWriteMissingDB(t *testing.T) {
	f := newFx(t)
	if err := os.Remove(f.goDB); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), f.in, func(s Settings) (*memory.Memory, error) { return f.realOpen(s, f.in.DB) })
	if err == nil || !strings.Contains(f.logs.String(), "写入保持只读（原因 "+ReasonPathError+"）") {
		t.Fatalf("该报错并说 path_error：%v %q", err, f.logs)
	}
	if _, statErr := os.Stat(f.goDB); statErr == nil {
		t.Fatal("没把库建出来")
	}
}

// 前提 5（后台模型、catchUp）不满足，只关整理、remember 照开；名单、路径、迁移、开写前的备份不满足两档都不开（见 TestWritePreconditionRememberTier），开整理前的备份不满足也只关整理（见 TestWriteSummarizeBackupFailure）。不存在"退到半开"
func TestWritePreconditionSummarizeTier(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fx)
		reason string
	}{
		{"没后台模型", func(f *fx) { f.in.HasModel = false; f.in.Cfg.CatchUp = "cursor" }, ReasonNoBackgroundModel},
		{"catchUp 没选", func(f *fx) { f.in.Cfg.CatchUp = "" }, ReasonCatchUpNotChosen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			f.in.Cfg.Summarize = []string{"suzu"}
			c.setup(f)
			m := f.run()
			s := f.last()
			if !m.Writable() || !s.Writable || s.SummarizeEnabled || m.SummarizeEnabled() {
				t.Fatalf("remember 照开、整理关：%+v", s)
			}
			if !strings.Contains(f.logs.String(), "整理 关（原因 "+c.reason+"）") || !strings.Contains(f.logs.String(), "写入已开") {
				t.Fatalf("日志该说整理关和原因：%q", f.logs)
			}
			if got := f.backups(); len(got) != 1 || !strings.Contains(got[0], "before-write") {
				t.Fatalf("整理没开就不该做开整理前的备份：%v", got)
			}
			if _, ok := f.meta(memory.MetaSummarizeEnabledAt); ok {
				t.Fatal("整理没开不该记开整理时间")
			}
			f.noPathsInLogs()
		})
	}
}

// 整理的备份失败：remember 照开、整理关。让第二种备份失败的办法：在备份目录里预先放一个和它的最终文件名同名的目录，
// 它写好 .partial、校验过以后改名就会失败（第一种备份名字不同，照常做成）
func TestWriteSummarizeBackupFailure(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.Summarize = []string{"suzu"}
	f.in.Cfg.CatchUp = "cursor"
	probe, err := memory.BackupMemoryDB(context.Background(), f.goDB, t.TempDir(), "suzu", memory.BackupBeforeSummarize, f.in.now())
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(f.backup, filepath.Base(probe.File))
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "占着"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := f.run()
	if !m.Writable() || m.SummarizeEnabled() || f.last().SummarizeEnabled {
		t.Fatalf("整理的备份失败该只开 remember：%+v", f.last())
	}
	if !strings.Contains(f.logs.String(), "整理 关（原因 "+ReasonSummarizeBackup+"）") || !strings.Contains(f.logs.String(), "写入已开") {
		t.Fatalf("日志：%q", f.logs)
	}
	if _, ok := f.meta(memory.MetaSummarizeEnabledAt); ok {
		t.Fatal("整理没开不该记开整理时间")
	}
	if _, ok := f.meta(memory.MetaWriteEnabledAt); !ok {
		t.Fatal("remember 开了，开写时间该记")
	}
	f.noPathsInLogs()
}

// 备份目录算不出来（配置写错了）：已经开过写的，整理这一档要先备份，没地方放就只关整理、写入照旧
func TestWriteSummarizeOffWhenBackupDirBroken(t *testing.T) {
	f := newFx(t)
	f.run() // 先只开 remember：开写前的备份做了，开写时间记下
	if _, ok := f.meta(memory.MetaWriteEnabledAt); !ok {
		t.Fatal("前提：第一次该已开写")
	}
	f.in.Cfg.Summarize = []string{"suzu"}
	f.in.Cfg.CatchUp = "cursor"
	f.in.BackupDirErr = errors.New("备份目录在数据目录里面")
	m := f.run()
	if !m.Writable() || m.SummarizeEnabled() || f.last().SummarizeEnabled {
		t.Fatalf("备份没地方放：整理该关、写入照旧：%+v", f.last())
	}
	if !strings.Contains(f.logs.String(), "整理 关（原因 "+ReasonSummarizeBackup+"）") {
		t.Fatalf("日志：%q", f.logs)
	}
	f.noPathsInLogs()
}

// 两档都开：各做一份备份
func TestWriteBothTiersBackups(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.Summarize = []string{"suzu"}
	f.in.Cfg.CatchUp = "cursor"
	f.run()
	got := f.backups()
	if len(got) != 2 {
		t.Fatalf("两档都开时该有两份备份（开写前、开整理前）：%v", got)
	}
	slices.Sort(got)
	if !strings.Contains(got[0], "before-summarize") || !strings.Contains(got[1], "before-write") {
		t.Fatalf("备份种类不对：%v", got)
	}
}

func TestWriteBothTiers(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.Summarize = []string{"suzu"}
	f.in.Cfg.CatchUp = "cursor"
	f.in.Cfg.AutoConfirm = true
	m := f.run()
	s := f.last()
	if !m.Writable() || !m.SummarizeEnabled() || !s.AutoConfirm || s.ConfirmOccasions != 3 || !slices.Equal(s.ConfirmKinds, []string{"fact", "preference"}) || !slices.Equal(s.WritePlaces, []string{"owner-private"}) {
		t.Fatalf("两档都开、参数照配置：%+v", s)
	}
	got := f.backups()
	if len(got) != 2 {
		t.Fatalf("两档都开该做两份备份：%v", got)
	}
	for _, n := range got {
		if !strings.HasPrefix(n, "memory-suzu-20261102-030405-") {
			t.Errorf("备份文件名不对：%s", n)
		}
	}
	w, ok1 := f.meta(memory.MetaWriteEnabledAt)
	su, ok2 := f.meta(memory.MetaSummarizeEnabledAt)
	want := time.Date(2026, 11, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	if !ok1 || !ok2 || w != want || su != want {
		t.Fatalf("开写时间该是 %d：%d %v / %d %v", want, w, ok1, su, ok2)
	}
	log := f.logs.String()
	if !strings.Contains(log, "写入已开（库 go/characters/suzu/memory.db，备份 ") || !strings.Contains(log, "整理 开，场合 [owner-private]，") {
		t.Fatalf("日志一行不对：%q", log)
	}
	// 止血的 quarantineSince 照这一行填：两个毫秒时间都在日志里
	if !strings.Contains(log, fmt.Sprintf("write_enabled_at=%d", want)) || !strings.Contains(log, fmt.Sprintf("summarize_enabled_at=%d", want)) {
		t.Fatalf("日志该带两个开写时间（毫秒）：%q", log)
	}
	f.noPathsInLogs()

	// 只开 remember 时 autoConfirm 不带进去
	g := newFx(t)
	g.in.Cfg.AutoConfirm = true
	g.run()
	if g.last().AutoConfirm || g.last().SummarizeEnabled {
		t.Fatalf("只开 remember 时 autoConfirm 不该生效：%+v", g.last())
	}
	if !strings.Contains(g.logs.String(), "write_enabled_at=") || strings.Contains(g.logs.String(), "summarize_enabled_at=") {
		t.Fatalf("只开 remember 时日志只带 write_enabled_at：%q", g.logs)
	}
}

// 备份各只做一次：第二次启动不再备份；备份目录读配置，空才兜底
func TestBackupBeforeWriteOnce(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.Summarize = []string{"suzu"}
	f.in.Cfg.CatchUp = "cursor"
	m := f.run()
	m.Close()
	first := f.backups()
	if len(first) != 2 {
		t.Fatalf("第一次该两份：%v", first)
	}
	f.logs = &logs{}
	f.in.Logf = f.logs.logf
	f.in.Now = func() time.Time { return time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local) }
	f.opens = nil
	f.run()
	firstAt := time.Date(2026, 11, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	if !strings.Contains(f.logs.String(), fmt.Sprintf("write_enabled_at=%d", firstAt)) || !strings.Contains(f.logs.String(), fmt.Sprintf("summarize_enabled_at=%d", firstAt)) {
		t.Fatalf("第二次启动日志里的开写时间该是第一次记的，不是这次的钟：%q", f.logs)
	}
	if got := f.backups(); !slices.Equal(got, first) {
		t.Fatalf("第二次启动不该再备份：%v → %v", first, got)
	}
	if !strings.Contains(f.logs.String(), "备份 以前做过") {
		t.Fatalf("日志该说以前做过：%q", f.logs)
	}

	// 备份目录空：用兜底
	g := newFx(t)
	g.in.BackupDir = "  "
	g.run()
	es, err := os.ReadDir(g.in.BackupFallback)
	if err != nil || len(es) != 1 {
		t.Fatalf("目录空该用兜底目录：%v %v", es, err)
	}
	if len(g.backups()) != 0 {
		t.Fatal("配置的目录是空串，不该落在别处")
	}
}

// catchUp=latest 在打开之后推游标，失败只关整理
func TestWriteCatchUp(t *testing.T) {
	const conv = "qq:private:20001:60001"
	seed := func(f *fx, cursor int64) {
		f.events.heads = []memory.ConvHead{{Conversation: conv, LastSeq: 9}}
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.goDB))
		if err != nil {
			f.t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`INSERT INTO summary_state (conversation, last_seq, updated_at, failures) VALUES (?, ?, 1, 0)`, conv, cursor); err != nil {
			f.t.Fatal(err)
		}
	}
	cursor := func(m *memory.Memory) int64 {
		st, _, err := m.SummaryState(context.Background(), conv)
		if err != nil {
			t.Fatal(err)
		}
		return st.LastSeq
	}
	t.Run("latest：推到最新", func(t *testing.T) {
		f := newFx(t)
		f.in.Cfg.Summarize, f.in.Cfg.CatchUp = []string{"suzu"}, "latest"
		seed(f, 2)
		m := f.run()
		if !m.SummarizeEnabled() || cursor(m) != 9 {
			t.Fatalf("游标该推到 9：%d", cursor(m))
		}
	})
	t.Run("cursor：游标原样", func(t *testing.T) {
		f := newFx(t)
		f.in.Cfg.Summarize, f.in.Cfg.CatchUp = []string{"suzu"}, "cursor"
		seed(f, 2)
		m := f.run()
		if !m.SummarizeEnabled() || cursor(m) != 2 {
			t.Fatalf("游标该原样 2：%d", cursor(m))
		}
	})
	t.Run("latest 出错：整理关、remember 照开", func(t *testing.T) {
		f := newFx(t)
		f.in.Cfg.Summarize, f.in.Cfg.CatchUp = []string{"suzu"}, "latest"
		seed(f, 2)
		f.events.err = errors.New("事件库忙")
		m := f.run()
		if !m.Writable() || m.SummarizeEnabled() || len(f.opens) != 2 || f.last().SummarizeEnabled {
			t.Fatalf("该退到只开 remember、打开了两次：%+v", f.opens)
		}
		if !strings.Contains(f.logs.String(), ReasonCatchUpFailed) || strings.Contains(f.logs.String(), "事件库忙") {
			t.Fatalf("日志该有原因码、没有错误原文：%q", f.logs)
		}
		if cursor(m) != 2 {
			t.Fatalf("出错时游标不该动：%d", cursor(m))
		}
		if _, ok := f.meta(memory.MetaSummarizeEnabledAt); ok {
			t.Fatal("catchUp 出错整理没开，不该留开整理时间")
		}
		if _, ok := f.meta(memory.MetaWriteEnabledAt); !ok {
			t.Fatal("remember 开了，开写时间该记")
		}
		// 下次启动 catchUp 成功：整理开了，这时才记开整理时间
		m.Close()
		f.events.err = nil
		f.opens = nil
		m2 := f.run()
		if !m2.SummarizeEnabled() || cursor(m2) != 9 {
			t.Fatalf("下次启动该成功：整理 %v 游标 %d", m2.SummarizeEnabled(), cursor(m2))
		}
		if _, ok := f.meta(memory.MetaSummarizeEnabledAt); !ok {
			t.Fatal("整理真开了才记开整理时间")
		}
	})
}

// 打开或收尾出错，退回只读，不留半开
func TestWriteFallbackToReadOnly(t *testing.T) {
	t.Run("可写打开失败", func(t *testing.T) {
		f := newFx(t)
		m := f.run(func(s Settings, db string) (*memory.Memory, error) {
			if s.Writable {
				return nil, errors.New("别的程序占着库")
			}
			return f.realOpen(s, db)
		})
		if m.Writable() || len(f.opens) != 2 || f.last().Writable {
			t.Fatalf("该退回只读：%+v", f.opens)
		}
		if !strings.Contains(f.logs.String(), "写入保持只读（原因 "+ReasonOpenFailed+"）") || strings.Contains(f.logs.String(), "别的程序占着库") {
			t.Fatalf("日志：%q", f.logs)
		}
		if _, ok := f.meta(memory.MetaWriteEnabledAt); ok {
			t.Fatal("没打开成功不该记开写时间")
		}
	})
	t.Run("只读打开也失败：报错", func(t *testing.T) {
		f := newFx(t)
		f.in.Cfg = config.Defaults().Memory.Write
		f.in.Cfg.Characters, f.in.Cfg.Summarize = nil, nil
		_, err := Open(context.Background(), f.in, func(Settings) (*memory.Memory, error) { return nil, errors.New("库坏了") })
		if err == nil {
			t.Fatal("只读都打不开该报错")
		}
	})
	t.Run("开写时间记不进库", func(t *testing.T) {
		f := newFx(t)
		m := f.run(func(s Settings, db string) (*memory.Memory, error) {
			s.Writable, s.SummarizeEnabled = false, false // 打开了一个只读的：SetMetaIfAbsent 会报 ErrNotWritable
			return f.realOpen(s, db)
		})
		if m.Writable() || f.last().Writable || !strings.Contains(f.logs.String(), ReasonMetaFailed) {
			t.Fatalf("该退回只读并说 meta_write_failed：%+v %q", f.opens, f.logs)
		}
	})
}

// 角色不在名单里时，名单非空才说一声；名单全空（默认）什么都不说
func TestWriteNotListedLogsOnlyWhenSomeoneIsListed(t *testing.T) {
	f := newFx(t)
	f.in.Char = "kiri"
	f.run()
	if strings.Count(f.logs.String(), ReasonNotListed) != 1 || f.last().Writable {
		t.Fatalf("别人开了写、这个没列：该说一次 not_listed：%q %+v", f.logs, f.last())
	}
	// 显式只列 a：b 只读，原因 not_listed（不是默认的 *）
	h := newFx(t)
	h.in.Char = "b"
	h.in.Cfg.Characters = []string{"a"}
	h.in.Cfg.Summarize = nil
	h.run()
	if h.last().Writable || !strings.Contains(h.logs.String(), ReasonNotListed) {
		t.Fatalf("显式只列 a 时 b 该只读并说 not_listed：%+v %q", h.last(), h.logs)
	}
	g := newFx(t)
	g.in.Char = "kiri"
	g.in.Cfg = config.Defaults().Memory.Write
	g.in.Cfg.Characters, g.in.Cfg.Summarize = nil, nil
	g.run()
	if g.logs.String() != "" {
		t.Fatalf("名单全空不该有日志：%q", g.logs)
	}
}

// config 里的场合名单和 memory 里的是同一组、顺序一致（config 是叶子包，不引 memory，靠这个测试钉住）
func TestPlaceNamesAgree(t *testing.T) {
	if !slices.Equal(config.WritePlaceNames, memory.AllWritePlaces()) {
		t.Fatalf("config.WritePlaceNames %v 和 memory.AllWritePlaces %v 不一致", config.WritePlaceNames, memory.AllWritePlaces())
	}
	for _, p := range config.WritePlaceNames {
		if !memory.ValidWritePlace(p) {
			t.Errorf("%s memory 不认", p)
		}
	}
	// 读侧的几个种类：memory 的写入认的种类就是 config 允许的
	for _, k := range config.Defaults().Memory.Write.ConfirmKinds {
		if !slices.Contains(config.WriteKindNames, k) {
			t.Errorf("默认的 confirmKinds %s 不在允许的取值里", k)
		}
	}
}

// 止血的隔离时间原样带进去，只读的角色也带着；配成不是正整数就按不隔离
func TestWriteQuarantinePassesThrough(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.QuarantineSince = "1762041600000"
	f.run()
	if got := f.last().QuarantineSince; got != 1762041600000 {
		t.Fatalf("可写的角色该带着隔离时间：%d", got)
	}
	g := newFx(t)
	g.in.Cfg = config.Defaults().Memory.Write
	g.in.Cfg.Characters, g.in.Cfg.Summarize = nil, nil
	g.in.Cfg.QuarantineSince = "1762041600000"
	g.run()
	if got := g.last().QuarantineSince; got != 1762041600000 || g.last().Writable {
		t.Fatalf("只读的角色也该带着隔离时间：%+v", g.last())
	}
	for _, bad := range []string{"", "abc", "0", "-5", " "} {
		h := newFx(t)
		h.in.Cfg.QuarantineSince = bad
		h.run()
		if h.last().QuarantineSince != 0 {
			t.Errorf("%q 该按不隔离：%d", bad, h.last().QuarantineSince)
		}
	}
}

// fakeGen：固定回复一条候选事实，数调用次数
type fakeGen struct{ calls int }

func (g *fakeGen) Call(context.Context, string, string, string) (memory.Reply, error) {
	g.calls++
	return memory.Reply{Text: `{"episode_record":"no","ops":[{"op":"candidate","kind":"fact","text":"主人喜欢吃橘子","subject":"owner","src":[1]}]}`}, nil
}

// 端到端（假库副本）：配置打开 → memwrite 前提检查和备份 → 整理一批（假模型）→ 库里多出候选和任务记录 → 重启后游标在原处。
// 真实游标、真实耗时在假库上测不到，那是影子整理按真实游标跑的事
func TestWriteEndToEnd(t *testing.T) {
	const conv = "qq:private:20001:60001"
	f := newFx(t)
	f.in.Cfg.Summarize, f.in.Cfg.CatchUp = []string{"suzu"}, "cursor"
	f.events.heads = []memory.ConvHead{{Conversation: conv, LastSeq: 3}}
	gen := &fakeGen{}
	open := func(s Settings, db string) (*memory.Memory, error) {
		opt := memory.Options{Character: "suzu", Events: f.events, Logf: f.logs.logf, Gen: gen, Model: func() string { return "bg" },
			Prompts: prompts.Store{Root: filepath.Join("..", "..", "..")}, Now: f.in.Now}
		s.Apply(&opt)
		return memory.Open(context.Background(), db, nil, nil, opt)
	}
	m := f.run(open)
	if !m.SummarizeEnabled() {
		t.Fatalf("两档该都开：%q", f.logs)
	}
	ctx := context.Background()
	if _, err := m.InitCursor(ctx, conv, 0); err != nil {
		t.Fatal(err)
	}
	res, err := m.SummarizeBatch(ctx, conv, false)
	if err != nil || res.Skipped != "" || gen.calls != 1 {
		t.Fatalf("该整理了一批：%+v %v 模型 %d 次", res, err, gen.calls)
	}
	var facts, jobs int
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.goDB)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*) FROM facts WHERE status = 'candidate' AND text = '主人喜欢吃橘子'`).Scan(&facts); err != nil || facts != 1 {
		t.Fatalf("库里该多出一条候选：%d %v", facts, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM memory_jobs`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("memory_jobs 该有一条记录：%d %v", jobs, err)
	}
	m.Close()

	// 重启：游标在原处，不再整理已经整理过的
	f.opens = nil
	m2 := f.run(open)
	st, ok, err := m2.SummaryState(ctx, conv)
	if err != nil || !ok || st.LastSeq != 3 {
		t.Fatalf("重启后游标该在 3：%+v %v %v", st, ok, err)
	}
	if res, err := m2.SummarizeBatch(ctx, conv, false); err != nil || gen.calls != 1 {
		t.Fatalf("重启后不该再整理：%+v %v 模型 %d 次", res, err, gen.calls)
	}
}

// 关了整理再开：只开 remember 的那次启动把 catchUp 的记录清空，再开整理像第一次一样再推一遍，不把停整理期间的聊天接着整理
func TestWriteCatchUpAgainAfterSummarizeWasOff(t *testing.T) {
	const conv = "qq:private:20001:60001"
	f := newFx(t)
	f.in.Cfg.Summarize, f.in.Cfg.CatchUp = []string{"suzu"}, "latest"
	f.events.heads = []memory.ConvHead{{Conversation: conv, LastSeq: 9}}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.goDB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO summary_state (conversation, last_seq, updated_at, failures) VALUES (?, 2, 1, 0)`, conv); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cursor := func(m *memory.Memory) int64 {
		st, _, err := m.SummaryState(context.Background(), conv)
		if err != nil {
			t.Fatal(err)
		}
		return st.LastSeq
	}
	// 第一次开整理：游标推到 9，位图记下
	m := f.run()
	if !m.SummarizeEnabled() || cursor(m) != 9 {
		t.Fatalf("第一次该推到 9：整理 %v 游标 %d", m.SummarizeEnabled(), cursor(m))
	}
	if v, ok := f.meta(memory.MetaCatchUpPlaces); !ok || v == 0 {
		t.Fatalf("前提：位图该记下了：%d %v", v, ok)
	}
	m.Close()
	// 出事停整理（summarize 清空，characters 还在）：位图清空，游标不动
	f.in.Cfg.Summarize = nil
	f.opens = nil
	m = f.run()
	if m.SummarizeEnabled() || !m.Writable() {
		t.Fatalf("该只开 remember：整理 %v 写入 %v", m.SummarizeEnabled(), m.Writable())
	}
	if v, ok := f.meta(memory.MetaCatchUpPlaces); !ok || v != 0 {
		t.Fatalf("整理没开的那次启动该把位图清成 0：%d %v", v, ok)
	}
	// 停整理期间聊天走到了 20；几天后重新开整理：再推一遍，游标到 20，不是停在旧的 9 上接着整理
	m.Close()
	f.events.heads = []memory.ConvHead{{Conversation: conv, LastSeq: 20}}
	f.in.Cfg.Summarize = []string{"suzu"}
	f.opens = nil
	m = f.run()
	if !m.SummarizeEnabled() || cursor(m) != 20 {
		t.Fatalf("重新开整理该把游标推到最新 20：整理 %v 游标 %d", m.SummarizeEnabled(), cursor(m))
	}
}

// 开整理时间记不进库（catchUp 本身没出错）：整理关、remember 照开，原因是 summarize_meta_failed，不是 catchup_failed
func TestWriteSummarizeMetaFailureHasItsOwnReason(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.Summarize, f.in.Cfg.CatchUp = []string{"suzu"}, "cursor"
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.goDB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER no_summarize_time BEFORE INSERT ON memory_meta WHEN NEW.key = 'summarize_enabled_at' BEGIN SELECT RAISE(ABORT, '库里不许记这个键'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	m := f.run()
	if !m.Writable() || m.SummarizeEnabled() {
		t.Fatalf("该退到只开 remember：写入 %v 整理 %v", m.Writable(), m.SummarizeEnabled())
	}
	log := f.logs.String()
	if !strings.Contains(log, ReasonSummarizeMeta) || strings.Contains(log, ReasonCatchUpFailed) || strings.Contains(log, "库里不许记") {
		t.Fatalf("日志该写 %s，不写 %s，也不带错误原文：%q", ReasonSummarizeMeta, ReasonCatchUpFailed, log)
	}
	if _, ok := f.meta(memory.MetaSummarizeEnabledAt); ok {
		t.Fatal("没记进去就不该有开整理时间")
	}
	if _, ok := f.meta(memory.MetaWriteEnabledAt); !ok {
		t.Fatal("remember 开了，开写时间该记")
	}
}

// 止血隔离的回执：线、藏起了几条（只有计数）；一条都没藏提醒线可能填晚了；线早于开写时间提醒会藏导入的旧记忆；没设线什么都不说
func TestWriteQuarantineLogsWhatItHid(t *testing.T) {
	cases := []struct {
		name      string
		since     string
		want      []string
		writeOnly []string // 开过写（库里记了开写时间）才会有的：线早于开写时间
		notWant   []string
	}{
		{"藏了东西", "1000", []string{"止血隔离已生效（线 1000）", "藏起 1 条事实、0 条待确认、0 条片段"}, []string{"线早于开写时间"}, []string{"一条都没藏"}},
		{"线填晚了：一条都没藏", "9999999999999", []string{"止血隔离已生效", "藏起 0 条事实", "一条都没藏，线可能填晚了"}, nil, []string{"线早于开写时间"}},
		{"没设线", "", nil, nil, []string{"止血隔离"}},
	}
	for _, c := range cases {
		for _, write := range []bool{true, false} {
			name := c.name
			if !write {
				name += "／只读"
			}
			t.Run(name, func(t *testing.T) {
				f := newFx(t)
				if !write {
					f.in.Cfg = config.Defaults().Memory.Write
					f.in.Cfg.Characters, f.in.Cfg.Summarize = nil, nil
				}
				f.in.Cfg.QuarantineSince = c.since
				f.run()
				log := f.logs.String()
				want := c.want
				if write {
					want = append(slices.Clone(want), c.writeOnly...)
				}
				for _, w := range want {
					if !strings.Contains(log, w) {
						t.Errorf("日志里该有 %q：%q", w, log)
					}
				}
				for _, w := range c.notWant {
					if strings.Contains(log, w) {
						t.Errorf("日志里不该有 %q：%q", w, log)
					}
				}
				f.noPathsInLogs()
				if strings.Contains(log, "编的事实") {
					t.Errorf("日志里不该有正文：%q", log)
				}
			})
		}
	}
}
