package watchdog

import (
	"fmt"
	"os"
	"time"
)

// 启动对账（docs/规格-看门狗.md 的“已知的坑”）：守护进程自己被外部结束时来不及写任何东西，
// 状态文件永远停在 running。下一个守护进程启动时拿旧状态对一遍，对得上“没人收尾就没了”就留一份事故记录。
//
// 只在这些条件同时成立时才报：旧状态是 running（正常结束、停止拉起都会改掉它）、旧守护进程和旧桌面程序的进程都不在、
// 旧状态是这次登录以后写的（重启、注销本来就会结束所有进程）、旧桌面程序没有在退出（心跳没带退出标记）。
// 旧守护进程还活着说明是另一个实例在跑，那不是遗物，一概不动。

// 事故记录的类别
const codeExternalKill = "external_kill_suspected"

// prevSuffix：覆盖状态文件和心跳之前，把旧的留一份
const prevSuffix = ".prev"

// reconcile 读旧状态做对账，返回要放进新状态的 LastIncident（没报就是 nil）。old 是启动时读到的旧状态，ok 表示读到了
func (s *Supervisor) reconcile(old State, ok bool) *IncidentRef {
	if !ok {
		return nil
	}
	if old.Pid == s.Pid || s.Alive(old.Pid) {
		return nil // 旧状态是自己的，或者另一个守护进程正在跑：它们的文件不是遗物
	}
	s.keepPrev(statePath(s.DataDir))
	hb, hbErr := ReadHeartbeat(s.DataDir)
	s.keepPrev(heartbeatPath(s.DataDir))
	if old.State != StateRunning {
		return nil
	}
	if old.ChildPid > 0 && s.Alive(old.ChildPid) {
		return nil // 守护进程没了，桌面程序还活着：不是“一起被结束”，这里不下结论
	}
	if ss := s.SessionStart(); !ss.IsZero() && old.UpdatedAt.Before(ss) {
		return nil // 旧状态写在这次登录之前：重启或注销结束的，不是外部结束
	}
	if hbErr == nil && hb.Quitting && (old.ChildPid == 0 || hb.Pid == old.ChildPid) {
		return nil // 桌面程序已经在走退出流程，守护进程跟着没了，也是正常退出的一种
	}

	now := s.Now()
	beat := "没有"
	var extra []string
	if hbErr == nil {
		beat = clock(hb.Time, now)
		if hb.Pid != old.ChildPid {
			beat += fmt.Sprintf("（进程 %d 的，心跳不是它的）", hb.Pid)
		}
		extra = append(extra, fmt.Sprintf("最后一次心跳：%s，进程 %d，阶段 %s，正在跑的回合 %d", clock(hb.Time, now), hb.Pid, hb.Phase, hb.RunningTurns))
	} else {
		extra = append(extra, "没有读到心跳文件")
	}
	updated := clock(old.UpdatedAt, now)
	s.Warnf("上一个守护进程没留结束记录：最后更新 %s，桌面程序 pid %d，最后心跳 %s；多半是被外部一起结束（例如从 Claude 里启动、Claude 自更新）", updated, old.ChildPid, beat)

	in := Incident{
		Time:   now,
		Reason: fmt.Sprintf("%s：上一个守护进程（进程 %d）没留结束记录就不在了，最后更新 %s，桌面程序 pid %d 也不在了；多半是被外部一起结束（例如从 Claude 里启动、Claude 自更新）", codeExternalKill, old.Pid, updated, old.ChildPid),
		Pid:    old.ChildPid, Code: codeExternalKill, NoExit: true,
		Details: append([]string{
			fmt.Sprintf("上一个守护进程：进程 %d，%s 启动，最后更新 %s，这一轮拉起过 %d 次", old.Pid, clock(old.StartedAt, now), updated, old.Restarts),
		}, extra...),
		DeskLog: deskTail(s.DataDir, s.Now),
	}
	file, err := writeIncident(s.DataDir, in)
	if err != nil {
		s.Warnf("写事件记录失败：%v", err)
	}
	return &IncidentRef{Time: now, Reason: in.Reason, ExitCode: -1, File: file}
}

// clock 把时间写成 15:04:05；和 now 不是同一天就带上日期
func clock(t, now time.Time) string {
	if t.IsZero() {
		return "未知"
	}
	y1, m1, d1 := t.Date()
	if y2, m2, d2 := now.Date(); y1 != y2 || m1 != m2 || d1 != d2 {
		return t.Format("01-02 15:04:05")
	}
	return t.Format("15:04:05")
}

// keepPrev 把 path 复制成 path.prev（文件不存在就算了）。失败只记日志，对账不因此中断
func (s *Supervisor) keepPrev(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := os.WriteFile(path+prevSuffix, b, 0o644); err != nil {
		s.Warnf("备份 %s 失败：%v", path, err)
	}
}
