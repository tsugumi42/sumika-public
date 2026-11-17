package settingtool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// 改动记录：只追加的 jsonl，一次改动写两行（开始写入、结局）。重启后照样能查。
// 开始写入之后没有结局的（崩溃、断电）读出来标“结局不明”，由调用方再按当前值判，不自动重放。

// 记录的阶段和结局
const (
	PhaseBegin = "begin"
	PhaseEnd   = "end"

	OutcomeApplied    = "applied"    // 写进去了，回读也对
	OutcomeUnverified = "unverified" // 写进去了，回读和预期对不上
	OutcomeFailed     = "failed"     // 写失败，设置没变
	OutcomeDenied     = "denied"     // 主人在卡片上拒绝
	OutcomeExpired    = "expired"    // 卡片到期没人点
	OutcomeCancelled  = "cancelled"  // 这一轮被停掉
	OutcomeConflict   = "conflict"   // 版本变了，没写
	OutcomeUnknown    = "unknown"    // 开始写入了但没有结局
)

// Record 是记录里的一行
type Record struct {
	Phase        string          `json:"phase"`
	Change       string          `json:"change"`
	Proposal     string          `json:"proposal,omitempty"`
	At           int64           `json:"at"` // 毫秒
	Key          string          `json:"key,omitempty"`
	Place        string          `json:"place,omitempty"` // 本地私聊 / QQ 私聊
	Character    string          `json:"character,omitempty"`
	Conversation string          `json:"conversation,omitempty"`
	Old          json.RawMessage `json:"old,omitempty"`
	New          json.RawMessage `json:"new,omitempty"`
	OldText      string          `json:"oldText,omitempty"`
	NewText      string          `json:"newText,omitempty"`
	Source       string          `json:"source,omitempty"` // 因为主人的哪句话（最多 200 字）
	SourceSeqs   []int64         `json:"sourceSeqs,omitempty"`
	Reason       string          `json:"reason,omitempty"` // 她自己说的理由，只当数据
	RollbackOf   string          `json:"rollbackOf,omitempty"`
	BaseVersion  string          `json:"baseVersion,omitempty"`
	NewVersion   string          `json:"newVersion,omitempty"`
	Outcome      string          `json:"outcome,omitempty"` // 只在结局行
	Detail       string          `json:"detail,omitempty"`
}

// Change 是合并了两行之后的一次改动
type Change struct {
	Record            // 开始行的内容（没有开始行时取结局行）
	Outcome    string // 结局；没有结局行是 OutcomeUnknown
	EndAt      int64
	Detail     string
	NewVersion string
}

// Log 是改动记录文件
type Log struct {
	path string
	mu   sync.Mutex
}

// NewLog 打开记录；文件在第一次写时才建
func NewLog(path string) *Log { return &Log{path: path} }

// Append 追加一行并落盘。写不进去就返回错误：调用方这时不能改设置
func (l *Log) Append(r Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}

// All 读出全部改动，最早的在前。坏行跳过并计数（半行是崩溃留下的）
func (l *Log) All() (changes []Change, bad int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	byID := map[string]*Change{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil || r.Change == "" || (r.Phase != PhaseBegin && r.Phase != PhaseEnd) {
			bad++
			continue
		}
		c := byID[r.Change]
		if c == nil {
			c = &Change{Outcome: OutcomeUnknown}
			byID[r.Change] = c
			order = append(order, r.Change)
		}
		switch r.Phase {
		case PhaseBegin:
			c.Record = r
		case PhaseEnd:
			if c.Record.Change == "" {
				c.Record = r
			}
			c.Outcome, c.EndAt, c.Detail, c.NewVersion = r.Outcome, r.At, r.Detail, r.NewVersion
		}
	}
	if err := sc.Err(); err != nil {
		return nil, bad, err
	}
	for _, id := range order {
		changes = append(changes, *byID[id])
	}
	return changes, bad, nil
}
