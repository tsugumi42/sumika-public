package ctlserver

// 回复里外面来的文字（群名、提醒、连接状态、日志行、错误原因）出去前统一过 ctlproto.Clean：
// 终端控制序列、回车换行不能原样交给客户端和 Agent。Backend 返回什么都经过这里，不靠每个后端各自记得过一遍。

import "sumika-native/internal/ctlproto"

func cleanStatus(s ctlproto.Status) ctlproto.Status {
	s.Reason, s.Account, s.NoRecv = ctlproto.Clean(s.Reason), ctlproto.Clean(s.Account), ctlproto.Clean(s.NoRecv)
	s.Connection, s.DataDir, s.Version = ctlproto.Clean(s.Connection), ctlproto.Clean(s.DataDir), ctlproto.Clean(s.Version)
	if s.Alerts != nil {
		alerts := make([]ctlproto.Alert, len(s.Alerts))
		for i, a := range s.Alerts {
			alerts[i] = ctlproto.Alert{Title: ctlproto.Clean(a.Title), Body: ctlproto.Clean(a.Body)}
		}
		s.Alerts = alerts
	}
	return s
}

func cleanReceive(r ctlproto.Receive) ctlproto.Receive {
	r.Reason = ctlproto.Clean(r.Reason)
	return r
}

func cleanLogs(l ctlproto.Logs) ctlproto.Logs {
	if l.Lines != nil {
		lines := make([]string, len(l.Lines))
		for i, ln := range l.Lines {
			lines[i] = ctlproto.Clean(ln)
		}
		l.Lines = lines
	}
	return l
}
