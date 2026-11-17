package config

import "fmt"

// Watchdog 是看门狗（docs/规格-看门狗.md）：桌面程序按它写心跳，守护进程（sumika.exe -watch）按它判卡死、限重启。
// 守护进程每次拉起桌面程序前重读一遍，改了下次拉起生效；配置读坏了守护进程用默认值，不挡着桌面程序起来。
type Watchdog struct {
	// HeartbeatSeconds：桌面程序每隔多少秒写一次 <数据目录>/run/heartbeat.json
	HeartbeatSeconds int `yaml:"heartbeatSeconds"`
	// StuckMinutes：心跳超过这么久没更新，并且 sumika-ctl 的 status 也没人回话，才当卡死
	StuckMinutes int `yaml:"stuckMinutes"`
	// StartGraceMinutes：刚拉起来还没写出第一次心跳的宽限
	StartGraceMinutes int `yaml:"startGraceMinutes"`
	// CrashLimit 次异常退出出现在 CrashWindowMinutes 分钟里，就不再拉起，只通知
	CrashLimit         int `yaml:"crashLimit"`
	CrashWindowMinutes int `yaml:"crashWindowMinutes"`
	// RestartDelaySeconds：异常退出以后等几秒再拉起（也让 Windows 关机时来得及把守护进程一起带走）
	RestartDelaySeconds int `yaml:"restartDelaySeconds"`
	// Notify：异常退出、停止拉起时弹 Windows 桌面通知
	Notify bool `yaml:"notify"`
}

func DefaultWatchdog() Watchdog {
	return Watchdog{HeartbeatSeconds: 60, StuckMinutes: 5, StartGraceMinutes: 3, CrashLimit: 3, CrashWindowMinutes: 10, RestartDelaySeconds: 3, Notify: true}
}

func (w Watchdog) check() []string {
	var bad []string
	for _, r := range []struct {
		key             string
		value, min, max int
	}{
		{"heartbeatSeconds", w.HeartbeatSeconds, 5, 600},
		{"stuckMinutes", w.StuckMinutes, 2, 120},
		{"startGraceMinutes", w.StartGraceMinutes, 1, 60},
		{"crashLimit", w.CrashLimit, 1, 20},
		{"crashWindowMinutes", w.CrashWindowMinutes, 1, 240},
		{"restartDelaySeconds", w.RestartDelaySeconds, 0, 60},
	} {
		if r.value < r.min || r.value > r.max {
			bad = append(bad, fmt.Sprintf("watchdog.%s：%d 超出 %d～%d", r.key, r.value, r.min, r.max))
		}
	}
	// 判卡死的时限至少要容得下三次心跳，不然一次写慢就会被当成卡死
	if w.HeartbeatSeconds >= 5 && w.StuckMinutes >= 2 && w.StuckMinutes*60 < 3*w.HeartbeatSeconds {
		bad = append(bad, "watchdog.stuckMinutes：至少要有 heartbeatSeconds 的三倍")
	}
	return bad
}
