package storagestat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/dbguard"
	"sumika-native/internal/store"
)

// snapshotFormat 标记 Go 写的行。maintenance_runs 里有 Bun 时代留下的 167 行，sizes_json 是另一种形状：
// 读的时候只认 Format 对得上的行，其余当没有，不去猜它们的意思
const snapshotFormat = 2

// 快照行的种类：scan 是一次完整统计，flush 是把"快照加增量"折起来写回
const (
	KindScan  = "scan"
	KindFlush = "flush"
)

// CatSnap 是一个类别在快照里的样子
type CatSnap struct {
	Bytes  int64                  `json:"bytes"`
	Files  int64                  `json:"files"`
	Links  int                    `json:"links,omitempty"`
	Errors int                    `json:"errors,omitempty"`
	Absent bool                   `json:"absent,omitempty"`
	Items  []ctlproto.StorageItem `json:"items,omitempty"`
}

// Snapshot 是 maintenance_runs.sizes_json 的内容
type Snapshot struct {
	Format      int                `json:"format"`
	Kind        string             `json:"kind"`
	ScannedAtMS int64              `json:"scannedAtMs"` // 最近一次完整统计开始的时间（flush 行沿用）
	ScanMs      int64              `json:"scanMs"`
	Cats        map[string]CatSnap `json:"cats"`
}

// Total 各类别字节数之和
func (s Snapshot) Total() int64 {
	var t int64
	for _, c := range s.Cats {
		t += c.Bytes
	}
	return t
}

// Store 是快照的存放处
type Store interface {
	// Latest 返回最近一份认得的快照；一份也没有返回 nil, nil
	Latest(ctx context.Context) (*Snapshot, error)
	// Save 写一行新的。startedAt 是行的主键（毫秒，已经有就顺延），ms 是这次统计的用时
	Save(ctx context.Context, snap Snapshot, startedAt, ms int64) error
}

// ErrNoStore 是没有地方存快照
var ErrNoStore = errors.New("没有运行记录库，快照没法保存")

// TraceStore 把快照存在 trace.db 的 maintenance_runs 里：读用只读连接，写才开写连接，用完就关。
// 写之前只读探查一次：有待跑迁移、太新、认不出就不写（只记一行日志），不替主程序迁移、不拍快照。
type TraceStore struct {
	File string
	Logf func(string, ...any) // nil 用标准库 log
}

func (t TraceStore) logf(format string, args ...any) {
	if t.Logf != nil {
		t.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (t TraceStore) Latest(ctx context.Context) (*Snapshot, error) {
	if _, err := os.Stat(t.File); err != nil {
		return nil, nil
	}
	db, err := store.OpenReadOnlyFile(t.File)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT sizes_json FROM maintenance_runs ORDER BY started_at DESC LIMIT 40`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var s Snapshot
		if json.Unmarshal([]byte(js), &s) == nil && s.Format == snapshotFormat && s.Cats != nil {
			return &s, nil
		}
	}
	return nil, rows.Err()
}

func (t TraceStore) Save(ctx context.Context, snap Snapshot, startedAt, ms int64) error {
	if _, err := os.Stat(t.File); err != nil {
		return ErrNoStore
	}
	// 只读旁路：探查看一眼就够了。有待跑迁移、太新、认不出、还没建好都跳过这次，不迁移、不拍快照、不报错给调用方。
	tracks, err := store.TraceTracks()
	if err != nil {
		t.logf("存储统计：看不了 trace 库的迁移轨，跳过这次写回：%v", err)
		return nil
	}
	dec, err := dbguard.Inspect(ctx, t.File, tracks)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		t.logf("存储统计：trace 库 %s 现在不能写（%v），跳过这次写回", t.File, err)
		return nil
	}
	if dec.Action != dbguard.ActionOpen && dec.Action != dbguard.ActionReadNewer {
		t.logf("存储统计：trace 库 %s 有待跑迁移或还没建好（%v），跳过这次写回（不替主程序迁移）", t.File, dec.Action)
		return nil
	}
	db, err := store.OpenTraceFile(t.File)
	if err != nil {
		return err
	}
	defer db.Close()
	return saveRow(ctx, db, snap, startedAt, ms)
}

// saveRow 写一行：一条 INSERT，要么整行进去要么没有，不会有半行
func saveRow(ctx context.Context, db *sql.DB, snap Snapshot, startedAt, ms int64) error {
	snap.Format = snapshotFormat
	js, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	var max sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM maintenance_runs`).Scan(&max); err != nil {
		return err
	}
	if max.Valid && startedAt <= max.Int64 {
		startedAt = max.Int64 + 1
	}
	_, err = db.ExecContext(ctx, `INSERT INTO maintenance_runs (started_at, ms, total_bytes, sizes_json, cleaned_json) VALUES (?, ?, ?, ?, '{}')`,
		startedAt, ms, snap.Total(), string(js))
	if err != nil {
		return fmt.Errorf("写 maintenance_runs：%w", err)
	}
	return nil
}
