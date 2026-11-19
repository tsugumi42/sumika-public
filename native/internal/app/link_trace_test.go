package app

// 链接插件的插件日志走完整路径（规格-插件两层 第二点五节第 4、6 条），和 2026-11-12 读码发现的“提前 return 跳过收尾”。
// 号、群、链接都是编的；协议端、取数都是假的，日志写进测试的临时数据目录

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/linkplugin"
	"sumika-native/internal/plugintrace"
)

// linkTraceLines 读临时数据目录里链接插件的全部日志行
func linkTraceLines(t *testing.T, data string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(plugintrace.Root(data), linkplugin.ID, "*.jsonl"))
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
	}
	return out
}

// keysOf 是一行 JSON 的键，按出现的先后
func keysOf(t *testing.T, line string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// 一条消息走完取数、发送：取数一行、发送一行，字段和顺序照 9.7 的例子（t、plugin、gen、event 在前，后面按 site、route、url、ms、result 排）
func TestLinkTraceFetchAndSendRows(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", nil, "    doneEmoji: \"124\"\n")
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "961", testOwner, "https://x.com/paper/status/73001"))
	waitTrail(t, wire, "贴 76", "撤 76", "贴 124")
	var fetch, send map[string]any
	var fetchLine, sendLine string
	waitFor(t, "取数和发送各一行", func() bool {
		for _, line := range linkTraceLines(t, e.data) {
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) != nil || row["event"] != plugintrace.EventPlugin {
				continue
			}
			if row["route"] == linkplugin.RouteSend {
				send, sendLine = row, line
			} else {
				fetch, fetchLine = row, line
			}
		}
		return fetch != nil && send != nil
	})
	if fetch["site"] != "x" || fetch["result"] != "ok" || fetch["url"] != "https://x.com/paper/status/73001" || fetch["plugin"] != linkplugin.ID {
		t.Fatalf("取数那行不对：%s", fetchLine)
	}
	if send["site"] != "x" || send["result"] != "ok" || send["url"] != "https://x.com/paper/status/73001" {
		t.Fatalf("发送那行不对：%s", sendLine)
	}
	if got := strings.Join(keysOf(t, fetchLine), ","); got != "t,plugin,gen,event,site,route,url,ms,result" {
		t.Fatalf("取数那行的字段和顺序：%s", got)
	}
	if got := strings.Join(keysOf(t, sendLine), ","); got != "t,plugin,gen,event,site,route,url,ms,result" {
		t.Fatalf("发送那行的字段和顺序：%s", got)
	}
	// 启用时宿主记了一行 host.load
	if !strings.Contains(strings.Join(linkTraceLines(t, e.data), "\n"), `"event":"host.load"`) {
		t.Fatal("应当有 host.load")
	}
}

// bug（expand.go 约 282-316 行）：取完数后记关联失败就提前 return，跳过了收尾，开始表情一直挂着。
// 改后收尾挪进 defer：撤开始表情、贴失败表情，日志记一行 fail（kind store）。
// 放回旧代码就红：表情序列停在“贴 76”。删掉链接关联表来让记关联失败（只在这个测试的临时库里）
func TestLinkAssociateFailureStillConcludes(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", nil)
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Events.Exec(`DROP TABLE message_links`); err != nil {
		t.Fatal(err)
	}
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "962", testOwner, "https://x.com/paper/status/73002"))
	waitTrail(t, wire, "贴 76", "撤 76", "贴 9")
	waitFor(t, "记关联失败那一行", func() bool {
		return strings.Contains(strings.Join(linkTraceLines(t, e.data), "\n"), `"route":"send","url":"https://x.com/paper/status/73002","ms":`) &&
			strings.Contains(strings.Join(linkTraceLines(t, e.data), "\n"), `"result":"fail","kind":"store"`)
	})
}

// 发送条件不齐（qq.goSend 没开）：没发，日志记 skip（note 写原因），不当发送失败
func TestLinkTraceBlockedWhenSendNotReady(t *testing.T) {
	t.Parallel()
	e, a, _, _ := newExpandHostSend(t, "off", nil, false)
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "963", testOwner, "https://x.com/paper/status/73003"))
	waitFor(t, "发送条件不齐那一行", func() bool {
		for _, line := range linkTraceLines(t, e.data) {
			if strings.Contains(line, `"route":"send"`) && strings.Contains(line, `"result":"skip"`) && strings.Contains(line, "发送条件不齐") {
				return true
			}
		}
		return false
	})
}
