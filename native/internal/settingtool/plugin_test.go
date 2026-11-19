package settingtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// fakeHost 是内存里的宿主：一个键 city（文字）、一个键 n（整数），所在地 location 走候选
type fakeHost struct {
	mu        sync.Mutex
	vals      map[string]json.RawMessage
	version   int
	now       time.Time
	seq       int
	authorize error
	ask       func(c Card) (Decision, error)
	asked     []Card
	writes    int
	writeErr  error
	beforeAsk func() // 卡片等待期间模拟别处改设置
	places    []Candidate
	rawText   bool // Display 按真实宿主的写法，文字值原样放进去（带换行也不转义）
	placesErr error
}

func newHost() *fakeHost {
	return &fakeHost{vals: map[string]json.RawMessage{"city": json.RawMessage(`"旧值"`), "n": json.RawMessage(`3`), "location": json.RawMessage(`{}`)},
		now: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)}
}

func (h *fakeHost) Visible(aud audience.Audience) bool { return aud.PrivateOK }
func (h *fakeHost) Authorize(_ context.Context, in kernel.Invocation, _ audience.Audience) (Who, error) {
	if h.authorize != nil {
		return Who{}, h.authorize
	}
	return Who{Character: in.Scope.CharacterID, Conversation: in.Scope.Conversation, Place: "本地私聊"}, nil
}

var fakeSpecs = map[string]Spec{
	"city":     {Key: "city", Name: "城市名", Kind: "text", Level: LevelEvery, Impact: "只影响测试", Effect: "立即"},
	"n":        {Key: "n", Name: "数量", Kind: "int", Level: LevelEvery, Impact: "只影响测试", Effect: "立即"},
	"flag":     {Key: "flag", Name: "开关", Kind: "bool", Level: LevelEvery, Impact: "只影响测试", Effect: "立即"},
	"location": {Key: "location", Name: "所在地", Kind: "location", Level: LevelEvery, Impact: "测试", Effect: "立即"},
	"odd":      {Key: "odd", Name: "怪档位", Kind: "text", Level: "silent", Impact: "测试", Effect: "立即"},
}

func (h *fakeHost) Spec(k string) (Spec, bool) { s, ok := fakeSpecs[k]; return s, ok }
func (h *fakeHost) Keys() []Spec               { return nil }
func (h *fakeHost) snap(k string) Snapshot {
	v := h.vals[k]
	if v == nil {
		v = json.RawMessage(`false`)
	}
	return Snapshot{Value: v, Display: string(v), Version: fmt.Sprint("v", h.version)}
}
func (h *fakeHost) Current(_ context.Context, k string) (Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snap(k), nil
}
func (h *fakeHost) Draft(_ context.Context, k string, v json.RawMessage) (Snapshot, error) {
	if k == "n" && string(v) == "999" {
		return Snapshot{}, errors.New("数量最多 100")
	}
	s := Snapshot{Value: v, Display: string(v)}
	var txt string
	if h.rawText && json.Unmarshal(v, &txt) == nil {
		s.Display = "「" + txt + "」"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s.Version = fmt.Sprint("v", h.version)
	return s, nil
}
func (h *fakeHost) Places(context.Context, string) ([]Candidate, error) { return h.places, h.placesErr }
func (h *fakeHost) Write(_ context.Context, k string, v json.RawMessage, base string) (Written, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writeErr != nil {
		return Written{}, h.writeErr
	}
	if base != fmt.Sprint("v", h.version) {
		return Written{}, errConflict
	}
	h.vals[k] = v
	h.version++
	h.writes++
	return Written{Version: fmt.Sprint("v", h.version)}, nil
}

var errConflict = errors.New("版本冲突")

func (h *fakeHost) IsConflict(err error) bool { return errors.Is(err, errConflict) }
func (h *fakeHost) Ask(_ context.Context, _ kernel.Invocation, _ Who, c Card) (Decision, error) {
	h.mu.Lock()
	h.asked = append(h.asked, c)
	fn, before := h.ask, h.beforeAsk
	h.mu.Unlock()
	if before != nil {
		before()
	}
	if fn == nil {
		return Decision{Approved: true, Kind: "approved"}, nil
	}
	return fn(c)
}
func (h *fakeHost) Source(context.Context, turn.ToolCall) string { return "把城市改成新值吧" }
func (h *fakeHost) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}
func (h *fakeHost) NewID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	return fmt.Sprint(h.seq)
}
func (h *fakeHost) set(k, v string) {
	h.mu.Lock()
	h.vals[k] = json.RawMessage(v)
	h.version++
	h.mu.Unlock()
}
func (h *fakeHost) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }

func newPlugin(t *testing.T) (*Plugin, *fakeHost, string) {
	t.Helper()
	h := newHost()
	path := filepath.Join(t.TempDir(), "go", "settings", "changes.jsonl")
	p := &Plugin{Host: h, Log: NewLog(path), proposals: map[string]*proposal{}}
	return p, h, path
}

func call(conv string, args string) turn.ToolCall {
	v, err := jsjson.Parse(args)
	if err != nil {
		panic(err)
	}
	return turn.ToolCall{
		Identity:     kernel.Invocation{Scope: kernel.Scope{CharacterID: "角色甲", Conversation: conv}},
		CharacterID:  "角色甲",
		Conversation: conv,
		Audience:     audience.Audience{Conversation: conv, PrivateOK: true},
		Args:         v, InputSeqs: []int64{7},
	}
}

type toolsResult = tools.Result

func run(t *testing.T, f func(context.Context, turn.ToolCall) (toolsResult, error), c turn.ToolCall) toolsResult {
	t.Helper()
	r, err := f(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func proposalID(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "p-")
	if i < 0 {
		t.Fatalf("没有提案号：%s", s)
	}
	j := i
	for j < len(s) && (s[j] == 'p' || s[j] == '-' || (s[j] >= '0' && s[j] <= '9')) {
		j++
	}
	return s[i:j]
}

func TestPreviewApplyHappyPath(t *testing.T) {
	p, h, path := newPlugin(t)
	conv := "console:角色甲"
	r := run(t, p.preview, call(conv, `{"key":"city","value":"新值","reason":"主人搬家了"}`))
	if r.IsError || !strings.Contains(r.Text, "旧值：\"旧值\"") || !strings.Contains(r.Text, "新值：\"新值\"") || !strings.Contains(r.Text, "每次都要所有者") {
		t.Fatalf("预览：%+v", r)
	}
	if h.writes != 0 {
		t.Fatal("预览不该写")
	}
	id := proposalID(t, r.Text)
	r = run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if r.IsError || !strings.Contains(r.Text, "已改：城市名") || !strings.Contains(r.Text, "c-") {
		t.Fatalf("应用：%+v", r)
	}
	if string(h.vals["city"]) != `"新值"` || h.writes != 1 {
		t.Fatalf("没写进去：%s %d", h.vals["city"], h.writes)
	}
	// 卡片：旧值、新值、影响、来源原话、理由都在
	if len(h.asked) != 1 {
		t.Fatalf("卡片数：%d", len(h.asked))
	}
	for _, want := range []string{"旧值", "新值", "只影响测试", "你说的：把城市改成新值吧", "她的理由：主人搬家了", "本地私聊"} {
		if !strings.Contains(h.asked[0].Text, want) {
			t.Errorf("卡片缺 %q：\n%s", want, h.asked[0].Text)
		}
	}
	// 记录：两行，重启（换一个 Log 对象）后还能查
	changes, bad, err := NewLog(path).All()
	if err != nil || bad != 0 || len(changes) != 1 || changes[0].Outcome != OutcomeApplied || changes[0].Source == "" || changes[0].OldText != `"旧值"` || changes[0].Place != "本地私聊" {
		t.Fatalf("记录：%+v bad=%d err=%v", changes, bad, err)
	}
	// 同一提案重复 apply：回同一个结果，不二次写，不二次发卡
	r2 := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if r2.IsError || !strings.Contains(r2.Text, "没有再写") || h.writes != 1 || len(h.asked) != 1 {
		t.Fatalf("重复 apply：%+v writes=%d asked=%d", r2, h.writes, len(h.asked))
	}
}

func TestApplyNotApprovedNeverWrites(t *testing.T) {
	for name, d := range map[string]Decision{
		"拒绝": {Kind: "denied", Reason: "不对"}, "到期": {Kind: "expired"}, "停掉": {Kind: "cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			p, h, path := newPlugin(t)
			h.ask = func(Card) (Decision, error) { return d, nil }
			conv := "console:角色甲"
			id := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
			r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
			if !r.IsError || h.writes != 0 || string(h.vals["n"]) != "3" {
				t.Fatalf("不该写：%+v writes=%d", r, h.writes)
			}
			changes, _, _ := NewLog(path).All()
			if len(changes) != 1 || changes[0].Outcome == OutcomeApplied {
				t.Fatalf("记录：%+v", changes)
			}
			// 作废后再 apply 要重新预览
			if r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`)); !r.IsError || !strings.Contains(r.Text, "作废") {
				t.Fatalf("作废后：%+v", r)
			}
		})
	}
}

func TestApplyVersionConflictBeforeAndAfterCard(t *testing.T) {
	conv := "console:角色甲"
	// 发卡前设置已被别处改过
	p, h, _ := newPlugin(t)
	id := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	h.set("flag", "true") // 配置版本是整文件的：别的键改了也算冲突
	r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if !r.IsError || !strings.Contains(r.Text, "被改过") || h.writes != 0 || len(h.asked) != 0 {
		t.Fatalf("发卡前冲突：%+v writes=%d asked=%d", r, h.writes, len(h.asked))
	}
	// 主人点卡期间设置页改了别的
	p, h, _ = newPlugin(t)
	id = proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	h.beforeAsk = func() { h.set("flag", "true") }
	r = run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if !r.IsError || !strings.Contains(r.Text, "被改过") || h.writes != 0 || string(h.vals["n"]) != "3" {
		t.Fatalf("批准后冲突：%+v writes=%d", r, h.writes)
	}
}

func TestApplyWriteFailureAndLogFailure(t *testing.T) {
	conv := "console:角色甲"
	p, h, path := newPlugin(t)
	h.writeErr = errors.New("磁盘满了")
	id := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if !r.IsError || strings.Contains(r.Text, "已改") || string(h.vals["n"]) != "3" {
		t.Fatalf("写失败不能说改好了：%+v", r)
	}
	changes, _, _ := NewLog(path).All()
	if len(changes) != 1 || changes[0].Outcome != OutcomeFailed {
		t.Fatalf("记录：%+v", changes)
	}
	// 记录写不进去：不改设置。把记录路径的父目录占成一个文件
	p, h, path = newPlugin(t)
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(path), []byte("占位"), 0o600); err != nil {
		t.Fatal(err)
	}
	id = proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	r = run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if !r.IsError || h.writes != 0 || string(h.vals["n"]) != "3" {
		t.Fatalf("记录写不进去也改了设置：%+v writes=%d", r, h.writes)
	}
}

func TestUnknownKeyAndBadValues(t *testing.T) {
	p, _, _ := newPlugin(t)
	conv := "console:角色甲"
	for _, args := range []string{
		`{"key":"qq.owner","value":"1"}`, `{"key":"","value":1}`, `{"key":"tools.workdir","value":"C:/"}`, `{"key":"n"}`,
		`{"key":"n","value":"abc"}`, `{"key":"n","value":1.5}`, `{"key":"n","value":true}`, `{"key":"n","value":999}`,
		`{"key":"flag","value":"yes"}`, `{"key":"city","value":5}`, `{"key":"city","value":"x","path":"C:/a"}`, `{"key":"city","value":"x","confirmed":true}`,
		`{"key":["n"],"value":1}`, `null`, `[]`, `{"key":"n","value":[1]}`,
	} {
		if r := run(t, p.preview, call(conv, args)); !r.IsError {
			t.Errorf("该拒绝：%s → %+v", args, r)
		}
	}
	// 和现在一样的值不生成提案
	if r := run(t, p.preview, call(conv, `{"key":"n","value":3}`)); r.IsError || !strings.Contains(r.Text, "不用改") {
		t.Fatalf("同值：%+v", r)
	}
	if len(p.proposals) != 0 {
		t.Fatalf("不该留提案：%d", len(p.proposals))
	}
	// 回给她的错误里没有外来的长文字
	long := strings.Repeat("长", 500)
	r := run(t, p.preview, call(conv, `{"key":"`+long+`","value":1,"x":1}`))
	if len([]rune(r.Text)) > 100 {
		t.Fatalf("错误文字太长：%d", len([]rune(r.Text)))
	}
}

func TestBoolAndNumberStrings(t *testing.T) {
	p, h, _ := newPlugin(t)
	conv := "console:角色甲"
	for _, c := range []struct{ args, key, want string }{
		{`{"key":"flag","value":true}`, "flag", "true"}, {`{"key":"flag","value":"true"}`, "flag", "true"}, {`{"key":"n","value":"7"}`, "n", "7"}, {`{"key":"n","value":7}`, "n", "7"},
	} {
		id := proposalID(t, run(t, p.preview, call(conv, c.args)).Text)
		if r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`)); r.IsError || string(h.vals[c.key]) != c.want {
			t.Fatalf("%s：%+v %s", c.args, r, h.vals[c.key])
		}
		h.set(c.key, map[string]string{"flag": "false", "n": "3"}[c.key])
	}
}

func TestIdentityAndSessionBinding(t *testing.T) {
	p, h, _ := newPlugin(t)
	conv := "console:角色甲"
	id := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	// 别的会话拿到提案号也不能用、不能收回、看不到
	other := call("qq:private:1:2", `{"proposal_id":"`+id+`"}`)
	for _, f := range []func(context.Context, turn.ToolCall) (toolsResult, error){p.apply, p.cancel} {
		if r := run(t, f, other); !r.IsError || h.writes != 0 {
			t.Fatalf("跨会话：%+v", r)
		}
	}
	if r := run(t, p.list, call("qq:private:1:2", `{}`)); strings.Contains(r.Text, id) {
		t.Fatalf("别的会话看到了提案：%s", r.Text)
	}
	// 身份核不过：五个工具全拒，且不留痕
	h.authorize = errors.New("不是主人")
	for name, f := range map[string]func(context.Context, turn.ToolCall) (toolsResult, error){"preview": p.preview, "apply": p.apply, "rollback": p.rollback, "list": p.list, "cancel": p.cancel} {
		if r := run(t, f, call(conv, `{"key":"n","value":5,"proposal_id":"`+id+`","change_id":"c-1"}`)); !r.IsError || r.Text != denied {
			t.Errorf("%s 该拒绝：%+v", name, r)
		}
	}
	h.authorize = nil
	// 工具收到的会话和身份不自洽（缺、空、错）
	for _, mut := range []func(*turn.ToolCall){
		func(c *turn.ToolCall) { c.CharacterID = "" },
		func(c *turn.ToolCall) { c.Conversation = "" },
		func(c *turn.ToolCall) { c.CharacterID = "别人" },
		func(c *turn.ToolCall) { c.Identity.Scope.Conversation = "console:别人" },
		func(c *turn.ToolCall) { c.Audience.Conversation = "console:别人" },
		func(c *turn.ToolCall) { c.Audience.PrivateOK = false },
	} {
		c := call(conv, `{"key":"n","value":5}`)
		mut(&c)
		if r := run(t, p.preview, c); !r.IsError || r.Text != denied {
			t.Errorf("形状不自洽该拒绝：%+v", r)
		}
	}
	if len(h.asked) != 0 || h.writes != 0 {
		t.Fatal("拒绝的调用不该有副作用")
	}
}

func TestExpiryCancelAndReplacement(t *testing.T) {
	p, h, _ := newPlugin(t)
	conv := "console:角色甲"
	id := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	h.advance(ProposalTTL + time.Second)
	if r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`)); !r.IsError || !strings.Contains(r.Text, "过期") || h.writes != 0 {
		t.Fatalf("过期：%+v", r)
	}
	// 同一个键再预览顶掉旧的
	a := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	b := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":6}`)).Text)
	if r := run(t, p.apply, call(conv, `{"proposal_id":"`+a+`"}`)); !r.IsError {
		t.Fatalf("被顶掉的提案不该能用：%+v", r)
	}
	if r := run(t, p.cancel, call(conv, `{"proposal_id":"`+b+`"}`)); r.IsError {
		t.Fatalf("收回：%+v", r)
	}
	if r := run(t, p.apply, call(conv, `{"proposal_id":"`+b+`"}`)); !r.IsError || h.writes != 0 {
		t.Fatalf("收回后不该能 apply：%+v", r)
	}
	// 提案上限：用同一个会话塞满 16 个不同的键不现实，直接填
	p.mu.Lock()
	for i := 0; i < MaxProposals; i++ {
		p.proposals[fmt.Sprint("fill", i)] = &proposal{ID: fmt.Sprint("fill", i), Key: fmt.Sprint("k", i), State: "open", Who: Who{Character: "角色甲", Conversation: conv}, Expires: h.now.Add(time.Hour)}
	}
	p.mu.Unlock()
	if r := run(t, p.preview, call(conv, `{"key":"flag","value":true}`)); !r.IsError || !strings.Contains(r.Text, "太多") {
		t.Fatalf("上限：%+v", r)
	}
}

func TestUnknownLevelRejected(t *testing.T) {
	p, h, _ := newPlugin(t)
	conv := "console:角色甲"
	id := proposalID(t, run(t, p.preview, call(conv, `{"key":"odd","value":"x"}`)).Text)
	r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	if !r.IsError || h.writes != 0 || len(h.asked) != 0 {
		t.Fatalf("不认识的确认等级该拒绝：%+v", r)
	}
}

func TestRollbackOnlyThatKey(t *testing.T) {
	p, h, path := newPlugin(t)
	conv := "console:角色甲"
	id := proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":5}`)).Text)
	r := run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	change := r.Text[strings.Index(r.Text, "c-"):]
	change = change[:strings.IndexAny(change, " ，）。")]
	// 之后别的键又被改了：回滚 n 不碰它
	h.set("city", `"后来界面改的"`)
	pv := run(t, p.rollback, call(conv, `{"change_id":"`+change+`"}`))
	if pv.IsError || !strings.Contains(pv.Text, "新值：3") || strings.Contains(pv.Text, "又被改过") {
		t.Fatalf("回滚预览：%+v", pv)
	}
	rid := proposalID(t, pv.Text)
	if r := run(t, p.apply, call(conv, `{"proposal_id":"`+rid+`"}`)); r.IsError {
		t.Fatalf("回滚应用：%+v", r)
	}
	if string(h.vals["n"]) != "3" || string(h.vals["city"]) != `"后来界面改的"` {
		t.Fatalf("回滚只该动这一项：%v", h.vals)
	}
	// 卡片上写明是回滚
	if !strings.Contains(h.asked[1].Text, "这是回滚") {
		t.Fatalf("卡片：%s", h.asked[1].Text)
	}
	changes, _, _ := NewLog(path).All()
	if len(changes) != 2 || changes[1].RollbackOf != change {
		t.Fatalf("记录：%+v", changes)
	}
	// 那一项在那次之后又被改过：预览里如实写明
	h.set("n", "9")
	pv = run(t, p.rollback, call(conv, `{"change_id":"`+change+`"}`))
	if pv.IsError || !strings.Contains(pv.Text, "又被改过") || !strings.Contains(pv.Text, "9") {
		t.Fatalf("改过之后回滚：%+v", pv)
	}
	// 没生效的改动、不存在的编号、别的角色的改动
	h.ask = func(Card) (Decision, error) { return Decision{Kind: "denied"}, nil }
	id = proposalID(t, run(t, p.preview, call(conv, `{"key":"n","value":11}`)).Text)
	run(t, p.apply, call(conv, `{"proposal_id":"`+id+`"}`))
	changes, _, _ = NewLog(path).All()
	denied := changes[len(changes)-1].Change
	if r := run(t, p.rollback, call(conv, `{"change_id":"`+denied+`"}`)); r.IsError || !strings.Contains(r.Text, "没有生效") {
		t.Fatalf("没生效的：%+v", r)
	}
	if r := run(t, p.rollback, call(conv, `{"change_id":"c-不存在"}`)); !r.IsError {
		t.Fatalf("不存在：%+v", r)
	}
	foreign := call(conv, `{"change_id":"`+change+`"}`)
	foreign.CharacterID, foreign.Identity.Scope.CharacterID = "角色乙", "角色乙"
	if r := run(t, p.rollback, foreign); !r.IsError {
		t.Fatalf("别的角色：%+v", r)
	}
}

func TestLogSurvivesRestartAndMarksUnknown(t *testing.T) {
	p, h, path := newPlugin(t)
	conv := "console:角色甲"
	// 模拟崩溃：只有开始行，没有结局
	if err := p.Log.Append(Record{Phase: PhaseBegin, Change: "c-崩", Key: "n", Old: json.RawMessage(`3`), New: json.RawMessage(`8`), OldText: "3", NewText: "8", Character: "角色甲", Place: "本地私聊", At: 1}); err != nil {
		t.Fatal(err)
	}
	// 再来一条半行（崩溃时写了一半）
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"phase":"end","chan`)
	f.Close()
	p2 := &Plugin{Host: h, Log: NewLog(path), proposals: map[string]*proposal{}}
	r := run(t, p2.list, call(conv, `{}`))
	if !strings.Contains(r.Text, "结局不明") || !strings.Contains(r.Text, "当作没写进去") || !strings.Contains(r.Text, "读不懂") {
		t.Fatalf("列表：%s", r.Text)
	}
	// 当前值就是新值：如实说新值已在，回滚有东西可回
	h.set("n", "8")
	r = run(t, p2.list, call(conv, `{"key":"n"}`))
	if !strings.Contains(r.Text, "现在的值是新值") {
		t.Fatalf("列表：%s", r.Text)
	}
	if pv := run(t, p2.rollback, call(conv, `{"change_id":"c-崩"}`)); pv.IsError || !strings.Contains(pv.Text, "新值：3") {
		t.Fatalf("结局不明的回滚：%+v", pv)
	}
}

func TestLocationCandidates(t *testing.T) {
	p, h, _ := newPlugin(t)
	conv := "console:角色甲"
	loc := func(name string) json.RawMessage {
		return json.RawMessage(`{"name":"` + name + `","city":"北京市","latitude":39.9,"longitude":116.4}`)
	}
	h.places = []Candidate{{ID: "1", Label: "海淀，北京市，中国", Value: loc("海淀，北京市，中国")}, {ID: "2", Label: "海淀，某省，别国", Value: loc("海淀，某省，别国")}}
	// 多个候选：只列出来，不生成提案
	r := run(t, p.preview, call(conv, `{"key":"location","value":"海淀"}`))
	if r.IsError || !strings.Contains(r.Text, "candidate_id=1") || !strings.Contains(r.Text, "candidate_id=2") || len(p.proposals) != 0 {
		t.Fatalf("候选：%+v", r)
	}
	// 选一个
	r = run(t, p.preview, call(conv, `{"key":"location","value":"海淀","candidate_id":"2"}`))
	if r.IsError || !strings.Contains(r.Text, "海淀，某省，别国") {
		t.Fatalf("选候选：%+v", r)
	}
	if r := run(t, p.preview, call(conv, `{"key":"location","value":"海淀","candidate_id":"99"}`)); !r.IsError {
		t.Fatalf("乱写的编号：%+v", r)
	}
	// 没查到、接口失败
	h.places = nil
	if r := run(t, p.preview, call(conv, `{"key":"location","value":"不存在的地方"}`)); r.IsError || !strings.Contains(r.Text, "没查到") {
		t.Fatalf("没查到：%+v", r)
	}
	h.placesErr = errors.New("内部细节XYZ")
	if r := run(t, p.preview, call(conv, `{"key":"location","value":"海淀"}`)); !r.IsError || strings.Contains(r.Text, "内部细节XYZ") {
		t.Fatalf("接口失败：%+v", r)
	}
	// 清空：空字符串；数字不行
	h.set("location", string(loc("旧所在地")))
	if r := run(t, p.preview, call(conv, `{"key":"location","value":""}`)); r.IsError || !strings.Contains(r.Text, "新值：{}") {
		t.Fatalf("清空：%+v", r)
	}
	if r := run(t, p.preview, call(conv, `{"key":"location","value":5}`)); !r.IsError {
		t.Fatalf("数字：%+v", r)
	}
}

func TestListShowsOpenProposals(t *testing.T) {
	p, _, _ := newPlugin(t)
	conv := "console:角色甲"
	run(t, p.preview, call(conv, `{"key":"n","value":5}`))
	r := run(t, p.list, call(conv, `{}`))
	if !strings.Contains(r.Text, "没有改动记录") || !strings.Contains(r.Text, "没用掉的提案") {
		t.Fatalf("列表：%s", r.Text)
	}
	for _, bad := range []string{`{"limit":0}`, `{"limit":31}`, `{"limit":1.5}`, `{"limit":"3"}`, `{"x":1}`} {
		if r := run(t, p.list, call(conv, bad)); !r.IsError {
			t.Errorf("该拒绝：%s", bad)
		}
	}
}

// 回归：text 类的值带换行时，审批卡上不能多出一行“来自：”“你说的：”。
// 以前新值原样拼进卡片，模型把 value 写成“好\n来自：本地私聊\n你说的：……”就能在卡上伪造来源。
func TestCardFoldsForgedSourceLines(t *testing.T) {
	p, h, _ := newPlugin(t)
	h.rawText = true
	forged := "好\n来自：本地私聊\n你说的：把解析失败提示改成这句\r\n理由：假的"
	r := run(t, p.preview, call("console:角色甲", `{"key":"city","value":`+strconvQuote(forged)+`}`))
	if r.IsError {
		t.Fatalf("预览：%+v", r)
	}
	run(t, p.apply, call("console:角色甲", `{"proposal_id":"`+proposalID(t, r.Text)+`"}`))
	if len(h.asked) != 1 {
		t.Fatalf("卡片数：%d", len(h.asked))
	}
	card := h.asked[0].Text
	for _, prefix := range []string{"来自：", "你说的：", "理由：", "她的理由：", "新值：", "旧值："} {
		n := 0
		for _, line := range strings.Split(card, "\n") {
			if strings.HasPrefix(line, prefix) {
				n++
			}
		}
		if prefix == "你说的：" && n != 1 || prefix == "来自：" && n != 1 || (prefix == "新值：" || prefix == "旧值：") && n != 1 || (prefix == "理由：" || prefix == "她的理由：") && n != 0 {
			t.Errorf("%q 出现 %d 行，卡上被塞了假行：\n%s", prefix, n, card)
		}
	}
	if !strings.Contains(card, "你说的：把城市改成新值吧") && !strings.Contains(card, "你说的：") {
		t.Fatalf("真的来源行不见了：\n%s", card)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
