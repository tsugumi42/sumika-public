package ctlserver

import (
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// ───────────── 验收 3：gate set 的形状表（服务端这一层）─────────────
// 行是会话和档位的写法，每一格都真发一次请求。off、dry 过；speak 的各种写法、空、别的值、私聊、本地会话、各种不合格的群会话写法
// 一律 invalid，而且一个后端函数都没调到。"库没变"在 internal/app 的同名表里对着真的库验；"不存在的群""只看历史模式"
// 是后端才知道的事，那两格也在 internal/app 的表里（服务端这层只验它们的回复码能原样传出来，见 TestBackendErrorsBecomeCodes）。

func TestGateSetShapeTable(t *testing.T) {
	const good = "qq:group:10001"
	type row struct{ what, conv, mode string }
	var reject []row
	zeroWidth := "spe" + string(rune(0x200b)) + "ak"
	for _, m := range []string{"speak", "SPEAK", "Speak", "sPeAk", " speak", "speak ", " speak ", "\tspeak", "speak\n", "speak\x00", "ｓｐｅａｋ", zeroWidth} {
		reject = append(reject, row{"speak 的写法", good, m})
	}
	for _, m := range []string{"", " ", "on", "record", "true", "1", "OFF", "Off", "DRY", "Dry", " off", "off ", " dry", "dry ", "dry\x00", "off\n", "dryrun", "dry-run", "all", "*", "speak,dry", "off;speak"} {
		reject = append(reject, row{"别的档位写法", good, m})
	}
	for _, c := range []string{
		"", " ", "*", "qq:*", "qq:group:*", "qq:group:", "qq:group:abc", "qq:group:-1", "qq:group:1.5", "qq:group:0x1f",
		"qq:private:90001:80002", "qq:private:10001", "qq:private::", "console:example", "console:example:topic:1", "console:",
		" qq:group:10001", "qq:group:10001 ", "qq:group: 10001", "qq:group:10001\n", "qq:group:10001\x00", "qq:group:10001:2", "qq:group:10001/..",
		"QQ:group:10001", "qq:GROUP:10001", "qq:Group:10001", "telegram:group:10001", "qq-group:10001", "group:10001", "10001",
		"qq:group:١٢٣", "qq:group:１２３", "qq:group:1234567890123456789012", // 阿拉伯数字、全角数字、22 位
		"qq:group:1;DROP TABLE gate_modes", "qq:group:1' OR '1'='1", "../qq:group:1", `qq:group:1\..\x`,
	} {
		for _, m := range []string{"off", "dry"} {
			reject = append(reject, row{"会话写法不合格", c, m})
		}
	}
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	for _, r := range reject {
		got := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", r.conv, r.mode}})
		if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != 3 || got.Error == "" {
			t.Errorf("%s：会话 %q 档位 %q 应被拒，得到 %+v", r.what, r.conv, r.mode, got)
		}
	}
	wantCalls(t, f) // 被拒的一格都没碰到后端
	t.Logf("被拒的格子 %d 个", len(reject))

	// 过的两格
	for _, m := range []string{"off", "dry"} {
		if got := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", good, m}}); !got.OK {
			t.Errorf("%s 应该过：%+v", m, got)
		}
	}
	// 边界：20 位数字的群号是上限
	if got := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:12345678901234567890", "off"}}); !got.OK {
		t.Errorf("20 位群号应该过：%+v", got)
	}
	wantCalls(t, f, "gate-set qq:group:10001 off", "gate-set qq:group:10001 dry", "gate-set qq:group:12345678901234567890 off")

	// speak 的提示要说清为什么、该怎么办
	r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", good, "SPEAK"}})
	if !strings.Contains(r.Error, "界面") {
		t.Errorf("speak 被拒时应说明请在界面上改：%q", r.Error)
	}
}
