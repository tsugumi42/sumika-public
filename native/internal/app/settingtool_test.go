package app

// 设置工具（规格-客户端自知与设置工具 第十一节）的装配验收：模型收到工具 → 预览 → 审批卡确认 → 写入 → 下一轮读到新值 → 回滚 → 再确认 → 恢复。
// 全程假网关、假地理编码服务器、临时数据目录；不碰真实 data，不发 QQ 消息。名字、地点、号码都是编的。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"errors"
	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/settingtool"
	"sumika-native/internal/turn"
)

// dynLLM 是按上一次请求内容现算下一个回复的假网关：apply 的提案号要等 preview 的结果出来才知道
type dynLLM struct {
	e     *testEnv
	steps []func(prev string) llmtest.Reply
}

func (d *dynLLM) handler() http.HandlerFunc {
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			llmtest.Handler(llmtest.Script{Models: models}, new([]string), new(sync.Mutex))(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		d.e.mu.Lock()
		prev := ""
		if n := len(d.e.bodies); n > 0 {
			prev = d.e.bodies[n-1]
		}
		d.e.bodies = append(d.e.bodies, string(body))
		n := len(d.e.bodies)
		d.e.mu.Unlock()
		reply := llmtest.Text("好的")
		if n-1 < len(d.steps) {
			reply = d.steps[n-1](string(body))
		}
		_ = prev
		r.Body = io.NopCloser(bytes.NewReader(body))
		llmtest.Handler(llmtest.Script{Models: models, Reply: reply}, new([]string), new(sync.Mutex))(w, r)
	}
}

var (
	proposalRe = regexp.MustCompile(`p-[0-9A-Z]{26}`)
	changeRe   = regexp.MustCompile(`c-[0-9A-Z]{26}`)
)

// lastMatch 取请求体里最后一个匹配（工具结果在消息列表的最后）
func lastMatch(t *testing.T, re *regexp.Regexp, body string) string {
	t.Helper()
	all := re.FindAllString(body, -1)
	if len(all) == 0 {
		t.Fatalf("请求里没有 %s：%.400s", re, body)
	}
	return all[len(all)-1]
}

// approver 在后台盯着审批卡，一出现就按 choice 回答；记下每张卡
type approver struct {
	mu    sync.Mutex
	cards []approval.Request
	done  map[string]bool
	stop  chan struct{}
}

func autoDecide(a *App, choice string) *approver {
	ap := &approver{done: map[string]bool{}, stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-ap.stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			for _, s := range a.approvals.Snapshot() {
				if s.Decision != nil || !strings.HasPrefix(s.Request.ID, "setting:") {
					continue
				}
				ap.mu.Lock()
				if ap.done[s.Request.ID] {
					ap.mu.Unlock()
					continue
				}
				ap.done[s.Request.ID] = true
				ap.cards = append(ap.cards, s.Request)
				ap.mu.Unlock()
				a.approvals.Decide(s.Request.ID, choice, "不对")
			}
		}
	}()
	return ap
}
func (ap *approver) Close() { close(ap.stop) }
func (ap *approver) Cards() []approval.Request {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	return append([]approval.Request(nil), ap.cards...)
}

func settingEnv(t *testing.T, d *dynLLM) (*testEnv, *lifeFake, *App) {
	t.Helper()
	e := newEnv(t)
	d.e = e
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)
	e.llm = srv
	fake := newLifeFake(t)
	fake.attach(e, "")
	a := e.open()
	t.Cleanup(func() { a.Close() })
	return e, fake, a
}

func settingToolNames(names []string) (n int) {
	for _, x := range names {
		switch x {
		case "preview_setting_change", "apply_setting_change", "rollback_setting_change", "list_setting_changes", "cancel_setting_change":
			n++
		}
	}
	return n
}

func readConfig(t *testing.T, e *testEnv) config.Config {
	t.Helper()
	c, err := config.LoadIn(e.root, e.data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func configBytes(t *testing.T, e *testEnv) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.data, "config", "sumika.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 完整路径：本地私聊和 QQ 主人私聊各走一遍
func TestSettingToolFullPath(t *testing.T) {
	t.Parallel()
	for _, scene := range []string{"local", "qq"} {
		t.Run(scene, func(t *testing.T) {
			t.Parallel()
			var apply1, apply2 string
			d := &dynLLM{}
			d.steps = []func(string) llmtest.Reply{
				func(string) llmtest.Reply {
					return llmtest.Call("t1", "preview_setting_change", `{"key":"location","value":"编的新城","reason":"主人说搬家了"}`)
				},
				func(b string) llmtest.Reply {
					return llmtest.Call("t2", "apply_setting_change", `{"proposal_id":"`+lastMatch(t, proposalRe, b)+`"}`)
				},
				func(b string) llmtest.Reply {
					apply1 = lastMatch(t, changeRe, b)
					return llmtest.Call("t3", "inspect_client", `{"keys":["location"]}`)
				},
				func(string) llmtest.Reply { return llmtest.Call("t4", "list_setting_changes", `{}`) },
				func(string) llmtest.Reply {
					return llmtest.Call("t5", "rollback_setting_change", `{"change_id":"`+apply1+`"}`)
				},
				func(b string) llmtest.Reply {
					return llmtest.Call("t6", "apply_setting_change", `{"proposal_id":"`+lastMatch(t, proposalRe, b)+`"}`)
				},
				func(b string) llmtest.Reply {
					apply2 = lastMatch(t, changeRe, b)
					return llmtest.Text("改好又恢复了")
				},
			}
			e, fake, a := settingEnv(t, d)
			_ = fake
			ap := autoDecide(a, "once")
			defer ap.Close()
			before := readConfig(t, e)
			if before.Location.Name != lifeFull {
				t.Fatalf("起始所在地：%+v", before.Location)
			}
			var out turn.Outcome
			if scene == "local" {
				out = <-a.hub.Send(testChar, "我搬到编的新城了，把所在地改一下")
			} else {
				in, aud := qqClientIdentity(a)
				out = <-a.hub.Respond(turn.Input{Identity: in, CharacterID: a.character, Conversation: in.Scope.Conversation, Scope: in.Scope.ScopeID, Audience: aud, Text: "我搬到编的新城了，把所在地改一下"})
			}
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			bodies := e.bodiesCopy()
			if len(bodies) != 7 {
				t.Fatalf("要 7 次请求，实际 %d", len(bodies))
			}
			if n := settingToolNames(toolNames(t, bodies[0])); n != 5 {
				t.Fatalf("模型该收到 5 个设置工具，实际 %d：%v", n, toolNames(t, bodies[0]))
			}
			// 预览结果进了下一轮：旧值、新值、影响
			for _, want := range []string{lifeFull, "编的新城，编的省，编的国", "还没有写入"} {
				if !strings.Contains(bodies[1], want) {
					t.Errorf("预览结果缺 %q", want)
				}
			}
			// 写入后下一轮读到新值（inspect_client），回滚前后的所在地各自对
			if !strings.Contains(bodies[3], "编的新城，编的省，编的国") || !strings.Contains(bodies[3], `writable\":true`) || strings.Count(bodies[3], `city\":\"编的新城\"`) != 2 {
				t.Errorf("写入后 inspect_client 该读到新值且标可写：%.600s", bodies[3])
			}
			if !strings.Contains(bodies[4], "改动记录") || !strings.Contains(bodies[4], apply1) || !strings.Contains(bodies[4], "因为") {
				t.Errorf("记录列表：%.600s", bodies[4])
			}
			if !strings.Contains(bodies[5], lifeFull) || !strings.Contains(bodies[5], "新值") {
				t.Errorf("回滚预览该写明恢复到旧所在地：%.600s", bodies[5])
			}
			after := readConfig(t, e)
			if after.Location.Name != lifeFull || after.Location.CityName != lifeCity || *after.Location.Latitude != 31.26 {
				t.Fatalf("回滚后该恢复成旧所在地：%+v", after.Location)
			}
			// 其余设置没被动
			if after.QQ.Owner != before.QQ.Owner || after.Calendar != before.Calendar || !reflect.DeepEqual(after.FreeAPI, before.FreeAPI) {
				t.Fatalf("回滚动了别的设置")
			}
			// 两张卡：第一张新值，第二张写明是回滚；卡片放在主人的本地私聊里
			cards := ap.Cards()
			if len(cards) != 2 {
				t.Fatalf("要两张卡，实际 %d", len(cards))
			}
			if cards[0].Conversation != "console:"+testChar || cards[1].Conversation != "console:"+testChar {
				t.Fatalf("卡片该放在本地私聊：%q %q", cards[0].Conversation, cards[1].Conversation)
			}
			wants := []string{"旧值：" + lifeFull, "新值：编的新城，编的省，编的国", "影响：", "生效：立即", "她的理由：主人说搬家了"}
			if scene == "local" { // 本地发的话入库有 seq；测试里直接 Respond 的 QQ 输入没有入库，取不到来源原话
				wants = append(wants, "你说的：我搬到编的新城了")
			}
			for _, want := range wants {
				if !strings.Contains(cards[0].Preview.Text, want) {
					t.Errorf("第一张卡缺 %q：\n%s", want, cards[0].Preview.Text)
				}
			}
			if !strings.Contains(cards[1].Preview.Text, "这是回滚") {
				t.Errorf("第二张卡：%s", cards[1].Preview.Text)
			}
			if scene == "qq" && !strings.Contains(strings.Join(cards[0].Notes, ""), "QQ 私聊") {
				t.Errorf("QQ 来的卡片要标明来源：%v", cards[0].Notes)
			}
			// 记录：重启（重新打开记录文件）后能查，两次改动都在，旧值新值对
			changes, bad, err := settingtool.NewLog(a.paths.SettingChangesFile()).All()
			if err != nil || bad != 0 || len(changes) != 2 {
				t.Fatalf("记录：%d 条 bad=%d err=%v", len(changes), bad, err)
			}
			if changes[0].Outcome != settingtool.OutcomeApplied || changes[1].RollbackOf != apply1 || changes[1].Change != apply2 || (scene == "local" && changes[0].Source == "") {
				t.Fatalf("记录内容：%+v", changes)
			}
			wantPlace := map[string]string{"local": "本地私聊", "qq": "QQ 私聊"}[scene]
			if changes[0].Place != wantPlace {
				t.Fatalf("记录的场合：%q", changes[0].Place)
			}
		})
	}
}

// 不批准就不写：拒绝时配置文件字节不变，下一轮请求里是“没有改”
func TestSettingToolDeniedLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	d := &dynLLM{}
	d.steps = []func(string) llmtest.Reply{
		func(string) llmtest.Reply {
			return llmtest.Call("t1", "preview_setting_change", `{"key":"linkReader.expand.maxLinks","value":7}`)
		},
		func(b string) llmtest.Reply {
			return llmtest.Call("t2", "apply_setting_change", `{"proposal_id":"`+lastMatch(t, proposalRe, b)+`"}`)
		},
		func(string) llmtest.Reply { return llmtest.Text("好吧") },
	}
	e, _, a := settingEnv(t, d)
	ap := autoDecide(a, "deny")
	defer ap.Close()
	before := configBytes(t, e)
	if out := <-a.hub.Send(testChar, "把每条消息最多展开的链接数改成 7"); out.Err != nil {
		t.Fatal(out.Err)
	}
	bodies := e.bodiesCopy()
	if len(bodies) != 3 || !strings.Contains(bodies[2], "拒绝了，没有改") {
		t.Fatalf("拒绝的结果该进下一轮：%d %.500s", len(bodies), bodies[len(bodies)-1])
	}
	if !bytes.Equal(before, configBytes(t, e)) {
		t.Fatal("拒绝后配置文件变了")
	}
	if len(ap.Cards()) != 1 || !strings.Contains(ap.Cards()[0].Preview.Text, "新值：7") {
		t.Fatalf("卡片：%+v", ap.Cards())
	}
}

// 链接键走 setLinkSettings：写入后运行值也跟着变（下一条消息使用），别的键不动
func TestSettingToolLinkKeyUsesLinkService(t *testing.T) {
	t.Parallel()
	d := &dynLLM{}
	d.steps = []func(string) llmtest.Reply{
		func(string) llmtest.Reply {
			return llmtest.Call("t1", "preview_setting_change", `{"key":"linkReader.expand.maxLinks","value":7}`)
		},
		func(b string) llmtest.Reply {
			return llmtest.Call("t2", "apply_setting_change", `{"proposal_id":"`+lastMatch(t, proposalRe, b)+`"}`)
		},
		func(string) llmtest.Reply { return llmtest.Text("改好了") },
	}
	e, _, a := settingEnv(t, d)
	ap := autoDecide(a, "once")
	defer ap.Close()
	before := readConfig(t, e)
	if out := <-a.hub.Send(testChar, "把每条消息最多展开的链接数改成 7"); out.Err != nil {
		t.Fatal(out.Err)
	}
	after := readConfig(t, e)
	if after.LinkReader.Expand.MaxLinks != 7 || a.currentLinks().Values.Expand.MaxLinks != 7 {
		t.Fatalf("保存值和运行值都该是 7：%d %d", after.LinkReader.Expand.MaxLinks, a.currentLinks().Values.Expand.MaxLinks)
	}
	if after.Location.Name != before.Location.Name || after.LinkReader.Expand.BareBiliID != before.LinkReader.Expand.BareBiliID {
		t.Fatal("动了别的键")
	}
}

// 谁能用：群里（含主人发言）、别人的私聊，工具看不见；伪造主人身份的别人私聊看得见但直接调用被拒，没有卡片、没有写入
func TestSettingToolScenes(t *testing.T) {
	t.Parallel()
	for _, sc := range lifeScenes() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			d := &dynLLM{}
			d.steps = []func(string) llmtest.Reply{
				func(string) llmtest.Reply {
					return llmtest.Call("t1", "preview_setting_change", `{"key":"location","value":"","reason":"试试"}`)
				},
				func(string) llmtest.Reply {
					return llmtest.Call("t2", "apply_setting_change", `{"proposal_id":"p-00000000000000000000000000"}`)
				},
				func(string) llmtest.Reply { return llmtest.Text("好") },
			}
			e, _, a := settingEnv(t, d)
			ap := autoDecide(a, "once")
			defer ap.Close()
			before := configBytes(t, e)
			out := <-a.hub.Respond(sc.input(a))
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			bodies := e.bodiesCopy()
			visible := settingToolNames(toolNames(t, bodies[0]))
			isGroup := strings.Contains(sc.name, "群")
			if isGroup && visible != 0 {
				t.Fatalf("群里不该看见设置工具：%d", visible)
			}
			if strings.Contains(sc.name, "QQ 主人私聊") {
				if visible != 5 {
					t.Fatalf("QQ 主人私聊该有 5 个设置工具：%d", visible)
				}
				return
			}
			if len(bodies) >= 2 && strings.Contains(bodies[1], "已改") {
				t.Fatalf("不该改：%.400s", bodies[1])
			}
			if !bytes.Equal(before, configBytes(t, e)) {
				t.Fatal("配置文件变了")
			}
			if len(ap.Cards()) != 0 {
				t.Fatalf("不该发卡：%d", len(ap.Cards()))
			}
			if !isGroup && visible > 0 && len(bodies) >= 2 && !strings.Contains(bodies[1], "当前身份不能改设置") {
				t.Fatalf("伪造身份的私聊预览该被拒：%.500s", bodies[1])
			}
		})
	}
}

// 形状 × 入口：认证函数对缺、空、0、类型错、机器人号、别的对端、别的账号、别的角色、子代理、群、读配置失败、主人号变了，每格都拒
func TestSettingToolAuthorizeShapes(t *testing.T) {
	t.Parallel()
	d := &dynLLM{}
	e, _, a := settingEnv(t, d)
	_ = e
	h := settingHost{a}
	in, aud := qqClientIdentity(a)
	// 真实的调用在宿主里登记后才算数：伪造的调用（没有登记）整体拒绝
	if _, err := h.Authorize(context.Background(), in, aud); err == nil {
		t.Fatal("没有登记的调用不该通过")
	}
	// 登记的调用 + 各种形状：用 clientInfoOwner 的判法直接对（它和 Authorize 共用），再叠 QQ 绑定
	if !a.clientInfoOwner(in, aud) || !a.lifeQQBindingCurrent(in) {
		t.Fatal("真主人该通过身份形状")
	}
	cases := map[string]func(*kernel.Invocation){
		"缺身份":  func(i *kernel.Invocation) { i.Scope.ActorID = "" },
		"0":    func(i *kernel.Invocation) { i.Scope.ActorID = "0" },
		"类型错":  func(i *kernel.Invocation) { i.Scope.ActorKind = "human" },
		"机器人号": func(i *kernel.Invocation) { i.Scope.ActorID = a.account },
		"别的对端": func(i *kernel.Invocation) { i.Scope.Conversation = "qq:private:" + a.account + ":503" },
		"别的账号": func(i *kernel.Invocation) { i.Scope.AccountID = "504" },
		"别的角色": func(i *kernel.Invocation) { i.Scope.CharacterID = "other" },
		"子代理":  func(i *kernel.Invocation) { i.Kind = kernel.KindChild },
		"有父运行": func(i *kernel.Invocation) { i.ParentRunID = "run-x" },
		"QQ 群": func(i *kernel.Invocation) { i.Scope.Scene = "group" },
	}
	for name, mut := range cases {
		x, y := in, aud
		mut(&x)
		y.Conversation = x.Scope.Conversation
		if a.clientInfoOwner(x, y) {
			t.Errorf("%s：不该通过", name)
		}
	}
	// 主人号变了：配置文件里的主人换了，旧主人立刻不行（读配置失败也一样）
	e.configure(lifeYAML + "\n")
	cfgPath := filepath.Join(e.data, "config", "sumika.yml")
	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, bytes.ReplaceAll(raw, []byte(testOwner), []byte("201")), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.lifeQQBindingCurrent(in) {
		t.Fatal("主人号变了之后旧主人不该通过")
	}
	if err := os.WriteFile(cfgPath, []byte("::坏的 yaml\n  - ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.lifeQQBindingCurrent(in) {
		t.Fatal("读配置失败不该通过")
	}
}

// 配置在预览之后被设置页改过：apply 冲突，不写
func TestSettingToolConflictAfterPreview(t *testing.T) {
	t.Parallel()
	var proposal string
	d := &dynLLM{}
	var e *testEnv
	d.steps = []func(string) llmtest.Reply{
		func(string) llmtest.Reply {
			return llmtest.Call("t1", "preview_setting_change", `{"key":"linkReader.expand.maxLinks","value":7}`)
		},
		func(b string) llmtest.Reply {
			proposal = lastMatch(t, proposalRe, b)
			// 模型想 apply 之前，设置页改了一项别的设置（版本变了）
			raw := configBytes(t, e)
			if err := os.WriteFile(filepath.Join(e.data, "config", "sumika.yml"), append(raw, []byte("\n# 界面改过\n")...), 0o600); err != nil {
				t.Error(err)
			}
			return llmtest.Call("t2", "apply_setting_change", `{"proposal_id":"`+proposal+`"}`)
		},
		func(string) llmtest.Reply { return llmtest.Text("冲突了") },
	}
	var a *App
	e, _, a = settingEnv(t, d)
	ap := autoDecide(a, "once")
	defer ap.Close()
	if out := <-a.hub.Send(testChar, "改成 7"); out.Err != nil {
		t.Fatal(out.Err)
	}
	bodies := e.bodiesCopy()
	if len(bodies) != 3 || !strings.Contains(bodies[2], "被改过") {
		t.Fatalf("该报冲突：%.500s", bodies[len(bodies)-1])
	}
	if got := readConfig(t, e).LinkReader.Expand.MaxLinks; got == 7 {
		t.Fatal("冲突时不该写")
	}
	if len(ap.Cards()) != 0 {
		t.Fatal("冲突在发卡前就该发现，不该打扰主人")
	}
}

// 设置页的所在地服务（搜候选、读、写、清空）：和设置工具走同一个写入；版本冲突如实报；写进配置的是规范结构，别的键不动
func TestLocationSettingsServiceForDesktop(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	fake.attach(e, "")
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	var w api.LocationSettings = &d.Reader
	ctx := context.Background()
	// 候选：假编码服务器把问的词当名字回；候选带“群里说到哪一级”的说明
	places, err := w.SearchPlaces(ctx, "编的新城")
	if err != nil || len(places) != 1 || places[0].Label != "编的新城，编的省，编的国" || !strings.Contains(places[0].Detail, "群里说到：编的新城") {
		t.Fatalf("候选：%+v %v", places, err)
	}
	v, err := w.Location(ctx)
	if err != nil || v.Location.Name != lifeFull {
		t.Fatalf("读：%+v %v", v, err)
	}
	before := readConfig(t, e)
	loc := places[0].Location
	nv, err := w.SetLocation(ctx, &loc, v.Version)
	if err != nil || nv.Location.Name != "编的新城，编的省，编的国" || nv.Version == v.Version {
		t.Fatalf("写：%+v %v", nv, err)
	}
	after := readConfig(t, e)
	if after.Location.CityName != "编的新城" || after.Location.Latitude == nil || *after.Location.Latitude != 31.5 || after.QQ.Owner != before.QQ.Owner || after.Calendar != before.Calendar {
		t.Fatalf("写进去的该是规范结构，别的键不动：%+v", after.Location)
	}
	// 用旧版本再写：冲突，如实报，返回最新的视图，不覆盖
	if _, err := w.SetLocation(ctx, nil, v.Version); !errors.Is(err, config.ErrConfigVersion) {
		t.Fatalf("旧版本该冲突：%v", err)
	}
	if got := readConfig(t, e).Location.Name; got != "编的新城，编的省，编的国" {
		t.Fatalf("冲突时不该写：%q", got)
	}
	// 清空
	cv, err := w.SetLocation(ctx, nil, nv.Version)
	if err != nil || cv.Location.Set() || readConfig(t, e).Location.Set() {
		t.Fatalf("清空：%+v %v", cv, err)
	}
	// 没装日历天气时搜不了：回一句人话
	a.life = nil
	if _, err := w.SearchPlaces(ctx, "x"); err == nil {
		t.Fatal("没装配时该报错")
	}
}

// 启动后设置页把 qq.owner 改了：旧主人的私聊预览要被拒，不发卡、不改配置。
// 回归（改坏检查 M3）：Authorize 里的 QQ 绑定核对删掉后，原有测试只直接调 lifeQQBindingCurrent，发现不了。
func TestSettingToolOldOwnerRefusedThroughAuthorize(t *testing.T) {
	t.Parallel()
	d := &dynLLM{}
	d.steps = []func(string) llmtest.Reply{
		func(string) llmtest.Reply {
			return llmtest.Call("t1", "preview_setting_change", `{"key":"linkReader.expand.maxLinks","value":7}`)
		},
		func(string) llmtest.Reply { return llmtest.Text("好") },
	}
	e, _, a := settingEnv(t, d)
	ap := autoDecide(a, "once")
	defer ap.Close()
	cfgPath := filepath.Join(e.data, "config", "sumika.yml")
	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, bytes.ReplaceAll(raw, []byte(testOwner), []byte("201")), 0o600); err != nil {
		t.Fatal(err)
	}
	before := configBytes(t, e)
	in, aud := qqClientIdentity(a)
	out := <-a.hub.Respond(turn.Input{Identity: in, CharacterID: a.character, Conversation: in.Scope.Conversation, Scope: in.Scope.ScopeID, Audience: aud, Text: "把最多链接数改成 7"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	bodies := e.bodiesCopy()
	if len(bodies) < 2 || !strings.Contains(bodies[1], `"role":"tool","content":"当前身份不能改设置。"`) {
		t.Fatalf("旧主人的预览该被拒：%d 次请求", len(bodies))
	}
	if !bytes.Equal(before, configBytes(t, e)) || len(ap.Cards()) != 0 {
		t.Fatal("旧主人不该改到配置或发卡")
	}
}

// 可见性的形状：缺一个条件都不给工具（改坏检查 M4、M5）。群、非主人发问、PrivateOK 为假、别的渠道、群会话编号
func TestSettingToolVisibleShapes(t *testing.T) {
	t.Parallel()
	h := settingHost{}
	ok := audience.Audience{Scene: "private", PrivateOK: true, OwnerAsked: true, Channel: "console", Conversation: "console:甲"}
	if !h.Visible(ok) {
		t.Fatal("本地主人私聊该可见")
	}
	qq := ok
	qq.Channel, qq.Conversation = "qq", "qq:private:100:200"
	if !h.Visible(qq) {
		t.Fatal("QQ 主人私聊该可见")
	}
	for name, mut := range map[string]func(*audience.Audience){
		"不是主人发的话":     func(x *audience.Audience) { x.OwnerAsked = false },
		"PrivateOK 假": func(x *audience.Audience) { x.PrivateOK = false },
		"群场合":         func(x *audience.Audience) { x.Scene = "group" },
		"群场合空":        func(x *audience.Audience) { x.Scene = "" },
		"群会话编号":       func(x *audience.Audience) { x.Conversation = "console:group:乙" },
		"别的渠道":        func(x *audience.Audience) { x.Channel = "telegram" },
		"渠道空":         func(x *audience.Audience) { x.Channel = "" },
	} {
		x := ok
		mut(&x)
		if h.Visible(x) {
			t.Errorf("%s：不该可见", name)
		}
	}
}
