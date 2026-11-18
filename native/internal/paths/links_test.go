package paths

import "testing"

// 设备路径前缀还原成普通写法；认不出的、没有前缀的原样返回（逐个写，不靠规律猜）
func TestStripDevicePrefix(t *testing.T) {
	for in, want := range map[string]string{
		`\\?\D:\备份`:          `D:\备份`,
		`\\.\D:\备份`:          `D:\备份`,
		`//?/D:/备份`:          `D:/备份`,
		`//./d:/x`:           `d:/x`,
		`\??\E:\x\y`:         `E:\x\y`,
		`\\?\D:`:             `D:`,
		`\\?\UNC\主机\共享\x`:    `\\主机\共享\x`,
		`\\?\unc\主机\共享`:      `\\主机\共享`,
		`\\.\UNC\主机\共享`:      `\\主机\共享`,
		`\\?\Volume{1234}\x`: `\\?\Volume{1234}\x`,
		`\\.\PhysicalDrive0`: `\\.\PhysicalDrive0`,
		`\\主机\共享`:            `\\主机\共享`,
		`\??\UNC\x\y`:        `\??\UNC\x\y`,
		`D:\备份`:              `D:\备份`,
		`\\?\1:\x`:           `\\?\1:\x`,
		`\\?\`:               `\\?\`,
		``:                   ``,
		`ab`:                 `ab`,
		`相对备份`:               `相对备份`,
		`C:\users\\?\D:\x`:   `C:\users\\?\D:\x`,
		`\\?\D:\\.\E:\混在一起不再递归处理`: `D:\\.\E:\混在一起不再递归处理`,
	} {
		if got := StripDevicePrefix(in); got != want {
			t.Errorf("%q：想要 %q 得到 %q", in, want, got)
		}
	}
}
