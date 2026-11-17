package sendlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// TakeoverEvidence 保留首次接管的核对结果；Take 和 Release 都不抹掉来源。
type TakeoverEvidence struct {
	At                int64 `json:"at"`
	BunStopped        bool  `json:"bunStopped"`
	PortClosed        bool  `json:"portClosed"`
	WatchdogConfirmed bool  `json:"watchdogConfirmed"`
}

type takeoverChecks struct {
	bunStopped        func(context.Context) (bool, error)
	portClosed        func(context.Context) (bool, error)
	watchdogConfirmed bool
}

// FirstTakeover 仅由宿主诊断入口显式调用，不注册为模型工具。
// 看门脚本和计划任务无法可靠穷举，必须由调用者显式确认已停用。
func FirstTakeover(ctx context.Context, dataDir, account string, watchdogConfirmed bool) error {
	return firstTakeover(ctx, dataDir, account, takeoverChecks{bunStopped: bunStopped, portClosed: legacyPortClosed, watchdogConfirmed: watchdogConfirmed})
}

func firstTakeover(ctx context.Context, dataDir, account string, checks takeoverChecks) error {
	if !isDigits(account) {
		return ErrAccount
	}
	if _, err := os.Lstat(LockFile(dataDir)); err == nil {
		return errors.New("已有交接记录，不能首次接管")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("不能确认交接记录不存在")
	}
	if !checks.watchdogConfirmed {
		return errors.New("尚未确认看门脚本和计划任务不会拉起 Bun")
	}
	if ok, err := checks.bunStopped(ctx); err != nil || !ok {
		return errors.New("不能确认 Bun 后台进程已退出")
	}
	if ok, err := checks.portClosed(ctx); err != nil || !ok {
		return errors.New("不能确认旧后台端口已关闭")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(LockFile(dataDir)), 0755); err != nil {
		return err
	}
	// 排他创建，防另一个首次接管入口在检查后抢先写入；绝不覆盖已有记录。
	f, err := os.OpenFile(LockFile(dataDir), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("已有交接记录或无法创建，不能首次接管")
	}
	defer f.Close()
	if err := lockExclusive(f); err != nil {
		return fmt.Errorf("首次接管抢锁失败：%w", err)
	}
	defer unlockExclusive(f)
	intent, _ := json.Marshal(Intent{Account: account, Holder: HolderGo})
	if err := os.WriteFile(IntentFile(dataDir), intent, 0600); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	return writeRecord(f, Record{Protocol: Protocol, Account: account, Holder: HolderGo, State: StateReleased, PID: os.Getpid(), ReleasedAt: now, Origin: "first-takeover", Takeover: &TakeoverEvidence{At: now, BunStopped: true, PortClosed: true, WatchdogConfirmed: true}})
}

func legacyPortClosed(ctx context.Context) (bool, error) {
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", "127.0.0.1:7720")
	if err == nil {
		c.Close()
		return false, nil
	}
	if connectionRefused(err) {
		return true, nil
	}
	return false, err
}
