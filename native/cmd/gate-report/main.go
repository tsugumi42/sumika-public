// gate-report 读 Go 后台试运行留下的闸门判断和 QQ 掉线记录，算两件事（规格-Go后台装配 第七节、第八节第 5 步）：
// 闸门放行了几次 / 判了几次，放行以后她真开口了几次 / 放行了几次；顺带把"合起来说了几句 / 该说几句"按 replay-gate.ts 的"①"算一遍。
//
// 只读：用 database/sql + modernc 的 sqlite 按 file:…?mode=ro 打开 go/trace.db 和 go/chat/qq.db，不跑迁移、不改主库；
// 读 WAL 库时 SQLite 可能更新旁边的 -shm（它自己的共享内存），所以不是"目录里一个字节都不动"。
// 另只读 logs/desk-日期.log 和 logs/go-日期.log，用连接事实补上跨重启时提醒表缺失的恢复。
// 掉线时段（down 到同号下一条 up 或连接恢复）里的判断不完整，单独列出来、不算进任何比例。
// 输出只有数字、比例和打码的号码（群号、号只留后两位），不含聊天内容和名字；报错也只说到哪个库，不打完整路径。
//
//	go run ./cmd/gate-report -data <仓库>\data
//	go run ./cmd/gate-report -data … -days 3 -since 2026-10-28T09:30 -account 100 -json
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/channel"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// errUsage：参数不对（退出码 2），别的错是 1
var errUsage = errors.New("参数不对")

// clock 是"现在"：统计截止到这一刻。测试里换成固定时刻
var clock = time.Now

// minSample 是一份比例要多少条样本才算数（规格 第七节：样本少于 20 次的标"样本不够"）
const minSample = 20

// qqPlatform 是 QQ 平台在库文件名里的名字（store 的平台登记表里 "qq" → "qq"）：掉线记录只在 QQ 平台的库里
const qqPlatform = "qq"

// 报错时说的库名：只说这两个，不打完整路径（路径里可能带号、名字，会绕过打码）
const (
	traceDBName = "go/trace.db"
	chatDBName  = "go/chat/" + qqPlatform + ".db"
)

// sinceLayout 是 -since 的格式：本地时刻、没有秒
const sinceLayout = "2006-01-02T15:04"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// run 是 main 的全部：解析参数、算、打出来。测试直接调它
func run(args []string, stdout io.Writer) error {
	const usage = "用法：gate-report -data <数据目录> [-days 7] [-since 2026-10-28T09:30] [-account <号>] [-json]"
	fs := flag.NewFlagSet("gate-report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data", "", "数据目录（里面要有 go/trace.db 和 go/chat/qq.db）")
	days := fs.Int("days", 7, "统计最近几天（按本地日期，含今天；7 是今天和前 6 天）")
	sinceArg := fs.String("since", "", "只统计这个本地时刻以后的判断和掉线（2006-01-02T15:04）；和 -days 同时给时取更晚的那个")
	account := fs.String("account", "", "只用这个号的掉线记录；不给就用 qq_alerts 里所有号的")
	asJSON := fs.Bool("json", false, "输出同样内容的 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, usage)
		return flagErr(fs, err)
	}
	if strings.TrimSpace(*dataDir) == "" {
		fmt.Fprintln(stdout, usage)
		return fmt.Errorf("%w：要给 -data（数据目录）", errUsage)
	}
	if *days < 1 {
		return fmt.Errorf("%w：-days 要 1 以上的整数", errUsage)
	}
	var since time.Time
	if s := strings.TrimSpace(*sinceArg); s != "" {
		t, err := time.ParseInLocation(sinceLayout, s, time.Local)
		if err != nil {
			return fmt.Errorf("%w：-since 要 %s 格式的本地时刻（例如 2026-10-28T09:30）", errUsage, sinceLayout)
		}
		since = t
	}
	now := clock()
	from, until := window(now, *days)
	basis := fmt.Sprintf("最近 %d 天", *days)
	if !since.IsZero() {
		// 改了影响她开不开口的代码以后，从改完那一刻重新起算：和 -days 同时给时取更晚的那个
		if since.After(from) {
			from = since
			basis = fmt.Sprintf("-since 起，比最近 %d 天的起点晚", *days)
		} else {
			basis = fmt.Sprintf("最近 %d 天、-since 更早没起作用", *days)
		}
	}
	rep, err := build(options{DataDir: *dataDir, From: from, Until: until, Days: *days, Account: *account, Basis: basis})
	if err != nil {
		return err
	}
	if *asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", b)
		return err
	}
	writeText(stdout, rep)
	return nil
}

// flagErr 把 flag 包的原话换成"哪个参数、要什么类型"：原话里带着用户输入的原始值
// （-json=<一串号码> 会把整个号打出来），会绕过号码打码
func flagErr(fs *flag.FlagSet, err error) error {
	name := flagErrorName(err.Error())
	if name == "" {
		return fmt.Errorf("%w：参数格式不对", errUsage)
	}
	f := fs.Lookup(name)
	if f == nil {
		return fmt.Errorf("%w：不认识的参数 -%s", errUsage, name)
	}
	return fmt.Errorf("%w：-%s 要 %s", errUsage, name, flagTypeName(f))
}

// flagNameRe 从 flag 包的报错里抠参数名（`invalid value "…" for flag -days: …`、`… for -json: …`、
// `flag provided but not defined: -foo`）。贪婪的 .* 是为了取最后那个：值里也可能带 "for -x" 这样的字
var flagNameRe = regexp.MustCompile(`.*(?:for flag |for |: )-([A-Za-z][A-Za-z0-9_-]*)`)

func flagErrorName(s string) string {
	if m := flagNameRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// flagTypeName 是这个参数要的值的类型，给参数错用
func flagTypeName(f *flag.Flag) string {
	g, ok := f.Value.(flag.Getter)
	if !ok {
		return "值"
	}
	switch g.Get().(type) {
	case bool:
		return "true 或 false"
	case int:
		return "整数"
	case string:
		return "字符串"
	}
	return "值"
}

// window 是统计窗口 [from, until)：本地日期的最后 days 天（含今天）的零点，到"现在"（含不含见 loadDecisions）
func window(now time.Time, days int) (from, until time.Time) {
	y, m, d := now.Date()
	from = time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	return from, now
}

type options struct {
	DataDir string
	From    time.Time
	Until   time.Time
	Days    int
	Account string
	Basis   string // 起点的说法（最近 N 天 / -since 和最近 N 天里更晚的那个），报告头照抄
}

// Report 是这份报告的全部数字：文本输出和 -json 是同一份
type Report struct {
	Days     int         `json:"days"`
	Basis    string      `json:"basis"` // 起点的说法
	From     string      `json:"from"`
	Until    string      `json:"until"`
	Downtime []Downtime  `json:"downtime"`
	Groups   []GroupStat `json:"groups"`
	Total    GroupStat   `json:"total"`
}

// Downtime 是一段掉线（本地时间；Account 打码）
type Downtime struct {
	Account string `json:"account"`
	From    string `json:"from"`
	To      string `json:"to"`
	Minutes int    `json:"minutes"`
}

// VerdictCounts 是判断次数按 verdict 分（规格 第七节、gate.go 开头的常量）
type VerdictCounts struct {
	Forced  int `json:"forced"`  // 被叫到
	Enter   int `json:"enter"`   // 过门槛
	Skip    int `json:"skip"`    // 没接
	Backoff int `json:"backoff"` // 退避
	Fuse    int `json:"fuse"`    // 保险丝
	Muted   int `json:"muted"`   // 频率 0
	Merged  int `json:"merged"`  // 合进一串
	// 下面三个另列：不算"判了"（放行比例的分母里没有它们）
	Off    int `json:"off"`    // 只记录档
	Taken  int `json:"taken"`  // 拦截链接了
	Paused int `json:"paused"` // 通道门
	// 不认识的 verdict：不藏，单独报，免得闸门改了常量这里悄悄漏数
	Unknown int `json:"unknown"`
}

// OutcomeStat 是放行以后回合结局的分布
type OutcomeStat struct {
	Released    int `json:"released"`     // 放行的判断条数（Outcome 为空不算没开口，单独数）
	WithOutcome int `json:"with_outcome"` // 结局不为空的条数：开口比例的分母
	Spoke       int `json:"spoke"`        // 开口
	Dry         int `json:"dry"`          // 试运行计划里写了（也算开口）
	Empty       int `json:"empty"`        // 没结局：正常情况下是 0，不是 0 就是有漏洞
	SelfSkip    int `json:"self_skip"`    // 模型没回复正文
	Blocked     int `json:"blocked"`      // 回复全被拦下
	Fuse        int `json:"fuse"`         // 被字数保险丝拦下
	Interrupted int `json:"interrupted"`  // 被打断
	Error       int `json:"error"`        // 内核出错
	Other       int `json:"other"`        // 不认识的结局
}

// ReleaseStat 是一个群的放行比例和放行以后的各种结局
type ReleaseStat struct {
	Judged   int         `json:"judged"`   // 放行比例的分母：判了的（不含合进一串、只记录、被拦、暂停）
	Released int         `json:"released"` // 放行的判断条数
	Ratio    float64     `json:"ratio"`    // Released / Judged
	Forced   OutcomeStat `json:"forced"`   // 被叫到的那些判断
	Enter    OutcomeStat `json:"enter"`    // 过门槛的那些判断
	All      OutcomeStat `json:"all"`      // 两者合起来
}

// HourStat 是"合起来说了几句 / 该说几句"（replay-gate.ts 的"①"那种算法）
type HourStat struct {
	Hours  int     `json:"hours"`  // target >= 1 的小时数
	Actual float64 `json:"actual"` // Σ actual
	Target float64 `json:"target"` // Σ target
	Ratio  float64 `json:"ratio"`  // Actual / Target
	Over   int     `json:"over"`   // actual > 1.5 × target 的小时
	Under  int     `json:"under"`  // actual < 0.5 × target 的小时
}

// GroupStat 是一个群的统计（Label 里是打码的群号）
type GroupStat struct {
	Label    string        `json:"label"`
	Verdicts VerdictCounts `json:"verdicts"`
	Judged   int           `json:"judged"`   // 判了的（含合进一串）
	Excluded int           `json:"excluded"` // 掉线时段里排掉的判断条数
	Release  ReleaseStat   `json:"release"`
	Hours    HourStat      `json:"hours"`
}

// decision 是一个群判断里这份报告要用的几列
type decision struct {
	at           int64
	conversation string
	verdict      string
	target       *float64
	actual       int
	outcome      string
}

// segment 是一段掉线：同一个号从 down 到下一条 up（最后一条没 up 的算到统计截止）
type segment struct {
	account string
	from    int64
	to      int64
}

// build 读两个库（只读）算出报告
func build(opts options) (*Report, error) {
	p, err := paths.Open(opts.DataDir)
	if err != nil {
		// 数据目录的报错原话里带着绝对路径：换成固定说法，不把 -data 里的号、名字打出来
		return nil, errors.New(hidePaths(err.Error(), opts.DataDir, absPath(opts.DataDir)))
	}
	traceDB, err := openReadOnly(p.GoTraceDB(), traceDBName)
	if err != nil {
		return nil, err
	}
	defer traceDB.Close()
	chatDB, err := openReadOnly(p.ChatDB(qqPlatform), chatDBName)
	if err != nil {
		return nil, err
	}
	defer chatDB.Close()

	from, until := opts.From.UnixMilli(), opts.Until.UnixMilli()
	ds, err := loadDecisions(traceDB, from, until)
	if err != nil {
		return nil, fmt.Errorf("读 %s 的 gate_decisions 出错：%s", traceDBName, hidePaths(err.Error(), p.GoTraceDB()))
	}
	// 掉线配对要往前翻（跨窗口起点的那一段得从头算），列出来只列和窗口相交的
	segs, err := loadDowntime(chatDB, until, opts.Account, p.LogsDir())
	if err != nil {
		return nil, fmt.Errorf("读 QQ 掉线记录出错：%s", hidePaths(err.Error(), p.ChatDB(qqPlatform), p.LogsDir()))
	}

	rep := &Report{Days: opts.Days, Basis: opts.Basis, From: opts.From.Format("2006-01-02 15:04"), Until: opts.Until.Format("2006-01-02 15:04")}
	for _, s := range segs {
		// 和统计窗口不相交的段（窗口以前就恢复完了）不列，段数也按这个算
		if s.to <= s.from || s.to <= from || s.from >= until {
			continue
		}
		rep.Downtime = append(rep.Downtime, Downtime{
			Account: maskID(s.account),
			From:    time.UnixMilli(s.from).In(time.Local).Format("2006-01-02 15:04"),
			To:      time.UnixMilli(s.to).In(time.Local).Format("2006-01-02 15:04"),
			Minutes: int(math.Round(float64(s.to-s.from) / float64(time.Minute/time.Millisecond))),
		})
	}

	// 按群攒：一个群一份
	accs := map[string]*groupAcc{}
	for _, d := range ds {
		acc := accs[d.conversation]
		if acc == nil {
			acc = &groupAcc{hours: map[int64]hourCell{}}
			accs[d.conversation] = acc
		}
		if inDowntime(d.at, segs) {
			acc.stat.Excluded++
			continue
		}
		acc.stat.Verdicts.add(d.verdict)
		switch d.verdict {
		case channel.VerdictForced:
			acc.stat.Release.Forced.add(d.outcome)
		case channel.VerdictEnter:
			acc.stat.Release.Enter.add(d.outcome)
		}
		// 每个小时最后一条带 target 的判断说了几次、该说几次（replay-gate 的"①"：后面的盖掉前面的）
		if d.target != nil {
			acc.hours[hourStart(d.at)] = hourCell{target: *d.target, actual: float64(d.actual)}
		}
	}

	keys := make([]string, 0, len(accs))
	for conv := range accs {
		keys = append(keys, conv)
	}
	sort.Strings(keys)
	for _, conv := range keys {
		acc := accs[conv]
		st := acc.finish(conv)
		rep.Groups = append(rep.Groups, st)
		rep.Total.add(st)
	}
	return rep, nil
}

// groupAcc 是一个群攒到一半的统计
type groupAcc struct {
	stat  GroupStat
	hours map[int64]hourCell // 本地小时 → 那个小时最后一条带 target 的判断
}

// hourCell 是一个小时里最后一条带 target 的判断的 actual 和 target
type hourCell struct {
	target float64
	actual float64
}

// finish 把攒的算完：比例、小时
func (a *groupAcc) finish(conv string) GroupStat {
	st := a.stat
	st.Label = groupLabel(conv)
	st.Judged = st.Verdicts.judged()
	st.Release.Judged = st.Verdicts.releasable()
	st.Release.Released = st.Verdicts.Forced + st.Verdicts.Enter
	st.Release.Ratio = ratio(st.Release.Released, st.Release.Judged)
	st.Release.All = st.Release.Forced.plus(st.Release.Enter)
	for _, c := range a.hours {
		if c.target < 1 {
			continue // 只有 target >= 1 的小时才算（replay-gate 一样）
		}
		st.Hours.Hours++
		st.Hours.Actual += c.actual
		st.Hours.Target += c.target
		if c.target > 0 && c.actual > 1.5*c.target {
			st.Hours.Over++
		}
		if c.target > 0 && c.actual < 0.5*c.target {
			st.Hours.Under++
		}
	}
	st.Hours.Ratio = ratioF(st.Hours.Actual, st.Hours.Target)
	return st
}

// add 把一个群的数字合进总计
func (st *GroupStat) add(g GroupStat) {
	y := &st.Verdicts
	x := g.Verdicts
	y.Forced += x.Forced
	y.Enter += x.Enter
	y.Skip += x.Skip
	y.Backoff += x.Backoff
	y.Fuse += x.Fuse
	y.Muted += x.Muted
	y.Merged += x.Merged
	y.Off += x.Off
	y.Taken += x.Taken
	y.Paused += x.Paused
	y.Unknown += x.Unknown
	st.Judged += g.Judged
	st.Excluded += g.Excluded
	st.Release.Judged += g.Release.Judged
	st.Release.Released += g.Release.Released
	st.Release.Forced = st.Release.Forced.plus(g.Release.Forced)
	st.Release.Enter = st.Release.Enter.plus(g.Release.Enter)
	st.Release.All = st.Release.All.plus(g.Release.All)
	st.Release.Ratio = ratio(st.Release.Released, st.Release.Judged)
	st.Hours.Hours += g.Hours.Hours
	st.Hours.Actual += g.Hours.Actual
	st.Hours.Target += g.Hours.Target
	st.Hours.Over += g.Hours.Over
	st.Hours.Under += g.Hours.Under
	st.Hours.Ratio = ratioF(st.Hours.Actual, st.Hours.Target)
}

// add 记一次判断的 verdict
func (v *VerdictCounts) add(verdict string) {
	switch verdict {
	case channel.VerdictForced:
		v.Forced++
	case channel.VerdictEnter:
		v.Enter++
	case channel.VerdictSkip:
		v.Skip++
	case channel.VerdictBackoff:
		v.Backoff++
	case channel.VerdictFuse:
		v.Fuse++
	case channel.VerdictMuted:
		v.Muted++
	case channel.VerdictMerged:
		v.Merged++
	case channel.VerdictOff:
		v.Off++
	case channel.VerdictTaken:
		v.Taken++
	case channel.VerdictPaused:
		v.Paused++
	default:
		v.Unknown++
	}
}

// judged 是"判了"的条数：除了只记录、被拦链接、暂停，别的都算（含合进一串）
func (v VerdictCounts) judged() int { return v.releasable() + v.Merged }

// releasable 是放行比例的分母：合进一串的不算（它没有独立的判断结果）
func (v VerdictCounts) releasable() int {
	return v.Forced + v.Enter + v.Skip + v.Backoff + v.Fuse + v.Muted
}

// add 记一条放行的判断的结局
func (o *OutcomeStat) add(outcome string) {
	o.Released++
	switch outcome {
	case "":
		o.Empty++
	case store.GateOutcomeSpoke:
		o.Spoke++
	case store.GateOutcomeDry:
		o.Dry++
	case store.GateOutcomeSelfSkip:
		o.SelfSkip++
	case store.GateOutcomeBlocked:
		o.Blocked++
	case store.GateOutcomeFuse:
		o.Fuse++
	case store.GateOutcomeInterrupted:
		o.Interrupted++
	case store.GateOutcomeError:
		o.Error++
	default:
		o.Other++
	}
	if outcome != "" {
		o.WithOutcome++
	}
}

// Opened 是"真开口"的条数：spoke 和 dry（试运行计划里写了也算）
func (o OutcomeStat) Opened() int { return o.Spoke + o.Dry }

// plus 合起来：被叫到的和过门槛的相加
func (o OutcomeStat) plus(p OutcomeStat) OutcomeStat {
	return OutcomeStat{
		Released: o.Released + p.Released, WithOutcome: o.WithOutcome + p.WithOutcome,
		Spoke: o.Spoke + p.Spoke, Dry: o.Dry + p.Dry, Empty: o.Empty + p.Empty,
		SelfSkip: o.SelfSkip + p.SelfSkip, Blocked: o.Blocked + p.Blocked, Fuse: o.Fuse + p.Fuse,
		Interrupted: o.Interrupted + p.Interrupted, Error: o.Error + p.Error, Other: o.Other + p.Other,
	}
}

// sqliteURI 把文件路径变成 SQLite 的 file: 地址（% # ? 必须转义，照 store.uriPath：目录名带这些字符时，
// 不转义会把后面的查询串一起吃掉，只读就只剩 mode=ro 那一句在挡）
func sqliteURI(file string) string {
	return "file:" + strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
}

// readOnlyDSN 是只读打开的地址：mode=ro，再加一道 query_only 保险（两道都和 store.dsn 一样，那边是包内私有的，这个工具不借它的写入口）
func readOnlyDSN(file string) string {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	return sqliteURI(file) + "?" + q.Encode()
}

// openReadOnly 打开一个已有的库：不跑迁移、不改主库；WAL 库旁边的 -shm（SQLite 自己的共享内存）可能被它更新。
// name 是报错时说的库名（"go/trace.db" 这种），file 的路径不进错误文字
func openReadOnly(file, name string) (*sql.DB, error) {
	if _, err := os.Stat(file); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("没有 %s（还没跑过试运行？）", name)
		}
		return nil, fmt.Errorf("%s 打不开：%s", name, hidePaths(err.Error(), file, filepath.Dir(file)))
	}
	db, err := sql.Open("sqlite", readOnlyDSN(file))
	if err != nil {
		return nil, fmt.Errorf("打开 %s：%s", name, hidePaths(err.Error(), file, sqliteURI(file)))
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 %s：%s", name, hidePaths(err.Error(), file, sqliteURI(file), filepath.Dir(file)))
	}
	return db, nil
}

// hidePaths 把错误文字里出现的路径换成固定的说法：报错只说到是哪个库，不打完整路径
// （路径里可能带号、名字，会绕过打码）
func hidePaths(s string, paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		s = strings.ReplaceAll(s, p, "（数据目录）")
		s = strings.ReplaceAll(s, filepath.ToSlash(p), "（数据目录）")
	}
	return s
}

// absPath 是 dir 的绝对路径（算不出来是空串）：paths.Open 报错里用的是它，脱敏时得连它一起换掉
func absPath(dir string) string {
	a, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return a
}

// loadDecisions 读窗口 [from, until) 里的判断，按 at、id 升序（同一毫秒按写的先后）。
// 半开：截止那一毫秒不算，和掉线段的"开始算、恢复不算"是同一个口径
func loadDecisions(db *sql.DB, from, until int64) ([]decision, error) {
	rows, err := db.Query(`SELECT at, conversation, verdict, target, actual, COALESCE(outcome, '')
		FROM gate_decisions WHERE at >= ? AND at < ? ORDER BY at, id`, from, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []decision
	for rows.Next() {
		var d decision
		var target sql.NullFloat64
		var actual sql.NullInt64
		if err := rows.Scan(&d.at, &d.conversation, &d.verdict, &target, &actual, &d.outcome); err != nil {
			return nil, err
		}
		if target.Valid {
			v := target.Float64
			d.target = &v
		}
		if actual.Valid {
			d.actual = int(actual.Int64)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// loadDowntime 合并提醒与连接日志；提醒状态不作为连接生命周期的唯一来源。
// account 非空只输出这个号，但账号掩码碰撞要对所有记录核对，不能因筛选而假装唯一。
func loadDowntime(db *sql.DB, until int64, account, logsDir string) ([]segment, error) {
	q := `SELECT at, kind, account FROM qq_alerts WHERE at < ? ORDER BY at, id`
	rows, err := db.Query(q, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := map[string]string{}
	var events []connectionEvent
	for rows.Next() {
		var at int64
		var kind, acct string
		if err := rows.Scan(&at, &kind, &acct); err != nil {
			return nil, err
		}
		label := config.MaskID(acct)
		if old, ok := accounts[label]; ok && old != acct {
			accounts[label] = ""
		} else if !ok {
			accounts[label] = acct
		}
		if kind == "down" || kind == "up" {
			events = append(events, connectionEvent{at: at, kind: kind, account: acct})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	logged, covered, err := logConnections(logsDir, until, accounts)
	if err != nil {
		return nil, fmt.Errorf("读 QQ 连接日志失败：%w", err)
	}
	if len(logged) != 0 {
		// 提醒只补日志未覆盖的区间；不能拿异步入库时刻推翻直接连接事实。
		sort.Slice(covered, func(i, j int) bool { return covered[i].from < covered[j].from })
		through := map[string]int64{}
		next := 0
		kept := events[:0]
		for _, e := range events {
			for next < len(covered) && covered[next].from <= e.at {
				span := covered[next]
				through[span.account] = max(through[span.account], span.to)
				next++
			}
			if e.at >= through[e.account] {
				kept = append(kept, e)
			}
		}
		events = append(kept, logged...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at < events[j].at })
	open := map[string]int64{}
	var segs []segment
	for _, e := range events {
		if account != "" && e.account != account {
			continue
		}
		at, kind, acct := e.at, e.kind, e.account
		switch kind {
		case "down":
			// 掉线期间又来一条 down（重连失败）：还算从第一条开始
			if _, ok := open[acct]; !ok {
				open[acct] = at
			}
		case "up":
			if s, ok := open[acct]; ok {
				segs = append(segs, segment{account: acct, from: s, to: at})
				delete(open, acct)
			}
		}
	}
	for acct, s := range open {
		segs = append(segs, segment{account: acct, from: s, to: until})
	}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].from != segs[j].from {
			return segs[i].from < segs[j].from
		}
		return segs[i].account < segs[j].account
	})
	return segs, nil
}

// inDowntime 说这一条判断落在没接上的那段时间里（开始那一毫秒算，恢复那一毫秒不算）
func inDowntime(at int64, segs []segment) bool {
	for _, s := range segs {
		if at >= s.from && at < s.to {
			return true
		}
	}
	return false
}

// hourStart 是这个时刻所在整点的开始（毫秒）：对着绝对时间取整，不靠本地的年月日小时重建
// （夏令时回拨那天两个不同的本地 01 点会重建成同一个键，后一条把前一条盖掉）
func hourStart(at int64) int64 {
	return at - at%int64(time.Hour/time.Millisecond)
}

// maskID 把号码打码，只留后两位（规格 第七节：只打数字和群的编号）。
// 两位以内的没法留后两位又不露全，整串打码
func maskID(id string) string {
	r := []rune(id)
	if len(r) <= 2 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-2) + string(r[len(r)-2:])
}

// groupLabel 把会话串（"qq:group:300"）变成能打出来的标签："qq 群 *00"。认不出群形式的不拼群号
func groupLabel(conv string) string {
	parts := strings.Split(conv, ":")
	switch {
	case len(parts) == 3 && parts[1] == "group":
		return parts[0] + " 群 " + maskID(parts[2])
	case len(parts) >= 2:
		return parts[0] + " 会话 " + maskID(parts[len(parts)-1])
	}
	return maskID(conv)
}

// ratio 是整数比例；分母是 0 给 0
func ratio(n, d int) float64 { return ratioF(float64(n), float64(d)) }

func ratioF(n, d float64) float64 {
	if d == 0 {
		return 0
	}
	return n / d
}

// pct 打"a/b = x%"。sample 是这一项的样本条数，不到 20 跟着标"样本不够"；
// 它不一定是分母：开口比例的样本是放行条数（附件：开口比例看放行数），分母是有结局的条数
func pct(n, d, sample int) string {
	if d == 0 {
		return "0/0 = —（没有样本）"
	}
	s := fmt.Sprintf("%d/%d = %.1f%%", n, d, 100*float64(n)/float64(d))
	if sample < minSample {
		s += "（样本不够）"
	}
	return s
}

// emptyNote 是"没结局"那一截：不是 0 就显眼地报（放行的判断都应该有结局，没有就是有漏洞）
func emptyNote(n int) string {
	if n > 0 {
		return fmt.Sprintf("没结局 %d ← 有漏洞，放行的判断缺结局", n)
	}
	return "没结局 0"
}

// writeText 是纯文本输出：一个群一段，最后一行总计
func writeText(w io.Writer, r *Report) {
	basis := r.Basis
	if basis == "" {
		basis = fmt.Sprintf("最近 %d 天", r.Days)
	}
	fmt.Fprintf(w, "统计 %s 到 %s（本地时间，%s）\n", r.From, r.Until, basis)
	if len(r.Downtime) == 0 {
		fmt.Fprintln(w, "掉线时段：没有")
	} else {
		fmt.Fprintf(w, "掉线时段 %d 段（这段时间收不全，里面的判断不算进下面的比例）：\n", len(r.Downtime))
		for _, d := range r.Downtime {
			fmt.Fprintf(w, "  %s 号：%s 到 %s（%d 分钟）\n", d.Account, d.From, d.To, d.Minutes)
		}
	}
	for _, g := range r.Groups {
		fmt.Fprintf(w, "\n%s：\n", g.Label)
		writeGroupBody(w, g)
	}
	if len(r.Groups) == 0 {
		fmt.Fprintln(w, "\n窗口里一条判断也没有：还没开始试运行？")
		return
	}
	fmt.Fprintf(w, "\n总计（%d 个群合起来）：\n", len(r.Groups))
	writeGroupBody(w, r.Total)
}

// writeGroupBody 是一个群（或总计）的那几行
func writeGroupBody(w io.Writer, g GroupStat) {
	v := g.Verdicts
	line := fmt.Sprintf("  判断 %d 条（不含掉线时段）：被叫到 %d、过门槛 %d、没接 %d、退避 %d、保险丝 %d、频率 0 %d、合进一串 %d；另列（不算判了）：只记录 %d、被拦 %d、暂停 %d",
		g.Judged, v.Forced, v.Enter, v.Skip, v.Backoff, v.Fuse, v.Muted, v.Merged, v.Off, v.Taken, v.Paused)
	if v.Unknown > 0 {
		line += fmt.Sprintf("、不认识的 %d", v.Unknown)
	}
	fmt.Fprintln(w, line)
	if g.Excluded > 0 {
		fmt.Fprintf(w, "  掉线时段里有 %d 条判断，已排除\n", g.Excluded)
	}
	fmt.Fprintf(w, "  放行比例（被叫到 + 过门槛 ÷ 判了的）%s\n", pct(g.Release.Released, g.Release.Judged, g.Release.Judged))
	fmt.Fprintln(w, "  放行以后真开口（开口 = 结局是 spoke 或 dry；分母是放行里有结局的条数）：")
	writeOutcome(w, "被叫到", g.Release.Forced)
	writeOutcome(w, "过门槛", g.Release.Enter)
	writeOutcome(w, "合起来", g.Release.All)
	if g.Hours.Hours == 0 {
		fmt.Fprintln(w, "  合起来说了几句 / 该说几句：没有 target ≥ 1 的小时，算不了")
		return
	}
	fmt.Fprintf(w, "  合起来说了几句 / 该说几句：%d 个 target ≥ 1 的小时，说了 %.0f 次 / 该说 %.1f 次（%.2f 倍）；1.5 倍以上的小时 %d/%d（%.1f%%），不到一半的 %d/%d（%.1f%%）\n",
		g.Hours.Hours, g.Hours.Actual, g.Hours.Target, g.Hours.Ratio,
		g.Hours.Over, g.Hours.Hours, 100*ratio(g.Hours.Over, g.Hours.Hours),
		g.Hours.Under, g.Hours.Hours, 100*ratio(g.Hours.Under, g.Hours.Hours))
}

// writeOutcome 是被叫到 / 过门槛 / 合起来各一行
func writeOutcome(w io.Writer, label string, o OutcomeStat) {
	fmt.Fprintf(w, "    %s：放行 %d 条，有结局 %d 条；开口 %s；%s；没开口 %d、被拦 %d、被打断 %d、出错 %d、保险丝 %d",
		label, o.Released, o.WithOutcome, pct(o.Opened(), o.WithOutcome, o.Released), emptyNote(o.Empty),
		o.SelfSkip, o.Blocked, o.Interrupted, o.Error, o.Fuse)
	if o.Other > 0 {
		fmt.Fprintf(w, "、不认识的 %d", o.Other)
	}
	fmt.Fprintln(w)
}
