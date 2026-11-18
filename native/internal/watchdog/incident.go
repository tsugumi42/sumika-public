package watchdog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/filelog"
)

// 事件记录放 <数据目录>/logs/incidents/<时间>.md，崩溃现场只抄一份，不挪动、不删原来的日志
const (
	incidentDir    = "incidents"
	crashLogName   = "crash.log" // 和桌面程序的 crashlog.go 同名同处
	crashDeltaMax  = 64 << 10    // 崩溃记录新增的部分最多抄这么多字节（太长只留尾巴）
	deskTailLines  = 60          // desk 日志抄最后多少行
	deskLogPrefix  = "desk"
	incidentWriteN = 20 // 同一秒内重名时最多试多少个后缀
)

// Incident 是一次异常退出（或拉起失败、卡死被杀）的记录
type Incident struct {
	Time     time.Time
	Reason   string
	ExitCode int
	Pid      int
	Uptime   time.Duration
	// Attempt：这一轮窗口里第几次；GaveUp：这一次之后不再拉起
	Attempt int
	GaveUp  bool
	// Code 是机器可读的类别（如 external_kill_suspected）；NoExit：没有谁看到它退出，退出码未知（启动对账发现的）；
	// Details 是额外几行说明，原样写成列表
	Code     string
	NoExit   bool
	Details  []string
	CrashLog string   // crash.log 在这次运行期间新增的部分
	DeskLog  []string // desk 日志的最后几十行
}

// Markdown 是事件文件的正文
func (in Incident) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sumika 异常退出 %s\n\n", in.Time.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 原因：%s\n", in.Reason)
	if in.Code != "" {
		fmt.Fprintf(&b, "- 类别：%s\n", in.Code)
	}
	if in.NoExit {
		b.WriteString("- 退出码：未知（没有谁看到它退出，是下一个守护进程启动时对账发现的）\n")
		fmt.Fprintf(&b, "- 桌面程序进程号：%d\n", in.Pid)
	} else {
		fmt.Fprintf(&b, "- 退出码：%d（0x%X）\n", in.ExitCode, uint32(in.ExitCode))
		fmt.Fprintf(&b, "- 进程号：%d；这次运行了 %s\n", in.Pid, in.Uptime.Round(time.Second))
	}
	if in.Attempt > 0 {
		fmt.Fprintf(&b, "- 窗口内第 %d 次异常退出\n", in.Attempt)
	}
	if in.GaveUp {
		b.WriteString("- 结果：连续异常退出太多次，**已停止自动拉起**；修好原因后手动启动\n")
	} else {
		b.WriteString("- 结果：守护进程会重新拉起\n")
	}
	for _, d := range in.Details {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	b.WriteString("\n## crash.log 里这次运行新增的部分\n\n")
	if strings.TrimSpace(in.CrashLog) == "" {
		b.WriteString("（没有新增：没有 Go 运行时崩溃信息。无声退出、被外部结束、卡死被杀都是这样）\n")
	} else {
		b.WriteString("```\n" + strings.TrimRight(in.CrashLog, "\r\n") + "\n```\n")
	}
	fmt.Fprintf(&b, "\n## 桌面日志（desk）最后 %d 行\n\n", deskTailLines)
	if len(in.DeskLog) == 0 {
		b.WriteString("（没有读到）\n")
	} else {
		b.WriteString("```\n" + strings.Join(in.DeskLog, "\n") + "\n```\n")
	}
	return b.String()
}

// writeIncident 写事件文件，返回文件名（不含目录）。同一秒内重名就加后缀
func writeIncident(dataDir string, in Incident) (string, error) {
	dir := filepath.Join(dataDir, "logs", incidentDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := in.Time.Format("2006-01-02_150405")
	var lastErr error
	for i := 0; i < incidentWriteN; i++ {
		name := base + ".md"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.md", base, i+1)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			lastErr = err
			break
		}
		_, werr := f.WriteString(in.Markdown())
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return name, werr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("事件文件重名太多次")
	}
	return "", lastErr
}

// crashLogSize 是 crash.log 现在的字节数；拉起桌面程序之前记下，它退出后从这里往后抄。文件不存在是 0
func crashLogSize(dataDir string) int64 {
	st, err := os.Stat(filepath.Join(dataDir, "logs", crashLogName))
	if err != nil {
		return 0
	}
	return st.Size()
}

// crashLogSince 读 crash.log 从 offset 往后新增的部分；超过 crashDeltaMax 只留最后那一段。文件变小了（被人清过）就从头读
func crashLogSince(dataDir string, offset int64) string {
	f, err := os.Open(filepath.Join(dataDir, "logs", crashLogName))
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	if st.Size() < offset {
		offset = 0
	}
	n := st.Size() - offset
	cut := false
	if n > crashDeltaMax {
		offset, n, cut = st.Size()-crashDeltaMax, crashDeltaMax, true
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(f, b); err != nil {
		return ""
	}
	s := string(b)
	if cut {
		s = "（前面的省略了）\n" + s
	}
	return s
}

// deskTail 是 desk 日志的最后几行
func deskTail(dataDir string, now func() time.Time) []string {
	lines, err := filelog.New(dataDir, deskLogPrefix, nil, now).Tail(deskTailLines)
	if err != nil {
		return nil
	}
	return lines
}
