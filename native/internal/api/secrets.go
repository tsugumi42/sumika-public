package api

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// SecretsAdmin 是设置页“数据 · 密钥”读写密钥库的入口（规格-数据目录与备份 5.3.2 界面、5.3.3）。
// 只供本机设置窗口，不扩大 Reader、QQ 或模型工具：值只进不出，名字列表不带值；
// 恢复码只在 SecretsSetPassphrase 成功的那一次返回值里出现，调用方显示一次就丢。
// 和 SettingsRegistry 一样不上界面协议：进程内桥内嵌 DesktopReader，方法原样提升过去。
type SecretsAdmin interface {
	// SecretsStatus 说文件在不在、锁着还是解开、有没有主密码、本机副本能不能用、名字个数；
	// 文件不存在不算错误。Note 是“格式坏了”“本机副本没包上”这类给人看的原因。
	SecretsStatus(ctx context.Context) (ctlproto.SecretsStatus, error)
	// SecretsList 只给排好序的名字，不给值；锁着时返回错误，先解锁。
	SecretsList(ctx context.Context) ([]string, error)
	// SecretsSet 存一条并当场落盘；文件不存在就新建。
	SecretsSet(ctx context.Context, name, value string) error
	// SecretsDelete 删一条；名字本来就不在时什么也不做，不因删除新建文件。
	SecretsDelete(ctx context.Context, name string) error
	// SecretsSetPassphrase 设或改主密码，返回新的恢复码（旧的作废）。恢复码只在这里出现一次。
	SecretsSetPassphrase(ctx context.Context, passphrase string) (string, error)
	// SecretsUnlock 用主密码或恢复码解开锁着的库；本机加密可用时顺手重包本机副本，
	// 没包上时 Rewrapped=false、Note 说原因，不影响这次解开。
	SecretsUnlock(ctx context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error)
}
