package ctlproto

import "time"

// DoctorSnapshot 是 doctor 的 data：环境清单的一份快照（规格-环境自检 设计第 4 条）。
// 服务端把宿主那一份原样拷过来；命令行 --json 输出它，给人看的输出也由它渲染。
// At 是生成时间，Mode 是后台当前所处的模式（receive / archive / stopped），Items 按规格固定的顺序。
type DoctorSnapshot struct {
	At    time.Time    `json:"at"`
	Mode  string       `json:"mode"`
	Items []DoctorItem `json:"items"`
}

// DoctorItem 是清单的一项。State 是 ok / warn / bad / na / unknown；Detail 是现在是什么样，
// Next 是下一步，Affects 是影响什么；Sub 是按程序或部件分的小项。
type DoctorItem struct {
	ID      string          `json:"id"`
	Group   string          `json:"group"`
	Name    string          `json:"name"`
	State   string          `json:"state"`
	Detail  string          `json:"detail"`
	Next    string          `json:"next,omitempty"`
	Affects string          `json:"affects"`
	Sub     []DoctorSubItem `json:"sub,omitempty"`
}

// DoctorSubItem 是清单项下面的一个小项（第一版是 env.tools 里的外部程序）。
type DoctorSubItem struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Next   string `json:"next,omitempty"`
}

// DoctorHasBad 说这份快照里有没有“有问题”的项：有就退出码 1，只有“注意”时仍是 0。
func DoctorHasBad(d DoctorSnapshot) bool {
	for _, it := range d.Items {
		if it.State == "bad" {
			return true
		}
	}
	return false
}
