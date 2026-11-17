// reply-backfill 给“发出去的消息”补上引用关系：以前出口落库时没写 events.reply_to（只在段里存了被回复那条的平台消息号），
// 界面的引用块只认 reply_to，于是显示“原消息不在本地”。现在出口落库时直接写；这个工具把已经落库的历史按平台消息号连上。
//
// 只改 events.reply_to 为空、段里带引用、而且那个平台消息号在库里（channel_msgs）能找到的行；重跑不变，已经有值的不动。
// 默认只数不改；加 -apply 才写：写之前把整个库复制一份到 <数据目录>/backups/reply-backfill-<时间>/，在一个事务里改。
// 要先 sumika-ctl quit（数据目录锁被占就直接退出）。输出只有数字，不打印聊天内容。
//
//	go run ./cmd/reply-backfill -data <仓库>\data            # 只数
//	go run ./cmd/reply-backfill -data <仓库>\data -apply     # 备份并补
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/datalock"
)

func main() {
	data := flag.String("data", "", "数据目录（含 go/chat）")
	platform := flag.String("platform", "qq", "平台库名（go/chat/<平台>.db）")
	apply := flag.Bool("apply", false, "真的写；不加只数")
	flag.Parse()
	if *data == "" {
		fmt.Fprintln(os.Stderr, "用法：reply-backfill -data <数据目录> [-platform qq] [-apply]")
		os.Exit(2)
	}
	lock, err := lockData(*data, *platform)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer lock.Close()
	file := filepath.Join(*data, "go", "chat", *platform+".db")
	rep, err := Run(context.Background(), file, filepath.Join(*data, "backups"), *apply, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("带引用的发出消息 %d 条；其中 reply_to 为空 %d 条，能按平台消息号连上 %d 条，连不上 %d 条\n", rep.WithQuote, rep.Empty, rep.Resolvable, rep.Empty-rep.Resolvable)
	if *apply {
		fmt.Printf("已补 %d 条；改之前的备份在 %s\n", rep.Updated, rep.Backup)
	} else {
		fmt.Println("这是只数不改；确认后加 -apply")
	}
}

// lockData 拿数据目录的锁。-platform local 会写 local.db：Bun 管本地私聊时（Go 不在、Bun 在写）光认 backend.lock 会和它同写，
// 所以也要拿到 go/local.lock，拿不到就拒绝；别的平台（qq）也探一下 local.lock，有人在写本地就不开工
func lockData(data, platform string) (*datalock.DataLock, error) {
	var (
		lock *datalock.DataLock
		err  error
	)
	if platform == "local" {
		lock, err = datalock.LockExistingForWrite(data)
	} else {
		lock, err = datalock.LockExisting(data)
	}
	switch {
	case errors.Is(err, datalock.ErrDataInUse):
		return nil, errors.New("Sumika 还开着，先 sumika-ctl quit 再跑")
	case errors.Is(err, datalock.ErrLocalInUse):
		return nil, errors.New("本地私聊的 Bun 后台还开着（占着 go/local.lock），先关掉它再跑")
	case err != nil:
		return nil, fmt.Errorf("锁数据目录：%w", err)
	}
	return lock, nil
}

// Report 是一次运行的数字。
type Report struct {
	WithQuote, Empty, Resolvable, Updated int
	Backup                                string
}

type fix struct{ id, replyTo string }

// Run 扫描一个聊天库文件；apply 为真时先备份再改。
func Run(ctx context.Context, file, backupRoot string, apply bool, now time.Time) (Report, error) {
	var rep Report
	if _, err := os.Stat(file); err != nil {
		return rep, fmt.Errorf("找不到聊天库：%w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return rep, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, channel, conversation, COALESCE(reply_to,''), segments_json FROM events
		WHERE actor_kind='character' AND kind='message' AND segments_json LIKE '%"quote"%'`)
	if err != nil {
		return rep, err
	}
	type cand struct{ id, channel, conv, quote string }
	var empties []cand
	for rows.Next() {
		var id, ch, conv, replyTo string
		var seg sql.NullString
		if err := rows.Scan(&id, &ch, &conv, &replyTo, &seg); err != nil {
			rows.Close()
			return rep, err
		}
		quote := firstQuote(seg.String)
		if quote == "" {
			continue
		}
		rep.WithQuote++
		if replyTo == "" {
			rep.Empty++
			empties = append(empties, cand{id, ch, conv, quote})
		}
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	rows.Close()
	var fixes []fix
	for _, c := range empties {
		var target string
		err := db.QueryRowContext(ctx, `SELECT e.id FROM channel_msgs m JOIN events e ON e.seq = m.event_seq
			WHERE m.channel=? AND m.conversation=? AND m.platform_id=?`, c.channel, c.conv, c.quote).Scan(&target)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return rep, err
		}
		fixes = append(fixes, fix{c.id, target})
	}
	rep.Resolvable = len(fixes)
	if !apply || len(fixes) == 0 {
		return rep, nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return rep, fmt.Errorf("备份前合并日志：%w", err)
	}
	rep.Backup = filepath.Join(backupRoot, "reply-backfill-"+now.Format("20060102-150405"))
	if err := os.MkdirAll(rep.Backup, 0o755); err != nil {
		return rep, err
	}
	if err := copyFile(file, filepath.Join(rep.Backup, filepath.Base(file))); err != nil {
		return rep, fmt.Errorf("备份失败，没有改任何东西：%w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	for _, f := range fixes {
		res, err := tx.ExecContext(ctx, `UPDATE events SET reply_to=? WHERE id=? AND (reply_to IS NULL OR reply_to='')`, f.replyTo, f.id)
		if err != nil {
			tx.Rollback()
			return rep, err
		}
		n, _ := res.RowsAffected()
		rep.Updated += int(n)
	}
	return rep, tx.Commit()
}

// firstQuote 取发出消息里第一个消息段的引用（被回复那条的平台消息号）；没有返回空。
func firstQuote(segments string) string {
	var env struct {
		Parts []struct {
			Kind  string `json:"kind"`
			Quote string `json:"quote"`
		} `json:"parts"`
	}
	if json.Unmarshal([]byte(segments), &env) != nil {
		return ""
	}
	for _, p := range env.Parts {
		if p.Kind == "message" && p.Quote != "" {
			return p.Quote
		}
	}
	return ""
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
