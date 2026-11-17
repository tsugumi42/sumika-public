package watchdog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/fileatomic"
)

// 数据目录下的 run/ 放运行中的小文件：心跳（桌面程序写）、守护进程状态（守护进程写）
const (
	RunDir        = "run"
	HeartbeatFile = "heartbeat.json"
	StateFile     = "watchdog.json"
)

// Heartbeat 是 <数据目录>/run/heartbeat.json 的内容：桌面程序自己说"我还活着、在干什么"。
// 守护进程只认 Pid 对得上它拉起的那个进程的心跳，上一次运行留下的旧文件不算数
type Heartbeat struct {
	Pid       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Time      time.Time `json:"time"`
	Version   string    `json:"version"`
	// Phase 是宿主阶段（starting archive receiving …），RunningTurns 是正在跑的回合数
	Phase        string `json:"phase"`
	RunningTurns int    `json:"runningTurns"`
	// Quitting：退出流程已经开始（托盘退出、sumika-ctl quit、系统关机请求）。此后非零退出码也不算崩溃，守护进程不拉起、不通知
	Quitting bool `json:"quitting,omitempty"`
}

func heartbeatPath(dataDir string) string { return filepath.Join(dataDir, RunDir, HeartbeatFile) }

// WriteHeartbeat 原子地写心跳：先写临时文件再改名，读的一方不会读到半截
func WriteHeartbeat(dataDir string, hb Heartbeat) error {
	return writeJSON(heartbeatPath(dataDir), hb)
}

// ReadHeartbeat 读心跳；文件不存在或读坏了返回错误，调用方当"没有新消息"，不据此下结论
func ReadHeartbeat(dataDir string) (Heartbeat, error) {
	var hb Heartbeat
	err := readJSON(heartbeatPath(dataDir), &hb)
	return hb, err
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := fileatomic.Replace(tmp, path, time.Second); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Snapshot 是每次写心跳时现取的几项
type Snapshot struct {
	Version      string
	Phase        string
	RunningTurns int
}

// Beater 在桌面程序里按间隔写心跳。取快照的函数（snap）不得碰界面线程，要读得快、不阻塞
type Beater struct {
	dir      string
	interval time.Duration
	snap     func() Snapshot
	logf     func(format string, args ...any)
	now      func() time.Time
	pid      int
	started  time.Time

	quitting atomic.Bool
	mu       sync.Mutex // 写盘串行：Run 的定时写和 MarkQuitting 的立刻写不会交叉
	failing  bool
}

// NewBeater 建一个心跳写入器；interval 不大于 0 按 60 秒
func NewBeater(dataDir string, interval time.Duration, snap func() Snapshot, logf func(string, ...any)) *Beater {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Beater{dir: dataDir, interval: interval, snap: snap, logf: logf, now: time.Now, pid: os.Getpid(), started: time.Now()}
}

// Beat 立刻写一次
func (b *Beater) Beat() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.snap()
	err := WriteHeartbeat(b.dir, Heartbeat{Pid: b.pid, StartedAt: b.started, Time: b.now(), Version: s.Version, Phase: s.Phase, RunningTurns: s.RunningTurns, Quitting: b.quitting.Load()})
	switch {
	case err != nil && !b.failing:
		b.failing = true
		b.logf("写心跳失败：%v（之后同样的失败不再重复记，写好了会记一行）", err)
	case err == nil && b.failing:
		b.failing = false
		b.logf("心跳又写进去了")
	}
	return err
}

// MarkQuitting 退出流程一开始就调：立刻把"正在退出"写进心跳，之后每次心跳都带着它
func (b *Beater) MarkQuitting() {
	b.quitting.Store(true)
	b.Beat()
}

// Run 先写一次，然后每隔 interval 写一次，直到 ctx 结束
func (b *Beater) Run(ctx context.Context) {
	b.Beat()
	t := time.NewTicker(b.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Beat()
		}
	}
}
