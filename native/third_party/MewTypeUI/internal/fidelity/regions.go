package fidelity

import (
	"fmt"
	"image"
)

// Regions records physical-pixel annotations, not upstream style values. Bounds
// use exclusive right/bottom edges. Crop/column placement is host calibration;
// Axes selects only dimensions whose semantics survive invented text/media.
func Regions(id string) []Region {
	missing := "paint/fidelity-coverage.md"
	r := func(name, label, component, spec, axes string, x, y, w, h int) Region {
		return Region{Name: name, Label: label, Component: component, Spec: spec, Classification: "规格未覆盖", Method: "layout", Axes: axes, Reference: image.Rect(x, y, x+w, y+h)}
	}
	var rows []Region
	full := id == "14-43-11" || id == "14-43-53" || id == "14-43-59" || id == "14-44-03" || id == "14-44-57" || id == "14-46-23" || id == "14-46-27" || id == "14-46-53"
	if full {
		if id != "14-44-03" {
			rows = append(rows,
				r("文件夹栏宽", "文件夹栏", "dialogs.Rail", missing+"#文件夹栏", "w", 0, 31, 90, 843),
				r("搜索框", "会话搜索", "dialogs.Search", missing+"#搜索栏", "xywh", 105, 42, 297, 45),
				r("会话行", "会话行0", "dialogs.Row", missing+"#会话行", "yh", 90, 98, 327, 78),
			)
		}
		rows = append(rows, r("历史底色", "历史背景", "gallery.NewFidelityScene", missing+"#背景", "", 420, 200, 10, 10))
		if id != "14-43-11" {
			rows = append(rows, r("聊天顶栏", "顶栏", "chat.Header", missing+"#顶栏", "h", 417, 31, 668, 67), r("置顶条", "置顶条", "compose.ReferenceBar", missing+"#置顶条", "h", 417, 99, 668, 60), r("输入区", "输入区", "compose.Editor", missing+"#输入区", "yh", 417, 817, 668, 57))
		}
		if id == "14-46-23" || id == "14-46-27" || id == "14-46-53" {
			rows = append(rows, r("侧栏面板", "侧栏选择面板", "compose.Picker", missing+"#侧栏面板", "wh", 1085, 31, 455, 843), r("侧栏标签", "表情", "compose.Picker", missing+"#侧栏面板", "h", 1085, 31, 152, 54), r("侧栏搜索", "侧栏搜索", "compose.Picker", missing+"#侧栏面板", "h", 1096, 91, 431, 42))
		}
		if id == "14-44-03" {
			rows = append(rows, r("主菜单抽屉", "", "无对应组件", missing+"#主菜单与资料栏", "wh", 0, 31, 341, 843))
		}
		if id == "14-43-59" || id == "14-44-03" || id == "14-44-57" {
			rows = append(rows, r("资料栏", "", "无对应组件", missing+"#主菜单与资料栏", "wh", 1085, 31, 455, 843))
		}
	}
	switch id {
	case "14-44-28":
		rows = append(rows, r("消息菜单操作区", "菜单", "overlay.ContextMenu", missing+"#消息菜单", "h", 45, 157, 223, 346))
	case "14-45-27":
		rows = append(rows, r("聊天顶栏", "顶栏", "chat.Header", missing+"#顶栏", "h", 0, 31, 669, 67), r("置顶条", "置顶条", "compose.ReferenceBar", missing+"#置顶条", "h", 0, 99, 669, 60), r("加载图片", "加载图片", "content.Photo", missing+"#媒体", "wh", 70, 159, 529, 377), r("输入区", "输入区", "compose.Editor", missing+"#输入区", "yh", 0, 817, 669, 57))
	case "14-45-46", "14-45-51":
		info := r("文件时间墨迹", "Date text", "chat.BottomInfo", "paint/message-bottom.md#基线与图标槽", "h", 382, 135, 106, 17)
		info.Method = "ink"
		for i := range 2 {
			icon := r(fmt.Sprintf("文件图标%d", i+1), fmt.Sprintf("文件行%d", i), "content.File", missing+"#文件", "wh", 80, i*67, 70, 74)
			icon.Method, icon.Seed, icon.OwnSeed = "surface", image.Pt(95, 30+i*67), image.Pt(25, 20)
			rows = append(rows, icon)
		}
		rows = append(rows, info)
	case "14-46-06":
		rows = append(rows, r("引用块", "引用块", "content.Markdown", missing+"#引用", "h", 132, 132, 495, 69), r("回复与输入区", "输入区", "compose.Editor", missing+"#输入区", "h", 48, 316, 669, 118))
	case "14-46-12":
		rows = append(rows, r("引用块", "引用块", "content.Markdown", missing+"#引用", "h", 104, 129, 496, 70), r("输入区", "输入区", "compose.Editor", missing+"#输入区", "yh", 20, 417, 669, 57))
	case "14-44-57":
		pill := r("回应胶囊", "回应0", "content.Reactions", "paint/message-bottom.md#形状与内容矩形", "h", 446, 354, 55, 27)
		pill.Classification = "规格未覆盖（截图取整与资源边界）"
		rows = append(rows, pill)
		bubble := r("出站气泡", "消息行1", "chat.Bubble", missing+"#气泡", "h", 808, 393, 270, 88)
		bubble.Method, bubble.Seed, bubble.OwnSeed = "surface", image.Pt(1046, 422), image.Pt(-30, 20)
		timestamp := r("气泡时间墨迹", "Date text", "chat.BottomInfo", "paint/message-bottom.md#基线与图标槽", "h", 735, 369, 62, 17)
		timestamp.Method = "ink"
		rows = append(rows, bubble, timestamp)
	}
	// Colors are measured independently from structural geometry: the dominant
	// fill suppresses differing text/avatar pixels, never scores their content.
	for i := range rows {
		switch rows[i].Name {
		case "文件时间墨迹":
			rows[i].Classification = "规格未覆盖（平台字体度量）"
		}
	}
	return rows
}
