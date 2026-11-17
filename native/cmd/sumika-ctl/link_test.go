package main

import (
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// link 的命令行选项：--template、--site、--sample 进到请求的 Link 字段，不混进位置参数
func TestParseLinkArgs(t *testing.T) {
	o, err := parseArgs([]string{"link", "preview", "https://example.invalid/v/1", "--template", "@封面文字", "--site=bilibili-video"})
	if err != nil {
		t.Fatal(err)
	}
	if o.req.Cmd != ctlproto.CmdLink || len(o.req.Args) != 2 || o.req.Args[0] != "preview" || o.req.Link == nil || o.req.Link.Template != "@封面文字" || o.req.Link.Site != "bilibili-video" || o.req.Link.Sample {
		t.Fatalf("%+v %+v", o.req, o.req.Link)
	}
	o, err = parseArgs([]string{"link", "preview", "--sample", "--site", "x"})
	if err != nil || o.req.Link == nil || !o.req.Link.Sample || o.req.Link.Site != "x" || len(o.req.Args) != 1 {
		t.Fatalf("%v %+v", err, o.req)
	}
	if _, err = parseArgs([]string{"link", "preview", "--template"}); err == nil {
		t.Fatal("--template 后面没有内容应当报错")
	}
	o, _ = parseArgs([]string{"status"})
	if o.req.Link != nil {
		t.Fatal("没写 link 选项时不该带 Link")
	}
}

func TestRenderLink(t *testing.T) {
	var out, errOut strings.Builder
	r := ctlproto.Reply{OK: true, Data: []byte(`{"site":"x","template":"$作者","forward":false,"source":"sample","messages":["甲\n[正文]"]}`)}
	renderLink(&out, &errOut, ctlproto.Request{Cmd: "link", Args: []string{"preview"}}, r)
	if !strings.Contains(out.String(), "站点：x（直发，示例数据）") || !strings.Contains(out.String(), "---- 消息 1 ----\n甲\n[正文]") || errOut.Len() != 0 {
		t.Fatalf("%q %q", out.String(), errOut.String())
	}
}
