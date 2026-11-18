package mediastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"sumika-native/internal/bunseg"
	"sumika-native/internal/channel/types"
)

// StickerHashes 取一个聊天库里表情包和头像的内容哈希（媒体文件名正好是哈希），给备份挑 dailyMedia: stickers 用（规格-数据目录与备份 10.1）。三种来源取并集：
//  1. blobs 表里 kind 是 sticker 或 avatar 的行（Bun 镜像表，Go 的聊天库和旧版 shared/events.db 里都有）；
//  2. qq_avatars.hash（头像登记）；
//  3. events.segments_json 里 kind 是 emoji 的段的 hash（表情包和商城表情，types.SegEmoji）；导入来的旧段中立段没有 hash 时，照 media_check 的做法从 import_meta 的对应旧段补。
//
// 表不在（这个平台没有）就当没有那一路，不报错；只读，调用方可以传只读连接或副本。
func StickerHashes(ctx context.Context, db *sql.DB) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, q := range []string{
		`SELECT hash FROM blobs WHERE kind IN ('sticker', 'avatar')`,
		`SELECT hash FROM qq_avatars WHERE hash IS NOT NULL AND hash <> ''`,
	} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			if isNoTable(err) {
				continue
			}
			return nil, err
		}
		for rows.Next() {
			var h sql.NullString
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			add(h.String)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	// 段里的表情包：只把文字里带 "emoji" 的行拉出来再解析，不整表解 JSON
	rows, err := db.QueryContext(ctx, `SELECT segments_json FROM events WHERE segments_json LIKE '%"emoji"%'`)
	if err != nil {
		if isNoTable(err) {
			return out, nil
		}
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var env struct {
			Segs       []types.Seg `json:"segs"`
			ImportMeta *struct {
				Version int              `json:"version"`
				Legacy  []map[string]any `json:"legacy_segments"`
			} `json:"import_meta"`
		}
		if json.Unmarshal([]byte(raw.String), &env) != nil {
			continue // 读不懂的行不让整个备份失败：少带一个表情包，不丢别的
		}
		for i, s := range env.Segs {
			if s.Kind != types.SegEmoji {
				continue
			}
			h := s.Hash
			if meta := env.ImportMeta; meta != nil && h == "" {
				// 导入来的旧段：中立段没有 hash 时，从对应的旧段补（和 mediaSegmentHashes 同一个做法，对不上就不补）
				if old, err := bunseg.LegacySegment(meta.Version, meta.Legacy, i, s); err == nil {
					h = bunseg.StrValue(old["hash"])
				}
			}
			add(h)
		}
	}
	return out, rows.Err()
}

func isNoTable(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "no such column"))
}
