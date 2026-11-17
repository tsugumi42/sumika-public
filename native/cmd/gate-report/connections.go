package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/filelog"
)

type connectionEvent struct {
	at            int64
	kind, account string
}

// 首次连接发生在新进程里时，内存提醒没有旧的 down，Clear 不会写 up。
// 连接事实覆盖的进程区间只按日志还原；提醒的消费时间可能跨过下一次连接变化。
func logConnections(dir string, until int64, accounts map[string]string) ([]connectionEvent, []segment, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		// Windows 对普通文件的 ReadDir 也可能报路径不存在；Stat 同样确认缺失才按无日志统计。
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return nil, nil, nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	// ReadDir 已按文件名排序；desk 和 go 只在各自连续的日志日期间继承上下文。
	current := map[string]string{}
	direct := map[string]bool{}
	previous := map[string]time.Time{}
	var covered []segment
	var events []connectionEvent
	buffer := make([]byte, 4096)
	for _, e := range entries {
		prefix, day, ok := strings.Cut(e.Name(), "-")
		if e.IsDir() || !ok || (prefix != "desk" && prefix != "go") || !strings.HasSuffix(day, ".log") {
			continue
		}
		date, err := time.ParseInLocation(filelog.DayLayout, strings.TrimSuffix(day, ".log"), time.Local)
		if err != nil || date.UnixMilli() >= until {
			continue
		}
		if last, ok := previous[prefix]; ok && !date.Equal(last.AddDate(0, 0, 1)) {
			// 缺失日期里可能换号重启；旧账号和连接覆盖都不再可信。
			delete(current, prefix)
			delete(direct, prefix)
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, nil, err
		}
		account := current[prefix]
		from := int64(0)
		if direct[prefix] {
			from = date.UnixMilli()
		}
		finish := func(to int64) {
			if from != 0 {
				covered = append(covered, segment{account: account, from: from, to: to})
				from = 0
			}
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(buffer, 4<<20)
		for scanner.Scan() {
			entry, ok := filelog.ParseLine(scanner.Text())
			if !ok {
				continue
			}
			message := entry.Msg
			kind := ""
			switch {
			case message == "QQ 连上了":
				kind = "up"
			case strings.HasPrefix(message, "QQ 断了，"), strings.HasPrefix(message, "QQ 还没连上，"), message == "QQ 被踢下线了", strings.HasPrefix(message, "QQ 连上以后号对不上（"):
				kind = "down"
			}
			if kind == "" && !strings.HasPrefix(message, "桌面程序启动，") && !strings.HasPrefix(message, "托盘可以开始接收的小号 ") && !strings.HasPrefix(message, "要连的号 ") && !strings.HasPrefix(message, "关完了（关机诊断：") {
				continue
			}
			at := entry.At
			if at.UnixMilli() >= until {
				continue
			}
			switch {
			case strings.HasPrefix(message, "桌面程序启动，"):
				finish(at.UnixMilli())
				account = ""
			case strings.HasPrefix(message, "关完了（关机诊断："):
				// 停接收可以在同一桌面进程里再开；账号仍有效，连接覆盖要重新开始。
				finish(at.UnixMilli())
			case strings.HasPrefix(message, "托盘可以开始接收的小号 "), strings.HasPrefix(message, "要连的号 "):
				finish(at.UnixMilli())
				label := strings.TrimPrefix(strings.TrimPrefix(message, "托盘可以开始接收的小号 "), "要连的号 ")
				label, _, _ = strings.Cut(label, "，")
				account = accounts[label]
			case kind != "" && account != "":
				if from == 0 {
					from = at.UnixMilli()
				}
				events = append(events, connectionEvent{at: at.UnixMilli(), kind: kind, account: account})
			}
		}
		scanErr := scanner.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return nil, nil, scanErr
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		// 日志按天轮转；不把有文件的一天扩展到没有日志的日期。
		direct[prefix] = from != 0
		finish(min(date.AddDate(0, 0, 1).UnixMilli(), until))
		current[prefix] = account
		previous[prefix] = date
	}
	return events, covered, nil
}
