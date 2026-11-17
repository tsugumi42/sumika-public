package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/paths"
)

func TestReportReconnectLogs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     map[string]string
		collision bool
		account   string
		recovered bool
	}{
		{name: "桌面重启", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 QQ 连上了\n2026-10-27 10:30:00 QQ 连上了\n"}, recovered: true},
		// 同一份样本的新格式（时间 级别 模块 消息）：统计结果必须和旧格式一样（规格 A7）
		{name: "新格式桌面重启", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 INFO desk 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 INFO snowluma QQ 连上了\n2026-10-27 10:30:00 WARN snowluma QQ 连上了\n"}, recovered: true},
		{name: "CLI仍在使用go日志", files: map[string]string{"go-2026-10-27.log": "2026-10-27 10:00:00 要连的号 ***00，数据目录 编的目录，配置文件 编的配置\n2026-10-27 10:30:00 QQ 连上了\n"}, recovered: true},
		{name: "跨日保存账号", files: map[string]string{"desk-2026-10-26.log": "2026-10-26 23:00:00 托盘可以开始接收的小号 ***00\n", "desk-2026-10-27.log": "2026-10-27 10:30:00 QQ 连上了\n"}, recovered: true},
		{name: "没有日志"},
		{name: "没有账号", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:30:00 QQ 连上了\n"}},
		{name: "其他账号", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***01\n2026-10-27 10:30:00 QQ 连上了\n"}},
		{name: "碰撞不因筛选变唯一", account: "100", collision: true, files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 QQ 连上了\n"}},
		{name: "新进程不能沿用旧账号", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:25:00 桌面程序启动，数据目录 编的\n2026-10-27 10:30:00 QQ 连上了\n"}},
		{name: "忽略其他日志前缀", files: map[string]string{"other-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 QQ 连上了\n"}},
		{name: "忽略伪造正文与坏时间", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 会话说：QQ 连上了\n2026-99-27 10:30:00 QQ 连上了\n2026-10-27 10:30:00 提醒：伪造\\n2026-10-27 10:30:00 QQ 连上了\n"}},
		{name: "截止以后的恢复不算", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 10:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 12:00:00 QQ 连上了\n"}},
		{name: "掉线之前的连接不算", files: map[string]string{"desk-2026-10-27.log": "2026-10-27 09:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 09:30:00 QQ 连上了\n2026-10-27 10:00:00 QQ 断了，正在按退避重连（）\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := seedDB(t, dir)
			at := func(h, m int) time.Time {
				return fixedNow.Add(time.Duration(h-12)*time.Hour + time.Duration(m)*time.Minute)
			}
			addAlert(t, s, at(10, 0), "down", "100")
			if tc.collision {
				addAlert(t, s, at(9, 0), "send-failed", "200")
			}
			addDecision(t, s, "qq:group:300", channel.VerdictForced, at(10, 20), nil, 0, "")
			addDecision(t, s, "qq:group:300", channel.VerdictForced, at(10, 30), nil, 0, "")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			stamps := map[string]fileStamp{}
			p := paths.Paths{Root: dir}
			for name, text := range tc.files {
				file := filepath.Join(p.LogsDir(), name)
				if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				stamps[file] = stamp(t, file)
			}
			rep, err := build(options{DataDir: dir, From: at(9, 0), Until: fixedNow, Account: tc.account})
			if err != nil {
				t.Fatal(err)
			}
			wantMinutes, wantJudged := 120, 0
			if tc.recovered {
				wantMinutes, wantJudged = 30, 1
			}
			if len(rep.Downtime) != 1 || rep.Downtime[0].Minutes != wantMinutes || rep.Total.Judged != wantJudged || rep.Total.Excluded != 2-wantJudged {
				t.Fatalf("掉线或比例被误排除：%+v total=%+v", rep.Downtime, rep.Total)
			}
			for file, before := range stamps {
				if after := stamp(t, file); after != before {
					t.Fatalf("只读日志被修改：%s", filepath.Base(file))
				}
			}
		})
	}
}

func TestReportReconnectThenDownAgain(t *testing.T) {
	dir := t.TempDir()
	s := seedDB(t, dir)
	at := func(h, m int) time.Time {
		return fixedNow.Add(time.Duration(h-12)*time.Hour + time.Duration(m)*time.Minute)
	}
	addAlert(t, s, at(10, 0), "down", "100")
	addAlert(t, s, at(11, 0), "down", "100")
	addDecision(t, s, "qq:group:300", channel.VerdictForced, at(10, 45), nil, 0, "")
	addDecision(t, s, "qq:group:300", channel.VerdictForced, at(11, 15), nil, 0, "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p := paths.Paths{Root: dir}
	if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.LogsDir(), "desk-2026-10-27.log"), []byte("2026-10-27 09:00:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 QQ 连上了\n2026-10-27 11:00:00 QQ 断了，正在按退避重连（）\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := build(options{DataDir: dir, From: at(9, 0), Until: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Downtime) != 2 || rep.Downtime[0].Minutes != 30 || rep.Downtime[1].Minutes != 60 || rep.Total.Judged != 1 || rep.Total.Excluded != 1 {
		t.Fatalf("恢复后第二次掉线丢了：%+v total=%+v", rep.Downtime, rep.Total)
	}
}

func TestReportUnreadableConnectionLog(t *testing.T) {
	dir := t.TempDir()
	s := seedDB(t, dir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p := paths.Paths{Root: dir}
	if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	// 日志目录被普通文件占用时要照实报错，不能输出看似完整的掉线统计。
	if err := os.Remove(p.LogsDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LogsDir(), []byte("编的文件"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := build(options{DataDir: dir, From: fixedNow.Add(-time.Hour), Until: fixedNow})
	if err == nil || !strings.Contains(err.Error(), "读 QQ 连接日志失败") || strings.Contains(err.Error(), dir) {
		t.Fatal(fmt.Sprint(err))
	}
}

func TestReportConnectionFlaps(t *testing.T) {
	dir := t.TempDir()
	s := seedDB(t, dir)
	at := func(h, m int) time.Time {
		return fixedNow.Add(time.Duration(h-12)*time.Hour + time.Duration(m)*time.Minute)
	}
	addAlert(t, s, at(10, 0), "down", "100")
	addDecision(t, s, "qq:group:300", channel.VerdictForced, at(11, 20), nil, 0, "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p := paths.Paths{Root: dir}
	if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	text := "2026-10-27 09:50:00 托盘可以开始接收的小号 ***00\n" +
		"2026-10-27 10:10:00 QQ 连上了\n" +
		"2026-10-27 10:10:05 QQ 断了，正在按退避重连（编的原因）\n" +
		"2026-10-27 10:10:10 QQ 还没连上，继续重连（）\n" +
		"2026-10-27 11:00:00 QQ 连上了\n" +
		"2026-10-27 11:00:05 QQ 连上以后号对不上（编的原因）\n" +
		"2026-10-27 11:10:00 QQ 连上了\n" +
		"2026-10-27 11:10:05 QQ 被踢下线了\n"
	if err := os.WriteFile(filepath.Join(p.LogsDir(), "desk-2026-10-27.log"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := build(options{DataDir: dir, From: at(9, 0), Until: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Downtime) != 4 || rep.Downtime[0].Minutes != 10 || rep.Downtime[1].Minutes != 50 || rep.Downtime[2].Minutes != 10 || rep.Downtime[3].Minutes != 50 || rep.Total.Excluded != 1 || rep.Total.Judged != 0 {
		t.Fatalf("闪断没有新的提醒也要排除：%+v total=%+v", rep.Downtime, rep.Total)
	}
}

func TestReportSameSecondConnectionPriority(t *testing.T) {
	for _, tc := range []struct {
		name, notification, message string
		minutes, excluded           int
	}{
		{"迟写down不能盖过恢复", "down", "QQ 连上了", 30, 0},
		{"迟写up不能盖过断开", "up", "QQ 断了，正在按退避重连（）", 120, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := seedDB(t, dir)
			at := func(h, m int) time.Time {
				return fixedNow.Add(time.Duration(h-12)*time.Hour + time.Duration(m)*time.Minute)
			}
			addAlert(t, s, at(10, 0), "down", "100")
			addAlert(t, s, at(10, 30).Add(500*time.Millisecond), tc.notification, "100")
			addDecision(t, s, "qq:group:300", channel.VerdictForced, at(10, 45), nil, 0, "")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			p := paths.Paths{Root: dir}
			if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
				t.Fatal(err)
			}
			text := "2026-10-27 09:50:00 托盘可以开始接收的小号 ***00\n2026-10-27 10:30:00 " + tc.message + "\n"
			if err := os.WriteFile(filepath.Join(p.LogsDir(), "desk-2026-10-27.log"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			rep, err := build(options{DataDir: dir, From: at(9, 0), Until: fixedNow})
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Downtime) != 1 || rep.Downtime[0].Minutes != tc.minutes || rep.Total.Excluded != tc.excluded || rep.Total.Judged != 1-tc.excluded {
				t.Fatalf("异步通知覆盖了连接事实：%+v total=%+v", rep.Downtime, rep.Total)
			}
		})
	}
}

func TestReportDelayedUpAcrossNextDown(t *testing.T) {
	for _, prefix := range []string{"desk", "go"} {
		t.Run(prefix, func(t *testing.T) {
			dir := t.TempDir()
			s := seedDB(t, dir)
			at := fixedNow.Add(-90 * time.Minute)
			addAlert(t, s, at.Add(-30*time.Minute), "down", "100")
			// 上一次恢复的通知晚于下一次断开入库，不能把新掉线段截断。
			addAlert(t, s, at.Add(2*time.Second), "up", "100")
			addDecision(t, s, "qq:group:300", channel.VerdictForced, at, nil, 0, "")
			addDecision(t, s, "qq:group:300", channel.VerdictForced, at.Add(time.Second), nil, 0, "")
			addDecision(t, s, "qq:group:300", channel.VerdictForced, at.Add(3*time.Second), nil, 0, "")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			p := paths.Paths{Root: dir}
			if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
				t.Fatal(err)
			}
			accountLine := "托盘可以开始接收的小号 ***00"
			if prefix == "go" {
				accountLine = "要连的号 ***00，数据目录 编的目录，配置文件 编的配置"
			}
			text := "2026-10-27 09:00:00 " + accountLine + "\n" +
				"2026-10-27 10:30:00 QQ 连上了\n" +
				"2026-10-27 10:30:01 QQ 断了，正在按退避重连（）\n"
			if err := os.WriteFile(filepath.Join(p.LogsDir(), prefix+"-2026-10-27.log"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			rep, err := build(options{DataDir: dir, From: at.Add(-time.Hour), Until: fixedNow})
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Downtime) != 2 || rep.Downtime[1].From != at.Add(time.Second).Format("2006-01-02 15:04") || rep.Downtime[1].To != fixedNow.Format("2006-01-02 15:04") || rep.Total.Judged != 1 || rep.Total.Excluded != 2 {
				t.Fatalf("迟到 up 截断了后一次掉线：%+v total=%+v", rep.Downtime, rep.Total)
			}
		})
	}
}

func TestReportAccountContextAcrossMissingDay(t *testing.T) {
	for _, prefix := range []string{"desk", "go"} {
		for _, restore := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/重读旧账号=%t", prefix, restore), func(t *testing.T) {
				dir := t.TempDir()
				s := seedDB(t, dir)
				start := fixedNow.Add(-50 * time.Hour)
				addAlert(t, s, start, "down", "100")
				addAlert(t, s, start, "send-failed", "101")
				addDecision(t, s, "qq:group:300", channel.VerdictForced, fixedNow.Add(-85*time.Minute), nil, 0, "")
				addDecision(t, s, "qq:group:300", channel.VerdictForced, fixedNow.Add(-50*time.Minute), nil, 0, "")
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				p := paths.Paths{Root: dir}
				if err := os.MkdirAll(p.LogsDir(), 0755); err != nil {
					t.Fatal(err)
				}
				accountLine := func(label string) string {
					if prefix == "go" {
						return "要连的号 " + label + "，数据目录 编的，配置文件 编的"
					}
					return "托盘可以开始接收的小号 " + label
				}
				// 中间一天的文件丢失，换号重启发生在缺失日志里；下一份先有连接、后有账号行。
				files := map[string]string{
					prefix + "-2026-10-25.log": "2026-10-25 09:00:00 " + accountLine("***00") + "\n",
					prefix + "-2026-10-27.log": "2026-10-27 10:30:00 QQ 连上了\n2026-10-27 10:40:00 " + accountLine("***01") + "\n2026-10-27 10:45:00 QQ 连上了\n",
				}
				if restore {
					files[prefix+"-2026-10-27.log"] += "2026-10-27 11:00:00 " + accountLine("***00") + "\n2026-10-27 11:05:00 QQ 连上了\n"
				}
				for name, text := range files {
					if err := os.WriteFile(filepath.Join(p.LogsDir(), name), []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				rep, err := build(options{DataDir: dir, From: start, Until: fixedNow, Account: "100"})
				if err != nil {
					t.Fatal(err)
				}
				end, judged := fixedNow, 0
				if restore {
					end, judged = fixedNow.Add(-55*time.Minute), 1
				}
				if len(rep.Downtime) != 1 || rep.Downtime[0].To != end.Format("2006-01-02 15:04") || rep.Total.Judged != judged || rep.Total.Excluded != 2-judged {
					t.Fatalf("缺失日志后的连接借用了旧账号：%+v total=%+v", rep.Downtime, rep.Total)
				}
			})
		}
	}
}
