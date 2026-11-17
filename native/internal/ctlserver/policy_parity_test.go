package ctlserver

import (
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/policy"
)

// 审批规则（policy/selfctl.go）里的“只读子命令”名单和服务端的 readOnly 表逐键对拍：
//   - 策略当只读、服务端当会写，必须红（模型能在最宽档不点卡跑一条服务端会改状态的命令）；
//   - 策略当要问、服务端当只读，只能是下面显式列出的例外（策略更严）。

// 策略更严的例外：键 → 原因
var policyStricterThanServer = map[string]string{
	"goal": "服务端光写 goal 当 goal show（只读）；策略不留“靠后面没有词判只读”的特例，光写的要问（续行折叠绕过的教训）",
	"loop": "服务端光写 loop 当 loop list（只读）；同上",
}

// shapes 是一个键可能的参数形状：子命令后面带 0、1、2 个多余参数都试
func shapes(key string) []ctlproto.Request {
	cmd, sub, _ := strings.Cut(key, " ")
	var out []ctlproto.Request
	base := []string{}
	if sub != "" {
		base = []string{sub}
	}
	for extra := 0; extra <= 2; extra++ {
		args := append([]string{}, base...)
		for i := 0; i < extra; i++ {
			args = append(args, "x")
		}
		out = append(out, ctlproto.Request{Cmd: cmd, Args: args})
	}
	return out
}

func serverSaysReadOnly(key string) bool {
	for _, r := range shapes(key) {
		if readOnly(r) {
			return true
		}
	}
	return false
}

func TestPolicyReadOnlyListMatchesServerReadOnlyTable(t *testing.T) {
	for _, k := range policy.CtlReadOnlyKeys() {
		if !serverSaysReadOnly(k) {
			t.Errorf("策略把 %q 当只读，服务端 readOnly 表认为它会写：会让最宽档不点卡就改状态", k)
		}
	}
	for _, k := range policy.CtlAskKeys() {
		if serverSaysReadOnly(k) {
			if why, ok := policyStricterThanServer[k]; !ok {
				t.Errorf("服务端把 %q 当只读，策略却要问：两边不一致，要么改名单要么把它列进 policyStricterThanServer 并写原因", k)
			} else if why == "" {
				t.Errorf("例外 %q 没写原因", k)
			}
		}
	}
	for k := range policyStricterThanServer {
		if !serverSaysReadOnly(k) {
			t.Errorf("例外 %q 已经不成立（服务端不再当它只读），删掉", k)
		}
	}
}
