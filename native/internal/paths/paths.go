// Package paths 是数据目录的路径表，照 daemon/util/paths.ts 的新布局（v1）。
// 只认 v1（有 sumika-data.json）：旧布局 2026-10-17 已经搬完，碰到就报错让人先跑 sumika-bun 仓库里的 scripts/migrate-data.ts（本机不留，要先克隆），不偷偷兼容（规格-Go后台 四点五）。
// 和 TS 版不同：打开已有目录时绝不写任何文件；只有 Init 会给一个新目录写 sumika-data.json（测试和全新安装用）。
package paths

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DataFormat 是这个程序认识的数据目录格式（和 TS 版的 DATA_FORMAT 一样）
const DataFormat = 1

// MetaFile 是数据目录的"身份证"
const MetaFile = "sumika-data.json"

// Meta 是 sumika-data.json 的内容
type Meta struct {
	Format int `json:"format"`
	// MinReaderFormat 是最低能读这个目录的程序格式号；缺省（0）等于 Format。
	// 写这个文件的地方只在它和 Format 不同时才写（规格-库版本与升级护栏 第 7 条）。
	MinReaderFormat int    `json:"minReaderFormat,omitempty"`
	CreatedAt       string `json:"createdAt"`
	MigratedFrom    string `json:"migratedFrom,omitempty"`
	MigratedAt      string `json:"migratedAt,omitempty"`
}

// RestoreDir 是数据目录里放“恢复记录”的目录：恢复成新目录时写的未完成标记、核对报告、抽验标记都在这里。
// 它不进备份（规格-数据目录与备份 10.1 的 .restore）
const RestoreDir = ".restore"

// RestoreIncompleteFile 是没恢复完的标记（相对 RestoreDir）：恢复开始时第一个写，核对全部通过才删。有它的目录不是可用的数据目录
const RestoreIncompleteFile = "incomplete.json"

// ErrRestoreIncomplete 表示这个目录是“恢复成新目录”做到一半（或没通过核对）留下的，不能当数据目录用
var ErrRestoreIncomplete = errors.New("这个目录是没恢复完的（恢复中断或没通过核对），不能当数据目录用")

// ErrLegacy 表示这是旧布局的目录（没有身份证、但看得到旧布局的标志）
var ErrLegacy = errors.New("数据目录还是旧布局：停掉后台后，在 sumika-bun 仓库（https://github.com/tsugumi42/sumika-bun，本机不留，要先克隆）里运行 bun scripts/migrate-data.ts（先不加参数演练，看过再加 --apply）")

// ErrUninitialized 表示数据目录还没初始化：目录不存在，或者顶层只有 config/、characters/、models/（配置和角色卡可以先放好）。
// 有别的任何东西（go/、shared/、.db、backup/、logs/……）都算“有数据、只是丢了身份证”，不返回它（规格-不带QQ也能用 设计第 1 条）。
var ErrUninitialized = errors.New("数据目录还没初始化")

// uninitializedNames 是“还没初始化”允许存在的顶层项：配置、角色卡、自带嵌入模型的默认位置
var uninitializedNames = map[string]bool{"config": true, "characters": true, "models": true}

// ignoredSystemNames 是系统自己生成的文件，不算“有数据”（只认这三个确切的名字，不分大小写）。
// Thumbs.db 是唯一的 .db 例外：别的 .db 照旧算有数据。
var ignoredSystemNames = map[string]bool{"desktop.ini": true, "thumbs.db": true, ".ds_store": true}

// uninitializedAllowed 判一个顶层项能不能出现在没初始化的目录里。Windows 的文件名不分大小写，
// Config、CONFIG 也算 config；系统文件在哪个平台都按确切名字忽略。
func uninitializedAllowed(e os.DirEntry) bool {
	name := e.Name()
	if !e.IsDir() && ignoredSystemNames[strings.ToLower(name)] {
		return true // 系统文件；同名目录里可能装着数据，不能忽略
	}
	if runtime.GOOS == "windows" {
		name = strings.ToLower(name)
	}
	return uninitializedNames[name]
}

// Paths 是一个数据目录里各样东西的位置
type Paths struct {
	Root string
}

// Open 打开一个已有的数据目录：读身份证、检查格式。不写任何文件
func Open(dir string) (Paths, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, err
	}
	// 恢复成新目录做到一半的目录：备份里带着 sumika-data.json，没有这一条它照样能被打开
	if _, err := os.Lstat(filepath.Join(root, RestoreDir, RestoreIncompleteFile)); err == nil {
		return Paths{}, fmt.Errorf("%w：%s（详见其中 %s/%s；确认不要它了就整个删掉重新恢复）", ErrRestoreIncomplete, root, RestoreDir, RestoreIncompleteFile)
	}
	b, err := os.ReadFile(filepath.Join(root, MetaFile))
	if errors.Is(err, os.ErrNotExist) {
		if looksLegacy(root) {
			return Paths{}, ErrLegacy
		}
		if ok, blocker := looksUninitialized(root); ok {
			return Paths{}, fmt.Errorf("%w：数据目录 %s 里没有 %s：不是 Sumika 的数据目录，或者还没初始化", ErrUninitialized, root, MetaFile)
		} else if blocker != "" {
			return Paths{}, fmt.Errorf("数据目录 %s 里有 %s，不是空的新目录，也没有 %s：不是 Sumika 的数据目录，或者还没初始化", root, blocker, MetaFile)
		}
		return Paths{}, fmt.Errorf("数据目录 %s 里没有 %s：不是 Sumika 的数据目录，或者还没初始化", root, MetaFile)
	}
	if err != nil {
		return Paths{}, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Paths{}, fmt.Errorf("%s 读不懂：%w", filepath.Join(root, MetaFile), err)
	}
	// 读取规则是区间：程序认识的最高格式不低于目录要求的最低格式就能读；format 比程序新、但 minReaderFormat
	// 不超过时照常打开。format 缺省、写成 0 或负数的身份证不认（多半是手改坏的），不带着猜下去。
	minReader := m.MinReaderFormat
	if minReader == 0 {
		minReader = m.Format
	}
	if m.Format < 1 || minReader > m.Format {
		return Paths{}, fmt.Errorf("%s 里的 format 是 %d、minReaderFormat 是 %d：身份证可能被改坏了", filepath.Join(root, MetaFile), m.Format, minReader)
	}
	if minReader > DataFormat {
		return Paths{}, fmt.Errorf("数据目录 %s 需要第 %d 版格式的程序才能打开，比程序认识的（第 %d 版）新：请升级程序再打开，Sumika 不会去改它", root, minReader, DataFormat)
	}
	return Paths{Root: root}, nil
}

// OpenOrInit 打开一个数据目录；只有“还没初始化”（ErrUninitialized）才写身份证并建目录，别的错误原样返回。
// 第二个返回值是这次真的初始化了目录（调用方要据此写一行日志）。接收模式用它；只看历史照样只用 Open，从不初始化。
// 别处不许靠错误文字判断这一件事。
func OpenOrInit(dir string) (Paths, bool, error) {
	p, err := Open(dir)
	if errors.Is(err, ErrUninitialized) {
		p, err = Init(dir)
		return p, err == nil, err
	}
	return p, false, err
}

// Init 给一个新目录写身份证（目录不存在就建）；已经有身份证的不动
func Init(dir string) (Paths, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Paths{}, err
	}
	meta := filepath.Join(root, MetaFile)
	if _, err := os.Stat(meta); errors.Is(err, os.ErrNotExist) {
		b, _ := json.MarshalIndent(Meta{Format: DataFormat, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, "", "  ")
		if err := os.WriteFile(meta, append(b, '\n'), 0o644); err != nil {
			return Paths{}, err
		}
	}
	return Open(root)
}

// looksLegacy：旧布局才有的东西（和 TS 版的 looksLegacy 一样）
func looksLegacy(root string) bool {
	for _, p := range []string{"shared/blobs", "shared/language.db", "shared/events.db"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			return true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "characters" {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "memory.db")); err == nil {
				return true
			}
		}
	}
	return false
}

// looksUninitialized：目录不存在，或者顶层只有白名单里的 config/、characters/、models/（以及系统自己生成的那三个文件）。
// 有别的任何东西都不算——那是有数据、只是丢了身份证，不能被盖上格式 1。第二个返回值是第一个挡住的项（目录带 /），错误里点名它。
func looksUninitialized(root string) (bool, string) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return true, ""
	}
	if err != nil {
		return false, ""
	}
	for _, e := range entries {
		if uninitializedAllowed(e) {
			continue
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		return false, name
	}
	return true, ""
}

func (p Paths) join(parts ...string) string {
	return filepath.Join(append([]string{p.Root}, parts...)...)
}

// RPCRunFile 是 Bun 后台的界面协议开着时写的地址和令牌（规格-Bun后台第1批 第三节），后台退出时删。
// 归 Bun：Go 的 -rpc-listen 写 RPCGoRunFile，两边不抢同一个文件（规格-本地私聊切到Bun 第一节第 2 条）
func (p Paths) RPCRunFile() string { return p.join("run", "rpc.json") }

// RPCGoRunFile 是 Go 的 -rpc-listen 写的地址和令牌（只读的界面协议）
func (p Paths) RPCGoRunFile() string { return p.join("run", "rpc-go.json") }

func (p Paths) ConfigDir() string             { return p.join("config") }
func (p Paths) ConfigFile() string            { return p.join("config", "sumika.yml") }
func (p Paths) MCPConfigFile() string         { return p.join("config", "mcp.json") }
func (p Paths) HooksConfigFile() string       { return p.join("config", "hooks.json") }
func (p Paths) SecretsFile() string           { return p.join("config", "secrets.bin") }
func (p Paths) PromptsDir() string            { return p.join("config", "prompts") }
func (p Paths) CharactersDir() string         { return p.join("characters") }
func (p Paths) CharacterDir(id string) string { return p.join("characters", id) }
func (p Paths) CardDir(id string) string      { return p.join("characters", id, "card") }
func (p Paths) MemoryDB(id string) string     { return p.join("go", "characters", id, "memory.db") }
func (p Paths) SharedDir() string             { return p.join("shared") }

// OwnerMemoryDB 是主人的共用记忆库（ADR-162）：Go 自己的库，住在 shared/ 里，但不是 Bun 时代的旧库
func (p Paths) OwnerMemoryDB() string { return p.join("shared", "owner-memory.db") }

// SelfcheckDir 放运行自检每天写的 <日期>.json 和它的状态文件（规格-运行时自检 第四节）
func (p Paths) SelfcheckDir() string { return p.join("selfcheck") }

// Bun 后台留下的库（所有平台一个文件）：Go 只读、不写它（ADR-199、规格-聊天记录按平台分库 第二节）。
// 测试里的 OpenFresh 也开在这个位置（它有一道拦截，不开真的 Bun 库）
func (p Paths) BunEventsDB() string { return p.join("shared", "events.db") }
func (p Paths) BunTraceDB() string  { return p.join("shared", "trace.db") }

// Go 自己的库都在 go/ 下：每个平台一个聊天库（平台名见 store.PlatformOf），运行记录 trace 共用一个。
// 切换日（Bun 退役）搬进 shared/chat/，见规格第三节
func (p Paths) ChatDir() string               { return p.join("go", "chat") }
func (p Paths) ChatDB(platform string) string { return p.join("go", "chat", platform+".db") }
func (p Paths) GoTraceDB() string             { return p.join("go", "trace.db") }

// UpgradeSnapshotDir 是迁移前自动快照的目录（规格-库版本与升级护栏 第 5 条）：
// 每个库留最近 3 份，整个目录不进每天的备份。
func (p Paths) UpgradeSnapshotDir() string { return p.join("go", "upgrade-snapshots") }
func (p Paths) WorkResultsDir() string     { return p.join("go", "work", "results") }
func (p Paths) CheckpointsDir() string     { return p.join("go", "work", "checkpoints") }
func (p Paths) GoalsDir() string           { return p.join("go", "goals") }

// HolidayCacheDir 放法定假日数据的缓存（每年一个 JSON，联网取来的，丢了会重新取）
func (p Paths) HolidayCacheDir() string { return p.join("go", "calendar") }
func (p Paths) LoopFile() string        { return p.join("go", "loop.json") }

// FreeAPIStateFile 放免费接口的统计、人工置顶和关闭、次数（派生数据，丢了重新学；用户号和会话号只存哈希）
func (p Paths) FreeAPIStateFile() string { return p.join("go", "freeapi", "state.json") }

// SettingChangesFile 是设置工具的改动记录（只追加，规格-客户端自知 第十一节）
func (p Paths) SettingChangesFile() string { return p.join("go", "settings", "changes.jsonl") }

// ApprovalSessionsFile 存本地私聊各会话选的审批档和“这个对话”的放行（规格-审批分级 8.2）：一个 JSON 文件，临时文件换名写
func (p Paths) ApprovalSessionsFile() string { return p.join("go", "approval-sessions.json") }

// RemindersFile 是提醒表（规格-提醒）：一个 JSON 文件，临时文件换名写
func (p Paths) RemindersFile() string { return p.join("go", "reminders.json") }

// ArtistsDir、ArtistsFile 是画师表（规格-画师原图）：一个平台一个文件，键是账号数字 ID
func (p Paths) ArtistsDir() string                 { return p.join("go", "artists") }
func (p Paths) ArtistsFile(platform string) string { return p.join("go", "artists", platform+".json") }

// 存储统计（规格-运行页 第五节）要点名的几处。GoMediaDir 是 Go 的媒体库（mediastore 的布局），
// 和 MediaDir（Bun 的 shared/media，旧版媒体）不是一处
func (p Paths) GoMediaDir() string         { return p.join("go", "media") }
func (p Paths) GoThumbsDir() string        { return p.join("go", "media-thumbs") }
func (p Paths) GoImportDir() string        { return p.join("go", "import") }
func (p Paths) GoCharactersDir() string    { return p.join("go", "characters") }
func (p Paths) SettingsHistoryDir() string { return p.join("go", "settings-history") }
func (p Paths) ConfigHistoryDir() string   { return p.join("config", "history") }
func (p Paths) BackupsDir() string         { return p.join("backups") }

// DesktopSettingsFile 是桌面程序自己的几个开关（启动后自动接收 QQ 等）：一个小 JSON，不是 sumika.yml，桌面程序不改用户的配置文件
func (p Paths) DesktopSettingsFile() string { return p.join("go", "desktop-settings.json") }

func (p Paths) LearningDB() string { return p.join("shared", "learning.db") }
func (p Paths) MediaDir() string   { return p.join("shared", "media") }
func (p Paths) ThumbsDir() string  { return p.join("cache", "thumbs") }
func (p Paths) CacheDir() string   { return p.join("cache") }
func (p Paths) LogsDir() string    { return p.join("logs") }

// ModelsDir 是自带嵌入模型的默认根目录（embed.modelsRoot 空时用它，规格-嵌入不依赖外部程序 第 4 条）。
// 全新安装只把模型放进这里就能用，不依赖别的软件的目录
func (p Paths) ModelsDir() string { return p.join("models") }

// ModelCatalogCache 是模型目录的缓存（来源地址、读到的时间、原始模型列表）：来源连不上时照它跑（规格-模型配置 6.1）
func (p Paths) ModelCatalogCache() string { return p.join("cache", "model-catalog.json") }

func (p Paths) DevDir() string     { return p.join("dev") }
func (p Paths) ReportsDir() string { return p.join("reports") }
func (p Paths) BackupRoot() string { return filepath.Join(filepath.Dir(p.Root), "Sumika-备份") }
