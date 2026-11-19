// Package storagestat 统计数据目录各类别的占用（规格-运行页 第五节）。只读：不删、不改任何文件，只往 trace.db 的
// maintenance_runs 写统计结果。路径都来自 internal/paths，不手拼；遇到联接和符号链接不跟进去。
package storagestat

import (
	"path/filepath"
	"strings"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/paths"
)

// Extra 是数据目录之外的位置，来自配置，读不到就留空
type Extra struct {
	ModelsRoot string // embed.modelsRoot
	BackupDir  string // backup.dir；空用 paths.BackupRoot
}

// category 是类别表的一行
type category struct {
	key, label string
	rule       string
	auto       bool // 有没有任何自动清理
	big        bool // 大目录：打开页面时不现量，靠后台扫描的快照
	external   bool // 位置在数据目录外：用户配的根，先解开联接再量
	roots      func(p paths.Paths, x Extra) []string
	itemOf     func(rel string) string // 相对类别根的路径 → 分行名；空表示不分行
}

func firstPart(rel string) string {
	rel = filepath.ToSlash(rel)
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return rel
}

// dbName 把 qq.db、qq.db-wal 都归到 qq
func dbName(rel string) string {
	n := firstPart(rel)
	if i := strings.IndexByte(n, '.'); i > 0 {
		return n[:i]
	}
	return n
}

// 清理规则的文字。逐处核对过代码，依据写在 docs/subsystems/runtime.md 的"存储清理规则核对"一节
const noAuto = "没有自动清理"

// categories 是类别表，顺序就是页面上的顺序。"其他"不在表里：它是数据目录里没被任何类别认领的部分，扫描时单独算
func categories() []category {
	return []category{
		{key: ctlproto.StoreMedia, label: "媒体", big: true, itemOf: firstPart,
			rule:  "正文没有自动清理（被聊天记录引用）；写到一半的临时文件超过 1 小时的，在收图启动时清",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.GoMediaDir()} }},
		{key: ctlproto.StoreLegacyMedia, label: "旧版媒体", big: true, rule: noAuto + "（Bun 退役后的遗留，只读展示）",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.MediaDir()} }},
		{key: ctlproto.StoreThumbs, label: "缩略图", big: true,
			rule:  "没有定期清理，可重建；写同名缩略图前顺手清超过 24 小时的 .partial",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.GoThumbsDir(), p.ThumbsDir()} }},
		{key: ctlproto.StoreChat, label: "聊天库", itemOf: dbName, rule: noAuto,
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.ChatDir()} }},
		{key: ctlproto.StoreTrace, label: "运行记录", rule: noAuto,
			roots: func(p paths.Paths, _ Extra) []string {
				f := p.GoTraceDB()
				return []string{f, f + "-wal", f + "-shm"}
			}},
		{key: ctlproto.StoreUpgradeSnapshots, label: "升级前快照", rule: "迁移前自动拍的，每个库留最近 3 份",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.UpgradeSnapshotDir()} }},
		{key: ctlproto.StoreMemory, label: "角色记忆库", itemOf: firstPart, rule: noAuto + "（开写入前的备份也不删旧的）",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.GoCharactersDir()} }},
		{key: ctlproto.StoreOwnerMemory, label: "共用记忆库", rule: noAuto,
			roots: func(p paths.Paths, _ Extra) []string {
				f := p.OwnerMemoryDB()
				return []string{f, f + "-wal", f + "-shm"}
			}},
		{key: ctlproto.StoreImport, label: "导入数据", big: true, rule: noAuto,
			roots: func(p paths.Paths, _ Extra) []string {
				return []string{p.GoImportDir(), filepath.Join(p.Root, "import")}
			}},
		{key: ctlproto.StoreWork, label: "工作结果、检查点", auto: true,
			rule: "无引用结果超过配置天数删、预留超过 24 小时回收（工作区初始化时跑一次）；检查点按保留 30 天、最多 100 个、512 MB 裁（新建检查点时）",
			roots: func(p paths.Paths, _ Extra) []string {
				return []string{p.WorkResultsDir(), p.CheckpointsDir(), p.GoalsDir()}
			}},
		{key: ctlproto.StoreBackups, label: "备份", rule: noAuto,
			roots: func(p paths.Paths, x Extra) []string {
				r := []string{p.BackupsDir()}
				if x.BackupDir != "" {
					return append(r, x.BackupDir)
				}
				return append(r, p.BackupRoot())
			}},
		{key: ctlproto.StoreLogs, label: "日志", rule: noAuto + "（每天一个文件，没有轮转和删旧）",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.LogsDir()} }},
		{key: ctlproto.StoreConfig, label: "配置和设置历史", rule: noAuto + "（设置历史、配置历史不删旧的）",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.ConfigDir(), p.SettingsHistoryDir()} }},
		{key: ctlproto.StoreCards, label: "角色卡", itemOf: firstPart, rule: "不清理",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.CharactersDir()} }},
		{key: ctlproto.StoreDev, label: "开发数据", big: true, rule: noAuto,
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.DevDir()} }},
		{key: ctlproto.StoreReports, label: "报告", rule: noAuto,
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.ReportsDir()} }},
		{key: ctlproto.StoreLegacyOther, label: "旧版数据", rule: noAuto + "（Bun 时代的库，只读）",
			roots: func(p paths.Paths, _ Extra) []string { return []string{p.SharedDir()} }},
		{key: ctlproto.StoreModels, label: "本地模型", big: true, external: true, rule: "只显示大小，不清理",
			roots: func(_ paths.Paths, x Extra) []string {
				if x.ModelsRoot == "" {
					return nil
				}
				return []string{x.ModelsRoot}
			}},
	}
}

const (
	otherLabel = "其他（没归进上面各类的）"
	otherRule  = "不清理：数据目录里没被任何一类认领的文件，多半是锁文件、小配置、旧目录"
)

func lowerKey(p string) string { return strings.ToLower(filepath.Clean(p)) }
