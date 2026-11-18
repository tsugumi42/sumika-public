package store

import (
	"context"
	"database/sql"
	"errors"
)

// ReadAvatarHash 读一个头像登记的内容哈希（qq_avatars：key → hash、fetched_at）。
// key 是 Bun 时代的写法：user:<QQ 号>、group:<群号>（card 封面是 url:…，调用方不该拿它来问）。
// 没有这一行，或者行里 hash 是空（上次没取到），ok 为假；只读，不补不改。
func (s *Store) ReadAvatarHash(ctx context.Context, key string) (hash string, fetchedAt int64, ok bool, err error) {
	var h sql.NullString
	err = s.Events.QueryRowContext(ctx, `SELECT hash,fetched_at FROM qq_avatars WHERE key=?`, key).Scan(&h, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return h.String, fetchedAt, h.Valid && h.String != "", nil
}
