package main

// 环境清单接进本机控制接口（规格-环境自检 设计第 4 条）：doctor 读宿主那一份快照，status 在“自检”后面加一句环境概况。
// 只有宿主生成快照，命令行和界面读同一份，不在请求时现读文件或查程序。

import (
	"context"
	"fmt"
	"strings"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/envcheck"
)

// EnvSnapshot 把宿主那份环境快照拷成协议结构（doctor 的 data）。后台没开也有基础部分；演示数据模式没有宿主。
func (b *ctlBackend) EnvSnapshot(context.Context) (ctlproto.DoctorSnapshot, error) {
	h, err := b.host()
	if err != nil {
		return ctlproto.DoctorSnapshot{}, err
	}
	return envDoctorSnapshot(h.EnvSnapshot()), nil
}

// envDoctorSnapshot 是 envcheck.Snapshot 到协议结构的拷贝：字段一一对应，不再判断什么。
func envDoctorSnapshot(s envcheck.Snapshot) ctlproto.DoctorSnapshot {
	out := ctlproto.DoctorSnapshot{At: s.At, Mode: string(s.Mode), Items: make([]ctlproto.DoctorItem, 0, len(s.Items))}
	for _, it := range s.Items {
		di := ctlproto.DoctorItem{
			ID: it.ID, Group: it.Group, Name: it.Name, State: string(it.State),
			Detail: it.Detail, Next: it.Next, Affects: it.Affects,
		}
		if len(it.Sub) > 0 {
			di.Sub = make([]ctlproto.DoctorSubItem, 0, len(it.Sub))
			for _, sub := range it.Sub {
				di.Sub = append(di.Sub, ctlproto.DoctorSubItem{
					Name: sub.Name, State: string(sub.State), Detail: sub.Detail, Next: sub.Next,
				})
			}
		}
		out.Items = append(out.Items, di)
	}
	return out
}

// envStatusLine 是 status 里“环境：”后面那句：有问题、要注意、不知道各有几项；都正常就写“正常”，还没有快照是空。
func envStatusLine(s envcheck.Snapshot) string {
	if len(s.Items) == 0 {
		return ""
	}
	var bad, warn, unknown int
	for _, it := range s.Items {
		switch it.State {
		case envcheck.Bad:
			bad++
		case envcheck.Warn:
			warn++
		case envcheck.Unknown:
			unknown++
		}
	}
	var parts []string
	if bad > 0 {
		parts = append(parts, fmt.Sprintf("%d 项有问题", bad))
	}
	if warn > 0 {
		parts = append(parts, fmt.Sprintf("%d 项要注意", warn))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d 项不知道", unknown))
	}
	if len(parts) == 0 {
		return "正常"
	}
	return strings.Join(parts, "、") + "（sumika-ctl doctor 看详情）"
}
