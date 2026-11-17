package ctlproto

import "errors"

// StatsRebuild 是 stats 的重建留痕子命令（规格-提示词前缀稳定第八节）：每天、每个场合重建几次、为什么、丢了几条、有没有摘要覆盖。
const StatsRebuild = "rebuild"

// RebuildRow 是一天 × 一个场合的汇总（Places 里的行 Date 是空的：整个范围的合计）。
// 只有计数和原因标识，不带会话串、号码、名字、正文。
type RebuildRow struct {
	Date  string `json:"date,omitempty"` // 本地时区的天，2006-01-02
	Place string `json:"place"`          // 本地私聊 / 本地群聊 / QQ 私聊 / QQ 群 / 悄悄话 / 未知场合
	// Rebuilds 是重建次数（含没记原因的旧记录）；Untraced 是其中没记原因的旧记录（rebuild 键上线前的），单列，不并进 unknown
	Rebuilds int64 `json:"rebuilds"`
	Untraced int64 `json:"untraced,omitempty"`
	// Reasons 是各原因的次数（只算记了原因的），从多到少
	Reasons []RebuildReasonCount `json:"reasons,omitempty"`
	// Counted 是数出了条数的次数（count=exact），NotCounted 是记了原因但这次没数出来的（count=failed）。
	// 平均和最大只在 Counted 上算，不拿没数的当 0 稀释
	Counted      int64   `json:"counted"`
	NotCounted   int64   `json:"notCounted,omitempty"`
	DroppedTotal int64   `json:"droppedTotal"`
	DroppedAvg   float64 `json:"droppedAvg"`
	DroppedMax   int64   `json:"droppedMax"`
	KeptAvg      float64 `json:"keptAvg"`
	// Covered 是丢掉的部分有压缩快照覆盖的次数（covered > 0）
	Covered int64 `json:"covered"`
}

// RebuildReasonCount 是一个原因标识的次数；中文说法由显示层按标识翻
type RebuildReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// RebuildReport 是 stats rebuild 的 data：Places 是每个场合在整个范围里的合计，Rows 是按天（新的在前）、场合的明细
type RebuildReport struct {
	Range  UsageRange   `json:"range"`
	Places []RebuildRow `json:"places,omitempty"`
	Rows   []RebuildRow `json:"rows,omitempty"`
}

// ValidateRebuild 检查 stats rebuild 的参数：只认 --days / --all
func (a StatsArgs) ValidateRebuild() error {
	if a.Year != 0 || a.By != "" || a.Full || a.Task != "" || a.Failures != 0 || a.Turn != "" {
		return errors.New("stats rebuild 只接受 --days、--all")
	}
	return a.Validate()
}

// RebuildReasons 是重建原因的标识和中文说法，顺序和 turn 里的固定优先级一致（turn 的测试核对两边相等）。显示层只用这一份翻，
// 不各写一份；不在表里的标识（以后新加、旧版本读新记录）原样显示，不当成别的原因
var RebuildReasons = []struct{ ID, Label string }{
	{"stale", "会话被标记要重读"},
	{"speaker", "说话人变了"},
	{"scope", "身份范围变了"},
	{"workspace", "工作目录变了"},
	{"model", "换了模型"},
	{"persona", "角色设定段变了"},
	{"scene", "场合提示词变了"},
	{"prompt", "通用提示词变了"},
	{"tools", "工具清单变了"},
	{"compacted", "有了新摘要"},
	{"tool_pair", "工具调用配对错乱"},
	{"work_root", "工作记录对不上"},
	{"provider_overflow", "服务商报超长后重建"},
	{"over_limit_compacted", "超硬门槛，压缩后重建"},
	{"over_limit_trimmed", "超硬门槛，只留最近的"},
	{"idle", "闲置超时"},
	{"evicted", "实例数超限被挤掉"},
	{"restart_in", "主动丢掉实例"},
	{"restart", "程序重启后第一次"},
	{"new_audience", "同会话换了听众档"},
	{"first", "这个会话第一次"},
	{"unknown", "不明"},
}

// RebuildReasonLabel 是重建原因的中文说法；认不出的标识原样返回
func RebuildReasonLabel(id string) string {
	for _, r := range RebuildReasons {
		if r.ID == id {
			return r.Label
		}
	}
	return id
}
