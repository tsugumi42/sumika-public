// Package sqlitedump 把一个 SQLite 库导出成可以直接执行的 SQL 文本。
// 只给测试冻结构建库用（里程碑 SQL）；常驻代码不得引用。
package sqlitedump

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type object struct {
	typ  string
	name string
	sql  string
}

// Dump 按 sqlite_master 的顺序导出表、索引、触发器和数据。虚拟表只建表，数据用 rebuild 重建；
// 它的影子表不单独导出。导出结果带 PRAGMA foreign_keys=OFF 和 BEGIN/COMMIT，可以直接 Exec。
func Dump(db *sql.DB) (string, error) {
	shadow, err := shadowTables(db)
	if err != nil {
		return "", err
	}
	objects, err := objects(db)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\n")
	var dataTables []string
	var virtual []string
	for _, o := range objects {
		if shadow[o.name] {
			continue
		}
		if o.typ == "table" {
			if isVirtual(o.sql) {
				virtual = append(virtual, o.name)
			} else {
				dataTables = append(dataTables, o.name)
			}
		}
		if o.typ == "view" || o.typ == "trigger" {
			continue // 触发器等数据插完再建，否则插入事件时会和导出的事件派生表重复
		}
		writeStatement(&b, o.sql)
	}
	for _, name := range dataTables {
		if err := dumpRows(&b, db, name); err != nil {
			return "", fmt.Errorf("导出 %s 的数据：%w", name, err)
		}
	}
	for _, o := range objects {
		if shadow[o.name] || (o.typ != "view" && o.typ != "trigger") {
			continue
		}
		writeStatement(&b, o.sql)
	}
	for _, name := range virtual {
		// FTS5 的索引和内容都存在影子表里；只建虚拟表，数据插完后 rebuild 一次保证一致。
		fmt.Fprintf(&b, "INSERT INTO %s(%s) VALUES('rebuild');\n", quoteIdent(name), quoteIdent(name))
	}
	b.WriteString("COMMIT;\n")
	return b.String(), nil
}

func shadowTables(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_list WHERE schema = 'main' AND type = 'shadow'`)
	if err != nil {
		return nil, fmt.Errorf("读影子表：%w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func objects(db *sql.DB) ([]object, error) {
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
		ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 WHEN 'view' THEN 2 ELSE 3 END, name`)
	if err != nil {
		return nil, fmt.Errorf("读 sqlite_master：%w", err)
	}
	defer rows.Close()
	var out []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.typ, &o.name, &o.sql); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func writeStatement(b *strings.Builder, stmt string) {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return
	}
	b.WriteString(stmt)
	if !strings.HasSuffix(stmt, ";") {
		b.WriteByte(';')
	}
	b.WriteByte('\n')
}

func dumpRows(b *strings.Builder, db *sql.DB, table string) error {
	order := ""
	if !strings.Contains(strings.ToUpper(tableDDL(db, table)), "WITHOUT ROWID") {
		order = " ORDER BY rowid"
	}
	rows, err := db.Query("SELECT * FROM " + quoteIdent(table) + order)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		parts := make([]string, len(values))
		for i, v := range values {
			lit, err := literal(v)
			if err != nil {
				return fmt.Errorf("%s.%s：%w", table, cols[i], err)
			}
			parts[i] = lit
		}
		fmt.Fprintf(b, "INSERT INTO %s (%s) VALUES (%s);\n", quoteIdent(table), strings.Join(quoted, ", "), strings.Join(parts, ", "))
	}
	return rows.Err()
}

// tableDDL 返回建表语句；调用方已经确认这张表存在。
func tableDDL(db *sql.DB, table string) string {
	var ddl sql.NullString
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl); err != nil {
		return ""
	}
	return ddl.String
}

func isVirtual(ddl string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ddl)), "CREATE VIRTUAL TABLE")
}

func literal(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "NULL", nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case int:
		return strconv.Itoa(v), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case []byte:
		return "X'" + hex.EncodeToString(v) + "'", nil
	case string:
		return quoteString(v), nil
	case time.Time:
		return quoteString(v.Format("2006-01-02 15:04:05.999999999")), nil
	default:
		return "", fmt.Errorf("不认识的列类型 %T", v)
	}
}

func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
