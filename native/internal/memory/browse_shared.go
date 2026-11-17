package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// 页面合并角色库与显式共用库的 SQL 视图，分页和搜索仍在 SQLite 内完成。
// mode=ro 保护两个磁盘文件；临时视图只存在当前查询连接，不复制事实或原话。
func (b *Browser) attachOwnerPool(file string) error {
	if file == "" {
		return nil
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	schema, err := b.Schema(context.Background())
	if err != nil {
		return err
	}
	b.localMigrated = schema.Migrated()
	for _, db := range []*sql.DB{b.q, b.poll} {
		if _, err := db.Exec(`ATTACH DATABASE ? AS owner_shared`, readOnlyDSN(file)); err != nil {
			return err
		}
	}
	if _, err := b.q.Exec(`PRAGMA query_only=OFF`); err != nil {
		return err
	}
	for _, table := range []string{"facts", "episodes", "fact_revisions", "fact_evidence", "item_scopes"} {
		names, err := browseColumns(b.q, "owner_shared", table)
		if err != nil {
			return err
		}
		local, err := browseColumns(b.q, "main", table)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, name := range local {
			have[name] = true
		}
		var left, right []string
		for _, name := range names {
			ident := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
			expression := `NULL`
			if have[name] {
				expression = ident
			}
			if !have[name] && table == "episodes" && name == "status" {
				expression = `'active'`
			}
			sharedExpression := ident
			// 修订自增号各库独立；映射成不相撞的游标值，不改磁盘里的编号。
			if table == "fact_revisions" && name == "id" {
				expression = ident + `*2`
				sharedExpression = ident + `*2+1`
			}
			left = append(left, expression+` AS `+ident)
			right = append(right, sharedExpression)
		}
		query := `CREATE TEMP VIEW "` + table + `" AS `
		if len(local) > 0 {
			query += `SELECT ` + strings.Join(left, ",") + ` FROM main."` + table + `" UNION ALL `
		}
		query += `SELECT ` + strings.Join(right, ",") + ` FROM owner_shared."` + table + `"`
		if _, err = b.q.Exec(query); err != nil {
			return fmt.Errorf("共用记忆视图 %s：%w", table, err)
		}
	}
	if _, err := b.q.Exec(`PRAGMA query_only=ON`); err != nil {
		return err
	}
	b.shared = true
	return nil
}

func browseColumns(db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?,?) ORDER BY cid`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (b *Browser) fillSharedSources(ctx context.Context, rows []BrowseRow) error {
	if !b.shared || len(rows) == 0 {
		return nil
	}
	ids := make([]any, 0, len(rows))
	for _, r := range rows {
		if strings.HasPrefix(r.Ref, "f:") {
			ids = append(ids, refID(r.Ref))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rs, err := b.q.QueryContext(ctx, `SELECT id,coalesce(learned_by,'') FROM owner_shared.facts WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...)
	if err != nil {
		return err
	}
	defer rs.Close()
	by := map[string]string{}
	for rs.Next() {
		var id, char string
		if err = rs.Scan(&id, &char); err != nil {
			return err
		}
		by["f:"+id] = char
	}
	if err = rs.Err(); err != nil {
		return err
	}
	for i := range rows {
		rows[i].LearnedBy = by[rows[i].Ref]
	}
	return nil
}
