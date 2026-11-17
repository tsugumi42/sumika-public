package askowner

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

const (
	testConv = "console:编的角色"
	goodArgs = `{"question":"先备份再改，还是直接改？","options":[{"label":"先备份","detail":"多占一点空间"},"直接改"]}`
)

type rig struct {
	p        *Plugin
	c        *Center
	said     []string
	answered []string
	notified []string
	alone    bool
	steer    bool
	wait     time.Duration
}

func newRig() *rig {
	r := &rig{c: NewCenter(), wait: time.Minute}
	r.p = New(Options{
		Center:       r.c,
		Allowed:      func(turn.ToolCall) bool { return true },
		Unattended:   func(string) bool { return r.alone },
		SteerPending: func(turn.ToolCall) bool { return r.steer },
		WaitFor:      func() time.Duration { return r.wait },
		Said:         func(_ turn.ToolCall, s string) error { r.said = append(r.said, s); return nil },
		Answered:     func(_ turn.ToolCall, s string) error { r.answered = append(r.answered, s); return nil },
		Notify:       func(_ turn.ToolCall, s string) { r.notified = append(r.notified, s) },
	})
	return r
}

func call(t *testing.T, args string) turn.ToolCall {
	t.Helper()
	v, err := jsjson.Parse(args)
	if err != nil {
		t.Fatal(err)
	}
	return turn.ToolCall{Identity: kernel.Invocation{ToolID: "tool-" + strconv.FormatInt(time.Now().UnixNano(), 36)}, CharacterID: "编的角色", Conversation: testConv, TurnID: "turn-1", Name: ToolName, Args: v}
}

func (r *rig) run(ctx context.Context, c turn.ToolCall) tools.Result {
	res, err := r.p.exec(ctx, c)
	if err != nil {
		panic(err)
	}
	return res
}

// asyncRun 起一次调用，等问题挂出来再交回
func (r *rig) asyncRun(t *testing.T, ctx context.Context, args string) (Question, <-chan tools.Result) {
	t.Helper()
	out := make(chan tools.Result, 1)
	go func() { out <- r.run(ctx, call(t, args)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if qs := r.c.Snapshot(); len(qs) > 0 {
			return qs[len(qs)-1], out
		}
		if time.Now().After(deadline) {
			t.Fatal("问题没挂出来")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan tools.Result) tools.Result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("等待没有结束")
		return tools.Result{}
	}
}

// 回答不是批准，靠结构保证：这个包不许引审批和判定（改坏检查：加一行 import 就红）
func TestDoesNotImportApproval(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		seen++
		for _, im := range parsed.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if path == "sumika-native/internal/approval" || path == "sumika-native/internal/policy" {
				t.Fatalf("%s 引了 %s：回答不是批准，不许碰授权", f, path)
			}
		}
	}
	if seen == 0 {
		t.Fatal("一个源文件都没读到")
	}
}

func TestChosenSavesAnswerAndSaysNotGrant(t *testing.T) {
	r := newRig()
	q, out := r.asyncRun(t, context.Background(), goodArgs)
	if q.Text != "先备份再改，还是直接改？" || len(q.Options) != 2 || q.Options[0] != (Option{"先备份", "多占一点空间"}) || q.Options[1] != (Option{Label: "直接改"}) {
		t.Fatalf("问题解析错了：%+v", q)
	}
	if !r.c.Waiting(testConv) || r.c.Waiting("console:别人") {
		t.Fatal("Waiting 认错会话")
	}
	if len(r.said) != 1 || r.said[0] != q.Text || len(r.notified) != 1 {
		t.Fatalf("问题要先存进聊天、提醒主人：%q %q", r.said, r.notified)
	}
	if !r.c.Answer(q.ID, 1) {
		t.Fatal("答不上")
	}
	res := recv(t, out)
	if res.IsError || !strings.Contains(res.Text, "「直接改」") || !strings.Contains(res.Text, "不是批准") {
		t.Fatalf("结果：%+v", res)
	}
	if len(r.answered) != 1 || r.answered[0] != "直接改" {
		t.Fatalf("选项要存成主人的话：%q", r.answered)
	}
	if r.c.Answer(q.ID, 0) {
		t.Fatal("答过的不能再答")
	}
	if len(r.c.Snapshot()) != 0 {
		t.Fatal("答完问题还挂着")
	}
}

func TestAnswerOutOfRangeLeavesQuestion(t *testing.T) {
	r := newRig()
	q, out := r.asyncRun(t, context.Background(), goodArgs)
	for _, id := range []string{"", "不存在的"} {
		if r.c.Answer(id, 0) {
			t.Fatalf("答了不存在的 %q", id)
		}
	}
	if r.c.Answer(q.ID, 2) {
		t.Fatal("越界的下标不该算")
	}
	if len(r.c.Snapshot()) != 1 {
		t.Fatal("越界不该动问题")
	}
	r.c.Answer(q.ID, -1)
	res := recv(t, out)
	if res.IsError || !strings.Contains(res.Text, "先不答") || !strings.Contains(res.Text, "别再问") {
		t.Fatalf("先不答：%+v", res)
	}
	if len(r.answered) != 0 {
		t.Fatalf("先不答不是主人说了什么：%q", r.answered)
	}
}

// 自由回答用中性措辞：主人可能是叫她等等，不能替他说“接着做”（Claude Code 更新记录修过同一个问题）
func TestInterruptEndsWaitNeutrally(t *testing.T) {
	r := newRig()
	_, out := r.asyncRun(t, context.Background(), goodArgs)
	if r.c.Interrupt("console:别人") {
		t.Fatal("别的会话的插话不该结束等待")
	}
	if !r.c.Interrupt(testConv) {
		t.Fatal("没结束")
	}
	res := recv(t, out)
	if res.IsError || !strings.Contains(res.Text, "直接回了一句") || !strings.Contains(res.Text, "不是批准") || strings.Contains(res.Text, "接着做") {
		t.Fatalf("结果：%+v", res)
	}
	if r.c.Interrupt(testConv) {
		t.Fatal("没有在等的不该算")
	}
}

func TestExpiredIsNotConsentThenNoMoreWaiting(t *testing.T) {
	r := newRig()
	r.wait = 30 * time.Millisecond
	res := r.run(context.Background(), call(t, goodArgs))
	if res.IsError || !strings.Contains(res.Text, "不算同意") || !strings.Contains(res.Text, "留在聊天里") {
		t.Fatalf("到期：%+v", res)
	}
	if len(r.answered) != 0 {
		t.Fatalf("到期不能替主人选：%q", r.answered)
	}
	// 同一轮再问：不等（等待设长，等了这里就超时）
	r.wait = time.Hour
	res = r.run(context.Background(), call(t, goodArgs))
	if !strings.Contains(res.Text, "没有等") {
		t.Fatalf("同一轮第二次：%+v", res)
	}
	if got := r.said[len(r.said)-1]; !strings.Contains(got, "可选：先备份 / 直接改") {
		t.Fatalf("没卡片时问题要带选项：%q", got)
	}
	// 下一轮可以再等
	next := call(t, goodArgs)
	next.TurnID = "turn-2"
	go func() { r.run(context.Background(), next) }()
	waitTrue(t, func() bool { return r.c.Waiting(testConv) })
	r.c.Interrupt(testConv)
}

func waitTrue(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("没等到")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCancelWithdrawsAtOnce(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	_, out := r.asyncRun(t, ctx, goodArgs)
	cancel()
	res := recv(t, out)
	if !res.IsError || !strings.Contains(res.Text, "停下") {
		t.Fatalf("取消：%+v", res)
	}
	if len(r.c.Snapshot()) != 0 {
		t.Fatal("取消后问题还挂着")
	}
}

// 同时到的回答优先：已经从 waiting 拿走的，取消不能把它改成没答
func TestAnswerWinsOverCancel(t *testing.T) {
	c := NewCenter()
	q := Question{ID: "q", Conversation: testConv, Options: []Option{{Label: "甲"}, {Label: "乙"}}}
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan Result, 1)
		go func() { out <- c.Ask(ctx, q, time.Minute) }()
		waitTrue(t, func() bool { return c.Waiting(testConv) })
		go cancel()
		answered := c.Answer(q.ID, 1)
		r := <-out
		if answered && r.Kind != Chosen {
			t.Fatalf("答上了结局却是 %s", r.Kind)
		}
		if !answered && r.Kind != Cancelled {
			t.Fatalf("没答上结局却是 %s", r.Kind)
		}
		cancel()
	}
}

func TestUnattendedDoesNotWait(t *testing.T) {
	r := newRig()
	r.alone = true
	res := r.run(context.Background(), call(t, goodArgs))
	if res.IsError || !strings.Contains(res.Text, "没有等") || !strings.Contains(res.Text, "提醒了所有者") {
		t.Fatalf("无人值守：%+v", res)
	}
	if len(r.c.Snapshot()) != 0 {
		t.Fatal("无人值守不该挂卡片")
	}
	if len(r.said) != 1 || !strings.Contains(r.said[0], "可选：") || len(r.notified) != 1 {
		t.Fatalf("问题要带选项留在聊天里、提醒主人：%q %q", r.said, r.notified)
	}
}

// 还有没送到的插话：问了就是互相等。这次当没问，不留东西
func TestPendingSteerSkipsQuestion(t *testing.T) {
	r := newRig()
	r.steer = true
	res := r.run(context.Background(), call(t, goodArgs))
	if res.IsError || !strings.Contains(res.Text, "还没看到") {
		t.Fatalf("结果：%+v", res)
	}
	if len(r.said) != 0 || len(r.notified) != 0 || len(r.c.Snapshot()) != 0 {
		t.Fatal("这次没问，不该留东西")
	}
}

func TestFullDoesNotWait(t *testing.T) {
	r := newRig()
	for i := range MaxWaiting {
		c := call(t, goodArgs)
		c.Conversation, c.Identity.ToolID = testConv+strconv.Itoa(i), "占位"+strconv.Itoa(i)
		go func() { r.run(context.Background(), c) }()
		want := i + 1
		waitTrue(t, func() bool { return len(r.c.Snapshot()) == want })
	}
	res := r.run(context.Background(), call(t, goodArgs))
	if !strings.Contains(res.Text, "没有等") {
		t.Fatalf("到上限还在等：%+v", res)
	}
	for i := range MaxWaiting {
		r.c.Interrupt(testConv + strconv.Itoa(i))
	}
}

// 这一轮的总时限是固定的：够的时候等待被压到时限之内，留够收尾时间；不够就不等，不能让时限把工具切断
func TestDeadlineClampsWait(t *testing.T) {
	r := newRig()
	r.wait = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), deadlineMargin+5*time.Second)
	defer cancel()
	q, out := r.asyncRun(t, ctx, goodArgs)
	if got := time.Duration(q.ExpiresAt-q.CreatedAt) * time.Millisecond; got > 6*time.Second || got <= 0 {
		t.Fatalf("等待没压到时限之内：%v", got)
	}
	r.c.Interrupt(testConv)
	recv(t, out)

	r = newRig()
	r.wait = time.Hour
	tight, cancel2 := context.WithTimeout(context.Background(), deadlineMargin-time.Second)
	defer cancel2()
	res := r.run(tight, call(t, goodArgs))
	if res.IsError || !strings.Contains(res.Text, "快到时限") || len(r.c.Snapshot()) != 0 {
		t.Fatalf("时限不够：%+v", res)
	}
}

func TestNotAllowedNeverAsks(t *testing.T) {
	r := newRig()
	r.p.o.Allowed = func(turn.ToolCall) bool { return false }
	res := r.run(context.Background(), call(t, goodArgs))
	if !res.IsError || len(r.said) != 0 || len(r.notified) != 0 || len(r.c.Snapshot()) != 0 {
		t.Fatalf("不许问的不该留任何东西：%+v %q", res, r.said)
	}
	r = newRig()
	c := call(t, goodArgs)
	c.Identity.ToolID = ""
	if res := r.run(context.Background(), c); !res.IsError {
		t.Fatalf("没有工具号：%+v", res)
	}
}

// 形状：缺、空、类型错、超长、控制字符、重复；都回一句怎么改，不挂卡片、不留东西
func TestParseShapes(t *testing.T) {
	long := func(n int) string { return strings.Repeat("字", n) }
	two := `"options":["甲","乙"]`
	for _, c := range []struct{ name, args, want string }{
		{"参数不是对象", `[]`, "question 和 options"},
		{"参数是 null", `null`, "question 和 options"},
		{"没有 question", `{` + two + `}`, "question 要是"},
		{"question 是数字", `{"question":1,` + two + `}`, "question 要是"},
		{"question 空", `{"question":"",` + two + `}`, "不能是空"},
		{"question 全空白", `{"question":" \n\t ",` + two + `}`, "不能是空"},
		{"question 超长", `{"question":"` + long(501) + `",` + two + `}`, "太长"},
		{"question 有控制字符", `{"question":"甲\u0007乙",` + two + `}`, "控制字符"},
		{"没有 options", `{"question":"问"}`, "2～4"},
		{"options 不是数组", `{"question":"问","options":"甲"}`, "2～4"},
		{"options 空", `{"question":"问","options":[]}`, "2～4"},
		{"一个选项", `{"question":"问","options":["甲"]}`, "2～4"},
		{"五个选项", `{"question":"问","options":["甲","乙","丙","丁","戊"]}`, "2～4"},
		{"选项是数字", `{"question":"问","options":[1,2]}`, "label"},
		{"选项是 null", `{"question":"问","options":[null,"乙"]}`, "label"},
		{"选项没有 label", `{"question":"问","options":[{"detail":"说明"},"乙"]}`, "label"},
		{"label 是数字", `{"question":"问","options":[{"label":1},"乙"]}`, "label"},
		{"label 空", `{"question":"问","options":["  ","乙"]}`, "不能是空"},
		{"label 超长", `{"question":"问","options":["` + long(41) + `","乙"]}`, "太长"},
		{"label 换行", `{"question":"问","options":["甲\n丙","乙"]}`, "一行"},
		{"label 重复", `{"question":"问","options":["甲"," 甲 "]}`, "重复"},
		{"detail 是数字", `{"question":"问","options":[{"label":"甲","detail":1},"乙"]}`, "detail"},
		{"detail 超长", `{"question":"问","options":[{"label":"甲","detail":"` + long(121) + `"},"乙"]}`, "太长"},
		{"detail 换行", `{"question":"问","options":[{"label":"甲","detail":"一\n二"},"乙"]}`, "一行"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig()
			res := r.run(context.Background(), call(t, c.args))
			if !res.IsError || !strings.Contains(res.Text, c.want) {
				t.Fatalf("结果：%+v", res)
			}
			if len(r.said) != 0 || len(r.notified) != 0 || len(r.c.Snapshot()) != 0 {
				t.Fatal("参数不对不该留任何东西")
			}
		})
	}
	// 边界上的都算对：正好 500、40、120 字，2、4 个选项，问题里可以换行，detail 写 null 等于没写
	for _, args := range []string{
		`{"question":"` + long(500) + `",` + two + `}`,
		`{"question":"第一行\n第二行","options":[{"label":"` + long(40) + `","detail":"` + long(120) + `"},"乙","丙","丁"]}`,
		`{"question":"问","options":[{"label":"甲","detail":null},"乙"]}`,
	} {
		v, err := jsjson.Parse(args)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, bad := parseQuestion(v); bad != "" {
			t.Fatalf("%s 该通过：%s", args, bad)
		}
	}
}
