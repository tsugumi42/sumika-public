package config

import "fmt"

// Selfcheck 是运行时自检（docs/规格-运行时自检.md）：桌面程序自己数几条守恒关系和卡住的东西，每天写一个 data/selfcheck/<日期>.json。
// 只读、只记，不修、不补录、不发消息。阈值都是暂定值（规格第三节），第 0 步在真实库的只读副本上量过再改。
type Selfcheck struct {
	// Enabled 关掉就不数、不写文件，sumika-ctl status 显示"已关闭"
	Enabled bool `yaml:"enabled"`
	// IntervalSeconds：每隔多少秒评估一次并刷新当天的文件（启动后固定等 2 分钟才开始第一次）
	IntervalSeconds int `yaml:"intervalSeconds"`
	// ReceiptUnknownPerDay：当天发送无回执达到几次算异常（S3）
	ReceiptUnknownPerDay int `yaml:"receiptUnknownPerDay"`
	// StuckTurnMinutes：回合开始后这么久既没结局、也不在跑、也不在等审批，算卡住（S4）
	StuckTurnMinutes int `yaml:"stuckTurnMinutes"`
	// ReconnectCount 次断开出现在 ReconnectWindowMinutes 分钟里算重连风暴（S5）
	ReconnectCount         int `yaml:"reconnectCount"`
	ReconnectWindowMinutes int `yaml:"reconnectWindowMinutes"`
	// CompactFailStreak：同一个角色在同一个会话里压缩连续失败几次算异常（S6）
	CompactFailStreak int `yaml:"compactFailStreak"`
	// StageFailStreak：同一个后台模型阶段最近几次全部失败算异常（S7）
	StageFailStreak int `yaml:"stageFailStreak"`
}

func DefaultSelfcheck() Selfcheck {
	return Selfcheck{Enabled: true, IntervalSeconds: 300, ReceiptUnknownPerDay: 3, StuckTurnMinutes: 30,
		ReconnectCount: 6, ReconnectWindowMinutes: 10, CompactFailStreak: 3, StageFailStreak: 5}
}

func (s Selfcheck) check() []string {
	var bad []string
	for _, r := range []struct {
		key             string
		value, min, max int
	}{
		{"intervalSeconds", s.IntervalSeconds, 30, 600}, // 状态行按 15 分钟没更新判“未更新”，评估间隔不能比它长
		{"receiptUnknownPerDay", s.ReceiptUnknownPerDay, 1, 1000},
		{"stuckTurnMinutes", s.StuckTurnMinutes, 5, 1440},
		{"reconnectCount", s.ReconnectCount, 2, 100},
		{"reconnectWindowMinutes", s.ReconnectWindowMinutes, 1, 60},
		{"compactFailStreak", s.CompactFailStreak, 2, 20},
		{"stageFailStreak", s.StageFailStreak, 2, 50},
	} {
		if r.value < r.min || r.value > r.max {
			bad = append(bad, fmt.Sprintf("selfcheck.%s：%d 超出 %d～%d", r.key, r.value, r.min, r.max))
		}
	}
	return bad
}
