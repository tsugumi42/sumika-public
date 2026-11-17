package projectcontext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sumika-native/internal/approval"
)

type Contribution struct {
	Lines      []string
	OnAccepted func()
}

type accepted struct {
	digest   [32]byte
	complete bool
	state    State
	sequence uint64
}

// Ledger 只记已被宿主接纳的实际版本。ContextID 由宿主生成；不同 ID 互不继承。
// 上下文丢弃时宿主可调用 Forget，旧贡献不会重新建立已丢弃的账本。
type Ledger struct {
	mu       sync.Mutex
	contexts map[string]map[string]accepted
	epochs   map[string]uint64
	sequence uint64
}

func NewLedger() *Ledger {
	return &Ledger{contexts: make(map[string]map[string]accepted), epochs: make(map[string]uint64)}
}
func (l *Ledger) Forget(contextID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.contexts, contextID)
	l.epochs[contextID]++
}

// Next 返回尾部追加资料；扫描本身从不提交账本。unavailable 保留已接纳旧版。
func (r *Resolver) Next(ctx context.Context, contextID string, l *Ledger) (Snapshot, *Contribution) {
	return r.NextTo(ctx, contextID, l, r.opts.CWD)
}

// NextTo 在成功 read/ls 后使用；dir 是文件父目录或列出的目录。
// 宿主保存触及过的目录，在回合起点用此入口核对相应作用域。
func (r *Resolver) NextTo(ctx context.Context, contextID string, l *Ledger, dir string) (Snapshot, *Contribution) {
	snap := r.DiscoverTo(ctx, dir)
	return snap, l.prepare(contextID, snap, false, r.opts.TotalBytes)
}

func (l *Ledger) prepare(id string, snap Snapshot, full bool, limit int) *Contribution {
	l.mu.Lock()
	defer l.mu.Unlock()
	old := l.contexts[id]
	l.sequence++
	seq, epoch := l.sequence, l.epochs[id]
	changes := make(map[string]accepted)
	var b strings.Builder
	if !snap.Complete {
		b.WriteString(incompleteMark)
	}
	for _, src := range snap.Sources {
		prev, exists := old[src.ID]
		if src.State == Unavailable {
			continue
		}
		if src.State == Absent {
			if exists && prev.state != Absent {
				fmt.Fprintf(&b, "[撤销来源 %q；此前版本不再适用]\n", src.ID)
				changes[src.ID] = accepted{state: Absent, complete: true, sequence: seq}
			}
			continue
		}
		effectiveComplete := src.Complete && snap.Complete
		unavailableDependency := false
		for _, file := range src.Files {
			if file.State == Unavailable {
				unavailableDependency = true
				break
			}
		}
		changed := !exists || prev.state != Present || prev.digest != src.Digest || prev.complete != effectiveComplete
		if changed || full {
			if exists && prev.state == Present && changed {
				b.WriteString(updateMark)
			}
			b.WriteString(src.Rendered)
			if changed && !unavailableDependency {
				changes[src.ID] = accepted{digest: src.Digest, complete: src.Complete && snap.Complete, state: Present, sequence: seq}
			}
		}
	}
	b.WriteString(snap.diagnostics)
	if full {
		b.WriteString(retryMark)
	}
	if b.Len() == 0 {
		return nil
	}
	text := b.String()
	// 路径、诊断或撤销标记也可能占满预算；绝不提交没实际给出的资料。
	if len(text) > limit {
		text = prefixUTF8(incompleteMark+text, limit)
		clear(changes)
	}
	c := &Contribution{Lines: []string{text}}
	if len(changes) > 0 {
		var once sync.Once
		c.OnAccepted = func() {
			once.Do(func() {
				l.mu.Lock()
				defer l.mu.Unlock()
				if l.epochs[id] != epoch {
					return
				}
				if l.contexts[id] == nil {
					l.contexts[id] = make(map[string]accepted)
				}
				for key, value := range changes {
					if prev, ok := l.contexts[id][key]; !ok || prev.sequence < value.sequence {
						l.contexts[id][key] = value
					}
				}
			})
		}
	}
	return c
}

type WriteStatus string

const (
	CanWrite        WriteStatus = "can_write"
	NeedContext     WriteStatus = "need_context"
	RulesIncomplete WriteStatus = "rules_incomplete"
)

type WriteDecision struct {
	Status       WriteStatus
	Snapshot     Snapshot
	Contribution *Contribution
	Err          error
}

// BeforeWrite 只判定，不执行写入。需要资料时必须接纳返回贡献，再发起新调用。
// 与普通轮次不同，写前强制重读所需正文，防止同身份、大小和时间戳的内容变化。
func (r *Resolver) BeforeWrite(ctx context.Context, id string, l *Ledger, target string) WriteDecision {
	dir, err := r.targetDirectory(target)
	if err != nil {
		return WriteDecision{Status: RulesIncomplete, Err: err}
	}
	snap := r.discover(ctx, dir, true)
	d := WriteDecision{Snapshot: snap}
	if !snap.Complete {
		var b strings.Builder
		b.WriteString("项目说明不完整，未允许写入：\n")
		for _, issue := range snap.Issues {
			b.WriteString(issue.String() + "\n")
		}
		d.Status = RulesIncomplete
		d.Err = fmt.Errorf("%s", b.String())
		return d
	}
	l.mu.Lock()
	enough := true
	for _, src := range snap.Sources {
		prev, ok := l.contexts[id][src.ID]
		if src.State == Present && (!ok || prev.state != Present || !prev.complete || prev.digest != src.Digest) {
			enough = false
		}
		if src.State == Absent && ok && prev.state != Absent {
			enough = false
		}
	}
	l.mu.Unlock()
	if enough {
		d.Status = CanWrite
		return d
	}
	d.Contribution = l.prepare(id, snap, true, r.opts.TotalBytes)
	// 完整规则组加更新／撤销提示也必须装得下，不能返回永远接纳不了的截断块。
	if d.Contribution == nil || d.Contribution.OnAccepted == nil {
		d.Status = RulesIncomplete
		d.Contribution = nil
		d.Err = fmt.Errorf("%s：规则加更新或撤销标记超过 %d 字节；精简来源或调整总预算后重新完整接纳", target, r.opts.TotalBytes)
		return d
	}
	d.Status = NeedContext
	return d
}

func (r *Resolver) targetDirectory(target string) (string, error) {
	if !filepath.IsAbs(target) || !inside(r.opts.Root, target) {
		return "", fmt.Errorf("写入目标必须在工作区内：%q", target)
	}
	root, err := approval.RealStrict(r.opts.Root)
	if err != nil {
		return "", fmt.Errorf("无法确认工作区；恢复根目录访问后重新核对：%w", err)
	}
	real, err := approval.RealStrict(target)
	if err == nil && !inside(root, real) {
		return "", fmt.Errorf("写入目标真实路径越界；选择工作区内目标：%s", target)
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("无法确认写入目标；恢复目标访问后重新核对：%w", err)
	}
	return filepath.Dir(target), nil
}
