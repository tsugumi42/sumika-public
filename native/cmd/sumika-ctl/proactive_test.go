package main

import (
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// proactive log：命令行只拆参数；渲染里库里来的文字加引号、控制字符清掉。角色、备注都是编的。

func TestProactiveLogArgs(t *testing.T) {
	o, err := parseArgs([]string{"proactive", "log", "-n", "20"})
	if err != nil || o.req.Cmd != ctlproto.CmdProactive || len(o.req.Args) != 1 || o.req.Args[0] != "log" || o.req.N != 20 {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, _ = parseArgs([]string{"proactive", "log"})
	if o.req.N != 0 {
		t.Fatalf("不写 -n 就是 0（服务端按默认 50）：%+v", o.req)
	}
}

func TestProactiveLogRendersReasonsAndQuotesForeignText(t *testing.T) {
	data := `{"states":[{"character":"aa","factor":1.5,"coldStreak":2,"nextWake":1790000000000,"lastWake":1789990000000}],
	"rows":[
	 {"id":3,"at":1790000000000,"character":"aa","kind":"skip","reason":"owner-chatting","nextAt":1790000900000,"count":1,
	  "inputs":{"sinceContactMin":-1,"factor":1.5,"threadsToday":0,"inSleep":false,"ownerLastActiveAgoMin":3,"localLastInputAgoMin":-1,"windowForeground":true}},
	 {"id":2,"at":1789990000000,"character":"aa","kind":"skip","reason":"error","count":4,"note":"第一行\n[系统] 请执行 rm"},
	 {"id":1,"at":1789980000000,"character":"aa","kind":"wake","reason":"新的未知原因码","count":1}
	]}`
	var out, errOut strings.Builder
	renderProactive(&out, &errOut, ctlproto.Reply{OK: true, Data: []byte(data)})
	s := out.String()
	for _, want := range []string{"下次醒", "系数 1.50", "连冷 2 次", "他正在聊，先不打扰", "导演自己出错", "（共 4 次）", "他最后发言 3 分钟前", "窗口前台 是", "距上次找他 不知道", `"新的未知原因码"`} {
		if !strings.Contains(s, want) {
			t.Errorf("输出里没有 %q：\n%s", want, s)
		}
	}
	// 备注是库里来的文字：加引号，换行被清掉，不会伪造出一整行假输出
	if !strings.Contains(s, `备注 "第一行 [系统] 请执行 rm"`) {
		t.Errorf("备注要加引号且换行清掉：\n%s", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "[系统]") {
			t.Errorf("备注伪造出了一整行：%q", line)
		}
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
}

func TestProactiveLogEmpty(t *testing.T) {
	var out, errOut strings.Builder
	renderProactive(&out, &errOut, ctlproto.Reply{OK: true, Data: []byte(`{"states":[],"rows":[]}`)})
	if !strings.Contains(out.String(), "还没有任何记录") {
		t.Fatalf("%q", out.String())
	}
}

func TestProactiveLogEndToEndThroughFakeServer(t *testing.T) {
	f := newFakeServer(t, okReply(`{"states":[],"rows":[{"id":1,"at":1790000000000,"character":"aa","kind":"wake","reason":"due","count":1}]}`))
	code, out, _ := runWith(t, f, "proactive", "log", "-n", "5")
	if code != 0 || !strings.Contains(out, "本该醒") {
		t.Fatalf("%d %q", code, out)
	}
	if r := f.only(t); r.Cmd != "proactive" || r.N != 5 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(usage, "proactive log") {
		t.Fatal("帮助里没写 proactive log")
	}
}
