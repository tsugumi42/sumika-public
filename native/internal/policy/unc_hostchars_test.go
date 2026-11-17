package policy

import (
	"fmt"
	"testing"
)

// 共享路径的主机名里带 @（WebDAV：\\host@SSL\DavWWWRoot）、非 ASCII（中文名的 NAS）、!~+%: 时也是共享：每次问。
// 2026-11-11 复核发现：主机名只认 [A-Za-z0-9_.$-] 时这些不算共享，auto、yolo、“这个对话”下只读命令直接放行。
var oddHostShares = []string{
	`\\evil.com@SSL\DavWWWRoot\a.txt`,
	`\\127.0.0.1@8080\DavWWWRoot\x`,
	`\\本机\C$\a.txt`,
	`\\nas!1\share\a.txt`,
	`\\h~1\share\a.txt`,
	`\\a+b\share\a.txt`,
	`\\a%20b\share\a.txt`,
	`//evil.com@SSL/DavWWWRoot/a.txt`,
	`//本机/C$/a.txt`,
}

func TestUNCOddHostCharsAsk(t *testing.T) {
	for _, p := range oddHostShares {
		if !IsNetworkOrDevicePath(p) {
			t.Errorf("工具路径不认作共享：%q", p)
		}
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				env := uncEnv(mode, granted)
				wantAskOnce(t, fmt.Sprintf("read %s %s %q", mode, granted, p), Decide("read", Specs["read"], ruleArgs("path", p), env))
				c := `Get-Content ` + p
				wantAskOnce(t, fmt.Sprintf("命令 %s %s %q", mode, granted, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
				env.Shell = &Verdict{ReadOnly: true}
				wantAskOnce(t, fmt.Sprintf("命令只读 %s %s %q", mode, granted, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
			}
		}
	}
}
