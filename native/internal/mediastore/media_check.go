package mediastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"sumika-native/internal/bunseg"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/datalock"
	"sumika-native/internal/store"
)

// MediaCheckCounts 按段计数；同一文件出现两次就读两次，不拿另一平台的结果代替。
type MediaCheckCounts struct {
	Platform                                                        string
	Segments, Read, Missing, ContentMismatch, EmptyHash, ReadErrors int64
}

// CheckMedia 只读已有平台聊天库，不开后台、不迁移、不更新 blob 使用时间。
// 缺文件单列计数；内容不符、非法段及读取失败才返回错误。
func CheckMedia(ctx context.Context, data string) ([]MediaCheckCounts, error) {
	if strings.TrimSpace(data) == "" {
		return nil, errors.New("媒体核对需要显式数据目录")
	}
	root, err := mediaRootPath(data)
	if err != nil {
		return nil, err
	}
	data = root
	// 复用后台的排他锁，但只读打开已有锁文件，不能为诊断创建文件。
	lock, err := datalock.LockExisting(data)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	entries, err := os.ReadDir(filepath.Join(data, "go", "chat"))
	if err != nil {
		return nil, err
	}
	var counts []MediaCheckCounts
	failed := false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		platform := strings.TrimSuffix(entry.Name(), ".db")
		if !store.ValidPlatform(platform) || entry.IsDir() {
			return counts, errors.New("平台聊天库名称不合法")
		}
		c := MediaCheckCounts{Platform: platform}
		err := checkPlatformMedia(ctx, data, platform, &c)
		if err != nil {
			c.ReadErrors++
		}
		failed = failed || c.ReadErrors > 0 || c.ContentMismatch > 0
		counts = append(counts, c)
	}
	if len(counts) == 0 {
		return nil, errors.New("没有已有平台聊天库")
	}
	if failed {
		return counts, errors.New("媒体核对有内容不符或读取错误")
	}
	return counts, nil
}

func checkPlatformMedia(ctx context.Context, data, platform string, c *MediaCheckCounts) error {
	file := filepath.Join(data, "go", "chat", platform+".db")
	// 已持有 backend.lock，SQLite 不再拿文件锁；独占读用内存 WAL 索引，不建/改 -wal/-shm。
	// 不用 immutable：尚未合并的 WAL 也必须读到。
	vfs := "unix-none"
	if runtime.GOOS == "windows" {
		vfs = "win32-none"
	}
	db, err := sql.Open("sqlite", "file:"+store.URIPath(file)+"?mode=ro&vfs="+vfs+"&_pragma=query_only(1)&_pragma=locking_mode(EXCLUSIVE)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `SELECT segments_json FROM events ORDER BY seq`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		hashes, err := mediaSegmentHashes(raw)
		if err != nil {
			c.ReadErrors++
		}
		for _, hash := range hashes {
			c.Segments++
			if hash == "" {
				c.EmptyHash++
				continue
			}
			f, err := OpenMedia(data, platform, hash)
			if err == nil {
				f, err = verifyMedia(f, hash)
			}
			switch {
			case errors.Is(err, ErrMediaMissing):
				c.Missing++
			case errors.Is(err, errMediaContentMismatch):
				c.ContentMismatch++
			case err != nil:
				c.ReadErrors++
			default:
				if err := f.Close(); err != nil {
					c.ReadErrors++
				} else {
					c.Read++
				}
			}
		}
	}
	return rows.Err()
}

// 旧段只在版本、下标、类型和身份都匹配时补缺失的 hash；冲突不覆盖中立段。
func mediaSegmentHashes(raw sql.NullString) ([]string, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var env struct {
		Segs       []types.Seg `json:"segs"`
		ImportMeta *struct {
			Version int              `json:"version"`
			Legacy  []map[string]any `json:"legacy_segments"`
		} `json:"import_meta"`
	}
	if err := json.Unmarshal([]byte(raw.String), &env); err != nil {
		return nil, err
	}
	var hashes []string
	var issue error
	for i, seg := range env.Segs {
		if seg.Kind != types.SegImage && seg.Kind != types.SegEmoji {
			continue
		}
		hash := seg.Hash
		if meta := env.ImportMeta; meta != nil {
			old, err := bunseg.LegacySegment(meta.Version, meta.Legacy, i, seg)
			if err != nil {
				issue = err
			} else if hash == "" {
				hash = bunseg.StrValue(old["hash"])
			}
		}
		hashes = append(hashes, hash)
	}
	return hashes, issue
}
