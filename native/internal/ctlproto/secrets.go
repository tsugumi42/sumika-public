package ctlproto

// CmdSecrets 是密钥库在命令行里的写法（规格-数据目录与备份 5.3.3 “谁写”）：只有主程序写密钥文件，
// 这里经管道交给主程序做，ctl 自己不开文件。
//
//	secrets status                                   文件在不在、锁着还是解开、有没有主密码、本机副本能不能用、名字个数
//	secrets list                                     只列名字（排好序）
//	secrets set <名字>                               值从标准输入读；值不写在命令行参数里
//	secrets delete <名字>                            删一条
//	secrets set-passphrase                           新主密码从标准输入读两次，成功后显示一次恢复码
//	secrets unlock [--recovery]                      主密码（或恢复码）从标准输入读，解开这个库
//
// 值、主密码、恢复码只放在请求的 Secrets 字段里，从不放进 Args：Args 会出现在日志和进程参数里。
const CmdSecrets = "secrets"

// 子命令
const (
	SecretsStatusCmd        = "status"
	SecretsListCmd          = "list"
	SecretsSetCmd           = "set"
	SecretsDeleteCmd        = "delete"
	SecretsSetPassphraseCmd = "set-passphrase"
	SecretsUnlockCmd        = "unlock"
)

// 名字和值的上限（规格 5.3.3）：非空、只许字母、数字和 . _ -、最长 128；值最长 64 KB。
const (
	SecretsMaxNameBytes  = 128
	SecretsMaxValueBytes = 64 << 10
)

// SecretsArgs 是 secrets 的请求参数。Secret 是值、主密码或恢复码（按子命令决定），
// 只从标准输入读；Recovery 只用于 unlock --recovery。
type SecretsArgs struct {
	Secret   string `json:"secret,omitempty"`
	Recovery bool   `json:"recovery,omitempty"`
}

// SecretsStatus 是 secrets status 的 data。Unlocked 为假时 Names 是 0（没解开就不知道名字）。
// LocalCopy 是这台电脑的本机副本能不能用（有它下次启动不用输密码）。
type SecretsStatus struct {
	File          bool   `json:"file"`          // 密钥文件在不在
	Unlocked      bool   `json:"unlocked"`      // 这个库解开了没有
	HasPassphrase bool   `json:"hasPassphrase"` // 设过主密码没有（能不能用主密码 / 恢复码解开）
	LocalCopy     bool   `json:"localCopy"`     // 本机副本可用
	Names         int    `json:"names"`         // 名字个数（没解开是 0）
	Note          string `json:"note,omitempty"`
}

// SecretsList 是 secrets list 的 data：只给名字，不给值。
type SecretsList struct {
	Names []string `json:"names"`
}

// SecretsWritten 是 secrets set / delete 的 data。
type SecretsWritten struct {
	Name string `json:"name"`
}

// SecretsPassphrase 是 secrets set-passphrase 的 data。RecoveryCode 只在这一条回复里出现一次，
// 回复不写日志；值、主密码不在任何回复里。
type SecretsPassphrase struct {
	RecoveryCode string `json:"recoveryCode"`
}

// SecretsUnlocked 是 secrets unlock 的 data。Rewrapped 为真表示已经用这台电脑的本机加密
// 重新包好本机副本，下次启动不用再输；为假时 Note 说原因。
type SecretsUnlocked struct {
	Rewrapped bool   `json:"rewrapped"`
	Note      string `json:"note,omitempty"`
}
