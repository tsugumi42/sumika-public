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
	"testing"
	"time"

	"sumika-native/internal/datalock"
	"sumika-native/internal/memory"
)

// recGen 记下每次收到的提示词，回复交给 reply（nil 用 demoGen：每批写一条引用第一句话的候选事实）
type recGen struct {
	mu      sync.Mutex
	prompts []string
	reply   func(prompt string) string
}

func (g *recGen) Call(ctx context.Context, model, prompt, key string) (memory.Reply, error) {
	g.mu.Lock()
	g.prompts = append(g.prompts, prompt)
	g.mu.Unlock()
	if g.reply != nil {
		return memory.Reply{Text: g.reply(prompt)}, nil
	}
	return demoGen{}.Call(ctx, model, prompt, key)
}

func (g *recGen) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.prompts)
}

func (g *recGen) saw(s string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.prompts {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// boomGen 被调用就让测试失败：用来证明估算模式不碰注入的生成器
type boomGen struct{ t *testing.T }

func (g boomGen) Call(context.Context, string, string, string) (memory.Reply, error) {
	g.t.Helper()
	g.t.Error("估算模式不该调用生成器")
	return memory.Reply{}, errors.New("不该被调用")
}

var ignoredAlways ignoreCheck = func(string) (ignoreVerdict, error) { return verdictIgnored, nil }

// richFixture：四种场合各有会话，每个会话的内容里带一个标记词，用来看提示词里有没有混进别的会话；
// 所有会话的整理游标种在 0（等于导入搬过来的旧游标）
type rich struct {
	*fixture
	seqOwner, seqPeer, seqGroup, seqLocal, seqLocalGp int64
}

func richFixture(t *testing.T) *rich {
	f := newFixture(t)
	r := &rich{fixture: f}
	f.ev(convOwner, "owner", "OWNERMARK 我下周三考试")
	f.ev(convOwner, "her", "加油")
	r.seqOwner = f.ev(convOwner, "owner", "OWNERMARK 谢谢")
	f.ev(convPeer, "qq:5", "PEERMARK 我在上海")
	r.seqPeer = f.ev(convPeer, "her", "哦哦")
	f.ev(convGroupA, "qq:5", "GROUPMARK 我最近在学吉他")
	r.seqGroup = f.ev(convGroupA, "qq:6", "GROUPMARK 我也是")
	r.seqLocal = f.ev(convLocal, "owner", "LOCALMARK 本地一句话")
	r.seqLocalGp = f.ev(convLocalGp, "owner", "LOCALGPMARK 本地群一句话")
	f.seedMem(func(db *sql.DB) {
		for _, c := range []string{convOwner, convPeer, convGroupA, convLocal, convLocalGp} {
			f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, 0, 1000)`, c)
		}
	})
	f.closeStores()
	return r
}

func (r *rich) cfg(g memory.Generator, m mode) runConfig {
	return runConfig{Data: r.Data, Char: testChar, Mode: m, Gen: g, Ignore: ignoredAlways, Logf: func(string, ...any) {}}
}

func sessionOf(t *testing.T, res *runResult, conv string) sessionInfo {
	t.Helper()
	for _, s := range res.Sessions {
		if s.Conv == conv {
			return s
		}
	}
	t.Fatalf("结果里没有会话 %s", conv)
	return sessionInfo{}
}

// 估算：一次生成器都不调（注入的 boomGen 被碰就红），但整理循环真的跑了，批数、字数数出来了；会话按场合分对
func TestEstimateCallsNoModel(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	res, err := runShadow(context.Background(), r.cfg(boomGen{t}, modeEstimate))
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != modeEstimate || res.Calls != 1 || len(res.Batches) != 1 || res.PromptRunes == 0 {
		t.Fatalf("默认只放行主人的私聊：应该 1 批 1 次（模拟的）：calls=%d batches=%d runes=%d", res.Calls, len(res.Batches), res.PromptRunes)
	}
	want := map[string]convKind{convOwner: kindQQOwnerPrivate, convPeer: kindQQOtherPrivate, convGroupA: kindQQGroup, convLocal: kindLocalPrivate, convLocalGp: kindLocalGroup}
	for conv, k := range want {
		if got := sessionOf(t, res, conv).Kind; got != k {
			t.Errorf("%s 认成了 %v，应该是 %v", conv, got, k)
		}
	}
	text := estimateText(res)
	for _, must := range []string{"估算", "一次后台模型都没调", "没有真调", "拷贝期间后台是退出的", "不声称原库全程没动", "真实游标"} {
		if !strings.Contains(text, must) {
			t.Errorf("估算文字里应该有 %q：\n%s", must, text)
		}
	}
	// 估算模式写进副本的是空回复：没有新事实
	if n := len(res.Extract.newFacts()); n != 0 {
		t.Errorf("估算不该写出事实：%d", n)
	}
}

// -places 默认只放行主人的私聊（Q2）：群、别人的私聊、本地的会话一批都不整理，游标原样，提示词里也没混进它们的话；
// 显式加了才跑
func TestPlacesDefaultOnlyOwnerPrivate(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	g := &recGen{}
	res, err := runShadow(context.Background(), r.cfg(g, modeFake))
	if err != nil {
		t.Fatal(err)
	}
	if g.count() != 1 || !g.saw("OWNERMARK") {
		t.Fatalf("默认应该只整理主人的私聊：调用 %d 次", g.count())
	}
	for _, other := range []string{"PEERMARK", "GROUPMARK", "LOCALMARK", "LOCALGPMARK"} {
		if g.saw(other) {
			t.Errorf("提示词里混进了不放行的会话 %s", other)
		}
	}
	for conv, allowed := range map[string]bool{convOwner: true, convPeer: false, convGroupA: false, convLocal: false, convLocalGp: false} {
		s := sessionOf(t, res, conv)
		if s.Allowed != allowed || (!allowed && (s.Batches != 0 || s.Behind != 0 || s.People != nil)) {
			t.Errorf("%s: %+v", conv, s)
		}
	}

	g2 := &recGen{}
	cfg := r.cfg(g2, modeFake)
	cfg.Places = []string{placePlatformPrivate, placePlatformGroup}
	if _, err := runShadow(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"OWNERMARK", "PEERMARK", "GROUPMARK"} {
		if !g2.saw(must) {
			t.Errorf("platform-private + platform-group 应该整理到 %s", must)
		}
	}
	for _, not := range []string{"LOCALMARK", "LOCALGPMARK"} {
		if g2.saw(not) {
			t.Errorf("本地会话没有放行，不该整理 %s", not)
		}
	}
}

// 认场合：渠道不认识、形状不对的会话永远不放行；QQ 私聊里没有主人的行按"别人的私聊"算（收紧）
func TestClassifyConvShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]convKind{
		"telegram:private:1:2": kindUnknown,
		"telegram:group:7":     kindUnknown,
		"qq:private:1":         kindUnknown,
		"qq:private::9":        kindUnknown,
		"qq:group:":            kindUnknown,
		"qq:group:7:extra":     kindUnknown,
		"qq:weird:1:2":         kindUnknown,
		"":                     kindUnknown,
		"console:main":         kindLocalPrivate,
		"console:group:t1":     kindLocalGroup,
		"qq:group:7":           kindQQGroup,
	}
	for conv, want := range cases {
		got, err := classifyConv(context.Background(), noEvents{}, conv)
		if err != nil || got != want {
			t.Errorf("%q → %v %v，应该是 %v", conv, got, err, want)
		}
	}
	if k, _ := classifyConv(context.Background(), noEvents{}, "qq:private:1:2"); k != kindQQOtherPrivate {
		t.Errorf("认不出主人的 QQ 私聊应该按别人的算：%v", k)
	}
	for _, k := range []convKind{kindUnknown} {
		if k.allowed(allPlaces) {
			t.Error("认不出的会话任何 places 都不放行")
		}
	}
}

// noEvents 是什么事件都没有的来源
type noEvents struct{}

func (noEvents) Events(context.Context, string, int64, int) ([]memory.Event, error) { return nil, nil }
func (noEvents) Revoked(context.Context, string, []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}
func (noEvents) LastSaid(context.Context, string) (memory.Event, bool, error) {
	return memory.Event{}, false, nil
}
func (noEvents) Find(context.Context, string, []string, int) ([]memory.Event, error) { return nil, nil }
func (noEvents) Conversations(context.Context) ([]memory.ConvHead, error)            { return nil, nil }

// 花费门：缺 -spend 的 -gen real 在调用任何东西之前就拒绝；-spend 配估算 / fake 也拒绝；到上限就停，上限那一次不会发出去
func TestSpendGate(t *testing.T) {
	r := richFixture(t)
	realCfg := func(spend int) runConfig {
		c := r.cfg(&recGen{}, modeReal)
		c.Spend, c.Model = spend, "shadow-test"
		return c
	}
	old := realGenReady
	defer func() { realGenReady = old }()

	// 缺 -spend：不管 realGenReady 开没开都拒绝，而且拒绝在拷库之前（副本目录不会建出来）
	for _, ready := range []bool{false, true} {
		realGenReady = ready
		c := realCfg(0)
		c.Work = filepath.Join(r.Data, "dev", "no-spend")
		if _, err := runShadow(context.Background(), c); err == nil || !strings.Contains(err.Error(), "必须同时给 -spend") {
			t.Fatalf("ready=%v 缺 -spend 应该拒绝：%v", ready, err)
		}
		if _, err := os.Stat(c.Work); err == nil {
			t.Fatal("参数就不对，不该建 -work")
		}
	}
	// -spend 配估算 / fake
	for _, m := range []mode{modeEstimate, modeFake} {
		c := r.cfg(&recGen{}, m)
		c.Spend = 5
		if err := c.validate(); err == nil {
			t.Errorf("%s 模式配 -spend 应该拒绝", m)
		}
	}
	// 关着的时候带 -spend 也拒绝
	realGenReady = false
	if _, err := runShadow(context.Background(), realCfg(3)); err == nil || !strings.Contains(err.Error(), "关着") {
		t.Fatalf("realGenReady 关着应该拒绝：%v", err)
	}

	// 打开以后：每批 6000 个码点的上限，放多一点文字让一个会话切出 4 批以上；上限 2 次
	realGenReady = true
	f := newFixture(t)
	long := strings.Repeat("字", 900)
	for i := 0; i < 24; i++ {
		who := "owner"
		if i%2 == 1 {
			who = "her"
		}
		f.ev(convOwner, who, long)
	}
	f.seedMem(func(db *sql.DB) {
		f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, 0, 1000)`, convOwner)
	})
	f.closeStores()
	g := &recGen{}
	cfg := runConfig{Data: f.Data, Char: testChar, Mode: modeReal, Gen: g, Model: "shadow-test", Spend: 2, Ignore: ignoredAlways, Logf: func(string, ...any) {}}
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if g.count() != 2 || res.Calls != 2 || !res.Truncated {
		t.Fatalf("上限 2：应该只调 2 次、报被截断：生成器 %d 次，calls=%d truncated=%v", g.count(), res.Calls, res.Truncated)
	}
	if text := estimateText(res); !strings.Contains(text, "被预算截断") {
		t.Errorf("总览里要写清被预算截断：\n%s", text)
	}
	if len(res.Batches) != 2 {
		t.Errorf("截断以后不该把后面的批记成整理过：%d 批", len(res.Batches))
	}
}

// 真实游标：第一批从游标后面一条开始；seed：游标种在窗口起点之前最后一条
func TestCursorModes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ev(convOwner, "owner", "OLD1")
	cut := f.ev(convOwner, "her", "OLD2")
	cutAt := f.at
	f.ev(convOwner, "owner", "NEW1")
	f.ev(convOwner, "her", "NEW2")
	f.seedMem(func(db *sql.DB) {
		f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, ?, 1000)`, convOwner, cut)
	})
	f.closeStores()

	g := &recGen{}
	res, err := runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: g, Ignore: ignoredAlways, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Batches) != 1 || res.Batches[0].From != cut+1 || g.saw("OLD1") || !g.saw("NEW1") {
		t.Fatalf("真实游标：第一批应该从 %d 开始：%+v", cut+1, res.Batches)
	}
	s := sessionOf(t, res, convOwner)
	if !s.HasCursor || s.Cursor != cut || s.Behind != 2 {
		t.Errorf("落后应该是 2 条：%+v", s)
	}

	// seed：窗口起点在 OLD2 之后一点，游标应该种在 cut
	g2 := &recGen{}
	res, err = runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: g2, Ignore: ignoredAlways, Logf: func(string, ...any) {},
		Cursors: "seed", SeedSince: time.UnixMilli(cutAt + 1000)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Batches) != 1 || res.Batches[0].From != cut+1 || !g2.saw("NEW2") || g2.saw("OLD2") {
		t.Fatalf("seed：第一批应该从窗口起点开始：%+v", res.Batches)
	}
	if text := estimateText(res); !strings.Contains(text, "游标不是真实的") {
		t.Errorf("seed 模式要大字写不是真实游标：\n%s", text)
	}
	// seed 的参数检查
	bad := runConfig{Data: f.Data, Char: testChar, Cursors: "seed"}
	if err := bad.validate(); err == nil {
		t.Error("seed 不给窗口应该拒绝")
	}
	bad = runConfig{Data: f.Data, Char: testChar, Cursors: "real", SeedLast: time.Hour}
	if err := bad.validate(); err == nil {
		t.Error("real 配 -last 应该拒绝")
	}
}

// 拷完不再碰原库：拷完把原库的文件全删掉（Windows 上有句柄没关就删不掉），工具照跑、结果和没删一样
func TestOriginalNotTouchedAfterCopy(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	g := &recGen{}
	cfg := r.cfg(g, modeFake)
	deleted := false
	cfg.hookAfterCopy = func(string) {
		for _, d := range []string{"go", "characters", "shared"} {
			if err := os.RemoveAll(filepath.Join(r.Data, d)); err != nil {
				t.Errorf("拷完原库的 %s 删不掉（工具还握着它？）：%v", d, err)
				return
			}
		}
		deleted = true
	}
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted || g.count() != 1 || len(res.Batches) != 1 {
		t.Fatalf("原库删掉以后应该照跑：deleted=%v calls=%d batches=%d", deleted, g.count(), len(res.Batches))
	}
}

// 数据锁被别人拿着：整个运行拒绝，不留副本
func TestRunRefusesWhenDataLockHeld(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	lock, err := datalock.LockData(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	cfg := r.cfg(&recGen{}, modeFake)
	cfg.Work = filepath.Join(r.Data, "dev", "memory-shadow", "held")
	_, err = runShadow(context.Background(), cfg)
	if !errors.Is(err, datalock.ErrDataInUse) {
		t.Fatalf("应该拒绝：%v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Work, "copy")); err == nil {
		t.Fatal("拒绝以后不该留副本")
	}
}

// 固定位置缺库时整个运行拒绝，不回落到 Bun 原库。
func TestRunRefusesBunOriginal(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	if err := os.Remove(filepath.Join(r.Data, "go", "characters", testChar, "memory.db")); err != nil {
		t.Fatal(err)
	}
	g := &recGen{}
	if _, err := runShadow(context.Background(), r.cfg(g, modeFake)); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("应该拒绝：%v", err)
	}
	if g.count() != 0 {
		t.Fatal("拒绝以后不该调任何东西")
	}
}

// 副本默认删、报告目录留着；-keep 留副本；-work 不是空目录就拒绝
func TestKeepAndWorkMustBeEmpty(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	work := filepath.Join(r.Data, "dev", "memory-shadow", "k1")
	cfg := r.cfg(&recGen{}, modeFake)
	cfg.Work = work
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "copy")); err == nil {
		t.Error("默认应该删副本")
	}
	if _, err := os.Stat(res.Out); err != nil {
		t.Errorf("报告目录应该留着：%v", err)
	}
	if _, err := runShadow(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "不是空目录") {
		t.Errorf("-work 已有东西应该拒绝：%v", err)
	}

	cfg2 := r.cfg(&recGen{}, modeFake)
	cfg2.Work, cfg2.Keep = filepath.Join(r.Data, "dev", "memory-shadow", "k2"), true
	res, err = runShadow(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.Kept, "go", "chat", "qq.db")); err != nil {
		t.Errorf("-keep 应该留着副本：%v", err)
	}
}

// 清上次崩溃遗留的副本：带标记、没人在用的清掉；有人持着锁的、没有标记的、有 .keep 的（-keep 特意留的）不动；out/ 里的报告不删
func TestCleanStale(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.closeStores()
	parent := defaultWorkParent(f.Data)
	now := time.Now()
	mk := func(name string, withMarker bool, withLock bool) string {
		dir := filepath.Join(parent, name)
		f.write(filepath.Join(dir, "copy", "go", "chat", "qq.db"), "x")
		f.write(filepath.Join(dir, "out", "summary.json"), "{}")
		if withMarker {
			f.write(filepath.Join(dir, markerName), "{}")
		}
		if withLock {
			f.write(filepath.Join(dir, "copy", "go", "backend.lock"), "")
		}
		return dir
	}
	crashed := mk("crashed", true, true)
	running := mk("running", true, true)
	foreign := mk("foreign", false, true)
	fresh := mk("fresh", true, false) // 刚建好、还没拿锁
	stale := mk("stale", true, false) // 没有锁文件但标记很旧
	kept := mk("kept", true, true)    // -keep 留下的：锁没人持、带标记，形状和崩溃遗留一样，只差 .keep
	keptStale := mk("kept-stale", true, false)
	f.write(filepath.Join(kept, keepName), "")
	f.write(filepath.Join(keptStale, keepName), "")
	old := now.Add(-3 * time.Hour)
	for _, d := range []string{stale, keptStale} {
		if err := os.Chtimes(filepath.Join(d, markerName), old, old); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := datalock.LockData(filepath.Join(running, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	var logs []string
	if n := cleanStale(f.Data, now, func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }); n != 2 {
		t.Fatalf("应该清 crashed 和 stale 两个：%d", n)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(filepath.Join(crashed, "copy")) || exists(filepath.Join(stale, "copy")) {
		t.Error("没人在用的副本应该清掉")
	}
	for _, keep := range []string{running, foreign, fresh, kept, keptStale} {
		if !exists(filepath.Join(keep, "copy")) {
			t.Errorf("不该动 %s 的副本", filepath.Base(keep))
		}
	}
	if !exists(filepath.Join(crashed, "out", "summary.json")) {
		t.Error("报告不该删")
	}
	// -keep 留下的每个记一行"跳过"，别的不记
	skipped := 0
	for _, l := range logs {
		if strings.Contains(l, "跳过 -keep 留下的副本 kept") {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("kept 和 kept-stale 各该记一行跳过：%q", logs)
	}
}

// -keep 留下的副本不会被下一次运行清掉，崩溃遗留的照旧清。-keep 的副本在运行结束、放锁以后是"带标记、锁没人持"的形状，
// 和崩溃遗留一样，区别只在 .keep；这条走真的两次运行：第一次 -keep，中间手造一份崩溃遗留，第二次运行的启动清理只清后者
func TestKeepCopySurvivesNextRunButCrashedIsCleaned(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	cfg1 := r.cfg(&recGen{}, modeFake)
	cfg1.Keep = true
	res1, err := runShadow(context.Background(), cfg1)
	if err != nil {
		t.Fatal(err)
	}
	keptRun := filepath.Dir(res1.Kept)
	if _, err := os.Stat(filepath.Join(keptRun, keepName)); err != nil {
		t.Fatalf("-keep 的运行结束要留下 .keep：%v", err)
	}
	if _, err := os.Stat(filepath.Join(res1.Kept, "go", "chat", "qq.db")); err != nil {
		t.Fatalf("-keep 应该留着副本：%v", err)
	}
	// 手造一份崩溃遗留：带标记、有副本、副本里有锁文件但没人持，没有 .keep
	crashed := filepath.Join(defaultWorkParent(r.Data), "crashed")
	r.write(filepath.Join(crashed, "copy", "go", "chat", "qq.db"), "x")
	r.write(filepath.Join(crashed, "copy", "go", "backend.lock"), "")
	r.write(filepath.Join(crashed, "out", "summary.json"), "{}")
	r.write(filepath.Join(crashed, markerName), "{}")

	var logs []string
	cfg2 := r.cfg(&recGen{}, modeFake)
	cfg2.Logf = func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }
	res2, err := runShadow(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !exists(filepath.Join(res1.Kept, "go", "chat", "qq.db")) {
		t.Error("下一次运行把 -keep 留下的副本清掉了")
	}
	if exists(filepath.Join(crashed, "copy")) || !exists(filepath.Join(crashed, "out", "summary.json")) {
		t.Error("崩溃遗留的副本应该清掉，报告留着")
	}
	if res2.StaleCleaned != 1 {
		t.Errorf("启动清理应该只清崩溃遗留的那 1 个：%d", res2.StaleCleaned)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "跳过 -keep 留下的副本 "+filepath.Base(keptRun)) {
		t.Errorf("日志里应该记一行跳过 -keep 留下的副本：%q", logs)
	}
}

// -keep 遇到出错：返回值是 nil，不能 panic 把真正的错误盖掉；副本的位置走日志
func TestKeepWithErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	var logs []string
	cfg := r.cfg(boomGen{t}, modeEstimate)
	cfg.Keep = true
	cfg.PromptFile = filepath.Join(t.TempDir(), "没有这个文件.md")
	cfg.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	res, err := runShadow(context.Background(), cfg)
	if err == nil || res != nil || !strings.Contains(err.Error(), "读 -prompt") {
		t.Fatalf("应该返回 -prompt 读不了的错误：%v %v", res, err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "副本留在") {
		t.Errorf("-keep 出错时副本在哪要走日志：%q", logs)
	}
}

// -prompt 换提示词：换上的标记词真的进了发给模型的提示词（生产读的是 summarize-v2，写错文件名这里就红），总览写明来源；
// 换上的版本没有 {{candidates}} 时 W3a 关掉候选块，总览要点出来（prompt_stale）
func TestPromptOverride(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	file := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(file, []byte("PROMPTMARK {{name}} 已知：{{known}} 听说：{{candidates}} 说话人：{{speakers}} 现在：{{now}} 对话：\n{{transcript}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &recGen{}
	cfg := r.cfg(g, modeFake)
	cfg.PromptFile = file
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !g.saw("PROMPTMARK") || !strings.Contains(res.PromptSource, "p.md") {
		t.Fatalf("应该用换上的提示词：%q", res.PromptSource)
	}
	if res.Counts["prompt_stale"] != 0 || strings.Contains(estimateText(res), "没有 {{candidates}}") {
		t.Errorf("换上的提示词有 {{candidates}}，不该报 prompt_stale：%d", res.Counts["prompt_stale"])
	}
	// 没有 {{candidates}} 的版本：照样用，但候选块关着，A 节要说
	file2 := filepath.Join(t.TempDir(), "q.md")
	if err := os.WriteFile(file2, []byte("STALEMARK {{name}} 已知：{{known}} 对话：\n{{transcript}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	g3 := &recGen{}
	cfg3 := r.cfg(g3, modeFake)
	cfg3.PromptFile = file2
	res3, err := runShadow(context.Background(), cfg3)
	if err != nil {
		t.Fatal(err)
	}
	if !g3.saw("STALEMARK") || res3.Counts["prompt_stale"] == 0 || !strings.Contains(estimateText(res3), "没有 {{candidates}}") {
		t.Errorf("没有 {{candidates}} 的提示词：应该照样用、记 prompt_stale、总览点出来：%d", res3.Counts["prompt_stale"])
	}
	r2 := r.cfg(&recGen{}, modeFake)
	res2, err := runShadow(context.Background(), r2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.PromptSHA == res.PromptSHA || !strings.Contains(res2.PromptSource, "出厂") {
		t.Errorf("出厂版和换上的版本哈希应该不同：%s %s", res.PromptSHA, res2.PromptSHA)
	}
}

// 没有游标的放行会话（真开时从当时的最新一条起、不回填）：这次一批也不整理，落后和人数都不数
func TestNoCursorSessionIsNotCounted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ev(convOwner, "owner", "NOCURSORMARK 一句话")
	f.ev(convOwner, "her", "好")
	f.closeStores()
	g := &recGen{}
	res, err := runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: g, Ignore: ignoredAlways, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	s := sessionOf(t, res, convOwner)
	if !s.Allowed || s.HasCursor || s.Batches != 0 || s.Behind != 0 || s.People != nil || g.count() != 0 {
		t.Fatalf("没有游标：不整理、不数：%+v 调用 %d", s, g.count())
	}
	if text := estimateText(res); !strings.Contains(text, "没有游标") || !strings.Contains(text, "这次一批也没整理") {
		t.Errorf("总览里要说明：\n%s", text)
	}
}
