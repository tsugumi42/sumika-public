package snowluma

// 真实帧回放（规格-渠道中间件 第十五节第 6 行）。
//
// 录帧文件里是真实的聊天，所以这里只输出统计：帧种类、翻成的事件种类、个数，认不出的有多少。
// 断言失败时只说第几行、什么种类、哪一步失败，不带帧里任何内容；类型名也先过白名单
// （只留 `[a-z0-9_]{1,40}`），协议端给了奇怪的值也只记"其他"。

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// TestRealFramesReplay 拿录帧工具（native/cmd/snowluma-record）写的真实帧逐行回放翻译。
// 每行是 {"at": 本地收到时刻, "frame": 原帧}；只设了 SNOWLUMA_FRAMES（文件路径）才跑，
// 平时的 go test 和 CI 都跳过（真实数据要主人点头，每样单独问；结果由主会话自己跑）。
func TestRealFramesReplay(t *testing.T) {
	path := os.Getenv("SNOWLUMA_FRAMES")
	if path == "" {
		t.Skip("没设 SNOWLUMA_FRAMES：真实帧回放只在主会话自己跑时打开")
	}
	lines, err := readFrameLines(path)
	if err != nil {
		t.Fatalf("读录帧文件失败: %v", err)
	}
	if len(lines) == 0 {
		t.Fatalf("录帧文件里一帧都没有")
	}
	recs := replayFrames(lines)
	reportProblems(t, recs)
	// 另设了 SNOWLUMA_DUMP：导出每条消息翻成什么，给 scripts/compare-snowluma-bun.ts 和 Bun 的翻译逐段对照
	if out := os.Getenv("SNOWLUMA_DUMP"); out != "" {
		if err := dumpForBun(out, recs); err != nil {
			t.Fatalf("导出失败: %v", err)
		}
	}
	rep := summarize(recs)
	logReport(t, rep)
	if rep.Events == 0 {
		t.Fatalf("帧 %d 条，一个事件都没翻出来", rep.Frames)
	}
}

// dumpKey 是导出里内容的样子：空就是空串，不空取 sha256 的前 16 位，只用来比是不是同一个，不写出原文。
// 和 scripts/compare-snowluma-bun.ts 的 hk 是同一个算法
func dumpKey(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// dumpForBun 把每条消息翻成的种类、会话、发言人和每一段写成 JSONL（行号和录帧文件对得上）。
// 只写种类和哈希，不写原文（帧里是真实聊天）；导出文件由调用方放在不进 git 的地方
func dumpForBun(path string, recs []frameRecord) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("建不了导出文件")
	}
	defer f.Close()
	type seg struct {
		K     string `json:"k"`
		H     string `json:"h"`
		Empty bool   `json:"empty,omitempty"`
	}
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if r.In == nil {
			continue
		}
		segs := make([]seg, 0, len(r.In.Segs))
		for _, s := range r.In.Segs {
			v := ""
			switch s.Kind {
			case types.SegText:
				v = s.Text
			case types.SegAt, types.SegReply:
				v = s.Target
			case types.SegFace, types.SegSuperface, types.SegEmoji, types.SegForward:
				v = s.Ref
			}
			segs = append(segs, seg{K: s.Kind, H: dumpKey(v), Empty: s.Kind == types.SegText && s.Text == ""})
		}
		row := map[string]any{"line": r.Line, "kind": r.In.Kind, "conv": dumpKey(r.In.Conv.String()), "from": dumpKey(r.In.From.ID), "segs": segs}
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("写导出文件中断")
		}
	}
	return f.Close()
}

// frameLine 是一行录帧：Line 是文件里的行号（报错用），Frame 是原帧。
type frameLine struct {
	Line  int
	Frame []byte
}

// readFrameLines 读出整个录帧文件。报错只说第几行，不带行里的内容（行里是真实聊天）。
func readFrameLines(path string) ([]frameLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打不开")
	}
	defer f.Close()
	var out []frameLine
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var rec struct {
			Frame json.RawMessage `json:"frame"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			return nil, fmt.Errorf("第 %d 行不是有效的 JSONL", n)
		}
		frame, err := rawFrame(rec.Frame)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行没有可回放的帧", n)
		}
		out = append(out, frameLine{Line: n, Frame: frame})
	}
	if s.Err() != nil {
		return nil, fmt.Errorf("读文件中断")
	}
	return out, nil
}

// rawFrame 取出一行里的原帧。录帧工具原样嵌 JSON；原帧不是 JSON 时按字符串存（cmd/snowluma-record 的 line）。
func rawFrame(raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("空")
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, fmt.Errorf("字符串读不出")
		}
		return []byte(s), nil
	}
	return raw, nil
}

// frameRecord 是一帧回放的结果。Post、Kind、SegTypes 只留协议里的类型名（过白名单），不带帧里的内容。
type frameRecord struct {
	Line     int
	Post     string   // post_type
	Kind     string   // 帧种类：message/private、notice/group_recall、meta_event/heartbeat……
	SegTypes []string // 帧里 message 数组的 type，按顺序
	SegsOK   bool     // message 是数组，能逐个对上
	In       *types.Inbound
	Notice   *types.Notice
	Status   *types.Status
	Step     string // 翻译失败在哪一步：json、time、segments；空表示翻成了
	Panic    bool
}

var typeNameRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// typeName 只放行协议里的类型名（小写字母、数字、下划线）；别的值只记"其他"，不把帧里的字样带出去。
func typeName(s string) string {
	if typeNameRe.MatchString(s) {
		return s
	}
	return "其他"
}

// describeFrame 取出帧的种类和进来的段类型；只解析，不翻译。
func describeFrame(frame []byte) (post, kind string, segTypes []string, segsOK bool) {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return "", "认不出的帧", nil, false
	}
	switch raw := postType(o); raw {
	case "message", "message_sent":
		post = raw
		kind = raw + "/" + typeName(str(o, "message_type"))
	case "notice":
		post = raw
		kind = "notice/" + typeName(str(o, "notice_type"))
		if str(o, "notice_type") == "notify" && str(o, "sub_type") != "" {
			kind += "/" + typeName(str(o, "sub_type"))
		}
	case "meta_event":
		post = raw
		kind = "meta_event/" + typeName(str(o, "meta_event_type"))
		if str(o, "meta_event_type") == "lifecycle" {
			kind += "/" + typeName(str(o, "sub_type"))
		}
	case "":
		return "", "认不出的帧", nil, false
	default:
		// 认不出的 post_type：类型名本身也不往外带（帧里可能是聊天内容）
		post = "其他"
		kind = "其他"
	}
	var arr []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(o["message"], &arr) == nil {
		segTypes = make([]string, 0, len(arr))
		for _, x := range arr {
			segTypes = append(segTypes, typeName(x.Type))
		}
		segsOK = true
	}
	return post, kind, segTypes, segsOK
}

// replayFrames 逐行交给适配器现有的翻译入口（dispatch 用的就是 translate），顺便记下种类。
func replayFrames(lines []frameLine) []frameRecord {
	out := make([]frameRecord, 0, len(lines))
	for _, l := range lines {
		out = append(out, translateFrame(l.Line, l.Frame))
	}
	return out
}

// translateFrame 翻译一帧；翻译时 panic 也只坏这一帧（真连的时候 dispatch 会带走整条连接，回放里要全跑完再报）。
func translateFrame(line int, frame []byte) (r frameRecord) {
	r.Line = line
	r.Post, r.Kind, r.SegTypes, r.SegsOK = describeFrame(frame)
	defer func() {
		if v := recover(); v != nil {
			r.Panic = true
			r.Step = ""
			r.In, r.Notice, r.Status = nil, nil, nil
		}
	}()
	in, notice, status, err := translate(frame)
	r.In, r.Notice, r.Status = in, notice, status
	if err != nil {
		r.Step = stepOf(err)
	}
	return r
}

// stepOf 把翻译错误的分类说出来。不光用 err.Error()：JSON 的语法错误会带一个原文里的字符，不能打出去。
func stepOf(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "无效 JSON"):
		return "json"
	case strings.HasPrefix(msg, "无效消息段"):
		return "segments"
	}
	return "翻译"
}

func (r frameRecord) events() int {
	n := 0
	if r.In != nil {
		n++
	}
	if r.Notice != nil {
		n++
	}
	if r.Status != nil {
		n++
	}
	return n
}

// eventKind 是这一帧翻成的事件种类；没翻出事件是"没翻出事件"。
func (r frameRecord) eventKind() string {
	switch {
	case r.In != nil:
		return "inbound/" + r.In.Kind
	case r.Notice != nil:
		return "notice/" + r.Notice.Kind
	case r.Status != nil:
		return "status/" + r.Status.Kind
	}
	return "没翻出事件"
}

// problems 是这一帧该报的问题。只说哪一步、几个段，不带帧里的内容。
func (r frameRecord) problems() []string {
	if r.Panic {
		return []string{"翻译时 panic"}
	}
	if r.Step != "" {
		return []string{"翻译失败（" + r.Step + "）"}
	}
	if r.Post == "" {
		return []string{"没有 post_type，不像 OneBot 帧"}
	}
	var out []string
	// 被踢下线是一条通知加一条状态，别的帧都只翻出一个事件
	kicked := r.Notice != nil && r.Notice.Kind == types.NoticeKicked && r.Status != nil && r.Status.Kind == types.StatusKicked
	if n := r.events(); n > 1 && !(n == 2 && kicked) {
		out = append(out, fmt.Sprintf("一帧翻出了 %d 个事件", n))
	}
	switch r.Post {
	case "message", "message_sent":
		if r.In == nil {
			out = append(out, "消息帧没有翻成消息事件")
			break
		}
		if r.SegsOK && len(r.SegTypes) != len(r.In.Segs) {
			out = append(out, fmt.Sprintf("段数不对：进来 %d 段，出去 %d 段", len(r.SegTypes), len(r.In.Segs)))
		}
	case "notice":
		if r.Notice == nil {
			out = append(out, "通知帧没有翻成通知")
		}
	case "meta_event":
		// 心跳、连上（lifecycle 的 connect、enable）要翻成状态；lifecycle 的 disable 和别的元事件不要求
		if (r.Kind == "meta_event/heartbeat" || r.Kind == "meta_event/lifecycle/connect" || r.Kind == "meta_event/lifecycle/enable") && r.Status == nil {
			out = append(out, "元事件没有翻成状态")
		}
	}
	return out
}

// reportProblems 把每帧的问题报出来：第几行、什么种类、哪一步。
func reportProblems(t *testing.T, recs []frameRecord) {
	t.Helper()
	for _, r := range recs {
		for _, p := range r.problems() {
			t.Errorf("第 %d 行（%s）：%s", r.Line, r.Kind, p)
		}
	}
}

// replayReport 是统计表：帧种类 → 事件种类、段类型 → 段种类，以及认不出的各多少。
type replayReport struct {
	Frames       int
	Events       int
	Kinds        map[string]map[string]int // 帧种类 → 事件种类 → 个数
	Segs         map[string]map[string]int // 帧里的段类型 → 翻成的段种类 → 个数
	SegTotal     int
	UnknownSegs  map[string]int // 翻成 other 的段：帧里的类型 → 个数
	OtherNotices map[string]int // NoticeOther：帧种类 → 个数
}

func summarize(recs []frameRecord) replayReport {
	rep := replayReport{
		Kinds:        map[string]map[string]int{},
		Segs:         map[string]map[string]int{},
		UnknownSegs:  map[string]int{},
		OtherNotices: map[string]int{},
	}
	for _, r := range recs {
		rep.Frames++
		if rep.Kinds[r.Kind] == nil {
			rep.Kinds[r.Kind] = map[string]int{}
		}
		rep.Kinds[r.Kind][r.eventKind()]++
		if r.events() > 0 {
			rep.Events++
		}
		if r.Notice != nil && r.Notice.Kind == types.NoticeOther {
			rep.OtherNotices[r.Kind]++
		}
		// 段按位置对：进来几段、出去几段，逐段记 "类型 → 种类"。对不齐的已经在 problems 里报过
		if r.In == nil || !r.SegsOK || len(r.SegTypes) != len(r.In.Segs) {
			continue
		}
		for i, st := range r.SegTypes {
			sk := r.In.Segs[i].Kind
			if rep.Segs[st] == nil {
				rep.Segs[st] = map[string]int{}
			}
			rep.Segs[st][sk]++
			rep.SegTotal++
			if sk == types.SegOther {
				rep.UnknownSegs[st]++
			}
		}
	}
	return rep
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// logReport 打统计表。只有数字和协议里的类型名。
func logReport(t *testing.T, rep replayReport) {
	t.Helper()
	t.Logf("帧 %d 条，翻出事件 %d 个，没翻出事件的帧 %d 条", rep.Frames, rep.Events, rep.Frames-rep.Events)
	t.Logf("帧种类 → 事件种类 → 个数：")
	for _, k := range sortedKeys(rep.Kinds) {
		for _, e := range sortedKeys(rep.Kinds[k]) {
			t.Logf("  %s → %s：%d", k, e, rep.Kinds[k][e])
		}
	}
	t.Logf("段类型 → 段种类 → 个数（共 %d 段）：", rep.SegTotal)
	for _, st := range sortedKeys(rep.Segs) {
		for _, sk := range sortedKeys(rep.Segs[st]) {
			t.Logf("  %s → %s：%d", st, sk, rep.Segs[st][sk])
		}
	}
	t.Logf("认不出的段（翻成 %s）：%d", types.SegOther, sum(rep.UnknownSegs))
	for _, st := range sortedKeys(rep.UnknownSegs) {
		t.Logf("  %s：%d", st, rep.UnknownSegs[st])
	}
	t.Logf("认不出的通知（%s）：%d", types.NoticeOther, sum(rep.OtherNotices))
	for _, k := range sortedKeys(rep.OtherNotices) {
		t.Logf("  %s：%d", k, rep.OtherNotices[k])
	}
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// TestReplayCounting 用编的帧证明回放的统计和断言本身是对的：
// 数得对；少一段、没翻成、翻译出错、panic 都要报出来。
func TestReplayCounting(t *testing.T) {
	lines := []frameLine{
		{1, []byte(`{"post_type":"message","message_type":"group","time":100,"self_id":100,"user_id":200,"group_id":400,"message_id":300,"sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":{"text":"虚构乙"}},{"type":"at","data":{"qq":"100"}},{"type":"music","data":{"id":"x"}}]}`)},
		{2, []byte(`{"post_type":"notice","notice_type":"group_recall","time":100,"self_id":100,"user_id":200,"group_id":400,"message_id":300}`)},
		{3, []byte(`{"post_type":"notice","notice_type":"group_msg_emoji_like","time":100,"self_id":100,"user_id":200,"group_id":400,"message_id":300}`)},
		{4, []byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","time":100,"self_id":100,"interval":5000}`)},
		{5, []byte(`{"post_type":"meta_event","meta_event_type":"lifecycle","time":100,"self_id":100,"sub_type":"connect"}`)},
	}
	recs := replayFrames(lines)
	reportProblems(t, recs)

	rep := summarize(recs)
	if rep.Frames != 5 || rep.Events != 5 || rep.SegTotal != 3 {
		t.Fatalf("帧 %d 条、事件 %d 个、段 %d 个，要 5、5、3", rep.Frames, rep.Events, rep.SegTotal)
	}
	want := []struct{ kind, event string }{
		{"message/group", "inbound/message"},
		{"notice/group_recall", "notice/recall"},
		{"notice/group_msg_emoji_like", "notice/other"},
		{"meta_event/heartbeat", "status/heartbeat"},
		{"meta_event/lifecycle/connect", "status/connected"},
	}
	for _, w := range want {
		if got := rep.Kinds[w.kind][w.event]; got != 1 {
			t.Fatalf("%s → %s 记了 %d 个，要 1 个（整张表 %v）", w.kind, w.event, got, rep.Kinds)
		}
	}
	segWant := []struct{ in, out string }{
		{"text", types.SegText},
		{"at", types.SegAt},
		{"music", types.SegOther},
	}
	for _, w := range segWant {
		if got := rep.Segs[w.in][w.out]; got != 1 {
			t.Fatalf("段 %s → %s 记了 %d 个，要 1 个（整张表 %v）", w.in, w.out, got, rep.Segs)
		}
	}
	if rep.UnknownSegs["music"] != 1 || rep.OtherNotices["notice/group_msg_emoji_like"] != 1 {
		t.Fatalf("认不出的段 %v、认不出的通知 %v，各要 1 个", rep.UnknownSegs, rep.OtherNotices)
	}

	// 少一段：翻译结果砍掉一段，断言要红
	short := recs[0]
	in := *recs[0].In
	in.Segs = in.Segs[:2]
	short.In = &in
	if p := short.problems(); len(p) != 1 || !strings.Contains(p[0], "段数不对") {
		t.Fatalf("少一段的问题 = %q，要一条段数不对", p)
	}

	// 没翻成消息事件
	missing := recs[0]
	missing.In = nil
	if p := missing.problems(); len(p) != 1 || p[0] != "消息帧没有翻成消息事件" {
		t.Fatalf("没翻成消息事件的问题 = %q", p)
	}

	// 翻译出错：只报哪一步
	failed := recs[0]
	failed.In = nil
	failed.Step = "json"
	if p := failed.problems(); len(p) != 1 || p[0] != "翻译失败（json）" {
		t.Fatalf("翻译出错的问题 = %q", p)
	}

	// panic
	panicked := recs[0]
	panicked.Panic = true
	if p := panicked.problems(); len(p) != 1 || p[0] != "翻译时 panic" {
		t.Fatalf("panic 的问题 = %q", p)
	}

	// 不是 JSON 的帧：报在哪一步，不带帧里的字样
	if p := translateFrame(9, []byte(`{"这里不是 JSON`)).problems(); len(p) != 1 || p[0] != "翻译失败（json）" {
		t.Fatalf("坏帧的问题 = %q", p)
	}
	// 被踢下线：一条通知加一条状态，不算"一帧翻出了几个事件"
	if p := translateFrame(12, []byte(`{"post_type":"notice","notice_type":"bot_offline","time":100,"self_id":100,"message":"编的原因"}`)).problems(); len(p) != 0 {
		t.Fatalf("被踢的帧不该报问题，报了 %q", p)
	}
	// 没有 post_type 的帧
	if p := translateFrame(10, []byte(`{"time":100}`)).problems(); len(p) != 1 || !strings.Contains(p[0], "post_type") {
		t.Fatalf("没 post_type 的问题 = %q", p)
	}
	// 认不出的 post_type：只记"其他"，帧里的字样不出现在种类和问题里
	weird := translateFrame(11, []byte(`{"post_type":"消息<真实内容>","time":100}`))
	if weird.Kind != "其他" || strings.Contains(weird.Kind, "真实内容") {
		t.Fatalf("怪 post_type 的种类 = %q", weird.Kind)
	}
	if p := weird.problems(); len(p) != 0 {
		t.Fatalf("怪 post_type 不该报问题，报了 %q", p)
	}
	if got := typeName("消息<真实内容>"); got != "其他" {
		t.Fatalf("typeName 放行了怪类型名：%q", got)
	}
}
