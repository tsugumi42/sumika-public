// Package checkpoint 保存工具实际改动前后的原始字节；恢复只接受宿主绑定的主人身份。
package checkpoint

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
)

var (
	ErrUnavailable = errors.New("检查点不可访问或不存在")
	ErrConflict    = errors.New("检查点与当前文件状态冲突，请重新预览")
	ErrLimit       = errors.New("检查点容量或扫描预算不足")
	ErrLink        = errors.New("检查点不跟随符号链接、联接或硬链接")
	// ErrNoSnapshot：这次工具执行没有拍快照，没有可回退的内容。
	ErrNoSnapshot = errors.New("这次执行没有拍快照，不能用检查点回退")
	// errUncapturable：内容本身拍不了（设备文件、非法文件名之类），与权限无关。
	errUncapturable = errors.New("检查点无法保存这类文件")
)

// Options 的生产默认值在规格中冻结；缩小限额供临时目录验收使用。
type Options struct {
	MaxFileBytes, MaxScanBytes, MaxStoreBytes  int64
	MaxFiles, MaxEntries, MaxDepth, MaxRecords int
	Retain                                     time.Duration
	Now                                        func() time.Time
	Exclude                                    func(string) bool
	Authorize                                  func(context.Context, kernel.Scope, string) error
}

func (o Options) defaults() Options {
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = 16 << 20
	}
	if o.MaxScanBytes == 0 {
		o.MaxScanBytes = 64 << 20
	}
	if o.MaxStoreBytes == 0 {
		o.MaxStoreBytes = 512 << 20
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = 10000
	}
	if o.MaxEntries == 0 {
		o.MaxEntries = 20000
	}
	if o.MaxDepth == 0 {
		o.MaxDepth = 64
	}
	if o.MaxRecords == 0 {
		o.MaxRecords = 100
	}
	if o.Retain == 0 {
		o.Retain = 30 * 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

type FileState struct {
	Exists bool        `json:"exists"`
	Hash   string      `json:"hash,omitempty"`
	Bytes  int64       `json:"bytes"`
	Mode   os.FileMode `json:"mode"`
}
type Blob struct{ Offset, Bytes int64 }

type Record struct {
	Format                                           int
	ID, RunFirstID                                   string
	Sequence                                         uint64
	CreatedMS                                        int64
	Invocation                                       kernel.Invocation
	Owner                                            kernel.Scope
	CWD, RootIdentity, Tool, Status, Pack, InverseOf string
	Whole                                            bool
	Before, After                                    map[string]FileState
	Blobs                                            map[string]Blob
	Done                                             []string
	// SkipReason 只在 Status 为 "skipped"（这次没拍快照）时有值。
	SkipReason string `json:",omitempty"`
}

type Capture struct {
	Invocation kernel.Invocation
	Owner      kernel.Scope
	CWD, Tool  string
	// nil 表示 shell 的有界整目录捕获；其它情况只捕获给定的绝对路径。
	Paths []string
}

type Change struct {
	Path, CurrentPath string
	Before, After     FileState
	SourceID          string
}
type Plan struct {
	ID, Mode, Version, CWD string
	Invocation             kernel.Invocation
	Files                  []Change
	// 水位把预览之后的新执行也纳入 CAS，防旧审批覆盖新工作。
	Through uint64
}

func ownerScope(s kernel.Scope) bool {
	return s.Platform == "local" && s.Channel == "console" && s.Scene == "private" && s.ActorKind == "owner" && s.ActorID != "" && s.ActorID != "0" && !s.ActorIsAccount() && s.AccountID != "" && s.CharacterID != "" && s.ScopeID != "" && s.AuthorityID != "" && s.AuthorityRevision > 0 && (s.Conversation == "console:"+s.CharacterID || strings.HasPrefix(s.Conversation, "console:"+s.CharacterID+":topic:") || (audience.IsWhisper(s.Conversation) && audience.WhisperCharacter(s.Conversation) == s.CharacterID)) && !strings.Contains(s.Conversation, ":group:")
}

func hexID(s string, size int) bool {
	if len(s) != size {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
