package ctlproto

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// CurrentSID 是当前登录用户的 SID 字符串（S-1-5-21-…），照原样用，不转小写（管道名不区分大小写，但 SDDL 里要原样）
func CurrentSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("查当前用户：%w", err)
	}
	return u.User.Sid.String(), nil
}

// PipeName 是控制接口的管道名：\\.\pipe\sumika-ctl-<当前用户 SID>；演示模式另起一条，名字多一个 -demo
// （和单实例一样，正式和演示分开）。管道名里带用户 SID：同一台机器上别的用户的 Sumika 各用各的，互不相扰
func PipeName(demo bool) (string, error) {
	sid, err := CurrentSID()
	if err != nil {
		return "", err
	}
	name := `\\.\pipe\sumika-ctl-` + sid
	if demo {
		name += "-demo"
	}
	return name, nil
}

// Dial 连管道。管道不存在立刻失败（ERROR_FILE_NOT_FOUND）；所有实例都在忙时会等到 ctx 到期。
//
// 连上以后先核对管道另一头：它得是当前用户的进程。管道名里的 SID 不是秘密，同一台电脑上别的账号能抢先建一个同名的管道，
// 不核对的话客户端会把命令交给它、相信它回的 ok:true（比如 gate set off 其实没执行，Agent 却以为群已经不开口了）。
// 打不开（权限）和核对不过都是 *DeniedError；核对不了（查不到对方的用户）也算不过，宁可拒绝
func Dial(ctx context.Context, name string) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, &DeniedError{Msg: "没有权限打开控制接口的管道（它可能被别的用户占了）"}
		}
		return nil, err
	}
	if err := verifyServer(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// serverPIDOf 取管道另一头的进程号；测试里换掉，模拟别的用户的进程
var serverPIDOf = func(conn net.Conn) (uint32, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("管道连接没有句柄")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func verifyServer(conn net.Conn) error {
	me, err := CurrentSID()
	if err != nil {
		return &DeniedError{Msg: "查不到当前用户，不能确认管道另一头是谁：" + err.Error()}
	}
	pid, err := serverPIDOf(conn)
	if err != nil {
		return &DeniedError{Msg: "查不到管道另一头的进程，不能确认它是当前用户的：" + err.Error()}
	}
	return checkServerPID(pid, me)
}

// checkServerPID 进程 pid 得属于用户 me；查不到它的用户（别的账号的进程通常打不开）按不属于
func checkServerPID(pid uint32, me string) error {
	sid, err := userSIDOf(pid)
	if err != nil {
		return &DeniedError{Msg: fmt.Sprintf("查不到管道另一头的进程（进程号 %d）属于谁，不能确认它是当前用户的：%v", pid, err)}
	}
	if sid != me {
		return &DeniedError{Msg: fmt.Sprintf("管道另一头的进程（进程号 %d）不是当前用户的：有人占了这个管道名，不能把命令交给它", pid)}
	}
	return nil
}

// userSIDOf 取进程的用户 SID；测试里换掉，模拟别的用户的进程
var userSIDOf = processUserSID

// processUserSID 是进程 pid 的用户 SID
func processUserSID(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
