package sqlitesnap

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

// WriteLockWait 量“这时候来一次写入要等多久”：从连接池取连接、BEGIN IMMEDIATE（拿写锁），马上 ROLLBACK，**不写任何数据**，
// 返回从发起到拿到写锁花的时间。各库都是单连接池，所以这个时间包含排在入库写入后面等连接的时间，就是一次真实写入会遇到的等待。
// 备份跑着的时候周期性地调它，量备份对入库的影响（规格-数据目录与备份 10.10.6）；archive 模式的连接是只读的，会失败，调用方如实记“没探到”。
//
// 它借用的是持有方的池（入库用的那条连接）：所以不管出什么错，还回池以前一定把事务收掉——BEGIN 不跟可取消的 ctx
// （取消正好落在 BEGIN 生效之后，驱动可能回 ctx 的错而事务其实开着；等锁的上限由库自己的 busy_timeout 兜着），
// 而且不管 BEGIN 报不报错都 ROLLBACK（没有活动事务的错忽略）。
func WriteLockWait(ctx context.Context, db *sql.DB) (time.Duration, error) {
	if db == nil {
		return 0, errors.New("没有可用的库连接")
	}
	start := time.Now()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("取连接：%w", err)
	}
	defer conn.Close()
	_, berr := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`)
	waited := time.Since(start)
	// 回滚也不跟 ctx：事务开着连接就还给不了池，后面的写入会被它挡住
	_, rerr := conn.ExecContext(context.Background(), `ROLLBACK`)
	if berr != nil {
		return 0, fmt.Errorf("拿写锁：%w", berr)
	}
	if rerr != nil {
		// 回滚失败：这条连接上可能还开着事务。让池丢掉它，不能把它还给入库用（单连接池里后面所有写都会报“事务里不能再开事务”）
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return waited, fmt.Errorf("回滚：%w", rerr)
	}
	if err := ctx.Err(); err != nil { // 等的时候被取消了：这个数是在被取消的情况下量的，不当成采样
		return 0, err
	}
	return waited, nil
}
