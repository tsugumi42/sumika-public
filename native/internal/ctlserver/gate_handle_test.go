package ctlserver

// 群号不出控制接口，群用句柄指（gatehandle.go）。这是"谁能看到什么、谁能做什么"的改动，先列表再测。
//
// 形状表（行 = gate set 的第一个参数的写法，列 = 入口：服务端校验 / 句柄解析 / 后端调用 / 回复）：
//
//	完整 qq:group:<数字>                    校验过 · 不解析 · 调 GateSet(完整会话) · 回复里是打码的会话和句柄          → TestGateSetByFullConversationAndByHandle
//	合法句柄、对得上一个群                    校验过 · 解析出完整会话 · 调 GateSet(完整会话)                         → 同上
//	合法句柄、没有群对得上                    校验过 · 解析不到 · invalid · 没调 GateSet                           → TestGateSetByHandleRefusals
//	合法句柄、撞了（两个群一样）               校验过 · 解析出两个 · invalid（要求写完整群号）· 没调 GateSet               → TestHandleClash
//	纯数字的 8 个字符（也是合法句柄的写法）        当句柄解析；对得上才通过                                            → TestGateSetByHandleRefusals、TestEightDigitHandleIsStillAHandle
//	大写十六进制 / 7 位 / 9 位 / 非十六进制 / 空 / 带空白换行 校验不过 · invalid · 后端一个函数都没调（GateList 也没调）  → TestGateSetHandleShapeTable
//	打码的写法 qq:group:***01              校验不过 · invalid · 没调后端                                       → 同上
//	私聊、本地会话、通配符                   校验不过（原有的形状表 TestGateSetShapeTable）
//	任何写法的 speak（含配句柄）              档位这一道先拒 · invalid · 没调后端（GateList 也没调）                     → TestGateSetHandleShapeTable
//	演示模式 + 句柄                         demo · 没调后端                                                  → 同上
//	GateList 失败 / 密钥取不到               原样带码回 · 没调 GateSet                                           → TestGateSetByHandleRefusals
//
// 列表一侧（gate list）：每一行的会话打码、带句柄；回复整串 JSON 里找不到完整群号；非群会话的行丢掉；撞了的两行都标 handleClash。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

var testKey = []byte("测试用的固定密钥，三十二个字节以上才像样......")

func keyOpt(key []byte) Options {
	return Options{HandleKey: func() ([]byte, error) { return key, nil }}
}

func groupsOf(convs ...string) ctlproto.GateList {
	var l ctlproto.GateList
	for _, c := range convs {
		l.Groups = append(l.Groups, ctlproto.GateRow{Conversation: c, Mode: "dry", Source: "default", Name: "群" + c[len(c)-2:]})
	}
	return l
}

// 句柄的算法写在规格里：HMAC-SHA256(密钥, 完整会话号) 的前 8 个小写十六进制字符。这里不调 HandleOf，独立算一遍对照
func TestHandleIsHMACPrefix(t *testing.T) {
	m := hmac.New(sha256.New, testKey)
	m.Write([]byte("qq:group:10001"))
	want := hex.EncodeToString(m.Sum(nil))[:8]
	if got := HandleOf(testKey, "qq:group:10001"); got != want {
		t.Fatalf("%s 应是 %s", got, want)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(want) || !ctlproto.IsHandle(want) {
		t.Fatalf("格式：%s", want)
	}
	// 依赖密钥：换密钥句柄就变；不同的群句柄不同；同一个群稳定
	if HandleOf([]byte("别的密钥"), "qq:group:10001") == want {
		t.Error("句柄不依赖密钥：没有密钥的哈希是能穷举反推群号的")
	}
	if HandleOf(testKey, "qq:group:10002") == want {
		t.Error("两个群的句柄一样（8 位里撞上的概率极小，撞了说明算法有问题）")
	}
	if HandleOf(testKey, "qq:group:10001") != want {
		t.Error("同一个群的句柄不稳定")
	}
}

func TestGateListMasksGroupNumbersAndAddsHandles(t *testing.T) {
	f := &fakeBackend{list: groupsOf("qq:group:10001", "qq:group:123456789012", "qq:group:7")}
	f.list.Groups = append(f.list.Groups,
		ctlproto.GateRow{Conversation: "qq:private:90001:80002", Mode: "dry"}, // 后端不该给的：丢掉
		ctlproto.GateRow{Conversation: "qq:group:*", Mode: "dry"},
		ctlproto.GateRow{Conversation: "", Mode: "dry"})
	name, _ := start(t, f, keyOpt(testKey))
	r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})
	g := dataOf[ctlproto.GateList](t, r)
	if !r.OK || len(g.Groups) != 3 {
		t.Fatalf("非群会话的行要丢掉，只剩 3 个：%+v", r)
	}
	for i, want := range []struct{ conv, orig string }{{"qq:group:***01", "qq:group:10001"}, {"qq:group:***12", "qq:group:123456789012"}, {"qq:group:***", "qq:group:7"}} {
		row := g.Groups[i]
		if row.Conversation != want.conv || row.Handle != HandleOf(testKey, want.orig) || row.HandleClash {
			t.Errorf("第 %d 行：%+v，应是会话 %s、句柄 %s", i, row, want.conv, HandleOf(testKey, want.orig))
		}
	}
	// 回复整串里没有完整群号，也没有 10 位以上的数字
	for _, leak := range []string{"10001", "123456789012", "90001", "80002"} {
		if strings.Contains(string(r.Data), leak) {
			t.Errorf("gate list 的回复里不该有 %q：%s", leak, r.Data)
		}
	}
}

func TestGateSetByFullConversationAndByHandle(t *testing.T) {
	f := &fakeBackend{list: groupsOf("qq:group:10001", "qq:group:10002")}
	name, _ := start(t, f, keyOpt(testKey))
	h2 := HandleOf(testKey, "qq:group:10002")

	r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}})
	c := dataOf[ctlproto.GateChange](t, r)
	if !r.OK || c.Conversation != "qq:group:***01" || c.Handle != HandleOf(testKey, "qq:group:10001") || c.After != "off" {
		t.Fatalf("完整群号：%+v %+v", r, c)
	}
	wantCalls(t, f, "gate-set qq:group:10001 off") // 完整写法不需要解析：没调 GateList

	r = ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", h2, "dry"}})
	c = dataOf[ctlproto.GateChange](t, r)
	if !r.OK || c.Conversation != "qq:group:***02" || c.Handle != h2 || c.After != "dry" {
		t.Fatalf("句柄：%+v %+v", r, c)
	}
	// 后端拿到的是解析出来的完整会话，档位照旧只能是 off、dry
	wantCalls(t, f, "gate-set qq:group:10001 off", "gate-list", "gate-set qq:group:10002 dry")
	for _, leak := range []string{"10001", "10002"} {
		if strings.Contains(string(r.Data), leak) {
			t.Errorf("gate set 的回复里不该有完整群号 %q：%s", leak, r.Data)
		}
	}
}

func TestGateSetHandleShapeTable(t *testing.T) {
	f := &fakeBackend{list: groupsOf("qq:group:10001")}
	name, _ := start(t, f, keyOpt(testKey))
	h := HandleOf(testKey, "qq:group:10001")
	bad := []string{
		strings.ToUpper(h) + "x", "ABCDEF12", h[:7], h + "0", h + "00", "gggggggg", "abcdefg1", "", " ",
		" " + h, h + " ", h + "\n", "\t" + h, h[:4] + " " + h[4:], "0x" + h[:6], "qq:group:***01", "qq:group:*", "***01",
		"ab-cdef1", "abcd_ef1", "ｆｆｆｆｆｆｆｆ", "١٢٣٤٥٦٧٨",
	}
	// 小写以外的写法：至少有一个字母才会被大写改变，保证这里真的有一个“只差大小写”的格子
	if up := strings.ToUpper(h); up != h {
		bad = append(bad, up)
	}
	for _, ref := range bad {
		for _, mode := range []string{"off", "dry"} {
			r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", ref, mode}})
			if r.OK || r.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(r) != 3 {
				t.Errorf("%q %s 应被拒：%+v", ref, mode, r)
			}
		}
	}
	wantCalls(t, f) // 形状不对的一个后端函数都没碰到，GateList 也没调

	// speak 的任何写法，配合法句柄，也到不了后端（档位这一道先拒，连 GateList 都不调）
	for _, mode := range []string{"speak", "SPEAK", " speak", "speak ", "ｓｐｅａｋ", "on", ""} {
		r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", h, mode}})
		if r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("句柄配 %q 应被拒：%+v", mode, r)
		}
	}
	wantCalls(t, f)

	// 演示模式：demo，没调后端
	demo := keyOpt(testKey)
	demo.Demo = true
	nameDemo, _ := start(t, f, demo)
	if r := ask(t, nameDemo, ctlproto.Request{Cmd: "gate", Args: []string{"set", h, "off"}}); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	wantCalls(t, f)
}

func TestGateSetByHandleRefusals(t *testing.T) {
	// 对不上任何群；纯数字的 8 个字符也是句柄的写法，同样要对得上才行
	f := &fakeBackend{list: groupsOf("qq:group:10001")}
	name, _ := start(t, f, keyOpt(testKey))
	for _, ref := range []string{"deadbeef", "20261101", "0a0b0c0d"} {
		r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", ref, "off"}})
		if r.OK || r.Code != ctlproto.CodeInvalid || !strings.Contains(r.Error, ref) {
			t.Errorf("%s：%+v", ref, r)
		}
	}
	wantCalls(t, f, "gate-list", "gate-list", "gate-list") // 解析了三次，一次 GateSet 也没调

	// GateList 失败（后台没起来）：带码原样回，不调 GateSet
	f2 := &fakeBackend{err: ctlproto.Errorf(ctlproto.CodeFailed, "后台没在运行")}
	name2, _ := start(t, f2, keyOpt(testKey))
	if r := ask(t, name2, ctlproto.Request{Cmd: "gate", Args: []string{"set", "deadbeef", "off"}}); r.OK || r.Code != ctlproto.CodeFailed || !strings.Contains(r.Error, "后台没在运行") {
		t.Errorf("GateList 失败：%+v", r)
	}
	wantCalls(t, f2, "gate-list")

	// 密钥取不到：failed，不调 GateSet 之前就拿不到句柄的两条（句柄解析、gate list）都不往外给；
	// 完整群号 gate set 在后端改完以后才要句柄，取不到密钥就不往外给回复，也不退回成完整群号
	f3 := &fakeBackend{list: groupsOf("qq:group:10001")}
	name3, _ := start(t, f3, Options{HandleKey: func() ([]byte, error) { return nil, errors.New("设置文件写不了") }})
	for _, args := range [][]string{{"set", "deadbeef", "off"}, {"list"}, {"set", "qq:group:10001", "off"}} {
		r := ask(t, name3, ctlproto.Request{Cmd: "gate", Args: args})
		if r.OK || r.Code != ctlproto.CodeFailed || !strings.Contains(r.Error, "密钥") || strings.Contains(string(r.Data), "10001") || strings.Contains(r.Error, "10001") {
			t.Errorf("密钥取不到 %v：%+v", args, r)
		}
	}
	// 空密钥当取不到
	name4, _ := start(t, f3, keyOpt([]byte{}))
	if r := ask(t, name4, ctlproto.Request{Cmd: "gate", Args: []string{"list"}}); r.OK {
		t.Errorf("空密钥：%+v", r)
	}
}

// 撞了：gate list 里两行都标 handleClash；gate set 用撞的句柄回 invalid，要求写完整群号，一个群也不改
func TestHandleClash(t *testing.T) {
	old := computeHandle
	computeHandle = func(key []byte, conv string) string {
		if conv == "qq:group:10001" || conv == "qq:group:10002" {
			return "abcd1234"
		}
		return old(key, conv)
	}
	t.Cleanup(func() { computeHandle = old })
	f := &fakeBackend{list: groupsOf("qq:group:10001", "qq:group:10002", "qq:group:10003")}
	name, _ := start(t, f, keyOpt(testKey))

	g := dataOf[ctlproto.GateList](t, ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"list"}}))
	if len(g.Groups) != 3 || !g.Groups[0].HandleClash || !g.Groups[1].HandleClash || g.Groups[2].HandleClash {
		t.Fatalf("撞的两行标 handleClash，另一行不标：%+v", g.Groups)
	}
	r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "abcd1234", "off"}})
	if r.OK || r.Code != ctlproto.CodeInvalid || !strings.Contains(r.Error, "撞了") || !strings.Contains(r.Error, "完整") {
		t.Fatalf("撞了的句柄应回 invalid 并要求写完整群号：%+v", r)
	}
	wantCalls(t, f, "gate-list", "gate-list") // 不猜：没调 GateSet
	// 没撞的那个群照常能用句柄；完整群号照常能用
	if r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", HandleOf(testKey, "qq:group:10003"), "off"}}); !r.OK {
		t.Errorf("没撞的句柄：%+v", r)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}}); !r.OK {
		t.Errorf("撞了的群用完整群号：%+v", r)
	}
	wantCalls(t, f, "gate-list", "gate-list", "gate-list", "gate-set qq:group:10003 off", "gate-set qq:group:10001 off")
}

// 纯数字的 8 位句柄和群号不会混：完整写法必须带前缀，裸的 8 位数字只当句柄
func TestEightDigitHandleIsStillAHandle(t *testing.T) {
	old := computeHandle
	computeHandle = func(key []byte, conv string) string {
		switch conv {
		case "qq:group:12345678": // 这个群的群号是 8 位数字，它的句柄不是纯数字
			return "abcd1234"
		case "qq:group:10002": // 这个群的句柄恰好是纯数字
			return "20261101"
		}
		return old(key, conv)
	}
	t.Cleanup(func() { computeHandle = old })
	f := &fakeBackend{list: groupsOf("qq:group:12345678", "qq:group:10002")}
	name, _ := start(t, f, keyOpt(testKey))
	if r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "20261101", "dry"}}); !r.OK {
		t.Fatalf("纯数字的句柄也是句柄：%+v", r)
	}
	// 裸的群号 12345678 是真有的群的群号，但它不是任何群的句柄：当句柄找不到，不会当群号去改
	if r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "12345678", "dry"}}); r.OK || r.Code != ctlproto.CodeInvalid {
		t.Fatalf("%+v", r)
	}
	wantCalls(t, f, "gate-list", "gate-set qq:group:10002 dry", "gate-list")
}

// 默认密钥（没给 HandleKey）：进程里随机一个，同一个服务端内稳定
func TestDefaultHandleKeyIsStablePerServer(t *testing.T) {
	f := &fakeBackend{list: groupsOf("qq:group:10001")}
	name, _ := start(t, f, Options{})
	h1 := dataOf[ctlproto.GateList](t, ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})).Groups[0].Handle
	h2 := dataOf[ctlproto.GateList](t, ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})).Groups[0].Handle
	name2, _ := start(t, f, Options{})
	h3 := dataOf[ctlproto.GateList](t, ask(t, name2, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})).Groups[0].Handle
	if h1 != h2 || !ctlproto.IsHandle(h1) || h1 == h3 {
		t.Fatalf("同一个服务端稳定，不同服务端不一样：%s %s %s", h1, h2, h3)
	}
}
