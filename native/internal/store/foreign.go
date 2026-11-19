package store

// 另一个进程管着的平台库（当年是 backend.local 为 bun 时的 local.db，ADR-401 删了那条路线；这层现在没有生产入口，待办挂着）：
// Go 只读打开（mode=ro 加 query_only，从连接上就写不了），所以 Go 里没有任何一处需要逐个加判断。
// 三件事：
//  1. 只读打开：不跑迁移，只核对版本是不是最新（落后就报错，不在别人写着的库上迁移）；
//  2. 别的进程写了：每秒查 PRAGMA data_version，变了就叫醒订阅（本进程写入有进程内通知，别的进程写的叫不醒）；
//  3. 启动收拾不碰它的在途行：SweepInterrupted、RecoverKernelInputs 是整张表的 UPDATE，不限范围会把对方正在跑的回合记成被打断。

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/paths"
)

// OpenOptions 是 OpenGoOpts 的选项
type OpenOptions struct {
	// ReadOnly 是由别的进程写的平台（登记里的平台名）：只读打开、不迁移，对应渠道的在途行也不在启动时收拾
	ReadOnly []string
	// Logf 记“库比程序新、按兼容版本打开”这类一行日志；nil 不记
	Logf func(string, ...any)
}

// OpenGoOpts 和 OpenGoWith 一样打开登记里的全部平台，只是 opt.ReadOnly 里的平台只读打开
func OpenGoOpts(p paths.Paths, reg Registry, opt OpenOptions) (*Set, error) {
	var names []string
	for _, pl := range reg.list {
		names = append(names, pl.Name)
	}
	for _, ro := range opt.ReadOnly {
		found := false
		for _, n := range names {
			found = found || n == ro
		}
		if !found {
			return nil, fmt.Errorf("只读平台 %q 不在登记里", ro)
		}
	}
	return openSetOpts(p, reg, names, opt)
}

// ReadOnly 说这个库是不是只读打开的（别的进程写它）
func (s *Store) ReadOnly() bool { return s != nil && s.readOnly }

// openReadOnlyPlatform 只读打开平台库并核对迁移版本。库不存在、版本落后都报错：Go 不在别人写着的库上建表、迁移
func openReadOnlyPlatform(p paths.Paths, name string) (*sql.DB, error) {
	file := p.ChatDB(name)
	if _, err := os.Stat(file); err != nil {
		return nil, fmt.Errorf("%s 不存在：这个库归另一个进程写，Go 不在别人写着的库上建库，先让写它的那一端建好：%w", file, err)
	}
	db, err := OpenReadOnlyFile(file)
	if err != nil {
		return nil, err
	}
	if err := checkSchemaCurrent(db, name); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s：%w", file, err)
	}
	return db, nil
}

// checkSchemaCurrent：通用迁移和该平台自己的迁移都已经跑到本程序内嵌的最新版本
func checkSchemaCurrent(db *sql.DB, platform string) error {
	check := func(dir, table string) error {
		want, err := latestMigration(dir)
		if err != nil {
			return err
		}
		var have sql.NullInt64
		if err := db.QueryRow("SELECT max(version_id) FROM " + table + " WHERE is_applied").Scan(&have); err != nil {
			return fmt.Errorf("读迁移记录 %s：%w", table, err)
		}
		if have.Int64 < want {
			return fmt.Errorf("库结构落后（%s 在 %d，本程序要 %d）：这个库归另一个进程写，Go 不在上面迁移，先让写它的那一端升级", table, have.Int64, want)
		}
		if have.Int64 > want {
			return fmt.Errorf("库结构比本程序新（%s 在 %d，本程序只到 %d）", table, have.Int64, want)
		}
		return nil
	}
	if err := check("migrations/chat", "goose_db_version"); err != nil {
		return err
	}
	if hasMigrations(platform) {
		return check("migrations/"+platform, "goose_db_version_"+platform)
	}
	return nil
}

// latestMigration 是内嵌迁移目录里最大的版本号（文件名 0007_xxx.sql 的前缀）
func latestMigration(dir string) (int64, error) {
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, e := range entries {
		head, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(head, 10, 64)
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max, nil
}

// ExternalChangeKey 是别的进程写库时叫醒订阅用的失效身份：随便一个非空键都会叫醒“看全部变更”的订阅（概览、会话），
// 带专用键的订阅（比如补录对齐）不受影响
const ExternalChangeKey = "@external"

// CheckExternalChange 查一次 PRAGMA data_version（它在别的连接提交后才变，本连接自己的提交不改它）：
// 和上次不同就叫醒订阅并回 true。第一次调用只记基线、回 false。
func (s *Store) CheckExternalChange() (bool, error) {
	var v int64
	if err := s.Events.QueryRow("PRAGMA data_version").Scan(&v); err != nil {
		return false, err
	}
	s.extMu.Lock()
	changed := s.extSeen && v != s.extVersion
	s.extSeen, s.extVersion = true, v
	s.extMu.Unlock()
	if changed {
		s.changed(ExternalChangeKey)
	}
	return changed, nil
}

// PollExternalChanges 每收到一次 tick 查一次，ctx 取消返回。测试自己发 tick，不等墙钟
func (s *Store) PollExternalChanges(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			_, _ = s.CheckExternalChange()
		}
	}
}

// ExternalPollInterval 是生产环境查 data_version 的间隔
const ExternalPollInterval = time.Second

// PollExternalEvery 先记一次基线，再起一个协程按间隔查；返回的函数取消并等它退出（关库之前要调）
func (s *Store) PollExternalEvery(interval time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t := time.NewTicker(interval)
	_, _ = s.CheckExternalChange()
	go func() {
		defer close(done)
		defer t.Stop()
		s.PollExternalChanges(ctx, t.C)
	}()
	return func() { cancel(); <-done }
}

// foreignClauses 是启动收拾里排除别的进程那些渠道的条件：turns 看 channel，steps 看会话串前缀（"渠道:"）。
// 没有别的进程管的渠道时三项都是空，语句和原来一样
func (s *Store) foreignClauses() (turns, steps string, turnArgs, stepArgs []any) {
	if s == nil || len(s.foreignChannels) == 0 {
		return "", "", nil, nil
	}
	var marks, likes []string
	for _, ch := range s.foreignChannels {
		marks = append(marks, "?")
		likes = append(likes, "coalesce(conversation,'') NOT LIKE ?")
		turnArgs = append(turnArgs, ch)
		stepArgs = append(stepArgs, ch+":%")
	}
	return " AND coalesce(channel,'') NOT IN (" + strings.Join(marks, ",") + ")", " AND " + strings.Join(likes, " AND "), turnArgs, stepArgs
}
