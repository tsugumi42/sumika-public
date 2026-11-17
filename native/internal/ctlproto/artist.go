package ctlproto

import (
	"regexp"
	"strings"
)

// CmdArtist 是画师表的命令（规格-画师原图 三点二）：list 看（默认看待确认的）、import 导入确认页导出的文件或按批次撤回、
// confirm 把待确认的改成生效、remove 把生效或待确认的改成“不是”。只有 list 是只读；别的会改表。
// 表在正在运行的程序里：这个命令行只把参数交过去，文件由程序自己读，不依赖后台。
const CmdArtist = "artist"

// 子命令（放在 Args[0]）。
const (
	ArtistListCmd    = "list"    // list [--status pending|confirmed|declined|all] [--platform x] [-n 100]
	ArtistImportCmd  = "import"  // import <文件> | import --undo <批次>
	ArtistConfirmCmd = "confirm" // confirm <ID>...
	ArtistRemoveCmd  = "remove"  // remove <ID>...
)

// 限制
const (
	// DefaultArtistRows、MaxArtistRows 是 list 的行数（-n）
	DefaultArtistRows = 100
	MaxArtistRows     = 500
	// MaxArtistIDs 是 confirm、remove 一次最多几个 ID
	MaxArtistIDs = 200
	// MaxArtistPath 是导入文件路径的长度上限
	MaxArtistPath = 1024
)

// ArtistArgs 是 artist 的选项。Platform 空是 x；Status 只给 list；Undo 只给 import（撤回的批次号）。
type ArtistArgs struct {
	Platform string `json:"platform,omitempty"`
	Status   string `json:"status,omitempty"`
	Undo     string `json:"undo,omitempty"`
}

var (
	artistPlatform = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	artistBatch    = regexp.MustCompile(`^imp-[0-9-]{1,32}$`)
)

// Validate 只查形状；平台做没做、ID 合不合格由后台认（表在后台）。
func (a ArtistArgs) Validate() string {
	if a.Platform != "" && !artistPlatform.MatchString(a.Platform) {
		return "--platform 要写小写字母数字，比如 x"
	}
	switch a.Status {
	case "", "pending", "confirmed", "declined", "all":
	default:
		return "--status 只能是 pending、confirmed、declined、all"
	}
	if a.Undo != "" && !artistBatch.MatchString(a.Undo) {
		return "--undo 要写导入时给的批次号（imp- 开头）"
	}
	return ""
}

// ValidArtistID 查 confirm、remove 的一个 ID 的形状（只是别带空白和乱字符；按平台的格式由后台查）。
func ValidArtistID(id string) bool {
	return id != "" && len(id) <= 24 && !strings.ContainsAny(id, " \t\r\n\x00")
}

// ArtistRow 是 list 的一行。Handle、Name 是外来文字，出去前清过控制字符，只当数据看。
type ArtistRow struct {
	ID     string   `json:"id"`
	Handle string   `json:"handle,omitempty"`
	Name   string   `json:"name,omitempty"`
	Status string   `json:"status"`
	Source string   `json:"source"`
	Score  float64  `json:"score,omitempty"`
	Clues  []string `json:"clues,omitempty"`
	At     int64    `json:"at"` // 毫秒时间戳
}

// ArtistListResult 是 list 的结果：Counts 是整张表各状态的行数，Rows 是按状态筛出来的前 n 行（新到旧）。
type ArtistListResult struct {
	Platform string         `json:"platform"`
	Status   string         `json:"status"`
	Counts   map[string]int `json:"counts"`
	Rows     []ArtistRow    `json:"rows"`
}

// ArtistChange 是 import、confirm、remove 的结果；用不到的计数是 0。
// import：Batch 是新批次号（没有变化为空），Added 新增、Upgraded 改为生效、Already 本来就生效、Skipped 跳过、Malformed 格式不对的行；
// import --undo：Restored 恢复、Deleted 删除；confirm：Confirmed、Already；remove：Removed、Already。
type ArtistChange struct {
	Platform  string `json:"platform"`
	Batch     string `json:"batch,omitempty"`
	Added     int    `json:"added,omitempty"`
	Upgraded  int    `json:"upgraded,omitempty"`
	Already   int    `json:"already,omitempty"`
	Skipped   int    `json:"skipped,omitempty"`
	Malformed int    `json:"malformed,omitempty"`
	// Declined 是 rejected 里新标成“不是”的，AlreadyDeclined 本来就是“不是”的（import）
	Declined        int `json:"declined,omitempty"`
	AlreadyDeclined int `json:"alreadyDeclined,omitempty"`
	Restored        int `json:"restored,omitempty"`
	Deleted         int `json:"deleted,omitempty"`
	Confirmed       int `json:"confirmed,omitempty"`
	Removed         int `json:"removed,omitempty"`
}
