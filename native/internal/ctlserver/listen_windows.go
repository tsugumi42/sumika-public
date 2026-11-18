package ctlserver

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"

	"sumika-native/internal/ctlproto"
)

// ListenPipe 在 name（ctlproto.PipeName 给的名字）上开管道，只给当前 Windows 用户：安全描述是 SDDL(当前用户 SID)。
// 拒绝远程客户端的标志（FILE_PIPE_REJECT_REMOTE_CLIENTS）是 go-winio 建服务端管道时固定带上的，这里改不了也不用管；
// listen_windows_test.go 里有一个钉住 go-winio 版本的测试，升级库时它会提醒回头确认这一条还在。
// 管道已经存在时（别的进程占着这个名字）报错，不去抢：go-winio 建第一个实例用 FILE_CREATE
func ListenPipe(name string) (net.Listener, error) {
	sid, err := ctlproto.CurrentSID()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: SDDL(sid)})
	if err != nil {
		return nil, fmt.Errorf("开控制接口的管道：%w", err)
	}
	return ln, nil
}
