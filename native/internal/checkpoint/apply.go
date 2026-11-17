package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
)

// Windows 上每个原子替换都要等持久化；仅并行互不相同的文件，外层服务门仍持有到所有实际 I/O 结束。
const restoreWorkers = 32

func applyPlan(ctx context.Context, work, dir *os.Root, rec *Record, files []Change, limit int64, buffer *[]byte, completed func()) (err error) {
	pack, err := dir.Open(rec.Pack)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, pack.Close()) }()
	if _, err := checkHandle(pack, filepath.Join(dir.Name(), rec.Pack)); err != nil {
		return err
	}
	journal, err := newMaterialJournal(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, journal.file.Close()) }()
	if len(files) <= 1 {
		var parent restoreParent
		defer func() { err = errors.Join(err, parent.close()) }()
		for i := range files {
			if err := applyFile(ctx, work, pack, rec, &files[i], i, limit, buffer, &parent, journal); err != nil {
				return err
			}
			rec.Done = append(rec.Done, files[i].Path)
			completed()
		}
		return ctx.Err()
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var workers sync.WaitGroup
	var done sync.Mutex
	var failed sync.Once
	var failure error
	fail := func(err error) { failed.Do(func() { failure = err; cancel() }) }
	for range min(restoreWorkers, len(files)) {
		workers.Go(func() {
			var local []byte
			var parent restoreParent
			defer func() {
				if err := parent.close(); err != nil {
					fail(err)
				}
			}()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(files) {
					return
				}
				if err := applyFile(child, work, pack, rec, &files[i], i, limit, &local, &parent, journal); err != nil {
					fail(err)
					return
				}
				done.Lock()
				rec.Done = append(rec.Done, files[i].Path)
				done.Unlock()
				completed()
			}
		})
	}
	workers.Wait()
	sort.Strings(rec.Done)
	if failure != nil {
		return failure
	}
	return ctx.Err()
}

func applyFile(ctx context.Context, work *os.Root, pack *os.File, rec *Record, change *Change, index int, limit int64, buffer *[]byte, parent *restoreParent, journal *materialJournal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, name, err := parent.resolve(work, change.Path)
	if err != nil {
		return err
	}
	// 冻结计划已限制前后各 64 MiB；先按预期大小限制读取，文件变大不能让并发缓冲膨胀。
	current, err := readState(ctx, root, name, min(limit, change.After.Bytes), buffer, nil)
	if errors.Is(err, ErrLimit) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if current != change.After {
		return ErrConflict
	}
	var body []byte
	if change.Before.Exists {
		body, err = readObject(pack, rec, change.Before.Hash, limit, buffer)
		if err != nil {
			return err
		}
	}
	return restoreFile(root, parent.file, name, change.Before, body, fmt.Sprintf("%s-%d", rec.ID, index), journal)
}
