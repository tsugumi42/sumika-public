package selfcheck

import "time"

// Thresholds 是各条不变量的阈值。全是暂定值（规格-运行时自检 第三节），第 0 步在真实库的只读副本上量过再改；
// app 用配置里的 selfcheck.* 填它。
type Thresholds struct {
	ReceiptUnknownPerDay int           // S3：当天发送无回执的次数
	StuckTurn            time.Duration // S4：回合没结局、不在跑、不在等审批多久算卡住
	ReconnectCount       int           // S5：窗口内断开几次算风暴
	ReconnectWindow      time.Duration
	CompactFailStreak    int           // S6：同一会话压缩连续失败几次
	StageFailStreak      int           // S7：同一阶段最近几次全部失败
	BackupStale          time.Duration // S9：每天备份多久没成功算异常；0 = 26 小时
	DrillStale           time.Duration // S12：抽验多久没通过算异常；0 = 10 天
}

// DefaultThresholds 是规格里的暂定值
func DefaultThresholds() Thresholds {
	return Thresholds{ReceiptUnknownPerDay: 3, StuckTurn: 30 * time.Minute, ReconnectCount: 6, ReconnectWindow: 10 * time.Minute, CompactFailStreak: 3, StageFailStreak: 5}
}

// Checks 是启用的全部不变量，顺序就是当天文件里 checks 的顺序
func Checks(t Thresholds) []Check {
	return []Check{
		&intakeUnaccounted{},
		intakeStoreFailed{},
		receiptUnknown{threshold: int64(t.ReceiptUnknownPerDay)},
		turnStuck{stuckAfter: t.StuckTurn},
		reconnectStorm{count: t.ReconnectCount, window: t.ReconnectWindow},
		memoryCompactFailed{k: t.CompactFailStreak},
		modelStageFailing{k: t.StageFailStreak},
		&eventsSeqGap{},
		backupStale{after: t.BackupStale},
		kernelFaulted{},
		mcpQuarantined{},
		backupDrillStale{after: t.DrillStale},
		embedUnavailable{},
		storeUnavailable{},
	}
}
