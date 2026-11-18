package store

// 另一个进程管着的平台库（foreign.go）：只读打开写不进去、缺库和落后的库不打开、别的进程写了能叫醒订阅、
// 启动收拾不碰对方渠道的在途行（规格-本地私聊切到Bun 第三、四节）。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
)

// foreignPair 先用普通写模式把一组库建好（local + qq），再开一份 local 只读的组。返回写的那组和只读的那组
func foreignPair(t *testing.T) (writer, reader *Set, p paths.Paths) {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err = OpenGoWith(p, DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	reader, err = OpenGoOpts(p, DefaultRegistry, OpenOptions{ReadOnly: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return writer, reader, p
}

func localOf(t *testing.T, s *Set) *Store {
	t.Helper()
	st, err := s.For("console")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// 只读的那个平台从连接上就写不了：事件、已读标记、直接 SQL 都被拒；别的平台照旧能写
func TestForeignPlatformIsReadOnly(t *testing.T) {
	t.Parallel()
	_, reader, _ := foreignPair(t)
	local := localOf(t, reader)
	if !local.ReadOnly() {
		t.Fatal("local 该是只读")
	}
	if _, err := local.AppendEvent(NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "x", CreatedAt: 1}, []string{"a"}); err == nil {
		t.Fatal("只读的 local 不该能写事件")
	}
	if _, err := local.Events.Exec(`CREATE TABLE sneaky(x)`); err == nil {
		t.Fatal("只读的 local 不该能建表")
	}
	qq, err := reader.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	if qq.ReadOnly() {
		t.Fatal("qq 不该是只读")
	}
	if _, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: "qq:group:1", ActorKind: "user", ActorID: "9", Origin: "qq", Kind: "message", Text: "x", CreatedAt: 1}, []string{"a"}); err != nil {
		t.Fatalf("qq 该照常写：%v", err)
	}
}

// 库不存在、迁移落后、比程序新：都不打开（不在别人写着的库上建表、迁移），原因写清
func TestForeignPlatformRefusesMissingOrStale(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGoOpts(p, DefaultRegistry, OpenOptions{ReadOnly: []string{"local"}}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("local.db 不存在该拒绝：%v", err)
	}
	if _, err := os.Stat(p.ChatDB("local")); err == nil {
		t.Fatal("拒绝时不该建出 local.db")
	}
	w, err := OpenGoWith(p, DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := latestMigration("migrations/local")
	if err != nil || latest < 3 {
		t.Fatalf("local 的最新迁移号：%d %v", latest, err)
	}
	local := localOf(t, w)
	if _, err := local.Events.Exec(`DELETE FROM goose_db_version_local WHERE version_id = ?`, latest); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGoOpts(p, DefaultRegistry, OpenOptions{ReadOnly: []string{"local"}}); err == nil || !strings.Contains(err.Error(), "落后") {
		t.Fatalf("迁移落后该拒绝并说落后：%v", err)
	}
	if _, err := local.Events.Exec(`INSERT INTO goose_db_version_local(version_id, is_applied) VALUES(?, 1)`, latest+1); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGoOpts(p, DefaultRegistry, OpenOptions{ReadOnly: []string{"local"}}); err == nil || !strings.Contains(err.Error(), "比本程序新") {
		t.Fatalf("库比程序新该拒绝：%v", err)
	}
	w.Close()
	if _, err := OpenGoOpts(p, DefaultRegistry, OpenOptions{ReadOnly: []string{"nope"}}); err == nil {
		t.Fatal("不在登记里的只读平台该报错")
	}
}

// 别的进程写了 local.db：第一次检查只记基线；写入之后检查为真并叫醒“看全部变更”的订阅；没写就不叫
func TestCheckExternalChangeWakesSubscribers(t *testing.T) {
	t.Parallel()
	writer, reader, _ := foreignPair(t)
	rl := localOf(t, reader)
	sub := rl.SubscribeChanges()
	defer sub.Close()
	if changed, err := rl.CheckExternalChange(); err != nil || changed {
		t.Fatalf("第一次只记基线：%v %v", changed, err)
	}
	if changed, _ := rl.CheckExternalChange(); changed {
		t.Fatal("没人写，不该报变了")
	}
	select {
	case <-sub.Wake():
		t.Fatal("没人写，不该叫醒")
	default:
	}
	if _, err := localOf(t, writer).AppendEvent(NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "来自另一个进程", CreatedAt: 1}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	changed, err := rl.CheckExternalChange()
	if err != nil || !changed {
		t.Fatalf("别的连接写了，该报变了：%v %v", changed, err)
	}
	select {
	case <-sub.Wake():
	default:
		t.Fatal("该叫醒订阅")
	}
	if d := sub.Take(); !d[ExternalChangeKey] {
		t.Fatalf("失效身份该带 %s：%v", ExternalChangeKey, d)
	}
	if changed, _ := rl.CheckExternalChange(); changed {
		t.Fatal("同一次写入只报一次")
	}
}

// PollExternalChanges 靠注入的 tick 驱动，不等墙钟；ctx 取消就返回
func TestPollExternalChangesUsesInjectedTick(t *testing.T) {
	t.Parallel()
	writer, reader, _ := foreignPair(t)
	rl := localOf(t, reader)
	sub := rl.SubscribeChanges()
	defer sub.Close()
	_, _ = rl.CheckExternalChange()
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rl.PollExternalChanges(ctx, tick); close(done) }()
	if _, err := localOf(t, writer).AppendEvent(NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "x", CreatedAt: 1}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	tick <- time.Now()
	tick <- time.Now() // 第二个 tick 收下，说明第一个已经处理完
	select {
	case <-sub.Wake():
	case <-time.After(5 * time.Second):
		t.Fatal("一次 tick 之后该叫醒订阅")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后该返回")
	}
}

// 启动收拾（SweepInterrupted、RecoverKernelInputs）限范围：local 交给别的进程时 console 的在途回合、步骤、输入、运行都不碰；
// qq 的照收拾。没有只读平台时两边都收拾（邻居不变）
func TestStartupSweepsSkipForeignChannel(t *testing.T) {
	t.Parallel()
	scope := func(ch string) kernel.Invocation {
		return kernel.Invocation{Scope: kernel.Scope{CharacterID: "a", Conversation: ch + ":x", Channel: ch}}
	}
	seed := func(t *testing.T, s *Set) {
		t.Helper()
		tr := s.TraceStore()
		for _, ch := range []string{"console", "qq"} {
			if err := tr.TurnStart("turn-"+ch, "a", ch+":x", ch, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.Trace.Exec(`INSERT INTO steps(id,character_id,conversation,turn_id,kind,status) VALUES(?,?,?,?, 'tool','running')`, "run-"+ch, "a", ch+":x", "turn-"+ch); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.Trace.Exec(`INSERT INTO steps(id,character_id,conversation,turn_id,kind,status) VALUES(?,?,?,?, 'tool','waiting')`, "wait-"+ch, "a", ch+":x", "turn-"+ch); err != nil {
				t.Fatal(err)
			}
			raw := mustJSON(t, scope(ch))
			if _, err := tr.Trace.Exec(`INSERT INTO kernel_inbox(input_id,domain,client_key,digest,identity_json,payload_json,mode,sequence,state,run_id) VALUES(?,?,?,?,?,?,?,?,'accepted',?)`,
				"in-"+ch, "d-"+ch, "k", "g", raw, "{}", "queue", 1, "r-"+ch); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.Trace.Exec(`INSERT INTO kernel_runs(run_id,identity_json,state) VALUES(?,?,'running')`, "r-"+ch, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := func(t *testing.T, s *Set, ch string) (turnDone bool, stepRun, stepWait, inbox, run string) {
		t.Helper()
		tr := s.TraceStore().Trace
		var ended *int64
		if err := tr.QueryRow(`SELECT ended_at FROM turns WHERE id=?`, "turn-"+ch).Scan(&ended); err != nil {
			t.Fatal(err)
		}
		must := func(q, id string, dst *string) {
			if err := tr.QueryRow(q, id).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		must(`SELECT status FROM steps WHERE id=?`, "run-"+ch, &stepRun)
		must(`SELECT status FROM steps WHERE id=?`, "wait-"+ch, &stepWait)
		must(`SELECT state FROM kernel_inbox WHERE input_id=?`, "in-"+ch, &inbox)
		must(`SELECT state FROM kernel_runs WHERE run_id=?`, "r-"+ch, &run)
		return ended != nil, stepRun, stepWait, inbox, run
	}
	sweep := func(t *testing.T, s *Set) {
		t.Helper()
		if _, err := s.TraceStore().SweepInterrupted(100); err != nil {
			t.Fatal(err)
		}
		if err := s.TraceStore().RecoverKernelInputs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("local 只读时 console 不动、qq 照收拾", func(t *testing.T) {
		t.Parallel()
		_, reader, _ := foreignPair(t)
		seed(t, reader)
		sweep(t, reader)
		if d, sr, sw, in, run := state(t, reader, "console"); d || sr != "running" || sw != "waiting" || in != "accepted" || run != "running" {
			t.Fatalf("console 的在途行被动了：回合收尾=%v 步骤=%s/%s 输入=%s 运行=%s", d, sr, sw, in, run)
		}
		if d, sr, sw, in, run := state(t, reader, "qq"); !d || sr != "error" || sw != "cancelled" || in != "held" || run != "settled" {
			t.Fatalf("qq 的在途行该照常收拾：回合收尾=%v 步骤=%s/%s 输入=%s 运行=%s", d, sr, sw, in, run)
		}
	})
	t.Run("没有只读平台时两边都收拾", func(t *testing.T) {
		t.Parallel()
		writer, _, _ := foreignPair(t)
		seed(t, writer)
		sweep(t, writer)
		for _, ch := range []string{"console", "qq"} {
			if d, sr, sw, in, run := state(t, writer, ch); !d || sr != "error" || sw != "cancelled" || in != "held" || run != "settled" {
				t.Fatalf("%s 该照常收拾：回合收尾=%v 步骤=%s/%s 输入=%s 运行=%s", ch, d, sr, sw, in, run)
			}
		}
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
