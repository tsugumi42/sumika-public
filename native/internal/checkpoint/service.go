package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sumika-native/internal/kernel"
)

// Admit 是 write、edit、powershell 进入检查点之前的身份和授权关。
// 不拍快照的工具调用（只读命令、看不清的命令）也必须先过这一关，不能借"不拍"绕开权限。
func (s *Service) Admit(ctx context.Context, c Capture) error {
	if c.Tool != "write" && c.Tool != "edit" && c.Tool != "powershell" || c.Invocation.RunID == "" || c.Invocation.ToolID == "" || c.Invocation.RootID == "" {
		return ErrUnavailable
	}
	if !ownerScope(c.Owner) {
		return ErrUnavailable
	}
	if c.Invocation.Kind == kernel.KindChild {
		child := c.Invocation.Scope
		if child.CharacterID != c.Owner.CharacterID || child.ScopeID != c.Owner.ScopeID || child.Platform != c.Owner.Platform || child.Channel != c.Owner.Channel || child.Scene != c.Owner.Scene || child.AccountID != c.Owner.AccountID || child.ActorKind != c.Owner.ActorKind || child.ActorID != c.Owner.ActorID || child.Conversation != "internal:"+c.Invocation.RunID || child.AuthorityID == "" || child.AuthorityRevision == 0 || c.Invocation.ParentRunID == "" {
			return ErrUnavailable
		}
	} else if c.Invocation.Kind != kernel.KindForeground || c.Invocation.Scope != c.Owner {
		return ErrUnavailable
	}
	return s.opts.Authorize(ctx, c.Invocation.Scope, c.Tool)
}

// 快照这一步因内容本身拍不成（容量、扫描预算、非普通文件、拍的时候文件在变）才可放行；
// 链接（ErrLink）不放行：硬链接、联接可能把写入带到工作目录外，拍不了快照时更不能放过。
// 身份、授权、读写失败和取消都不在其中，照原样报错。
func uncapturable(err error) bool {
	return errors.Is(err, ErrLimit) || errors.Is(err, ErrConflict) || errors.Is(err, errUncapturable)
}

// Run 先拍前态再执行 execute。拍不成快照（uncapturable）时 execute 照常执行，
// 返回的记录 Status 为 "skipped"，SkipReason 写明原因；记录本身也存不下时 ID 为空。
func (s *Service) Run(ctx context.Context, c Capture, execute func() error) (Record, error) {
	if err := s.Admit(ctx, c); err != nil {
		return Record{}, err
	}
	root, leave, err := s.enter(ctx)
	if err != nil {
		return Record{}, err
	}
	defer leave()
	records, used, err := s.records(root, false)
	if err != nil {
		if uncapturable(err) {
			return s.runSkipped(ctx, root, nil, false, 0, c, nil, "", err, execute)
		}
		return Record{}, err
	}
	reserve := 4*s.opts.MaxScanBytes + 2*metadataLimit
	if c.Paths != nil {
		// 一次扫描的字节有 MaxScanBytes 封顶，预留也按它封顶；否则目标一多就永远预留不出。
		scanned := min(s.opts.MaxFileBytes*int64(len(c.Paths)), s.opts.MaxScanBytes)
		reserve = 4*scanned + 2*metadataLimit
	}
	if err := s.reserve(root, records, used, reserve, nil); err != nil {
		if uncapturable(err) {
			return s.runSkipped(ctx, root, records, true, used, c, nil, "", err, execute)
		}
		return Record{}, err
	}
	work, identity, err := openWorkspace(c.CWD)
	if err != nil {
		return Record{}, err
	}
	defer work.Close()
	c.CWD = work.Name()
	rec, dir, err := s.create(root, records, c, identity)
	if err != nil {
		return rec, err
	}
	defer dir.Close()
	published := false
	defer func() {
		if !published {
			root.RemoveAll(rec.ID)
		}
	}()
	writer, err := openPack(dir, &rec)
	if err != nil {
		return Record{}, err
	}
	rec.Before, err = scan(ctx, work, c.Paths, s.opts, &s.buffer, writer.put)
	err = errors.Join(err, writer.close())
	if err != nil {
		if uncapturable(err) {
			dir.Close()
			root.RemoveAll(rec.ID)
			return s.runSkipped(ctx, root, records, true, used, c, work, identity, err, execute)
		}
		return Record{}, err
	}
	if err := saveRecord(dir, rec); err != nil {
		return Record{}, err
	}
	published = true
	if err := ctx.Err(); err != nil {
		return rec, err
	}
	runErr := execute()
	writer, err = openPack(dir, &rec)
	if err != nil {
		return rec, errors.Join(runErr, err)
	}
	rec.After, err = scan(context.WithoutCancel(ctx), work, c.Paths, s.opts, &s.buffer, writer.put)
	err = errors.Join(err, writer.close())
	if err != nil {
		rec.Status = "interrupted"
		rec.After = nil
		return rec, errors.Join(runErr, fmt.Errorf("执行可能已完成，检查点后态保存失败；不要重跑工具：%w", err), saveRecord(dir, rec))
	}
	changes, err := recordChanges(pathKeys{}, &rec)
	if err != nil {
		return rec, errors.Join(runErr, err)
	}
	before, after := map[string]FileState{}, map[string]FileState{}
	for _, change := range changes {
		if change.Before != change.After || change.Path != change.CurrentPath {
			before[change.Path], after[change.CurrentPath] = change.Before, change.After
		}
	}
	rec.Before, rec.After, rec.Status = before, after, "ready"
	if _, err := s.compact(root, dir, &rec); err != nil {
		return rec, errors.Join(runErr, fmt.Errorf("执行可能已完成，检查点结局保存失败；不要重跑工具：%w", err))
	}
	return rec, runErr
}

// Skip 用于调用方自己判定"这次不拍快照"（看不清命令会动什么）：过同一道身份授权关，照常执行，并留一条 skipped 记录。
func (s *Service) Skip(ctx context.Context, c Capture, reason string, execute func() error) (Record, error) {
	if err := s.Admit(ctx, c); err != nil {
		return Record{}, err
	}
	root, leave, err := s.enter(ctx)
	if err != nil {
		return Record{}, err
	}
	defer leave()
	records, used, err := s.records(root, false)
	if err != nil {
		if !uncapturable(err) {
			return Record{}, err
		}
		records = nil
	}
	return s.runSkipped(ctx, root, records, err == nil, used, c, nil, "", errors.New(reason), execute)
}

// runSkipped 在拍不成快照时照常执行，并尽力留一条 Status 为 "skipped" 的空记录。
// 记录存不下（存储满、目录列不出）不影响执行，只是返回的记录没有 ID。
func (s *Service) runSkipped(ctx context.Context, root *os.Root, records []Record, listed bool, used int64, c Capture, work *os.Root, identity string, cause error, execute func() error) (Record, error) {
	slog.Warn("检查点没拍成快照，工具照常执行", "tool", c.Tool, "run", c.Invocation.RunID, "cwd", c.CWD, "err", cause)
	out := Record{Status: "skipped", SkipReason: cause.Error(), Tool: c.Tool}
	if listed && s.reserve(root, records, used, 2*metadataLimit, nil) != nil {
		listed = false // 记录名额或空间不够，只执行不留记录
	}
	if work == nil && listed {
		var err error
		if work, identity, err = openWorkspace(c.CWD); err == nil {
			defer work.Close()
		} else {
			work = nil
		}
	}
	if listed && work != nil {
		if rec, dir, err := s.create(root, records, c, identity); err == nil {
			rec.Status, rec.SkipReason, rec.Pack = "skipped", out.SkipReason, "ready.bin"
			f, ferr := dir.OpenFile("ready.bin", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if ferr == nil {
				ferr = f.Close()
			}
			if ferr == nil {
				ferr = saveRecord(dir, rec)
			}
			dir.Close()
			if ferr == nil {
				out = rec
			} else {
				root.RemoveAll(rec.ID)
				slog.Warn("检查点没拍快照的记录没存下", "err", ferr)
			}
		} else {
			slog.Warn("检查点没拍快照的记录没存下", "err", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, execute()
}
func (s *Service) List(ctx context.Context, owner kernel.Scope) ([]Record, error) {
	if err := s.authorize(ctx, owner, "read"); err != nil {
		return nil, err
	}
	root, leave, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	records, _, err := s.records(root, false)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Owner == owner {
			out = append(out, records[i])
		}
	}
	return out, nil
}
func (s *Service) Preview(ctx context.Context, owner kernel.Scope, id, mode string) (Plan, error) {
	if err := s.authorize(ctx, owner, "read"); err != nil {
		return Plan{}, err
	}
	root, leave, err := s.enter(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer leave()
	records, _, err := s.records(root, false)
	if err != nil {
		return Plan{}, err
	}
	return s.plan(ctx, root, records, owner, id, mode, true)
}

// Notices 以检查点记录作持久来源；新 Root 用自己的材料 ID 接纳，不复用原工具结果。
func (s *Service) Notices(ctx context.Context, owner kernel.Scope) ([]string, error) {
	records, err := s.List(ctx, owner)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, r := range records {
		if r.InverseOf == "" {
			continue
		}
		paths := append([]string(nil), r.Done...)
		sort.Strings(paths)
		detail := paths
		if len(detail) > 20 {
			detail = detail[:20]
		}
		result = append(result, fmt.Sprintf("检查点恢复 %s：state=%s target=%s inverse=%s completed=%d paths=%s；未列路径请按检查点编号查看。", r.ID, r.Status, r.InverseOf, r.ID, len(paths), strings.Join(detail, ", ")))
		if len(result) == 8 {
			break
		}
	}
	return result, nil
}

func fullPaths(cwd string, states map[string]FileState) []string {
	paths := make([]string, 0, len(states))
	for path := range states {
		paths = append(paths, filepath.Join(cwd, path))
	}
	sort.Strings(paths)
	return paths
}
