package app

// 主人 QQ 私聊审批的端到端验收（规格-主人QQ私聊审批 第七节）：假 SnowLuma 逐帧断言。
// 号、角色、命令都是编的；数据目录只用 t.TempDir()；不碰真的 data/、不连真协议端。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/approval"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

type remoteEnv struct {
	t    *testing.T
	e    *testEnv
	a    *App
	conn *websocket.Conn
	seen []remoteFrame
	ids  int
}

// remoteFrame 是假协议端收到的一条发送帧
type remoteFrame struct {
	Action string
	Target string // 私聊对方或群号
	Text   string
	Raw    string
}

func parseSendFrame(raw string) (remoteFrame, bool) {
	if !isSendAction(raw) {
		return remoteFrame{}, false
	}
	var f struct {
		Action string `json:"action"`
		Params struct {
			UserID  json.Number `json:"user_id"`
			GroupID json.Number `json:"group_id"`
			Message []struct {
				Type string `json:"type"`
				Data struct {
					Text string `json:"text"`
				} `json:"data"`
			} `json:"message"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(raw), &f) != nil {
		return remoteFrame{Raw: raw}, true
	}
	var b strings.Builder
	for _, m := range f.Params.Message {
		b.WriteString(m.Data.Text)
	}
	target := f.Params.UserID.String()
	if f.Params.GroupID.String() != "" && f.Params.GroupID.String() != "0" {
		target = "group:" + f.Params.GroupID.String()
	}
	return remoteFrame{Action: f.Action, Target: target, Text: b.String(), Raw: raw}, true
}

// newRemoteEnv 起一整套能真发的假环境（sendApp「全部齐全」），再把审批开关写进配置。kind 是 sendApp 的场景名。
func newRemoteEnv(t *testing.T, kind string, enabled bool) *remoteEnv {
	t.Helper()
	e, a, _ := sendApp(t, kind)
	// 关着时也写 awayAfterSeconds: 0：要是开关没被认，卡会立刻发出去，“默认关”的测试才抓得到
	patchRemoteConfig(t, e, "  remoteApproval:\n    enabled: "+strconv.FormatBool(enabled)+"\n    awayAfterSeconds: 0\n")
	if kind == "开关关" {
		a.cfg.QQ.GoSend = false // 发送启用条件不齐：出口档位算出来是试运行（同 TestSend57Conditions 的做法）
	}
	conn := startSend(t, e, a)
	return &remoteEnv{t: t, e: e, a: a, conn: conn}
}

// patchRemoteConfig 在 qq: 下面的 goSend 行后面插入几行（配置文件是测试自己写的临时文件）
func patchRemoteConfig(t *testing.T, e *testEnv, block string) {
	t.Helper()
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	marker := "  goSend: true\n"
	if !strings.Contains(s, marker) {
		t.Fatalf("配置里没有 goSend: true：%s", s)
	}
	s = strings.Replace(s, marker, marker+block, 1)
	if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rewriteConfig(t *testing.T, e *testEnv, old, repl string) {
	t.Helper()
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("配置里找不到 %q", old)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(b), old, repl, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ask 在后台发起一张本地审批卡（本地私聊 console:<角色>），返回结局通道
func (r *remoteEnv) ask(id, command string) chan approval.Decision {
	r.t.Helper()
	now := r.e.now
	req := approval.Request{ID: id, CharacterID: testChar, Conversation: "console:" + testChar, ToolCallID: "call-" + id, Tool: "powershell", Title: "运行 PowerShell 命令",
		Subject: command, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(10 * time.Minute).UnixMilli()}
	ch := make(chan approval.Decision, 1)
	go func() { ch <- r.a.approvals.Ask(context.Background(), req, nil) }()
	waitFor(r.t, "审批进入等待", func() bool {
		for _, p := range r.a.approvals.Pending() {
			if p.ID == id {
				return true
			}
		}
		return false
	})
	return ch
}

// drain 把假协议端目前收到的发送帧收进 seen
func (r *remoteEnv) drain() {
	for {
		select {
		case raw := <-r.e.peer.actions:
			if f, ok := parseSendFrame(raw); ok {
				r.seen = append(r.seen, f)
			}
		default:
			return
		}
	}
}

// waitFrame 等一条满足条件的发送帧（从上次取走的位置往后找）
func (r *remoteEnv) waitFrame(what string, pred func(remoteFrame) bool) remoteFrame {
	r.t.Helper()
	var got remoteFrame
	waitFor(r.t, what, func() bool {
		r.drain()
		for i, f := range r.seen {
			if pred(f) {
				got = f
				r.seen = append(r.seen[:i:i], r.seen[i+1:]...)
				return true
			}
		}
		return false
	})
	return got
}

// noFrames 断言一段时间内没有发送帧
func (r *remoteEnv) noFrames(d time.Duration) {
	r.t.Helper()
	time.Sleep(d)
	r.drain()
	if len(r.seen) != 0 {
		r.t.Fatalf("不该有发送帧，收到 %d 条：%+v", len(r.seen), r.seen)
	}
}

var idPattern = func(text string) string {
	i := strings.Index(text, "待批准 ")
	if i < 0 {
		return ""
	}
	rest := []rune(text[i+len("待批准 "):])
	if len(rest) < 4 {
		return ""
	}
	return string(rest[:4])
}

// cardID 等主人私聊里出现这张卡，返回编号
func (r *remoteEnv) cardID(command string) string {
	r.t.Helper()
	f := r.waitFrame("主人私聊收到审批卡", func(f remoteFrame) bool {
		return strings.Contains(f.Text, "待批准 ") && strings.Contains(f.Text, command)
	})
	if f.Target != testOwner {
		r.t.Fatalf("卡发错了人：%+v", f)
	}
	id := idPattern(f.Text)
	if id == "" {
		r.t.Fatalf("卡里没有编号：%q", f.Text)
	}
	// 卡的最后一段（编号说明）也会到；吞掉它
	r.waitFrame("卡的说明段", func(f remoteFrame) bool { return strings.Contains(f.Text, "回复“同意 "+id+"”") })
	return id
}

// say 让假协议端发一条私聊（from 是发言人号）；返回库里这条的事件，等它入库
func (r *remoteEnv) say(from, msgID, text string) {
	r.t.Helper()
	r.e.peer.send(r.t, r.conn, privateFrame(r.e.now, testAccount, msgID, from, text))
	r.stored(msgID)
}

func (r *remoteEnv) stored(msgID string) {
	r.t.Helper()
	conv := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}
	st, _ := r.a.set.For("qq")
	waitFor(r.t, "消息入库", func() bool {
		for _, c := range []string{conv.String(), types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: "201"}.String()} {
			if _, ok, err := st.EventByPlatformID("qq", c, msgID); ok && err == nil {
				return true
			}
		}
		return false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.a.intake.Idle(ctx); err != nil {
		r.t.Fatal(err)
	}
}

func (r *remoteEnv) decided(ch chan approval.Decision, kind string) approval.Decision {
	r.t.Helper()
	select {
	case d := <-ch:
		if d.Kind != kind {
			r.t.Fatalf("结局应是 %s，得到 %+v", kind, d)
		}
		return d
	case <-time.After(60 * time.Second):
		r.t.Fatalf("60 秒内没有 %s 结局", kind)
	}
	return approval.Decision{}
}

func (r *remoteEnv) pending(ch chan approval.Decision) {
	r.t.Helper()
	select {
	case d := <-ch:
		r.t.Fatalf("不该有结局，收到 %+v", d)
	case <-time.After(150 * time.Millisecond):
	}
}

func (r *remoteEnv) nextID() string {
	r.ids++
	return fmt.Sprintf("remote-%d", r.ids)
}

// ownerOrigin 读主人这条回复在库里的来源
func (r *remoteEnv) originOf(msgID string) string {
	r.t.Helper()
	st, _ := r.a.set.For("qq")
	conv := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}.String()
	ev, ok, err := st.EventByPlatformID("qq", conv, msgID)
	if err != nil || !ok {
		r.t.Fatalf("找不到事件 %s：%v", msgID, err)
	}
	return ev.Origin
}

// 主人不在、本地单发到私聊，回“同意 编号”就结算；完整内容在卡里。
func TestQQApprovalLocalApprovedFromQQ(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	ch := r.ask(r.nextID(), "echo 编的命令 > 编的文件.txt")
	id := r.cardIDOpen("echo 编的命令 > 编的文件.txt")
	r.say(testOwner, "92001", "同意 "+id)
	d := r.decided(ch, "approved")
	if d.Via != "qq" {
		t.Fatalf("来源要记 qq：%+v", d)
	}
	r.waitFrame("确认帧", func(f remoteFrame) bool { return strings.Contains(f.Text, "已批准") && f.Target == testOwner })
	if got := r.originOf("92001"); got != history.OriginRemoteApproval {
		t.Fatalf("被截走的回复行要改标来源，现在是 %q", got)
	}
	if len(r.a.approvals.Pending()) != 0 {
		t.Fatal("本地列表要少一张")
	}
	r.noFrames(100 * time.Millisecond)
}

func TestQQApprovalDeniedFromQQ(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	ch := r.ask(r.nextID(), "rm 编的")
	id := r.cardIDOpen("rm 编的")
	r.say(testOwner, "92001", "拒绝 "+id)
	r.decided(ch, "denied")
	r.waitFrame("确认帧", func(f remoteFrame) bool { return strings.Contains(f.Text, "已拒绝") })
}

// 默认关：一帧不发，同意是普通消息（不改标、不结算）。
func TestQQApprovalOffBehavesAsBefore(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", false)
	ch := r.ask(r.nextID(), "echo 编的")
	r.noFrames(300 * time.Millisecond)
	r.say(testOwner, "92001", "同意 1234")
	r.say(testOwner, "92002", "同意")
	r.pending(ch)
	r.noFrames(100 * time.Millisecond)
	if got := r.originOf("92001"); got != "owner" {
		t.Fatalf("关着时回复行不能改标，现在是 %q", got)
	}
}

// 别人（陌生号）回同意、昵称写主人、自己的号的回声、群里的同意：都不结算。
func TestQQApprovalHostileShapes(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	ch := r.ask(r.nextID(), "echo 编的")
	id := r.cardIDOpen("echo 编的")

	// 陌生号（昵称就叫“主人”也一样，身份按号认）
	r.e.peer.send(t, r.conn, fmt.Sprintf(`{"post_type":"message","message_type":"private","time":%d,"self_id":%s,"user_id":201,"message_id":"92101","sender":{"nickname":"主人","card":"%s"},"message":[{"type":"text","data":{"text":"同意 %s"}}]}`, r.e.now.Unix(), testAccount, testOwner, id))
	r.stored("92101")
	// 自己的号发的（message_sent 回声）
	r.e.peer.send(t, r.conn, fmt.Sprintf(`{"post_type":"message_sent","message_type":"private","time":%d,"self_id":%s,"user_id":%s,"target_id":%s,"message_id":"92102","message":[{"type":"text","data":{"text":"同意 %s"}}]}`, r.e.now.Unix(), testAccount, testAccount, testOwner, id))
	// 群里：主人在群里说同意
	r.e.peer.send(t, r.conn, groupFrame(r.e.now, "92103", testOwner, "同意 "+id))
	// 零宽字符
	r.say(testOwner, "92104", "同"+string(rune(0x200b))+"意 "+id)
	// 转发里写同意、夹图
	r.e.peer.send(t, r.conn, fmt.Sprintf(`{"post_type":"message","message_type":"private","time":%d,"self_id":%s,"user_id":%s,"message_id":"92105","sender":{"nickname":"编的甲"},"message":[{"type":"forward","data":{"id":"x"}},{"type":"text","data":{"text":"同意 %s"}}]}`, r.e.now.Unix(), testAccount, testOwner, id))
	r.stored("92105")
	r.pending(ch)
	// 零宽的那条被截走并提示；其余都是普通消息，不发任何审批相关的帧
	r.waitFrame("零宽提示", func(f remoteFrame) bool { return strings.Contains(f.Text, "看不见的字符") })
	time.Sleep(200 * time.Millisecond)
	r.drain()
	for _, f := range r.seen {
		if strings.Contains(f.Text, "已批准") || strings.Contains(f.Text, "已拒绝") {
			t.Fatalf("不该结算：%+v", f)
		}
	}
	// 陌生号、自己、群、转发那几条没有被改标
	if got := r.originOf("92104"); got != history.OriginRemoteApproval {
		t.Fatalf("零宽那条是提示场合，来源 %q", got)
	}
	// 主人自己用对的形状还是能批
	r.say(testOwner, "92106", "同意 "+id)
	r.decided(ch, "approved")
}

// 迟到的回复：过期之后不结算，也不转到新单。
func TestQQApprovalLateReplyIgnored(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	chA := r.ask(r.nextID(), "echo 甲")
	idA := r.cardIDOpen("echo 甲")
	r.e.setNow(r.e.now.Add(6 * time.Minute)) // 过了 5 分钟，本机的卡（10 分钟）还在
	r.say(testOwner, "92201", "同意 "+idA)
	r.pending(chA)
	r.waitFrame("过期说明", func(f remoteFrame) bool { return strings.Contains(f.Text, "过期") })
	// 新单来了，迟到的 A 号和裸同意都批不了 B
	chB := r.ask(r.nextID(), "echo 乙")
	idB := r.cardIDOpen("echo 乙")
	r.say(testOwner, "92202", "同意 "+idA)
	r.say(testOwner, "92203", "同意")
	r.pending(chB)
	r.pending(chA)
	r.say(testOwner, "92204", "同意 "+idB)
	r.decided(chB, "approved")
}

// 几单同时等：裸同意只追问，带号才结算那一单。
func TestQQApprovalParallelAsksFirst(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	chA := r.ask(r.nextID(), "echo 一号")
	idA := r.cardIDOpen("echo 一号")
	chB := r.ask(r.nextID(), "echo 二号")
	idB := r.cardIDOpen("echo 二号")
	r.say(testOwner, "92301", "同意")
	r.waitFrame("追问", func(f remoteFrame) bool { return strings.Contains(f.Text, idA) && strings.Contains(f.Text, idB) })
	r.pending(chA)
	r.pending(chB)
	r.say(testOwner, "92302", "拒绝 "+idB)
	r.decided(chB, "denied")
	r.pending(chA)
}

// 本机先答：QQ 收到结局帧，之后的回复不再结算。
func TestQQApprovalLocalFirstUpdatesQQ(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	ch := r.ask("remote-local", "echo 编的")
	id := r.cardIDOpen("echo 编的")
	if ok, err := r.a.approvals.Decide("remote-local", "once", ""); !ok || err != nil {
		t.Fatal(ok, err)
	}
	r.decided(ch, "approved")
	r.waitFrame("本机结局帧", func(f remoteFrame) bool {
		return strings.Contains(f.Text, "本机已批准") && strings.Contains(f.Text, id)
	})
	r.say(testOwner, "92401", "同意 "+id)
	r.waitFrame("已结束说明", func(f remoteFrame) bool { return strings.Contains(f.Text, "已经结束") })
	if len(r.a.approvals.Pending()) != 0 {
		t.Fatal("没有别的单")
	}
}

// 发不出去：不开票，本机的卡照常等。协议端明确回失败（一段都没发出去）：之后的“同意”是普通消息；
// 回包是 retcode 非零但没写 failed（结果未知，可能已发出）：留墓碑，之后的“同意”被截走并说明，不进模型。
func TestQQApprovalSendFailed(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		result     int32
		wantOrigin string
	}{
		"明确失败": {1, "owner"},
		"结果未知": {2, history.OriginRemoteApproval},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, a, peer := sendApp(t, "全部齐全")
			patchRemoteConfig(t, e, "  remoteApproval:\n    enabled: true\n    awayAfterSeconds: 0\n")
			peer.result.Store(c.result)
			conn := startSend(t, e, a)
			r := &remoteEnv{t: t, e: e, a: a, conn: conn}
			ch := r.ask(r.nextID(), "echo 编的")
			waitFor(t, "试过一次发送", func() bool { return peer.sends.Load() >= 1 })
			time.Sleep(300 * time.Millisecond)
			rl := a.remote.relay.Load()
			if rl.OpenCount() != 0 {
				t.Fatal("没送达不能开票")
			}
			r.say(testOwner, "92501", "同意")
			r.pending(ch)
			if got := r.originOf("92501"); got != c.wantOrigin {
				t.Fatalf("来源应是 %q，实际 %q", c.wantOrigin, got)
			}
		})
	}
}

// 档位是试运行（发送启用条件不齐）：不发帧，本机的卡照常等。
func TestQQApprovalDryRunNotSent(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "开关关", true)
	ch := r.ask(r.nextID(), "echo 编的")
	time.Sleep(400 * time.Millisecond)
	r.noFrames(50 * time.Millisecond)
	r.pending(ch)
	if r.a.remote.relay.Load().OpenCount() != 0 {
		t.Fatal("试运行没有发出去，不能开票")
	}
}

// 等待期间配置被关掉：立刻作废，之后的回复不结算。
func TestQQApprovalTurnedOffWhileWaiting(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	ch := r.ask(r.nextID(), "echo 编的")
	id := r.cardIDOpen("echo 编的")
	rewriteConfig(t, r.e, "    enabled: true\n", "    enabled: false\n")
	r.say(testOwner, "92601", "同意 "+id)
	r.pending(ch)
	if got := r.originOf("92601"); got != "owner" {
		t.Fatalf("关了以后回复是普通消息，来源 %q", got)
	}
	rewriteConfig(t, r.e, "    enabled: false\n", "    enabled: true\n")
	r.say(testOwner, "92602", "同意 "+id)
	r.pending(ch)
}

// 主人号变了、主人号等于账号：都按关。
func TestQQApprovalBindingChanges(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*testing.T, *testEnv){
		"主人号变了": func(t *testing.T, e *testEnv) { rewriteConfig(t, e, "  owner: "+testOwner+"\n", "  owner: 999\n") },
		"主人号等于账号": func(t *testing.T, e *testEnv) {
			rewriteConfig(t, e, "  owner: "+testOwner+"\n", "  owner: "+testAccount+"\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRemoteEnv(t, "全部齐全", true)
			ch := r.ask(r.nextID(), "echo 编的")
			id := r.cardIDOpen("echo 编的")
			change(t, r.e)
			r.say(testOwner, "92701", "同意 "+id)
			r.pending(ch)
			if got := r.originOf("92701"); got != "owner" {
				t.Fatalf("绑定不成立时回复是普通消息，来源 %q", got)
			}
		})
	}
}

// 绑定不成立时根本不发卡。
func TestQQApprovalNoCardWhenBindingBroken(t *testing.T) {
	t.Parallel()
	e, a, _ := sendApp(t, "全部齐全")
	patchRemoteConfig(t, e, "  remoteApproval:\n    enabled: true\n    awayAfterSeconds: 0\n")
	rewriteConfig(t, e, "  owner: "+testOwner+"\n", "  owner: "+testAccount+"\n")
	conn := startSend(t, e, a)
	r := &remoteEnv{t: t, e: e, a: a, conn: conn}
	ch := r.ask(r.nextID(), "echo 编的")
	r.noFrames(400 * time.Millisecond)
	r.pending(ch)
}

// 本机的卡在不是本地私聊的会话（比如 QQ 来源）里，不发 QQ。
func TestQQApprovalOnlyLocalPrivateCards(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	for i, conv := range []string{"qq:private:" + testAccount + ":" + testOwner, "qq:group:" + testGroup, "console:" + testChar + ":topic:selftest-" + testChar, "console:别的角色"} {
		now := r.e.now
		req := approval.Request{ID: fmt.Sprintf("other-%d", i), CharacterID: testChar, Conversation: conv, Tool: "powershell", Title: "t", Subject: "echo",
			CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(10 * time.Minute).UnixMilli()}
		go r.a.approvals.Ask(context.Background(), req, nil)
	}
	waitFor(t, "都进了等待", func() bool { return len(r.a.approvals.Pending()) == 4 })
	r.noFrames(400 * time.Millisecond)
}

// 运行中的主人号就等于接收账号（自己的号发的话和主人的话分不开）：不发卡。
func TestQQApprovalOwnerEqualsAccount(t *testing.T) {
	t.Parallel()
	e, a, _ := sendApp(t, "全部齐全")
	patchRemoteConfig(t, e, "  remoteApproval:\n    enabled: true\n    awayAfterSeconds: 0\n")
	rewriteConfig(t, e, "  owner: "+testOwner+"\n", "  owner: "+testAccount+"\n")
	a.cfg.QQ.Owner = config.QQID(testAccount) // 运行中的名册也是这个号，才只剩“等于账号”这一道门在挡
	conn := startSend(t, e, a)
	r := &remoteEnv{t: t, e: e, a: a, conn: conn}
	ch := r.ask(r.nextID(), "echo 编的")
	r.noFrames(400 * time.Millisecond)
	r.pending(ch)
}

// 配置里的主人号和运行中的名册对不上（改了主人号、还没重启接收）：不给新主人号发卡，旧主人号也不发。
func TestQQApprovalNoCardWhenOwnerChanged(t *testing.T) {
	t.Parallel()
	e, a, _ := sendApp(t, "全部齐全")
	patchRemoteConfig(t, e, "  remoteApproval:\n    enabled: true\n    awayAfterSeconds: 0\n")
	rewriteConfig(t, e, "  owner: "+testOwner+"\n", "  owner: 999\n")
	conn := startSend(t, e, a)
	r := &remoteEnv{t: t, e: e, a: a, conn: conn}
	ch := r.ask(r.nextID(), "echo 编的")
	r.noFrames(400 * time.Millisecond)
	r.pending(ch)
}

// ---- 端到端：从真实闸门 Gate.Check 经审批、假 SnowLuma 到工具是否执行（独立复核补，规格第七节）----

type gateResult struct {
	block  bool
	reason string
}

// checkTool 在后台走真实闸门 Check（本地私聊、主人 × 主窗口）；返回结果通道。执行计数由调用方按“没拦下才执行”记。
func (r *remoteEnv) checkTool(workdir, tool, argsJSON string) chan gateResult {
	r.t.Helper()
	args, err := jsjson.Parse(argsJSON)
	if err != nil {
		r.t.Fatal(err)
	}
	identity := kernel.Invocation{Scope: kernel.Scope{CharacterID: testChar, Conversation: "console:" + testChar, ScopeID: "console", Platform: "local", Channel: "console", AccountID: "fixture-desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "fixture-authority", AuthorityRevision: 1}, InputID: "in", RootID: "root", RunID: "run", StepID: "step", ToolID: "tool", Generation: 1, Kind: kernel.KindForeground}
	r.a.approvalGate.Now, r.a.approvals.Now = r.a.now, r.a.now // 闸门、审批、投影的巡检用同一个测试时钟
	ch := make(chan gateResult, 1)
	go func() {
		block, reason := r.a.approvalGate.Check(context.Background(), workdir, testChar, policy.Audience{Channel: "console", PrivateOk: true}, "console:"+testChar,
			approval.Call{Identity: identity, ID: "call-e2e-" + tool, Name: tool, Args: args})
		ch <- gateResult{block, reason}
	}()
	return ch
}

// cardIDOpen 等这张卡发完、票开了（回复才会被认），返回编号
func (r *remoteEnv) cardIDOpen(contains string) string {
	r.t.Helper()
	id := r.cardID(contains)
	// 卡片帧先到、过期时间等发送返回才定：等确认送达再往下（不然随后拨钟会被算进过期起点）
	// 等到没有票还在发、至少有一张开了：回复早于确认送达会被当成普通消息，机器忙时才露出来
	waitFor(r.t, "票开了", func() bool {
		rl := r.a.remote.relay.Load()
		return rl.DeliveredCount() > 0 && rl.DeliveredCount() == rl.OpenCount()
	})
	return id
}

func (r *remoteEnv) gateDone(ch chan gateResult) gateResult {
	r.t.Helper()
	select {
	case g := <-ch:
		return g
	case <-time.After(60 * time.Second):
		r.t.Fatal("闸门 60 秒内没有结果")
	}
	return gateResult{}
}

func (r *remoteEnv) gatePending(ch chan gateResult) {
	r.t.Helper()
	select {
	case g := <-ch:
		r.t.Fatalf("闸门不该有结果，收到 %+v", g)
	case <-time.After(150 * time.Millisecond):
	}
}

// 回“同意”：工具恰好执行一次，卡里有完整内容；之后再回同意也不会再执行。
func TestQQApprovalGateEndToEndApprovedRunsOnce(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	wd := t.TempDir()
	execs := 0
	ch := r.checkTool(wd, "write", `{"path":"编的文件.txt","content":"编的内容 TAIL_MARK"}`)
	id := r.cardIDOpen("TAIL_MARK") // 卡里有完整内容（含写入内容的预览）
	r.say(testOwner, "93001", "同意 "+id)
	g := r.gateDone(ch)
	if !g.block {
		execs++
	}
	if execs != 1 {
		t.Fatalf("批准后工具要执行恰好一次：%+v", g)
	}
	r.say(testOwner, "93002", "同意 "+id)
	r.pendingNone()
	if execs != 1 || len(r.a.approvals.Pending()) != 0 {
		t.Fatal("重复同意不能再执行")
	}
}

func (r *remoteEnv) pendingNone() {
	r.t.Helper()
	time.Sleep(100 * time.Millisecond)
	if n := len(r.a.approvals.Pending()); n != 0 {
		r.t.Fatalf("不该有等待中的审批，有 %d 张", n)
	}
}

// 回“拒绝”：工具零执行，模型收到“主人拒绝了”的说明。
func TestQQApprovalGateEndToEndDeniedNotExecuted(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	wd := t.TempDir()
	ch := r.checkTool(wd, "write", `{"path":"编的文件.txt","content":"x"}`)
	id := r.cardIDOpen("编的文件.txt")
	r.say(testOwner, "93101", "拒绝 "+id)
	g := r.gateDone(ch)
	if !g.block || !strings.Contains(g.reason, "拒绝") {
		t.Fatalf("拒绝要拦下并告诉模型：%+v", g)
	}
}

// 过期：工具零执行；迟到的同意不执行，QQ 收到说明。
func TestQQApprovalGateEndToEndExpiredNotExecuted(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	r.a.approvalGate.ApprovalMinutes = 0.012 // 约 0.7 秒
	wd := t.TempDir()
	ch := r.checkTool(wd, "write", `{"path":"编的文件.txt","content":"x"}`)
	id := r.cardID("编的文件.txt") // 卡很快就过期，不等票开
	g := r.gateDone(ch)
	if !g.block || !strings.Contains(g.reason, "过期") {
		t.Fatalf("过期要拦下并告诉模型：%+v", g)
	}
	r.say(testOwner, "93201", "同意 "+id)
	r.waitFrame("迟到回复的说明", func(f remoteFrame) bool {
		return strings.Contains(f.Text, id) && !strings.Contains(f.Text, "待批准")
	})
	r.pendingNone()
}

// 没声明的工具参数超过上限：QQ 不发卡、只告诉主人回本机；QQ 的“同意”不结算，本机点了才执行。
func TestQQApprovalGateEndToEndTruncatedArgsStayLocal(t *testing.T) {
	t.Parallel()
	r := newRemoteEnv(t, "全部齐全", true)
	long := strings.Repeat("无害说明", 1600) + "TAIL_DANGEROUS"
	ch := r.checkTool(t.TempDir(), "some_mcp_tool", `{"payload":"`+long+`"}`)
	r.gatePending(ch)
	r.waitFrame("只告诉主人回电脑处理", func(f remoteFrame) bool {
		return strings.Contains(f.Text, "回电脑上处理") && !strings.Contains(f.Text, "待批准")
	})
	r.noFrames(200 * time.Millisecond)
	r.say(testOwner, "93301", "同意")
	r.gatePending(ch)
	pend := r.a.approvals.Pending()
	if len(pend) != 1 {
		t.Fatalf("本机应有一张卡：%d", len(pend))
	}
	if ok, err := r.a.approvals.Decide(pend[0].ID, "once", ""); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if g := r.gateDone(ch); g.block {
		t.Fatalf("本机批准后应放行：%+v", g)
	}
}
