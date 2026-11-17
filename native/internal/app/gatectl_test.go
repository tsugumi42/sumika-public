package app

// 控制接口的群档位窄接口（gatectl.go）：对着真的临时库（真迁移、真表）跑。
// 验收 3 的形状表在这里：off、dry 过；speak 的各种写法、空、大小写和空白变体、不认识的值、私聊、本地会话、不存在的群、
// 只看历史模式，每一格都断言"被拒，而且库没变"（库的内容和文件字节都比）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type gateEnv struct {
	p   paths.Paths
	set *store.Set
	now time.Time
}

const (
	gEvents   = "qq:group:10001" // 有消息
	gNameOnly = "qq:group:10002" // 只在群名表里
	gModeOnly = "qq:group:10003" // 只在档位表里（speak）
	gNone     = "qq:group:77777" // 哪里都没有
	gPrivate  = "qq:private:90001:80002"
)

func newGateEnv(t *testing.T) *gateEnv {
	t.Helper()
	p, err := paths.Init(filepath.Join(t.TempDir(), "数据"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	e := &gateEnv{p: p, set: set, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)}
	qq, _ := set.Platform("qq")
	for i, c := range []string{gEvents, gPrivate, "qq:group:123abc"} { // 最后一个是 GLOB 会放进来的奇怪形状，不是合格的群
		if _, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: c, ActorKind: "human", ActorID: "qq:1", Origin: "peer", Kind: "message", Text: "话", CreatedAt: e.now.UnixMilli() + int64(i)}, []string{"ca"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := qq.Events.Exec(`INSERT INTO qq_groups(gid, name, updated_at) VALUES ('10002', '编的测试群', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := set.TraceStore().SetGateMode(gModeOnly, "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *gateEnv) admin() *gateAdmin {
	return &gateAdmin{set: e.set, def: "dry", now: func() time.Time { return e.now }, logf: func(string, ...any) {}}
}

// snapshot 是库的全部内容：trace.db 和 qq.db 里每张表的每一行，加上档位表的明细（不比 updated_at 以外的东西会漏掉"只改了时间"）
func (e *gateEnv) snapshot(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	qq, _ := e.set.Platform("qq")
	dump := func(label string, q func(string, ...any) (*sql.Rows, error)) {
		tables, err := q(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for tables.Next() {
			var n string
			tables.Scan(&n)
			names = append(names, n)
		}
		tables.Close()
		for _, n := range names {
			rows, err := q(`SELECT * FROM "` + n + `"`)
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				rows.Scan(ptrs...)
				fmt.Fprintf(&b, "%s.%s %v\n", label, n, vals)
			}
			rows.Close()
		}
	}
	dump("trace", e.set.Trace().Query)
	dump("qq", qq.Events.Query)
	return b.String()
}

func fileHashes(t *testing.T, p paths.Paths) string {
	t.Helper()
	var b strings.Builder
	for _, f := range []string{p.GoTraceDB(), p.GoTraceDB() + "-wal", p.ChatDB("qq"), p.ChatDB("qq") + "-wal"} {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(&b, "%s 无\n", filepath.Base(f))
			continue
		}
		fmt.Fprintf(&b, "%s %x\n", filepath.Base(f), sha256.Sum256(data))
	}
	return b.String()
}

func TestLowerGateShapeTable(t *testing.T) {
	t.Parallel()
	zeroWidth := "spe" + string(rune(0x200b)) + "ak"
	type row struct {
		what, conv, mode string
		readonly         bool
		want             error // nil = 应该成功
	}
	var rows []row
	// 成功的格子：群有消息 / 只在群名表 / 只在档位表
	rows = append(rows,
		row{"有消息的群调成 off", gEvents, "off", false, nil},
		row{"有消息的群调成 dry", gEvents, "dry", false, nil},
		row{"只在群名表里的群调成 dry", gNameOnly, "dry", false, nil},
		row{"只在档位表里的群（原来是 speak）调成 off", gModeOnly, "off", false, nil},
		row{"只在档位表里的群（原来是 speak）调成 dry", gModeOnly, "dry", false, nil},
	)
	// 档位：speak 的各种写法，空，大小写和空白变体，别的值
	for _, m := range []string{"speak", "SPEAK", "Speak", " speak", "speak ", "\tspeak\n", "speak\x00", "ｓｐｅａｋ", zeroWidth} {
		rows = append(rows, row{"speak 的写法", gEvents, m, false, ErrGateNotAllowed})
	}
	for _, m := range []string{"", " ", "OFF", "Dry", " off", "dry ", "off\n", "on", "record", "dry-run", "dryrun", "*", "1"} {
		rows = append(rows, row{"别的档位写法", gEvents, m, false, ErrGateNotAllowed})
	}
	// 会话：私聊、本地会话、通配符、不合格的写法
	for _, c := range []string{
		gPrivate, "qq:private:10001", "console:example", "console:example:topic:1", "", "*", "qq:*", "qq:group:*", "qq:group:", "qq:group:abc",
		"qq:group:123abc", " " + gEvents, gEvents + " ", gEvents + "\n", "QQ:group:10001", "qq:GROUP:10001", gEvents + ":2", "qq:group:١٠٠٠١",
		"qq:group:1234567890123456789012", "qq:group:1;DROP TABLE gate_modes", "telegram:group:10001",
	} {
		rows = append(rows, row{"会话写法不对", c, "off", false, ErrGateBadGroup})
	}
	// 不存在的群：写法对，哪里都没有
	for _, c := range []string{gNone, "qq:group:1", "qq:group:12345678901234567890"} {
		rows = append(rows, row{"不存在的群", c, "dry", false, ErrGateUnknownGroup})
	}
	// 只看历史模式：参数全对也拒绝（含不存在的群：先说只读，不去查库）
	for _, c := range []string{gEvents, gNameOnly, gModeOnly, gNone} {
		for _, m := range []string{"off", "dry"} {
			rows = append(rows, row{"只看历史模式", c, m, true, ErrGateReadOnly})
		}
	}
	// 只看历史模式 + 不合格的参数：先按参数不合格回（参数错和模式无关）
	rows = append(rows, row{"只看历史模式下的 speak", gEvents, "speak", true, ErrGateNotAllowed})

	rejected := 0
	shared := newGateEnv(t) // 被拒的格子共用一个库：每格前后都对比"一模一样"，被拒的不该留下任何痕迹；成功的格子各用全新的库，好单独看改了什么
	for _, r := range rows {
		e := shared
		if r.want == nil {
			e = newGateEnv(t)
		}
		before, hashBefore := e.snapshot(t), fileHashes(t, e.p)
		g := e.admin()
		g.readonly = r.readonly
		ch, err := g.lower(r.conv, r.mode)
		if r.want == nil {
			if err != nil || ch.After != r.mode || ch.Conversation != r.conv {
				t.Errorf("%s（%q %q）应成功，得到 %+v %v", r.what, r.conv, r.mode, ch, err)
				continue
			}
			got, ok, _ := e.set.TraceStore().GateModeOf(r.conv)
			if !ok || got != r.mode {
				t.Errorf("%s：库里的档位是 %q，应是 %q", r.what, got, r.mode)
			}
			// 只改了档位表：别的东西一个字节都没动
			diff := lineDiff(before, e.snapshot(t))
			if len(diff) == 0 {
				t.Errorf("%s：库里什么都没变", r.what)
			}
			for _, d := range diff {
				if !strings.HasPrefix(d, "-trace.gate_modes ") && !strings.HasPrefix(d, "+trace.gate_modes ") {
					t.Errorf("%s：改动不止档位表：%s", r.what, d)
				}
			}
			continue
		}
		rejected++
		if !errors.Is(err, r.want) {
			t.Errorf("%s（%q %q 只读=%v）应被拒为 %v，得到 %+v %v", r.what, r.conv, r.mode, r.readonly, r.want, ch, err)
		}
		if after := e.snapshot(t); after != before {
			t.Errorf("%s（%q %q）被拒了，库的内容却变了：%v", r.what, r.conv, r.mode, lineDiff(before, after))
		}
		if h := fileHashes(t, e.p); h != hashBefore {
			t.Errorf("%s（%q %q）被拒了，库文件的字节却变了", r.what, r.conv, r.mode)
		}
	}
	t.Logf("形状表 %d 格，其中被拒 %d 格", len(rows), rejected)
}

// lineDiff 两份快照里只出现在一边的行
func lineDiff(a, b string) []string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for l := range ma {
		if !mb[l] {
			out = append(out, "-"+l)
		}
	}
	for l := range mb {
		if !ma[l] {
			out = append(out, "+"+l)
		}
	}
	return out
}

// 真的只读打开的库（archive 就是这样开的）：调档位被拒，文件字节不变；列表照样能读
func TestLowerGateOnReadOnlySet(t *testing.T) {
	t.Parallel()
	e := newGateEnv(t)
	e.set.Close() // 关掉读写的，下面用只读的重新开
	ro, err := store.OpenGoReadOnly(context.Background(), e.p)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	hash := fileHashes(t, e.p)
	g := &gateAdmin{set: ro, readonly: true, def: "dry", now: func() time.Time { return e.now }, logf: func(string, ...any) {}}
	if _, err := g.lower(gEvents, "off"); !errors.Is(err, ErrGateReadOnly) {
		t.Fatalf("只读库上调档位应被拒为只读：%v", err)
	}
	rows, err := g.list(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("只读库上应能列出三个群：%+v %v", rows, err)
	}
	if h := fileHashes(t, e.p); h != hash {
		t.Fatalf("只读库的文件字节变了")
	}
}

func TestGateList(t *testing.T) {
	t.Parallel()
	e := newGateEnv(t)
	g := e.admin()
	got, err := g.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s|%s", r.Conversation, r.Name, r.Mode, r.Source, r.Actual))
	}
	// 有消息的、只在群名表的、只在档位表的三个；私聊和 qq:group:123abc 不在里面；按会话排序
	want := []string{
		"qq:group:10001||dry|default|",
		"qq:group:10002|编的测试群|dry|default|",
		"qq:group:10003||speak|own|",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("列表不对：\n%s\n应是\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	// "所有群"的那一行：没自己设置的群跟着它；自己有的不受影响
	if err := e.set.TraceStore().SetGateMode("*", "off", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	g.actual = func(c types.Conversation) string {
		if c.ID == "10003" {
			return channel.ModeDryRun // speak 被压成试运行
		}
		return channel.ModeRecord
	}
	got, _ = g.list(context.Background())
	lines = nil
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s", r.Conversation, r.Mode, r.Source, r.Actual))
	}
	want = []string{"qq:group:10001|off|all|off", "qq:group:10002|off|all|off", "qq:group:10003|speak|own|dry"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("有「所有群」那一行时：\n%s", strings.Join(lines, "\n"))
	}
	// 默认档跟着 group.mode
	for cfgMode, def := range map[string]string{"off": "off", "dry": "dry", "speak": "speak", "": "dry", "乱写": "dry"} {
		if storedDefault(cfgMode) != def {
			t.Errorf("group.mode=%q 的默认档应是 %q", cfgMode, def)
		}
	}
}

// 通过 *App 调：接收模式能写，只看历史不能写；日志里写明是 ctl 改的，会话号打了码
func TestAppLowerGateModesAndLog(t *testing.T) {
	t.Parallel()
	e := newGateEnv(t)
	var mu sync.Mutex
	var logs []string
	a := &App{opt: Options{Mode: Receive, Logf: func(f string, args ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, args...)); mu.Unlock() }}, set: e.set, cfg: config.Defaults(), now: func() time.Time { return e.now }}
	a.logf = a.newLogf()
	a.modeOf = func(types.Conversation) string { return channel.ModeDryRun }
	ch, err := a.LowerGate(gModeOnly, "off")
	if err != nil || ch.Before != "speak" || ch.After != "off" {
		t.Fatalf("%+v %v", ch, err)
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "ctl") || strings.Contains(joined, "10003") || !strings.Contains(joined, "qq:group:***03") {
		t.Fatalf("日志应写明是 ctl 改的，并且群号打码：%q", joined)
	}
	rows, err := a.GateList(context.Background())
	if err != nil || len(rows) != 3 || rows[2].Actual != "dry" {
		t.Fatalf("接收模式的列表应带真正生效的档位：%+v %v", rows, err)
	}
	// 同一个 App 换成只看历史：写不了，列表里没有生效档位
	a.opt.Mode = Archive
	if _, err := a.LowerGate(gEvents, "off"); !errors.Is(err, ErrGateReadOnly) {
		t.Fatalf("只看历史模式应被拒：%v", err)
	}
	if rows, _ := a.GateList(context.Background()); len(rows) != 3 || rows[0].Actual != "" {
		t.Fatalf("只看历史模式的列表不该有生效档位：%+v", rows)
	}
	// 设成 speak 以后「调回去」也要一视同仁：已经是 speak 的群调成 dry 可以，反过来不行（上面的形状表已覆盖）
}
