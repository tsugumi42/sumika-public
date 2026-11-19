package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

// fakeInputs 只替后台回一个收据或一个错误，并把收到的提交记下来；不碰真实数据。
type fakeInputs struct {
	api.Reader
	mu   sync.Mutex
	got  []api.LocalInputSubmission
	err  error
	gate chan struct{} // 非 nil 时 SubmitLocal 先等它

	saveErr  error // 存粘贴的图时的错误
	savePath string
	saved    [][]byte
	saveGate chan struct{} // 非 nil 时 SaveLocalImage 先等它
}

func (f *fakeInputs) SaveLocalImage(_ context.Context, _ api.ConversationKey, png []byte) (api.LocalAttachment, error) {
	if f.saveGate != nil {
		<-f.saveGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, png)
	if f.saveErr != nil {
		return api.LocalAttachment{}, f.saveErr
	}
	return api.LocalAttachment{Kind: "image", Path: f.savePath, Name: "粘贴的图片.png"}, nil
}

func (f *fakeInputs) SubmitLocal(_ context.Context, _ api.ConversationKey, s api.LocalInputSubmission) (api.InputReceipt, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, s)
	return api.InputReceipt{InputID: "i1", RunID: "r1", State: "accepted", Sequence: 1, Revision: 1}, f.err
}

func (f *fakeInputs) StopLocalRun(context.Context, api.ConversationKey, string, uint64) (api.StopReceipt, error) {
	return api.StopReceipt{}, nil
}

type settledRun struct {
	out chan api.Snapshot[api.LocalRunSnapshot]
}

func (s settledRun) Updates() <-chan api.Snapshot[api.LocalRunSnapshot] { return s.out }
func (s settledRun) Close()                                             {}

func (f *fakeInputs) WatchLocalRun(context.Context, api.ConversationKey, string) (api.Subscription[api.LocalRunSnapshot], error) {
	out := make(chan api.Snapshot[api.LocalRunSnapshot], 1)
	out <- api.Snapshot[api.LocalRunSnapshot]{Result: api.Result[api.LocalRunSnapshot]{State: api.Ready, Value: api.LocalRunSnapshot{RunID: "r1", State: "settled", Outcome: "done", Drained: true}}}
	close(out)
	return settledRun{out}, nil
}

func (f *fakeInputs) submissions() []api.LocalInputSubmission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.LocalInputSubmission(nil), f.got...)
}

type wbRig struct {
	a   *app
	q   *uiQueue
	tt  *ui.Tester
	f   *fakeInputs
	key api.ConversationKey
	p   *chatui.Composer
}

func newWbRig(t *testing.T, f *fakeInputs) *wbRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
	a := &app{ctx: ctx, viewCtx: ctx, sel: key.Conversation, keys: map[string]api.ConversationKey{key.Conversation: key}, reader: f}
	a.msgs = []*msg{{id: "m1", who: "编造角色", isChar: true, actor: "char00", at: 1000, text: "第一句在这。第二句在那。", blocks: segview.TextBlocks("第一句在这。第二句在那。")}}
	a.list = a.newList()
	q := &uiQueue{}
	a.postFn = q.post
	tt := ui.NewTester(func(c *ui.Context) { a.workbenchChat(c) }, 900, 700)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	return &wbRig{a: a, q: q, tt: tt, f: f, key: key, p: a.composerOf(key.Conversation)}
}

func (r *wbRig) settle() {
	for range 6 {
		r.q.drain()
		r.tt.Frame()
	}
}

// settleUntil 转到条件成立：异步任务在协程里跑，机器忙时固定帧数等不到。上限只防卡死
func (r *wbRig) settleUntil(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("等了一分钟异步任务还没结束")
		}
		r.settle()
		time.Sleep(time.Millisecond)
	}
	r.settle()
}

// submitted 等提交的回音回到界面线程（后台协程调完假后台再 post 回来，固定刷几帧等不到）
func (r *wbRig) submitted(t *testing.T) {
	t.Helper()
	st := r.a.localRun(r.key.Conversation)
	r.settleUntil(t, func() bool { return !st.submitting })
}

func (r *wbRig) quote(id, text string) *chatui.Block {
	return r.p.AddQuote(chatui.Quoted{MessageID: id, Name: "编造角色", Text: text, Start: 2, End: 4}, false)
}

func TestWorkbenchComposerSendsCardsAndPlainTextInOrder(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	a := r.quote("m1", "第二句在那")
	b := r.quote("m2", "第三句收尾")
	a.Text, b.Text, r.p.Main.Text = " 这句什么意思 ", "这句我懂了", "另外今晚吃什么"
	r.a.submitComposer(f, r.key, r.p)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || got[0].Text != "" || got[0].Mode != "start" || got[0].ClientKey == "" {
		t.Fatalf("提交不对：%+v", got)
	}
	parts := got[0].Parts
	if len(parts) != 3 {
		t.Fatalf("应当是两张卡加一段普通文字：%+v", parts)
	}
	for i, want := range []struct{ id, sel, text string }{{"m1", "第二句在那", "这句什么意思"}, {"m2", "第三句收尾", "这句我懂了"}} {
		q := parts[i].Quote
		if q == nil || q.Ref.Event.EventID != want.id || q.Ref.Conversation != r.key || q.Ref.Event.Platform != "local" || q.Text != want.sel || q.Start != 2 || q.End != 4 || parts[i].Text != want.text {
			t.Fatalf("第 %d 块不对：%+v", i+1, parts[i])
		}
	}
	if parts[2].Quote != nil || parts[2].Text != "另外今晚吃什么" {
		t.Fatalf("最后一块应当是不带引用的普通文字：%+v", parts[2])
	}
	if len(r.p.Cards) != 0 || r.p.Main.Text != "" || r.p.Main.Sending {
		t.Fatal("收下之后卡和输入框都该清空")
	}
}

// 引用回复还没进库时，待发气泡就画出引用，不是先一句普通话再跳成引用。
func TestWorkbenchComposerPendingBubbleShowsQuote(t *testing.T) {
	f := &fakeInputs{gate: make(chan struct{})}
	r := newWbRig(t, f)
	r.quote("m1", "第二句在那").Text = "这句什么意思"
	r.a.submitComposer(f, r.key, r.p)
	r.tt.Frame()
	r.tt.Frame()
	pending := r.a.localRun(r.key.Conversation).pending
	if len(pending) != 1 {
		t.Fatalf("应该有一行待发：%+v", pending)
	}
	v := chatMessage(pending[0])
	if len(v.Parts) != 1 || v.Parts[0].Text != "这句什么意思" || v.Parts[0].Quote == nil {
		t.Fatalf("待发气泡该按块画：%+v", v.Parts)
	}
	if q := v.Parts[0].Quote; q.Target != "m1" || q.Name != "编造角色" || q.Snippet != "第二句在那" || q.Gone {
		t.Fatalf("待发气泡的引用不对：%+v", q)
	}
	if _, ok := r.tt.Find("引用 编造角色：点一下回到原消息"); !ok {
		t.Fatalf("待发气泡里没画出引用：%v", r.tt.Texts())
	}
	close(f.gate)
	r.settle()
}

// 普通一句话不走分块，待发气泡还是普通气泡。
func TestWorkbenchComposerPendingPlainHasNoParts(t *testing.T) {
	f := &fakeInputs{gate: make(chan struct{})}
	r := newWbRig(t, f)
	r.p.Main.Text = "只有一句话"
	r.a.submitComposer(f, r.key, r.p)
	r.tt.Frame()
	if pending := r.a.localRun(r.key.Conversation).pending; len(pending) != 1 || len(pending[0].chatParts) != 0 {
		t.Fatalf("普通一句话不该带分块：%+v", pending)
	}
	close(f.gate)
	r.settle()
}

func TestWorkbenchComposerPlainTextKeepsOldShape(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	r.p.Main.Text = "只有一句话"
	r.a.submitComposer(f, r.key, r.p)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || got[0].Text != "只有一句话" || len(got[0].Parts) != 0 {
		t.Fatalf("只有普通文字时应当和旧输入一样用 Text：%+v", got)
	}
}

func TestWorkbenchComposerRejectedKeepsBlocksAndMarksTheBadOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
		on   int // 标在第几块（0 起），-1 是输入区上方
	}{
		{"引用已看不到", fmt.Errorf("第 2 块：%w", api.ErrQuoteTarget), "引用的消息已经看不到了", 1},
		{"空块", fmt.Errorf("第 1 块：%w：没有文字也没有附件", api.ErrPart), "这一块是空的", 0},
		{"附件来源不对", fmt.Errorf("第 2 块：%w：附件媒体凭据只收本会话里的图片", api.ErrPart), "附件媒体凭据只收本会话里的图片", 1},
		{"没有块号", errors.New("后台忙"), "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInputs{err: tc.err}
			r := newWbRig(t, f)
			a, b := r.quote("m1", "甲"), r.quote("m2", "乙")
			a.Text, b.Text, r.p.Main.Text = "甲的回复", "乙的回复", "另外"
			r.a.submitComposer(f, r.key, r.p)
			r.submitted(t)
			if len(r.p.Cards) != 2 || a.Text != "甲的回复" || b.Text != "乙的回复" || r.p.Main.Text != "另外" {
				t.Fatal("被拒时所有块都要原样留着")
			}
			if a.Sending || b.Sending || r.p.Main.Sending || r.a.localRun(r.key.Conversation).busy || r.a.localRun(r.key.Conversation).submitting {
				t.Fatal("被拒后输入区仍然锁着")
			}
			blocks := []*chatui.Block{a, b}
			for i, blk := range blocks {
				if (i == tc.on) != (blk.Err != "") {
					t.Fatalf("原因标错了块：第 %d 块 Err=%q", i+1, blk.Err)
				}
			}
			if tc.on >= 0 && (blocks[tc.on].Err != tc.want || !r.tt.HasText(tc.want)) {
				t.Fatalf("原因不对或没画出来：%q", blocks[tc.on].Err)
			}
			if tc.on < 0 && r.a.localRun(r.key.Conversation).err == "" {
				t.Fatal("认不出是哪一块时该写在输入区上方")
			}
			// 改好那一块再发：这次收下
			f.err = nil
			if tc.on >= 0 {
				blocks[tc.on].Text += "，改过了"
			}
			r.a.localRun(r.key.Conversation).err = ""
			r.a.submitComposer(f, r.key, r.p)
			r.submitted(t)
			if len(r.p.Cards) != 0 {
				t.Fatalf("改好后重发应当收下：%d 张卡还在", len(r.p.Cards))
			}
		})
	}
}

func TestWorkbenchComposerEmptyCardIsMarkedAndNothingIsSent(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	empty := r.quote("m1", "甲")
	r.p.Main.Text = "有字"
	r.a.submitComposer(f, r.key, r.p)
	r.settle()
	if len(f.submissions()) != 0 || empty.Err == "" || len(r.p.Cards) != 1 {
		t.Fatalf("空卡不该发出去，也不该被悄悄丢掉：提交 %d，Err %q", len(f.submissions()), empty.Err)
	}
}

func TestWorkbenchComposerCardAddedWhileSendingSurvives(t *testing.T) {
	f := &fakeInputs{gate: make(chan struct{})}
	r := newWbRig(t, f)
	a := r.quote("m1", "甲")
	a.Text = "甲的回复"
	r.a.submitComposer(f, r.key, r.p)
	r.settle()
	st := r.a.localRun(r.key.Conversation)
	if !st.submitting || !a.Sending {
		t.Fatal("提交还没回音时这张卡该只读")
	}
	late := r.quote("m2", "乙")
	late.Text = "发送期间写的"
	r.a.submitComposer(f, r.key, r.p) // 回音没到又按发送：不能重复提交
	close(f.gate)
	for range 200 {
		r.settle()
		if !st.submitting {
			break
		}
	}
	if len(f.submissions()) != 1 {
		t.Fatalf("重复提交了：%d 次", len(f.submissions()))
	}
	if len(r.p.Cards) != 1 || r.p.Cards[0] != late || late.Text != "发送期间写的" || late.Sending {
		t.Fatalf("发送期间新加的卡该原样留下：%d 张", len(r.p.Cards))
	}
	if _, ok := r.tt.Find("回复引用 1"); !ok {
		t.Fatal("新加的卡没画出来")
	}
}
