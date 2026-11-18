package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"sumika-native/internal/ctlproto"
)

// ProbeCtl 对桌面程序的控制接口问一次 status（和 sumika-ctl status 同一条路）。
// 回话的得是我们拉起的那个进程（pid 对得上）：别的 Sumika（演示、另一个用户）回了不算。ctx 到期就算没回话
func ProbeCtl(ctx context.Context, pid int) error {
	name, err := ctlproto.PipeName(false)
	if err != nil {
		return err
	}
	conn, err := ctlproto.Dial(ctx, name)
	if err != nil {
		return fmt.Errorf("连不上控制接口：%w", err)
	}
	return probeConn(ctx, conn, pid)
}

// probeConn 在一条已连上的连接上问 status 并核对进程号；连接用完关掉
func probeConn(ctx context.Context, conn net.Conn, pid int) error {
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := ctlproto.WriteJSON(conn, ctlproto.Request{V: ctlproto.Version, Cmd: ctlproto.CmdStatus}); err != nil {
		return fmt.Errorf("发请求：%w", err)
	}
	line, err := ctlproto.ReadLine(bufio.NewReader(conn), 1<<20)
	if err != nil {
		return fmt.Errorf("等回话：%w", err)
	}
	var reply ctlproto.Reply
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("回话读不懂：%w", err)
	}
	if !reply.OK {
		return fmt.Errorf("控制接口回了错误：%s", reply.Error)
	}
	var st ctlproto.Status
	if err := json.Unmarshal(reply.Data, &st); err != nil {
		return fmt.Errorf("状态读不懂：%w", err)
	}
	if st.Pid != pid {
		return fmt.Errorf("回话的是进程 %d，不是我们拉起的 %d", st.Pid, pid)
	}
	return nil
}
