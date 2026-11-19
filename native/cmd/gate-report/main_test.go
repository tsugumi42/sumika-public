package main

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// fixedNow 是这套测试里的"现在"：2026-10-27 12:00（本地时间）。判断、掉线都按它往前推
var fixedNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local)

// fixture 是造好的一份数据：临时数据目录里 go/trace.db 和 go/chat/qq.db 已经写满编的判断和掉线记录，库已经关了
type fixture struct {
	dir string
}

// newFixture 造数据（规格 第八节第 5 步的"gate-report 对造的判断记录算出对的比例"）。群 300、301、399，号 100、200 都是编的

// fakeNum 是编的 9 位号码，运行时拼出来：源码里写死一串 9 位数字会被提交前的隐私扫描当成真号
var fakeNum = strings.Repeat("7", 9)

func newFixture(t *testing.T) fixture {
	t.Helper()
	return seed(t, t.TempDir())
}

// seedDB 在 dir 里建库（还没有身份证就先造），返回能写的 store：造完要 Close 再跑报告（报告是只读打开的）
func seedDB(t *testing.T, dir string) *store.Set {
	t.Helper()
	p, err := paths.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenGo(p, qqPlatform)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addDecision 写一条判断（outcome 空 = 没结局）
func addDecision(t *testing.T, s *store.Set, conv, verdict string, at time.Time, target *float64, actual int, outcome string) {
	t.Helper()
	trace := s.TraceStore()
	id, err := trace.AddGateDecision(store.GateDecisionRow{
		At: at.UnixMilli(), CharacterID: "tester", Conversation: conv, Frequency: 0.3, Verdict: verdict,
		Reason: "编的", Target: target, Actual: &actual,
	})
	if err != nil {
		t.Fatalf("写判断（%s %s）：%v", conv, verdict, err)
	}
	if outcome != "" {
		if err := trace.SetGateOutcome(id, outcome, "编的结局"); err != nil {
			t.Fatalf("写结局：%v", err)
		}
	}
}

// addAlert 写一条 QQ 提醒（down / up）
func addAlert(t *testing.T, s *store.Set, at time.Time, kind, account string) {
	t.Helper()
	qq, ok := s.Platform(qqPlatform)
	if !ok {
		t.Fatal("没有 qq 平台的库")
	}
	if _, err := qq.AddQQAlert(store.QQAlert{At: at.UnixMilli(), Kind: kind, Account: account, Text: "编的" + kind}); err != nil {
		t.Fatalf("写提醒（%s %s）：%v", account, kind, err)
	}
}

// seed 在 dir 里造数据（用 scratchpad 目录做命令行冒烟时也用它）
func seed(t *testing.T, dir string) fixture {
	t.Helper()
	s := seedDB(t, dir)
	hour := func(h int, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.Local) }

	g300, g301, g399 := "qq:group:300", "qq:group:301", "qq:group:399"
	// 群 300，09 点：两条带 target 的判断，最后一条（09:40 的 2/2）决定这个小时（前一条 1/1 被盖掉，取错了就看得出）
	addDecision(t, s, g300, channel.VerdictForced, hour(9, 0).Add(10*time.Second), new(1.0), 1, store.GateOutcomeSpoke)
	addDecision(t, s, g300, channel.VerdictSkip, hour(9, 40), new(2.0), 2, "")
	// 10 点：被叫到的一句（3 说了 / 该 1，1.5 倍以上）
	addDecision(t, s, g300, channel.VerdictEnter, hour(10, 10), new(1.0), 3, store.GateOutcomeDry)
	// 11 点：1 说了 / 该 4（不到一半），后面那些没有 target，不盖掉它
	addDecision(t, s, g300, channel.VerdictEnter, hour(11, 5), new(4.0), 1, store.GateOutcomeSelfSkip)
	addDecision(t, s, g300, channel.VerdictForced, hour(11, 6), nil, 0, store.GateOutcomeBlocked)
	addDecision(t, s, g300, channel.VerdictForced, hour(11, 7), nil, 0, store.GateOutcomeInterrupted)
	addDecision(t, s, g300, channel.VerdictForced, hour(11, 8), nil, 0, store.GateOutcomeError)
	addDecision(t, s, g300, channel.VerdictForced, hour(11, 9), nil, 0, store.GateOutcomeFuse)
	addDecision(t, s, g300, channel.VerdictMuted, hour(11, 10), nil, 0, "")
	addDecision(t, s, g300, channel.VerdictBackoff, hour(11, 11), nil, 0, "")
	addDecision(t, s, g300, channel.VerdictFuse, hour(11, 12), nil, 0, "")
	addDecision(t, s, g300, channel.VerdictOff, hour(11, 13), nil, 0, "")
	addDecision(t, s, g300, channel.VerdictTaken, hour(11, 14), nil, 0, "")
	addDecision(t, s, g300, channel.VerdictPaused, hour(11, 15), nil, 0, "")
	addDecision(t, s, g300, channel.VerdictMerged, hour(11, 20), nil, 0, "")
	// 放行了却没有结局：正常是 0，不是 0 要显眼地报
	addDecision(t, s, g300, channel.VerdictEnter, hour(11, 25), nil, 0, "")

	// 群 301：10:35 那条在掉线时段里（100 号 10:30 掉到 10:50），11:55 那条在 200 号没恢复的那段里
	addDecision(t, s, g301, channel.VerdictForced, hour(10, 35), new(5.0), 0, store.GateOutcomeSpoke)
	addDecision(t, s, g301, channel.VerdictEnter, hour(11, 30), new(1.0), 1, store.GateOutcomeSpoke)
	addDecision(t, s, g301, channel.VerdictEnter, hour(11, 55), new(1.0), 1, store.GateOutcomeSpoke)

	// 群 399：三天前的一条，-days 7 算进来、-days 2 不算
	addDecision(t, s, g399, channel.VerdictForced, time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local), nil, 0, store.GateOutcomeSpoke)

	// 群 302：08 点那条 target 不到 1，按 replay-gate 的口径不算进"该说几句"，这个群就没有能算的小时
	addDecision(t, s, "qq:group:302", channel.VerdictEnter, hour(8, 30), new(0.5), 1, store.GateOutcomeSpoke)

	addAlert(t, s, hour(10, 30), "down", "100")
	addAlert(t, s, hour(10, 50), "up", "100")
	// 200 号掉下去没恢复：这一段算到统计截止
	addAlert(t, s, hour(11, 50), "down", "200")
	// 10-20 的一段早就恢复完了：和窗口（-days 7 从 10-21 起）不相交，报告不该列出来、也不该算段数
	addAlert(t, s, time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local), "down", "400")
	addAlert(t, s, time.Date(2026, 10, 20, 10, 30, 0, 0, time.Local), "up", "400")
	// 库要关掉：报告是只读打开的，同时开着看的是另一份会话
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return fixture{dir: dir}
}

// runReport 跑一遍报告，返回输出
func runReport(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := run(args, &buf); err != nil {
		t.Fatalf("跑报告出错：%v\n输出：%s", err, buf.String())
	}
	return buf.String()
}

// 每个比例的数对：放行比例、被叫到 / 过门槛 / 合起来的开口比例、说了几句 / 该说几句；掉线时段里的判断被排除；
// 没结局的条数显眼地报出来；样本不够的标记；群号打码；跑完两个库的大小和修改时间没变
func TestReport(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow }
	defer func() { clock = old }()

	fx := newFixture(t)
	traceFile := paths.Paths{Root: fx.dir}.GoTraceDB()
	qqFile := paths.Paths{Root: fx.dir}.ChatDB(qqPlatform)
	before := map[string]fileStamp{traceFile: stamp(t, traceFile), qqFile: stamp(t, qqFile)}

	out := runReport(t, "-data", fx.dir)

	want := []string{
		// 掉线时段：100 号 20 分钟，200 号从 11:50 到截止 12:00
		"掉线时段 2 段",
		"*00 号：2026-10-27 10:30 到 2026-10-27 10:50（20 分钟）",
		"*00 号：2026-10-27 11:50 到 2026-10-27 12:00（10 分钟）",
		// 群 300：判了 13 条（合进一串那一条在内），放行比例 8/12
		"qq 群 *00：",
		"判断 13 条（不含掉线时段）：被叫到 5、过门槛 3、没接 1、退避 1、保险丝 1、频率 0 1、合进一串 1；另列（不算判了）：只记录 1、被拦 1、暂停 1",
		"放行比例（被叫到 + 过门槛 ÷ 判了的）8/12 = 66.7%（样本不够）",
		// 被叫到 5 条 1 条开口；过门槛 3 条 2 条有结局、1 条开口、1 条没结局
		"被叫到：放行 5 条，有结局 5 条；开口 1/5 = 20.0%（样本不够）；没结局 0；没开口 0、被拦 1、被打断 1、出错 1、保险丝 1",
		"过门槛：放行 3 条，有结局 2 条；开口 1/2 = 50.0%（样本不够）；没结局 1 ← 有漏洞，放行的判断缺结局；没开口 1、被拦 0、被打断 0、出错 0、保险丝 0",
		"合起来：放行 8 条，有结局 7 条；开口 2/7 = 28.6%（样本不够）；没结局 1 ← 有漏洞，放行的判断缺结局",
		// 09 点 2/2、10 点 3/1、11 点 1/4
		"合起来说了几句 / 该说几句：3 个 target ≥ 1 的小时，说了 6 次 / 该说 7.0 次（0.86 倍）；1.5 倍以上的小时 1/3（33.3%），不到一半的 1/3（33.3%）",
		// 群 301：掉线里的 2 条排掉，只剩 11:30 那一条
		"qq 群 *01：",
		"掉线时段里有 2 条判断，已排除",
		"判断 1 条（不含掉线时段）：被叫到 0、过门槛 1、没接 0、退避 0、保险丝 0、频率 0 0、合进一串 0；另列（不算判了）：只记录 0、被拦 0、暂停 0",
		"放行比例（被叫到 + 过门槛 ÷ 判了的）1/1 = 100.0%（样本不够）",
		// 群 399：三天前那条，默认 -days 7 算
		"qq 群 *99：",
		// 群 302：只有一条 target 0.5 的，没有能算的小时
		"qq 群 *02：",
		"合起来说了几句 / 该说几句：没有 target ≥ 1 的小时，算不了",
		// 总计：判了 16 条、放行 11 条；hours 4 个、说了 7 次 / 该说 8 次
		"总计（4 个群合起来）：",
		"放行比例（被叫到 + 过门槛 ÷ 判了的）11/15 = 73.3%（样本不够）",
		"合起来说了几句 / 该说几句：4 个 target ≥ 1 的小时，说了 7 次 / 该说 8.0 次（0.88 倍）；1.5 倍以上的小时 1/4（25.0%），不到一半的 1/4（25.0%）",
	}
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("输出里没有 %q\n---- 实际输出 ----\n%s", s, out)
		}
	}
	// 群号整串不许出现（打码只留后两位）
	for _, id := range []string{"300", "301", "399"} {
		if strings.Contains(out, id) {
			t.Errorf("输出里有完整的群号 %q\n---- 实际输出 ----\n%s", id, out)
		}
	}
	// 只读：跑完两个库的大小和修改时间不变
	for f, b := range before {
		if a := stamp(t, f); a != b {
			t.Errorf("%s 跑完变了：之前 %+v，之后 %+v", f, b, a)
		}
	}
}

// -account 只用这个号的掉线记录：200 号那段不算数，11:55 那条判断回到样本里；-days 2 把三天前那个群滤掉
func TestReportAccountAndDays(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow }
	defer func() { clock = old }()

	fx := newFixture(t)
	out := runReport(t, "-data", fx.dir, "-account", "100")
	for _, s := range []string{
		"掉线时段 1 段",
		"掉线时段里有 1 条判断，已排除",
		"qq 群 *01：",
		"放行比例（被叫到 + 过门槛 ÷ 判了的）2/2 = 100.0%（样本不够）",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("（-account 100）输出里没有 %q\n---- 实际输出 ----\n%s", s, out)
		}
	}

	out = runReport(t, "-data", fx.dir, "-days", "2")
	if strings.Contains(out, "qq 群 *99") {
		t.Errorf("（-days 2）三天前的群不该算进来\n---- 实际输出 ----\n%s", out)
	}
	// 10-20 那段掉线早就恢复完了：和窗口不相交，不列、也不该算进段数
	if strings.Contains(out, "2026-10-20") {
		t.Errorf("（-days 2）窗口以前的掉线段不该列出来\n---- 实际输出 ----\n%s", out)
	}
	if !strings.Contains(out, "掉线时段 2 段") {
		t.Errorf("（-days 2）掉线段数只算和窗口相交的（该是 2 段）\n---- 实际输出 ----\n%s", out)
	}
	if !strings.Contains(out, "统计 2026-10-26 00:00 到 2026-10-27 12:00（本地时间，最近 2 天）") {
		t.Errorf("（-days 2）窗口开始该是昨天的零点\n---- 实际输出 ----\n%s", out)
	}
}

// -json 和文本是同一份数字；群里、号里的完整号码都不出现
func TestReportJSON(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow }
	defer func() { clock = old }()

	fx := newFixture(t)
	out := runReport(t, "-data", fx.dir, "-json")
	for _, s := range []string{
		`"label": "qq 群 *00"`,
		`"released": 8`,
		`"with_outcome": 7`,
		`"empty": 1`,
		`"hours": 3`,
		`"actual": 6`,
		`"target": 7`,
		`"account": "*00"`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("JSON 里没有 %q\n---- 实际输出 ----\n%s", s, out)
		}
	}
	for _, id := range []string{"300", "301", "399"} {
		if strings.Contains(out, id) {
			t.Errorf("JSON 里有完整的群号 %q\n---- 实际输出 ----\n%s", id, out)
		}
	}
}

// 库文件不存在就报错退出：只说是哪个库，不打完整路径；不建、不补
func TestReportMissingDB(t *testing.T) {
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err := run([]string{"-data", dir}, &buf)
	if err == nil || !strings.Contains(err.Error(), "还没跑过试运行") {
		t.Fatalf("库不在时要报错说清缺什么：%v", err)
	}
	if !strings.Contains(err.Error(), traceDBName) {
		t.Errorf("报错要说清缺的是哪个库（%s）：%v", traceDBName, err)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("报错里有完整路径（%s）：%v", dir, err)
	}
}

// QQ 库不在也一样：只说库名，不打完整路径
func TestReportMissingChatDB(t *testing.T) {
	fx := newFixture(t)
	if err := os.Remove(paths.Paths{Root: fx.dir}.ChatDB(qqPlatform)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err := run([]string{"-data", fx.dir}, &buf)
	if err == nil || !strings.Contains(err.Error(), chatDBName) {
		t.Fatalf("QQ 库不在时要报错说清缺的是哪个库（%s）：%v", chatDBName, err)
	}
	if strings.Contains(err.Error(), fx.dir) {
		t.Errorf("报错里有完整路径（%s）：%v", fx.dir, err)
	}
}

// 没有 -data 是参数错（退出码 2）
func TestReportNoData(t *testing.T) {
	var buf bytes.Buffer
	err := run(nil, &buf)
	if err == nil || !strings.Contains(err.Error(), "要给 -data") {
		t.Fatalf("没给 -data 要报参数错：%v", err)
	}
	if !strings.Contains(buf.String(), "用法：gate-report") {
		t.Fatalf("要打一行用法，实际：%q", buf.String())
	}
}

// 把 trace.db 设成只读文件属性（Windows 上就是去掉写权限）以后报告照样能跑。
// 先确认这台机器上只读属性真的拦得住按可写打开，不然这条证明不了"只读打开也能跑"
func TestReportReadOnlyFile(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow }
	defer func() { clock = old }()

	fx := newFixture(t)
	trace := paths.Paths{Root: fx.dir}.GoTraceDB()
	if err := os.Chmod(trace, 0o444); err != nil {
		t.Skipf("这台机器设不了只读属性：%v", err)
	}
	// 临时目录收工时要把属性改回去，不然 Windows 删不掉这个文件
	defer os.Chmod(trace, 0o644)
	if f, err := os.OpenFile(trace, os.O_RDWR, 0); err == nil {
		f.Close()
		t.Skip("设了只读属性还能按可写打开：这台机器上这条证明不了只读（写测试文件属性不生效）")
	}
	out := runReport(t, "-data", fx.dir)
	if !strings.Contains(out, "放行比例（被叫到 + 过门槛 ÷ 判了的）8/12 = 66.7%") {
		t.Fatalf("只读的 trace.db 读出来的数不对：\n%s", out)
	}
}

// 报告用的地址写不进去：同一个 UPDATE 在只读地址上被拒、在可写地址上成功（证明拒绝的原因不是 SQL 写错了）
func TestReadOnlyDSNRefusesWrite(t *testing.T) {
	fx := newFixture(t)
	trace := paths.Paths{Root: fx.dir}.GoTraceDB()
	const update = `UPDATE gate_decisions SET reason = '改一下'`

	ro, err := sql.Open("sqlite", readOnlyDSN(trace))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.Exec(update); err == nil {
		ro.Close()
		t.Fatal("只读地址上写进去了：报告不是只读打开的")
	}
	ro.Close()

	rw, err := sql.Open("sqlite", sqliteURI(trace)+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Exec(update); err != nil {
		t.Fatalf("同一条 SQL 在可写地址上也失败（%v）：上面那条断言证明不了只读", err)
	}
}

// 参数错只报参数名和期望类型、不回显用户输入的原始值；库不在只说到哪个库、不打完整路径
func TestReportErrorsHideInputs(t *testing.T) {
	fx := newFixture(t)
	var buf bytes.Buffer
	err := run([]string{"-data", fx.dir, "-json=" + fakeNum}, &buf)
	if err == nil {
		t.Fatal("-json=<编的号> 应该报参数错")
	}
	if !errors.Is(err, errUsage) {
		t.Errorf("布尔参数写错要算参数错（退出码 2）：%v", err)
	}
	if strings.Contains(err.Error(), fakeNum) {
		t.Errorf("参数错把原始值打出来了（绕过打码）：%v", err)
	}
	for _, s := range []string{"-json", "true 或 false"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("参数错要说清是哪个参数、要什么类型（缺 %q）：%v", s, err)
		}
	}
	if !strings.Contains(buf.String(), "用法：gate-report") {
		t.Errorf("参数错要打一行用法，实际：%q", buf.String())
	}

	// -data 的路径里带号：数据目录不存在时报错也不能把路径打出来
	hidden := filepath.Join(t.TempDir(), "100")
	buf.Reset()
	err = run([]string{"-data", hidden}, &buf)
	if err == nil {
		t.Fatal("数据目录不存在要报错")
	}
	if strings.Contains(err.Error(), "100") {
		t.Errorf("报错里有带号码的路径：%v", err)
	}

	// -since 写错也一样：只说格式，不回显
	buf.Reset()
	err = run([]string{"-data", fx.dir, "-since", fakeNum}, &buf)
	if err == nil || !errors.Is(err, errUsage) {
		t.Fatalf("-since 写错要报参数错：%v", err)
	}
	if strings.Contains(err.Error(), fakeNum) {
		t.Errorf("-since 的原始值被打出来了：%v", err)
	}
	if !strings.Contains(err.Error(), sinceLayout) {
		t.Errorf("-since 报错要说清格式：%v", err)
	}
}

// 统计窗口是半开区间 [from, until)：截止那一毫秒的判断不算；掉线段恢复那一毫秒的判断算
func TestReportWindowBounds(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow } // 截止 2026-10-27 12:00:00.000
	defer func() { clock = old }()

	dir := t.TempDir()
	s := seedDB(t, dir)
	at := func(h, m, sec int) time.Time { return time.Date(2026, 10, 27, h, m, sec, 0, time.Local) }
	// 100 号 11:50 掉、11:55 恢复：恢复那一毫秒的判断不算掉线
	addAlert(t, s, at(11, 50, 0), "down", "100")
	addAlert(t, s, at(11, 55, 0), "up", "100")
	addDecision(t, s, "qq:group:300", channel.VerdictForced, at(11, 55, 0), nil, 0, store.GateOutcomeSpoke)
	// 200 号 11:56 掉了没恢复：截止前最后一毫秒（11:59:59.999）的判断在掉线里，排掉
	addAlert(t, s, at(11, 56, 0), "down", "200")
	addDecision(t, s, "qq:group:300", channel.VerdictForced, at(11, 59, 59).Add(999*time.Millisecond), nil, 0, store.GateOutcomeSpoke)
	// 正好截止那一毫秒：窗口是半开的，不算进任何数
	addDecision(t, s, "qq:group:300", channel.VerdictForced, fixedNow, nil, 0, store.GateOutcomeSpoke)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	out := runReport(t, "-data", dir)
	for _, want := range []string{
		"掉线时段 2 段",
		"判断 1 条（不含掉线时段）：被叫到 1、过门槛 0、没接 0、退避 0、保险丝 0、频率 0 0、合进一串 0；另列（不算判了）：只记录 0、被拦 0、暂停 0",
		"掉线时段里有 1 条判断，已排除",
		"放行比例（被叫到 + 过门槛 ÷ 判了的）1/1 = 100.0%（样本不够）",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q\n---- 实际输出 ----\n%s", want, out)
		}
	}
}

// 夏令时回拨那一天，两个不同的本地 01 点要各算一个小时：小时按绝对时间取整，不靠本地年月日小时重建
func TestReportHoursFallBack(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("这台机器没有时区数据：%v", err)
	}
	oldLocal := time.Local
	time.Local = loc
	defer func() { time.Local = oldLocal }()
	old := clock
	clock = func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, loc) }
	defer func() { clock = old }()

	dir := t.TempDir()
	s := seedDB(t, dir)
	// 2026-11-01 05:30Z 是本地 01:30 EDT（回拨前），06:30Z 是本地 01:30 EST（回拨后）：两个不同的小时
	addDecision(t, s, "qq:group:350", channel.VerdictForced, time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), new(1.0), 1, store.GateOutcomeSpoke)
	addDecision(t, s, "qq:group:350", channel.VerdictEnter, time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), new(2.0), 3, store.GateOutcomeSpoke)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	out := runReport(t, "-data", dir)
	want := "合起来说了几句 / 该说几句：2 个 target ≥ 1 的小时，说了 4 次 / 该说 3.0 次（1.33 倍）；1.5 倍以上的小时 0/2（0.0%），不到一半的 0/2（0.0%）"
	if !strings.Contains(out, want) {
		t.Errorf("输出里没有 %q\n---- 实际输出 ----\n%s", want, out)
	}
}

// "样本不够"按放行数算：放行 20 条、1 条没结局时不标（比例的分母还是有结局的 19 条）
func TestReportSampleByReleased(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow }
	defer func() { clock = old }()

	dir := t.TempDir()
	s := seedDB(t, dir)
	day := time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local)
	for i := range 20 {
		outcome := store.GateOutcomeSpoke
		if i == 0 {
			outcome = "" // 一条放行了没结局
		}
		addDecision(t, s, "qq:group:350", channel.VerdictForced, day.Add(time.Duration(i)*time.Second), nil, 0, outcome)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	out := runReport(t, "-data", dir)
	want := "被叫到：放行 20 条，有结局 19 条；开口 19/19 = 100.0%；没结局 1 ← 有漏洞，放行的判断缺结局"
	if !strings.Contains(out, want) {
		t.Errorf("输出里没有 %q\n---- 实际输出 ----\n%s", want, out)
	}
	if strings.Contains(out, "19/19 = 100.0%（样本不够）") {
		t.Errorf("放行 20 条不该再标样本不够（标记要看放行数，不是分母）\n---- 实际输出 ----\n%s", out)
	}
}

// -since 只统计这个时刻以后：和 -days 同时给取更晚的那个；报告头写明实际起点
func TestReportSince(t *testing.T) {
	old := clock
	clock = func() time.Time { return fixedNow }
	defer func() { clock = old }()

	fx := newFixture(t)
	out := runReport(t, "-data", fx.dir, "-since", "2026-10-27T10:00")
	for _, want := range []string{
		"统计 2026-10-27 10:00 到 2026-10-27 12:00（本地时间，-since 起，比最近 7 天的起点晚）",
		// 群 300：10 点以后判了 11 条（含合进一串 1 条），放行 7 条
		"放行比例（被叫到 + 过门槛 ÷ 判了的）7/10 = 70.0%（样本不够）",
		// 10 点 3/1、11 点 1/4
		"合起来说了几句 / 该说几句：2 个 target ≥ 1 的小时，说了 4 次 / 该说 5.0 次（0.80 倍）；1.5 倍以上的小时 1/2（50.0%），不到一半的 1/2（50.0%）",
		// 群 301：11:30 那一条（10:35 在 100 号的掉线里、11:55 在 200 号的掉线里，都排掉）
		"判断 1 条（不含掉线时段）：被叫到 0、过门槛 1、没接 0、退避 0、保险丝 0、频率 0 0、合进一串 0；另列（不算判了）：只记录 0、被拦 0、暂停 0",
		"掉线时段里有 2 条判断，已排除",
		"总计（2 个群合起来）：",
		"放行比例（被叫到 + 过门槛 ÷ 判了的）8/11 = 72.7%（样本不够）",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("（-since 2026-10-27T10:00）输出里没有 %q\n---- 实际输出 ----\n%s", want, out)
		}
	}
	if strings.Contains(out, "qq 群 *99") || strings.Contains(out, "qq 群 *02") {
		t.Errorf("（-since）10 点以前的判断不该算进来\n---- 实际输出 ----\n%s", out)
	}

	// -since 比窗口起点早：起点还是 -days 算的，报告头说清 -since 没起作用
	out = runReport(t, "-data", fx.dir, "-days", "2", "-since", "2026-10-01T00:00")
	if !strings.Contains(out, "统计 2026-10-26 00:00 到 2026-10-27 12:00（本地时间，最近 2 天、-since 更早没起作用）") {
		t.Errorf("（-since 更早）窗口起点该还是 -days 的\n---- 实际输出 ----\n%s", out)
	}
}

// fileStamp 是文件的大小和修改时间：只读要证明这两位没变
type fileStamp struct {
	size  int64
	mtime time.Time
}

func stamp(t *testing.T, file string) fileStamp {
	t.Helper()
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	return fileStamp{size: st.Size(), mtime: st.ModTime()}
}
