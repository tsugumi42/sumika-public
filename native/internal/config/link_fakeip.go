package config

import (
	"fmt"
	"slices"

	"sumika-native/internal/netsafe"
)

// linkReader.fakeIPRanges：代理 fake-ip 模式的假地址段（规格-链接解析 第五节“fake-ip 与代理”，用户 2026-11-13 同意）。
// 含义是“解析到这些段 = 真实地址未知”：取数 / 下载名单里的主机碰到这些地址，不连假地址，改成经 linkReader.proxy（或
// HTTPS_PROXY / HTTP_PROXY）按主机名 CONNECT，让代理自己解析；名单外的主机、没有代理可走时一律拒绝。
// 它是安全项：只在 yml 里改，不走通用设置写入（settingsreg 里只读）。

// DefaultFakeIPRanges 是默认的假地址段（Clash / mihomo 的 fake-ip-range 默认落在这里）
func DefaultFakeIPRanges() []string { return slices.Clone(netsafe.DefaultFakeIPRanges) }

func (c LinkReaderConfig) checkFakeIPRanges() []string {
	if _, err := netsafe.ParseFakeIPRanges(c.FakeIPRanges); err != nil {
		return []string{fmt.Sprintf("linkReader.fakeIPRanges：%v", err)}
	}
	return nil
}
