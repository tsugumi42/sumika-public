package main

// 真宿主进入接收中的路径（规格 §五第 1 条：断言调到了托盘用的同一个函数、回复字段和退出码对）。
// ctl_test.go 的夹具（hostFixture）里 receive 一定开不了，成功的一侧只在假后端上测过；这里用 desklog_test.go 的 logEnv
// （假网关加一个没人听的协议端地址，开始接收能走到“接收中”），不给宿主加测试钩子。

import (
	"strings"
	"testing"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// seedGroupEvent 在 QQ 库里放一条某个群的消息（群得是“认识的”才能调档位）。要在宿主打开库之前放
func seedGroupEvent(t *testing.T, opt backend.Options, conv string) {
	t.Helper()
	p, err := paths.Open(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	qq, _ := set.Platform("qq")
	if _, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:1", Origin: "peer", Kind: "message", Text: "话", CreatedAt: 1_790_000_000_000}, []string{"ca"}); err != nil {
		t.Fatal(err)
	}
}

func gateRows(t *testing.T, e *ctlEnv) []ctlproto.GateRow {
	t.Helper()
	r := e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})
	if !r.OK {
		t.Fatalf("gate list：%+v", r)
	}
	return dataAs[ctlproto.GateList](t, r).Groups
}

// receive start 成功、status 是接收中、gate list 带 actual、gate set off 经 host 写库并改了生效档位、receive stop 回只看历史、
// 之后 gate set 回 readonly。三条成功的路径（start 成功、stop 在接收中、gate set 成功）都是真宿主、真库
func TestCtlThroughARealReceivingHost(t *testing.T) {
	t.Parallel()
	opt := logEnv(t)
	opt.Logf = t.Logf
	seedGroupEvent(t, opt, "qq:group:10001")
	e := newCtlEnvWith(t, false, opt)
	e.sh.host.start()

	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "30s"})
	rc := dataAs[ctlproto.Receive](t, r)
	if !r.OK || !rc.Receiving || !rc.Settled || rc.Phase != "receiving" || rc.Mode != "receive" || ctlproto.ExitCode(r) != 0 {
		t.Fatalf("receive start --wait 应成功：%+v %+v", r, rc)
	}
	if !sawPhase(e.log, phaseSwitching) || e.sh.host.current().Phase != phaseReceiving {
		t.Fatalf("没有经过 host.startReceive，或宿主不在接收中：%+v", e.sh.host.current())
	}
	if st := dataAs[ctlproto.Status](t, e.ask(t, ctlproto.Request{Cmd: "status"})); st.Phase != "receiving" || st.Mode != "receive" {
		t.Fatalf("status：%+v", st)
	}

	rows := gateRows(t, e)
	if len(rows) != 1 || rows[0].Conversation != "qq:group:***01" || rows[0].Mode != "dry" || rows[0].Source != "default" || rows[0].Actual != "dry" {
		t.Fatalf("接收中 gate list 应带 actual、会话打码：%+v", rows)
	}
	// 句柄用的是数据目录里桌面设置文件存的那个密钥（重启不变），群号不在回复里
	key, err := desksettings.New(opt.DataDir).HandleKey()
	if err != nil || rows[0].Handle != ctlserver.HandleOf(key, "qq:group:10001") {
		t.Fatalf("句柄应是 HMAC(设置文件里的密钥, 完整会话号) 的前 8 位：%+v %v", rows[0], err)
	}
	handle := rows[0].Handle

	// 用句柄调：服务端解析成完整会话，经 host 写库
	r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", handle, "off"}})
	ch := dataAs[ctlproto.GateChange](t, r)
	if !r.OK || ch.Conversation != "qq:group:***01" || ch.Handle != handle || ch.Before != "" || ch.After != "off" || strings.Contains(string(r.Data), "10001") {
		t.Fatalf("用句柄 gate set off 应成功：%+v %+v", r, ch)
	}
	rows = gateRows(t, e)
	if len(rows) != 1 || rows[0].Mode != "off" || rows[0].Source != "own" || rows[0].Actual != "off" {
		t.Fatalf("改完 gate list 应是自己的 off，生效档位也是 off：%+v", rows)
	}
	// 往开口的方向调到不了后台，档位也不变：完整群号、句柄两种写法都一样
	for _, ref := range []string{"qq:group:10001", handle} {
		if r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", ref, "speak"}}); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Fatalf("speak（%s）：%+v", ref, r)
		}
	}
	// 完整群号照旧能用（用户自己知道号）
	if r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "dry"}}); !r.OK || dataAs[ctlproto.GateChange](t, r).Before != "off" {
		t.Fatalf("完整群号：%+v", r)
	}
	if r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", handle, "off"}}); !r.OK {
		t.Fatalf("改回 off：%+v", r)
	}
	if rows = gateRows(t, e); rows[0].Mode != "off" || rows[0].Actual != "off" {
		t.Fatalf("speak 被拒以后档位变了：%+v", rows)
	}

	r = e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"stop"}})
	rc = dataAs[ctlproto.Receive](t, r)
	if !r.OK || rc.Receiving || rc.Phase != "archive" || !rc.Settled {
		t.Fatalf("receive stop 在接收中应回只看历史：%+v %+v", r, rc)
	}
	if r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "dry"}}); r.OK || r.Code != ctlproto.CodeReadOnly {
		t.Fatalf("停了以后库是只读的：%+v", r)
	}
	if rows = gateRows(t, e); len(rows) != 1 || rows[0].Mode != "off" || rows[0].Source != "own" || rows[0].Actual != "" {
		t.Fatalf("只看历史下 gate list 照样读得到，没有 actual：%+v", rows)
	}
}
