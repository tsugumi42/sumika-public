package watchdog

import (
	"path/filepath"
	"time"
)

// 守护进程的状态值
const (
	StateRunning = "running"
	StateGaveUp  = "gave-up" // 连续崩溃太多次，不再拉起了
	StateStopped = "stopped" // 桌面程序正常退出，守护进程跟着结束
)

// IncidentRef 是最近一次异常退出的摘要
type IncidentRef struct {
	Time     time.Time `json:"time"`
	Reason   string    `json:"reason"`
	ExitCode int       `json:"exitCode"`
	File     string    `json:"file,omitempty"` // 事件记录的文件名（在 logs/incidents/ 里）
}

// State 是 <数据目录>/run/watchdog.json：守护进程每次检查都刷新，sumika-ctl status 靠它说"守护进程在不在"。
// 守护进程重新启动时会把上一份里的 LastIncident 带过来，所以异常退出的记录不会因为守护进程换了一轮就丢
type State struct {
	Pid       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	State     string    `json:"state"`
	ChildPid  int       `json:"childPid,omitempty"`
	// Restarts 是这一轮守护进程已经拉起过几次（含第一次以外的每一次）
	Restarts     int          `json:"restarts"`
	LastIncident *IncidentRef `json:"lastIncident,omitempty"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, RunDir, StateFile) }

func WriteState(dataDir string, s State) error { return writeJSON(statePath(dataDir), s) }

func ReadState(dataDir string) (State, error) {
	var s State
	err := readJSON(statePath(dataDir), &s)
	return s, err
}

// stateFresh：守护进程每个检查周期（默认 30 秒）刷新一次，超过这么久没刷新就当它不在了（进程号可能被别人用上）
const stateFresh = 3 * time.Minute

// Running 守护进程现在在不在：状态是 running、刷新得够新、进程还活着
func (s State) Running(now time.Time, alive func(pid int) bool) bool {
	return s.State == StateRunning && now.Sub(s.UpdatedAt) < stateFresh && alive(s.Pid)
}
