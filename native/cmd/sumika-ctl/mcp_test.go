package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"sumika-native/internal/ctlproto"
)

func TestMCPReleaseArgsParse(t *testing.T) {
	o, err := parseArgs([]string{"mcp", "release", "docs", "--reason", "旧调用已结束", "--accept-unknown-risk"})
	if err != nil {
		t.Fatal(err)
	}
	if o.req.Cmd != ctlproto.CmdMCP || len(o.req.Args) != 2 || o.req.MCP == nil || o.req.MCP.Reason != "旧调用已结束" || !o.req.MCP.AcceptRisk {
		t.Fatalf("%+v %+v", o.req, o.req.MCP)
	}
	o, err = parseArgs([]string{"mcp", "release", "docs", "--reason=等号写法"})
	if err != nil || o.req.MCP.Reason != "等号写法" || o.req.MCP.AcceptRisk {
		t.Fatalf("没写 --accept-unknown-risk 不能默认接受：%+v %v", o.req.MCP, err)
	}
	if _, err = parseArgs([]string{"mcp", "release", "docs", "--reason"}); err == nil {
		t.Fatal("--reason 后面没内容该报错")
	}
}

func TestMCPStatusRendersIsolationAndHowToRelease(t *testing.T) {
	var out, errOut bytes.Buffer
	data, _ := json.Marshal(ctlproto.MCPStatus{Servers: []ctlproto.MCPServer{
		{ID: "docs", Transport: "http", Connected: true, Isolated: true, Calls: []ctlproto.MCPCall{{ID: "ab12cd34", State: "unknown", Cause: "timeout", DispatchedAt: 1763200000000}}},
		{ID: "files", Transport: "stdio", Connected: true},
	}})
	r := ctlproto.Reply{OK: true, Data: data}
	renderMCP(&out, &errOut, ctlproto.Request{Cmd: "mcp", Args: []string{"status"}}, r)
	for _, want := range []string{"docs｜远端 HTTP｜被隔离", "调用时限到了还没回结果", "sumika-ctl mcp release docs --reason", "files｜本机程序｜正常"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("缺 %q：\n%s", want, out.String())
		}
	}
}
