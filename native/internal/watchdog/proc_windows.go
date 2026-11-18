package watchdog

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessAlive 进程还在不在（用进程号查，只看有没有退出）
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

var getSystemMetrics = syscall.NewLazyDLL("user32.dll").NewProc("GetSystemMetrics")

const smShuttingDown = 0x2000

// SystemShuttingDown 系统正在关机或注销（SM_SHUTTINGDOWN）。这时桌面程序被系统结束，不是崩溃
func SystemShuttingDown() bool {
	r, _, _ := getSystemMetrics.Call(smShuttingDown)
	return r != 0
}

var wtsQuerySessionInformation = syscall.NewLazyDLL("wtsapi32.dll").NewProc("WTSQuerySessionInformationW")

const (
	wtsCurrentSession = 0xFFFFFFFF // WTS_CURRENT_SESSION
	wtsSessionInfo    = 24         // WTS_INFO_CLASS 的 WTSSessionInfo
)

// wtsInfo 是 WTSINFOW（wtsapi32.h）；只用到 LogonTime，前面的字段按原样排满，保证偏移对得上
type wtsInfo struct {
	State                                                              int32
	SessionID                                                          uint32
	IncomingBytes, OutgoingBytes, IncomingFrames, OutgoingFrames       uint32
	IncomingCompressedBytes, OutgoingCompressedBytes                   uint32
	WinStationName                                                     [32]uint16
	Domain                                                             [17]uint16
	UserName                                                           [21]uint16
	ConnectTime, DisconnectTime, LastInputTime, LogonTime, CurrentTime int64
}

// SessionStart 是当前登录会话开始的时间（WTSSessionInfo 的 LogonTime）；取不到返回零值。
// 不用开机时间：开了快速启动的“关机”不会重置 GetTickCount64，按它算会把每次关机都当成外部结束
func SessionStart() time.Time {
	var buf unsafe.Pointer
	var n uint32
	r, _, _ := wtsQuerySessionInformation.Call(0, wtsCurrentSession, wtsSessionInfo, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || buf == nil {
		return time.Time{}
	}
	defer windows.WTSFreeMemory(uintptr(buf))
	if n < uint32(unsafe.Sizeof(wtsInfo{})) {
		return time.Time{}
	}
	info := (*wtsInfo)(buf)
	if info.LogonTime <= 0 {
		return time.Time{}
	}
	return time.Unix(0, (info.LogonTime-116444736000000000)*100) // FILETIME：1601 年起的 100 纳秒；减到 1970 年
}
