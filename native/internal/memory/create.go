package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
)

// createOpenDB 是新建角色库时护栏拿的可写连接。生产就是 openBaseDB；只有测试会换成会失败的版本，
// 生产代码不读环境变量、不加开关。替换的测试不能 t.Parallel。
var createOpenDB = openBaseDB

// CreateIfEmpty 给一个“空”的角色记忆库建当前结构的空库：文件不存在、0 字节、或只有 sumika_schema。
// 空不空由护栏判定（dbguard.Inspect 的 ActionCreate），这里不另写一套；已有用户表的库一律不碰、返回 created=false，
// 启动时不迁移，留给 memory-migrate。建库走 dbguard.Open：先执行 BaseSchema，再跑 memory 轨 0001～0003、写版本表。
//
// 建库前先查位置（paths.CheckMemoryCreatePath）：库必须在 <data>/go/characters/<角色>/ 下，已经存在的每一级不许是链接；
// 角色 id 再过 ValidCharacterID。调用前就在的空文件先连旁文件一起改名到 memory.db.empty-<时间戳>，按“不存在”新建；
// 失败把新建的删掉、旁名挪回原名（字节不变），成功删掉旁名。父目录不存在就建。
//
// 失败返回护栏的错误（新建失败是 *dbguard.Unavailable，原因 create_failed）；只有护栏这次的判定真是 ActionCreate
// 时调用方才会把结果当成“建库失败”。ctx 取消直接返回取消错误，Report.Action 不标 Create。snapshotDir / relName
// 只在有迁移时才用来拍快照。
func CreateIfEmpty(ctx context.Context, data, file, snapshotDir, relName string) (created bool, rep dbguard.Report, err error) {
	rep.File = file
	tracks, err := memoryTracks()
	if err != nil {
		return false, rep, err
	}
	dec, err := dbguard.Inspect(ctx, file, tracks)
	if err != nil {
		return false, rep, err
	}
	rep.Action = dec.Action
	if dec.Action != dbguard.ActionCreate {
		return false, rep, nil
	}
	failCreate := func(err error) (bool, dbguard.Report, error) {
		return false, rep, &dbguard.Unavailable{File: file, Reason: dbguard.ReasonCreateFailed, Detail: err.Error(), Err: err}
	}
	if err := paths.CheckMemoryCreatePath(data, file); err != nil {
		return failCreate(err)
	}
	if err := ValidCharacterID(filepath.Base(filepath.Dir(file))); err != nil {
		return failCreate(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return failCreate(fmt.Errorf("建角色记忆库的目录 %s：%w", filepath.Dir(file), err))
	}
	staged, err := stageEmptyOriginal(file)
	if err != nil {
		return failCreate(err)
	}
	db, opened, err := dbguard.Open(ctx, file, tracks, dbguard.Options{
		SnapshotDir: snapshotDir,
		RelName:     relName,
		OpenDB: func(file string, fresh bool) (*sql.DB, error) {
			if !fresh {
				return nil, fmt.Errorf("%s：不是新建，不开可写连接", filepath.Base(file))
			}
			return createOpenDB(ctx, file, fresh)
		},
	})
	rep = opened
	if db != nil {
		db.Close()
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			// 取消不是建库失败：调用方照旧整体失败，不写提醒
			rep.Action = dbguard.ActionNone
			if staged != nil {
				_ = staged.restore()
			}
			return false, rep, fmt.Errorf("新建 %s 时取消：%w", file, cerr)
		}
		if rep.Action == dbguard.ActionCreate {
			// 只有护栏这次也判定是新建，删的才是这次建出来的；中途被别人建出库时不动它
			discardNewDBFiles(file)
		}
		if staged != nil {
			if rerr := staged.restore(); rerr != nil {
				err = fmt.Errorf("%w；把调用前的空库挪回原名失败：%v", err, rerr)
			}
		}
		return false, rep, err
	}
	if staged != nil {
		staged.discard()
	}
	return rep.Action == dbguard.ActionCreate, rep, nil
}

// stagedEmptyOriginal 是调用前就在的空库（主文件 + 可能有的旁文件）改名后的位置。
type stagedEmptyOriginal struct {
	files map[string]string // 原名 → 旁名
}

// stageEmptyOriginal 把调用前就在的空库连同 -wal、-shm、-journal 挪到同目录的 memory.db.empty-<时间戳>。
// 主文件不存在（或有旁文件却没有主文件）时只挪看得见的；一个也没有返回 nil。挪到一半失败就把已经挪的挪回去。
func stageEmptyOriginal(file string) (*stagedEmptyOriginal, error) {
	staged := &stagedEmptyOriginal{files: map[string]string{}}
	base := file + ".empty-" + time.Now().UTC().Format("20060102T150405.000000000")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		src := file + suffix
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			_ = staged.restore()
			return nil, fmt.Errorf("看调用前的 %s：%w", src, err)
		}
		dst := base + suffix
		if err := os.Rename(src, dst); err != nil {
			_ = staged.restore()
			return nil, fmt.Errorf("把调用前的 %s 挪到旁名：%w", src, err)
		}
		staged.files[src] = dst
	}
	if len(staged.files) == 0 {
		return nil, nil
	}
	return staged, nil
}

// restore 把旁名挪回原名。原名已经有东西（这次新建没删干净）时不覆盖，返回错误。
func (s *stagedEmptyOriginal) restore() error {
	var first error
	for src, dst := range s.files {
		if _, err := os.Lstat(src); err == nil {
			if first == nil {
				first = fmt.Errorf("原名 %s 已经有文件，不能覆盖", src)
			}
			continue
		}
		if err := os.Rename(dst, src); err != nil && first == nil {
			first = fmt.Errorf("把 %s 挪回原名：%w", dst, err)
		}
	}
	return first
}

// discard 删掉旁名的几份：建库成功了，空库本来就没有数据。
func (s *stagedEmptyOriginal) discard() {
	for _, dst := range s.files {
		_ = os.Remove(dst)
	}
}

// discardNewDBFiles 删掉这次新建出来的主文件和旁文件；不存在的跳过。
func discardNewDBFiles(file string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(file + suffix)
	}
}
