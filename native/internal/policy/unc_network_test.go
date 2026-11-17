package policy

import "testing"

// IsNetworkSharePath 只认真的网络共享：本机的设备路径（\\?\D:\、\\.\D:\、\\?\Volume{GUID}\）不算，它们能安全解析成普通路径
func TestIsNetworkSharePath(t *testing.T) {
	for p, want := range map[string]bool{
		`\\主机\共享\x`:                  true,
		`//host/share/x`:             true,
		`\\10.255.255.1\share\备份`:    true,
		`\\?\UNC\host\share\x`:       true,
		`\\?\unc\host\share`:         true,
		`\\.\UNC\host\share`:         true,
		`//?/UNC/host/share`:         true,
		`\\localhost\C$\x`:           true,
		`\\?\D:\备份`:                  false,
		`\\.\D:\备份`:                  false,
		`\\?\Volume{1234-abcd}\备份\x`: false,
		`\\.\PhysicalDrive0`:         false,
		`\\?\UNC`:                    false,
		`D:\备份`:                      false,
		`备份`:                         false,
		`/usr/x`:                     false,
		`\\`:                         false,
		`\\ x\y`:                     false,
		``:                           false,
	} {
		if got := IsNetworkSharePath(p); got != want {
			t.Errorf("%q：想要 %v 得到 %v", p, want, got)
		}
	}
}
