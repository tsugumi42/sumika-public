package main

// 启动后自动接收 QQ（ADR-227 第 3 条）：默认关；开着时，桌面程序起来、只看历史（archive）开好以后，
// 自动走一遍和托盘"开始接收 QQ"同一个函数（host.startReceive）。值存在 <数据目录>/go/desktop-settings.json
// （internal/desksettings），不改用户的 sumika.yml；托盘的勾选项和 sumika-ctl 的 auto-receive 都经过 setAutoReceive。
// 读文件、写文件都在后台线程；界面线程只读 autoRecv 这个缓存。

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/desksettings"
)

// initSettings 定下数据目录、读出开关的当前值放进缓存（后台线程，openLog 之后）。
// 读不到配置就没有数据目录，开关当作关、托盘那一项点不了；文件读坏了也按关，写一行日志
func (s *shell) initSettings() {
	if s.demo || s.settings.Load() != nil {
		return
	}
	dir, err := resolveDataDir(s.opt)
	if err != nil {
		s.logf("找不到数据目录，桌面开关（启动后自动接收 QQ）用不了：%v", err)
		return
	}
	st := desksettings.New(dir)
	on, err := st.AutoReceive()
	if err != nil {
		s.logf("读桌面开关出错，启动后自动接收 QQ 按关：%v", err)
	}
	s.recvMu.Lock()
	s.autoRecv.Store(on)
	s.recvMu.Unlock()
	s.settings.Store(st)
	s.loadPanes(st)
	s.loadAppearance(st)
	s.loadMainView(st)
}

// maybeAutoReceive 宿主 start 返回以后调：开着、并且现在正好是只看历史或本地可用，才开始接收。
// 不是这两者（受阻了：数据锁被占用等）就不动，写明这次没接收
func (s *shell) maybeAutoReceive() {
	if !s.autoRecv.Load() {
		return
	}
	if ph := s.host.current().Phase; ph != phaseArchive && ph != phaseLocal {
		s.logf("设了启动后自动接收 QQ，但后台现在是「%s」，这次不接收", phaseText(ph))
		return
	}
	s.logf("按设置，启动后自动开始接收 QQ")
	// 开机时协议端等常比 Sumika 起得晚，第一次开接收连不上就放弃会让 QQ 一直停着（2026-11-08 重启后就这样）。
	// 失败了按退避再试几次；用户在这期间自己点了停止 / 开始、关了开关，或程序退出，就不再试。
	// 模型目录取不到不再让开接收失败（规格-模型配置 6.1：后台自己按退避重试），这里只管接收后台本身开不了
	retryAutoReceive(s.host.ctx, autoReceiveDelays, s.host.startReceiveAuto, func() bool {
		ph := s.host.current().Phase
		return s.autoRecv.Load() && (ph == phaseArchive || ph == phaseLocal)
	}, s.logf)
}

// autoReceiveDelays：自动接收失败后每次重试前等多久（合计约五分钟）
var autoReceiveDelays = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute, time.Minute}

// retryAutoReceive 先试一次；try 返回"值得重试"的失败就按 delays 逐个等了再试，其他结果（成功、已在接收、
// 没小号、后台受阻、退出中）各记一行日志就停。每次等完先问 wanted（还想不想要），不想要就停
func retryAutoReceive(ctx context.Context, delays []time.Duration, try func() receiveResult, wanted func() bool, logf func(string, ...any)) {
	if r := try(); !r.Retryable() {
		logAutoReceiveEnd(r, logf)
		return
	} else {
		logf("启动后自动接收没成功：%v", r.Err)
	}
	for i, d := range delays {
		logf("启动后自动接收没成功，%s 后再试（第 %d/%d 次）", d, i+1, len(delays))
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if !wanted() {
			logf("启动后自动接收不再重试：开关关了或已经手动切换过")
			return
		}
		r := try()
		if !r.Retryable() {
			logAutoReceiveEnd(r, logf)
			return
		}
		logf("启动后自动接收没成功：%v", r.Err)
	}
	logf("启动后自动接收重试 %d 次都没成功，请看状态区的原因，或在托盘点「开始接收 QQ」", len(delays))
}

// logAutoReceiveEnd 不再重试时按结果种类写一行日志
func logAutoReceiveEnd(r receiveResult, logf func(string, ...any)) {
	switch r.Outcome {
	case receiveStarted:
		logf("启动后自动接收：已开始接收")
	case receiveAlready:
		logf("启动后自动接收：已经在接收，不用再开")
	case receiveCancelled:
		logf("启动后自动接收：程序在退出，不再接收")
	case receiveNoAccount:
		logf("启动后自动接收没成功：没有能接收的小号（%v），重试没用", r.Err)
	case receiveBlocked:
		logf("启动后自动接收没成功：后台受阻（%v），重试没用", r.Err)
	case receiveLocalBusy:
		logf("启动后自动接收先不拆后台：%v", r.Err)
	case receiveLocalNotReady:
		logf("启动后自动接收先不拆后台：%v", r.Err)
	}
}

// setAutoReceive 改开关：先写文件，写成了再换缓存。托盘（点勾选项）和 sumika-ctl 都调它，调用方在后台线程
func (s *shell) setAutoReceive(on bool) error {
	st := s.settings.Load()
	if st == nil {
		err := errors.New("没找到数据目录，改不了这个开关")
		s.reportSettingsError(err)
		return err
	}
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	if err := st.SetAutoReceive(on); err != nil {
		s.logf("改启动后自动接收 QQ 失败：%v", err)
		s.reportSettingsError(err)
		return err
	}
	s.autoRecv.Store(on)
	if on {
		s.logf("启动后自动接收 QQ：已打开")
	} else {
		s.logf("启动后自动接收 QQ：已关闭")
	}
	return nil
}

// reportSettingsError 把失败原因放进状态区（和"改开机启动失败"一个位置），写界面状态要回到界面线程
func (s *shell) reportSettingsError(err error) {
	s.a.post(func() { s.a.status = "改启动后自动接收失败：" + err.Error() })
}
