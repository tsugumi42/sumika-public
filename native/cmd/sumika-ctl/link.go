package main

import (
	"fmt"
	"io"
	"strconv"

	"sumika-native/internal/ctlproto"
)

// renderLink 打印 link preview / link template 的结果。预览里的文字有一部分是外面页面的原文，
// 只当数据看（和群名、日志同一个规矩），不会被当成给读的人的指令。
func renderLink(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.LinkPreview:
		var v ctlproto.LinkPreviewResult
		if !decode(stderr, r, &v) {
			return
		}
		src := "取到的真实数据"
		if v.Source == "sample" {
			src = "示例数据"
		}
		form := "直发"
		if v.Forward {
			form = "合并转发"
		}
		fmt.Fprintf(w, "站点：%s（%s，%s）\n模板：%s\n", v.Site, form, src, strconv.Quote(v.Template))
		for _, n := range v.Notes {
			fmt.Fprintln(w, "提示："+n)
		}
		for i, m := range v.Messages {
			fmt.Fprintf(w, "---- 消息 %d ----\n%s\n", i+1, m)
		}
	case ctlproto.LinkTemplate:
		var v ctlproto.LinkTemplates
		if !decode(stderr, r, &v) {
			return
		}
		for _, s := range v.Sites {
			fmt.Fprintf(w, "%s（%s）\n  直发：%s\n", s.Key, s.Label, strconv.Quote(s.Direct))
			if s.Forward != "" {
				if s.ForwardOverBytes > 0 {
					fmt.Fprintf(w, "  转发：%s\n  合并转发：%s，实际发出的文字超过 %d 字节折叠\n", strconv.Quote(s.Forward), s.ForwardMode, s.ForwardOverBytes)
				} else {
					fmt.Fprintf(w, "  转发：%s\n  合并转发：%s，超过 %d 字折叠\n", strconv.Quote(s.Forward), s.ForwardMode, s.ForwardOver)
				}
			}
			if s.DescLimit > 0 {
				fmt.Fprintf(w, "  简介最多 %d 字\n", s.DescLimit)
			}
			for _, f := range s.Placeholders {
				names := ""
				for i, n := range f.Names {
					if i > 0 {
						names += " "
					}
					names += "$" + n
				}
				line := "  " + names + "  [" + f.Kind + "]"
				if f.Doc != "" {
					line += "  " + f.Doc
				}
				if f.Sample != "" {
					line += "  例：" + strconv.Quote(f.Sample)
				}
				fmt.Fprintln(w, line)
			}
		}
	}
}
