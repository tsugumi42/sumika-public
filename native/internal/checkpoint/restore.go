package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"sumika-native/internal/kernel"
)

func (s *Service) plan(ctx context.Context, root *os.Root, records []Record, owner kernel.Scope, id, mode string, verifyFiles bool) (Plan, error) {
	if !hexID(id, 32) || mode != "step" && mode != "turn" && mode != "recovery" && mode != "resume" {
		return Plan{}, ErrUnavailable
	}
	verifyFiles = verifyFiles || mode == "recovery" || mode == "resume"
	index := -1
	for i, r := range records {
		if r.ID == id && r.Owner == owner {
			index = i
			break
		}
	}
	if index < 0 {
		return Plan{}, ErrUnavailable
	}
	selected := &records[index]
	if selected.Status == "skipped" {
		return Plan{}, ErrNoSnapshot
	}
	cutoff := s.opts.Now().Add(-s.opts.Retain).UnixMilli()
	if selected.Status == "ready" && selected.CreatedMS < cutoff {
		return Plan{}, ErrUnavailable
	}
	if mode == "turn" {
		first := -1
		for i := range index + 1 {
			r := &records[i]
			if r.ID == selected.RunFirstID && r.Invocation.RunID == selected.Invocation.RunID && r.Invocation.RootID == selected.Invocation.RootID && r.Invocation.Scope == selected.Invocation.Scope && r.RootIdentity == selected.RootIdentity {
				first = i
				break
			}
		}
		if first < 0 {
			return Plan{}, ErrUnavailable
		}
		index = first
	}
	start := records[index]
	if start.Status == "ready" && start.CreatedMS < cutoff {
		return Plan{}, ErrUnavailable
	}
	plan := Plan{ID: id, Mode: mode, CWD: start.CWD, Invocation: selected.Invocation}
	if len(records) > 0 {
		plan.Through = records[len(records)-1].Sequence
	}
	work, identity, err := openWorkspace(start.CWD)
	if err != nil {
		return plan, err
	}
	defer work.Close()
	if identity != start.RootIdentity {
		return plan, ErrConflict
	}
	changes := map[string]Change{}
	first := map[string]uint64{}
	keys := pathKeys{}
	if mode == "recovery" || mode == "resume" {
		if selected.Status == "ready" {
			return plan, ErrUnavailable
		}
		if mode == "resume" && selected.InverseOf == "" {
			return plan, ErrUnavailable
		}
		var paths []string
		if !selected.Whole {
			paths = fullPaths(selected.CWD, selected.Before)
			paths, err = resolveRecoveryPaths(work, paths)
			if err != nil {
				return plan, err
			}
		}
		current, err := scan(ctx, work, paths, s.opts, &s.buffer, nil)
		if err != nil {
			return plan, err
		}
		frozen := *selected
		frozen.After = current
		if mode == "resume" {
			// pending/partial 的 After 在任何文件发布前已经持久冻结；续做仍要重新预览和单次审批。
			frozen.Before = selected.After
		}
		changes, err = recordChanges(keys, &frozen)
		if err != nil {
			return plan, err
		}
		for key := range changes {
			first[key] = selected.Sequence
		}
	} else {
		for _, r := range records[index:] {
			if r.Invocation.Scope != start.Invocation.Scope || r.RootIdentity != start.RootIdentity {
				continue
			}
			if r.Status == "skipped" {
				// 没拍快照的执行不提供变化；它动过的文件由当前状态与记录后态的比对（CAS）把关。
				continue
			}
			if r.Status != "ready" {
				return plan, fmt.Errorf("%w：存在缺可信后态的执行 %s", ErrConflict, r.ID)
			}
			delta, err := recordChanges(keys, &r)
			if err != nil {
				return plan, err
			}
			for key, change := range delta {
				if prior, ok := changes[key]; ok {
					if prior.After != change.Before {
						return plan, fmt.Errorf("%w：历史链不连续 %s", ErrConflict, change.Path)
					}
					prior.After, prior.CurrentPath = change.After, change.CurrentPath
					changes[key] = prior
				} else {
					changes[key] = change
					first[key] = r.Sequence
				}
			}
		}
	}
	// 同一实际父目录的大小写别名也共享跨域冲突门槛，敏感目录中的独立文件不误合并。
	for _, r := range records {
		if r.Invocation.Scope == start.Invocation.Scope && r.RootIdentity == start.RootIdentity {
			continue
		}
		for _, states := range []map[string]FileState{r.Before, r.After} {
			for name := range states {
				key, err := keys.key(filepath.Join(r.CWD, name))
				if err != nil {
					return plan, err
				}
				if seq, ok := first[key]; ok && r.Sequence >= seq {
					return plan, ErrConflict
				}
			}
		}
	}
	sources := map[string]*Record{}
	for i := range records {
		sources[records[i].ID] = &records[i]
	}
	var beforeBytes, afterBytes int64
	for _, change := range changes {
		current := change.After
		if verifyFiles {
			var err error
			current, err = readState(ctx, work, change.CurrentPath, s.opts.MaxFileBytes, &s.buffer, nil)
			if err != nil {
				return plan, err
			}
			if current != change.After {
				return plan, fmt.Errorf("%w：%s", ErrConflict, change.CurrentPath)
			}
		}
		beforeBytes += change.Before.Bytes
		afterBytes += current.Bytes
		if beforeBytes > s.opts.MaxScanBytes || afterBytes > s.opts.MaxScanBytes || len(plan.Files) >= s.opts.MaxFiles {
			return plan, ErrLimit
		}
		if verifyFiles && change.Before.Exists {
			if _, err := s.object(root, sources[change.SourceID], change.Before.Hash); err != nil {
				return plan, err
			}
		}
		if change.Before != current || change.Path != change.CurrentPath {
			plan.Files = append(plan.Files, change)
		}
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	plan.Version, err = digest(struct {
		Owner kernel.Scope
		Plan  Plan
	}{owner, plan})
	return plan, err
}

func (s *Service) Restore(ctx context.Context, owner kernel.Scope, in kernel.Invocation, id, mode, version string) (result Record, restoreErr error) {
	if err := s.authorize(ctx, owner, "write"); err != nil {
		return Record{}, err
	}
	if in.Scope != owner || in.Kind != kernel.KindForeground || in.RootID == "" || in.RunID == "" || in.ToolID == "" || !hexID(id, 32) || !hexID(version, 64) {
		return Record{}, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	op := &restoreOperation{owner: owner, progress: Progress{TargetID: id, State: "preparing"}, cancel: cancel, done: make(chan struct{})}
	// 先登记再取变更门，排队恢复也能被查询和取消；不允许覆盖仍活着的操作。
	s.progress.Lock()
	if s.progress.active == nil {
		s.progress.active = make(map[kernel.Scope]*restoreOperation)
	}
	if s.progress.active[owner] != nil {
		s.progress.Unlock()
		return Record{}, ErrConflict
	}
	s.progress.active[owner] = op
	// 与订阅登记共用状态锁；绑定不依赖轮询，短操作的终态不会被后续恢复挤掉。
	for w := range s.progress.waiting {
		if w.matches(op) {
			w.op = op
			w.stopWaiting()
			delete(s.progress.waiting, w)
		}
	}
	s.progress.Unlock()
	outcomeSaved := false
	defer func() {
		s.progress.Lock()
		if result.ID != "" {
			if !outcomeSaved {
				result.Status = "interrupted"
			}
			op.progress = recordProgress(result)
		} else {
			op.progress.State = "failed"
		}
		op.progress.OutcomeSaved = outcomeSaved
		if restoreErr != nil {
			op.progress.Error = restoreErr.Error()
		}
		close(op.done)
		s.progress.last = op
		delete(s.progress.active, owner)
		s.progress.Unlock()
	}()
	root, leave, err := s.enter(ctx)
	if err != nil {
		return Record{}, err
	}
	defer leave()
	records, used, err := s.records(root, false)
	if err != nil {
		return Record{}, err
	}
	// 正常恢复在保存逆前态时一次读取并核对 CAS；不在此前重复扫同一批文件。
	plan, err := s.plan(ctx, root, records, owner, id, mode, false)
	if err != nil {
		return Record{}, err
	}
	if plan.Version != version {
		return Record{}, ErrConflict
	}
	s.updateProgress(op, func(p *Progress) { p.Total = len(plan.Files) })
	pinned := map[string]bool{id: true}
	sources := map[string]*Record{}
	for i := range records {
		sources[records[i].ID] = &records[i]
	}
	expected := map[string]FileState{}
	for _, change := range plan.Files {
		pinned[change.SourceID] = true
		expected[change.CurrentPath] = change.After
	}
	if err := s.reserve(root, records, used, 4*s.opts.MaxScanBytes+2*metadataLimit, pinned); err != nil {
		return Record{}, err
	}
	work, identity, err := openWorkspace(plan.CWD)
	if err != nil {
		return Record{}, err
	}
	defer work.Close()
	paths := fullPaths(plan.CWD, expected)
	rec, dir, err := s.create(root, records, Capture{Invocation: in, Owner: owner, CWD: plan.CWD, Tool: "checkpoint_restore", Paths: paths}, identity)
	if err != nil {
		return rec, err
	}
	defer dir.Close()
	rec.InverseOf = id
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
	rec.Before, err = scan(ctx, work, paths, s.opts, &s.buffer, writer.put)
	if err == nil {
		for name, state := range expected {
			if rec.Before[name] != state {
				err = ErrConflict
				break
			}
		}
	}
	rec.After = map[string]FileState{}
	if err == nil {
		for _, change := range plan.Files {
			rec.After[change.Path] = change.Before
			if !change.Before.Exists {
				continue
			}
			body, readErr := s.object(root, sources[change.SourceID], change.Before.Hash)
			if readErr != nil {
				err = readErr
				break
			}
			if err = writer.put(change.Before.Hash, body); err != nil {
				break
			}
		}
	}
	err = errors.Join(err, writer.close())
	if err != nil {
		return Record{}, err
	}
	if err := saveRecord(dir, rec); err != nil {
		return Record{}, err
	}
	published = true
	s.updateProgress(op, func(p *Progress) { p.InverseID, p.State = rec.ID, "restoring" })
	applyErr := applyPlan(ctx, work, dir, &rec, plan.Files, s.opts.MaxFileBytes, &s.buffer, func() {
		s.updateProgress(op, func(p *Progress) { p.Done++ })
	})
	s.updateProgress(op, func(p *Progress) { p.State = "finalizing" })
	if applyErr != nil {
		rec.Status = "partial"
		saveErr := saveRecord(dir, rec)
		outcomeSaved = saveErr == nil
		return rec, errors.Join(fmt.Errorf("恢复未完成，inverse=%s：%w", rec.ID, applyErr), saveErr)
	}
	rec.Status = "ready"
	outcomeSaved, err = s.compact(root, dir, &rec)
	if err != nil {
		return rec, fmt.Errorf("文件已恢复，结局收尾失败，inverse=%s：%w", rec.ID, err)
	}
	return rec, nil
}

var publishDeletion = durableRename
var publishReplacement = publishRestoreTemp
var syncRestoreTemp = (*os.File).Sync

func restoreFile(root *os.Root, parent *os.File, name string, state FileState, body []byte, id string, journal *materialJournal) (err error) {
	if _, err := checkHandle(parent, root.Name()); err != nil {
		return err
	}
	if !state.Exists {
		if err := checkParents(root, name); err != nil {
			return err
		}
		// 持久移走原名再清理墓碑；后者中断最多留下自有材料，不会让已提交的目标名复现。
		tomb := materialName(id, false)
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		identity, err := journal.register(parent, file, id)
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		if err := publishDeletion(root, name, tomb); err != nil {
			return err
		}
		return removeRestoreMaterial(root, tomb, identity)
	}
	tmp := materialName(id, true)
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, state.Mode|0200)
	if err != nil {
		return err
	}
	identity, err := journal.register(parent, f, id)
	if err != nil {
		return errors.Join(err, f.Close())
	}
	defer func() {
		if tmp != "" {
			err = errors.Join(err, removeRestoreMaterial(root, tmp, identity))
		}
	}()
	_, err = f.Write(body)
	err = errors.Join(err, syncRestoreTemp(f), f.Close())
	if err != nil {
		return err
	}
	// 父目录可能在持有句柄后被移动或换成联接；临近发布重新核对，不沿缓存路径写到别处。
	if _, err := checkHandle(parent, root.Name()); err != nil {
		return err
	}
	// 自有材料先可写发布，再用同一发布句柄落实快照属性并刷新，避免只读材料无法重开。
	if err := publishReplacement(root, tmp, name, state.Mode); err != nil {
		return err
	}
	tmp = ""
	return nil
}
