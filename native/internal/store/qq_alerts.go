package store

// QQ 平台的提醒记录（规格-Go后台装配 第七节：试运行统计要把 QQ 掉线的时段排除掉）。
// Bun 把提醒记在自己库的 qq_alerts 表里（daemon/plugins/qq-health.ts），表在 migrations/qq/0002_qq_alerts.sql，
// 分库以后只有 QQ 平台的库有这张表；本地库、只读打开的单库都没有，调这两个方法要报错，不许悄悄给空。

import (
	"database/sql"
	"errors"
	"fmt"
)

// QQAlert 是 qq_alerts 的一行
type QQAlert struct {
	ID      int64
	At      int64 // 毫秒；调用方给的是提醒发生的时刻，不是写库的时刻
	Kind    string
	Account string
	Text    string
	Detail  *string // 有些提醒带细节，没有就是 NULL
}

// qqAlertKinds 是 Bun 版写的五种提醒（daemon/plugins/qq-health.ts 的 Raise / Clear）：
// 掉线、恢复、被踢、信息对不上、发送失败。别的都是调用方拼错了，放进去统计要跟着错
var qqAlertKinds = map[string]bool{"down": true, "up": true, "kicked": true, "mismatch": true, "send-failed": true}

// errNoQQAlerts：这个库里没有 qq_alerts 表（不是 QQ 平台的库）
func errNoQQAlerts() error {
	return errors.New("这个库没有 qq_alerts 表：QQ 的提醒只在 QQ 平台的库里（migrations/qq）")
}

// AddQQAlert 写一行提醒，返回 id；At 由调用方给（毫秒）
func (s *Store) AddQQAlert(a QQAlert) (int64, error) {
	if !qqAlertKinds[a.Kind] {
		return 0, fmt.Errorf("提醒的 kind 不认识：%q（只认 down、up、kicked、mismatch、send-failed）", a.Kind)
	}
	ok, err := tableExists(s.Events, "qq_alerts")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errNoQQAlerts()
	}
	res, err := s.Events.Exec(`INSERT INTO qq_alerts (at, kind, account, text, detail) VALUES (?, ?, ?, ?, ?)`,
		a.At, a.Kind, a.Account, a.Text, a.Detail)
	if err != nil {
		return 0, err
	}
	s.changed("")
	return res.LastInsertId()
}

// QQAlerts 是某个号 at 不早于 since 的提醒，按 at、id 升序（同一毫秒先后写的有先后）
func (s *Store) QQAlerts(account string, since int64) ([]QQAlert, error) {
	ok, err := tableExists(s.Events, "qq_alerts")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNoQQAlerts()
	}
	rows, err := s.Events.Query(`SELECT id, at, kind, account, text, detail FROM qq_alerts WHERE account = ? AND at >= ? ORDER BY at, id`, account, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QQAlert
	for rows.Next() {
		var a QQAlert
		var detail sql.NullString
		if err := rows.Scan(&a.ID, &a.At, &a.Kind, &a.Account, &a.Text, &detail); err != nil {
			return nil, err
		}
		if detail.Valid {
			a.Detail = &detail.String
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// tableExists 查这个库有没有这张表：平台自己的表只在自己库里，先问一句，报错比 sqlite 的 "no such table" 说得多
func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
