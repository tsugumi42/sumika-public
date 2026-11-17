package plugintrace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 和 Bun 对拍：同一批输入，Go 写的文件和 server/src/plugins/tracelog.ts 写的逐行一样；stats-cli 读得出 Go 写的文件。
// 没装 bun 就跳过。bun 进程的环境清掉 GIT_*（钩子里跑时别让子进程摸到真仓库）

func bunOrSkip(t *testing.T) string {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("没装 bun，跳过对拍")
	}
	return bun
}

func serverDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "..", "server", "src", "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "tracelog.ts")); err != nil {
		t.Skip("找不到 server/src/plugins/tracelog.ts，跳过对拍")
	}
	return d
}

func runBun(t *testing.T, bun string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, args...)
	cmd.Dir = t.TempDir()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("bun %v 没成：%v\n%s", args, err, stderr)
	}
	return out
}

type bunRow struct {
	Plugin string         `json:"plugin"`
	Gen    int            `json:"gen"`
	Event  string         `json:"event"`
	Tool   string         `json:"tool,omitempty"`
	Fields map[string]any `json:"fields"`
}

// 一批刁钻的输入：转义序列、各种换行、超长、表情、网址的各种写法、令牌段、非整数毫秒、坏的 result
func parityRows() []bunRow {
	esc := "\x1b"
	long := strings.Repeat("字😀", 400)
	rows := []bunRow{
		{Plugin: "link-reader", Gen: 3, Event: EventPlugin, Fields: map[string]any{"site": "bilibili", "route": "bili_view", "url": "https://www.example.test/video/BV1xx411c7mD", "ms": 2310, "result": "fail", "kind": "network", "error": "network，来源 bili_view，线路 bili_view:network:2300ms"}},
		{Plugin: "link-reader", Gen: 1, Event: EventPlugin, Fields: map[string]any{"result": "ok", "note": "cache", "ms": 1.5}},
		{Plugin: "link-reader", Gen: 1, Event: EventPlugin, Fields: map[string]any{"result": "skip", "ms": 2.5, "note": "a\r\nb c\td\x00e\x7f\u0085f"}},
		{Plugin: "link-reader", Gen: 1, Event: EventPlugin, Fields: map[string]any{"result": "bogus", "kind": esc + "[31m红" + esc + "[0m", "error": esc + "]0;t\x07尾<&>\"\\"}},
		{Plugin: "life-tools", Gen: 0, Event: EventTool, Tool: "weather\n工具", Fields: map[string]any{"result": "ok", "ms": 0}},
		{Plugin: "life-tools", Gen: 2, Event: EventPlugin, Fields: map[string]any{"site": long, "route": long, "kind": long, "error": long, "note": long, "result": "fail"}},
		{Plugin: "life-tools", Gen: 2, Event: EventLoad, Fields: map[string]any{"result": "ok", "note": "第 2 次"}},
		{Plugin: "life-tools", Gen: 2, Event: EventUnload, Fields: map[string]any{"result": "ok", "note": "关掉了"}},
	}
	for _, u := range []string{
		"https://user:pw@WWW.Example.TEST:443/a/b?q=1#frag",
		"http://example.test:8080",
		"http://example.test:80/x",
		"  https://example.test/a/../b/./c  ",
		`https:\\example.test\a\b`,
		"https://example.test/空 格/{x}|^`",
		"https://example.test/v/abcdefghijklmnopqrstuvwxyz0123/x",
		"https://example.test/v/123456789012345678901234567890",
		"https://example.test/%E7%A9%BA/%zz",
		"https://例子.测试/路径",
		"https://[2001:DB8:0:0:0:0:0:1]:8443/x",
		"https://example.test/a/%2e%2E/b",
		"https://example.test/" + strings.Repeat("长", 400),
		"HTTPS://EXAMPLE.TEST",
		"ftp://example.test/x",
		"不是网址",
		"https://",
		"mailto:a@example.test",
	} {
		rows = append(rows, bunRow{Plugin: "link-reader", Gen: 1, Event: EventPlugin, Fields: map[string]any{"result": "ok", "url": u}})
	}
	return rows
}

func eventOf(f map[string]any) Event {
	s := func(k string) string { v, _ := f[k].(string); return v }
	e := Event{Site: s("site"), Route: s("route"), URL: s("url"), Result: s("result"), Kind: s("kind"), Error: s("error"), Note: s("note")}
	if ms, ok := f["ms"].(float64); ok {
		e.Took, e.Timed = time.Duration(ms*float64(time.Millisecond)), true
	} else if ms, ok := f["ms"].(int); ok {
		e.Took, e.Timed = time.Duration(ms)*time.Millisecond, true
	}
	return e
}

const bunWriter = `const [lib, dataDir, input, iso] = process.argv.slice(2);
const { TraceLog } = await import(Bun.pathToFileURL(lib).href);
const rows = JSON.parse(await Bun.file(input).text());
const log = new TraceLog({ dataDir, log: () => {}, now: () => new Date(iso) });
for (const r of rows) log.write(r.plugin, r.gen, r.event, r.fields, r.tool || undefined);
`

func TestBunParityLineByLine(t *testing.T) {
	bun := bunOrSkip(t)
	lib := filepath.Join(serverDir(t), "tracelog.ts")
	rows := parityRows()
	tmp := t.TempDir()
	in := filepath.Join(tmp, "rows.json")
	b, _ := json.Marshal(rows)
	if err := os.WriteFile(in, b, 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(tmp, "writer.ts")
	if err := os.WriteFile(script, []byte(bunWriter), 0o644); err != nil {
		t.Fatal(err)
	}
	bunDir, goDir := filepath.Join(tmp, "bun"), filepath.Join(tmp, "go")
	runBun(t, bun, script, lib, bunDir, in, exampleNow.Format(time.RFC3339Nano))

	// Go 读同一份 JSON（数字是 float64），照样写
	var back []bunRow
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	l := New(Options{DataDir: goDir, Now: func() time.Time { return exampleNow }})
	for _, r := range back {
		l.Write(r.Plugin, r.Gen, r.Event, eventOf(r.Fields), r.Tool)
	}
	for _, plugin := range []string{"link-reader", "life-tools"} {
		want := readLines(t, fileOf(bunDir, plugin, exampleNow))
		got := readLines(t, fileOf(goDir, plugin, exampleNow))
		if len(want) != len(got) {
			t.Fatalf("%s 行数不一样：Bun %d，Go %d", plugin, len(want), len(got))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Errorf("%s 第 %d 行不一样：\nBun %s\nGo  %s", plugin, i+1, want[i], got[i])
			}
		}
	}
}

// stats-cli 读 Go 写的文件能出汇总（用真实的现在：--days 窗口按今天算）
func TestBunStatsReadsGoFiles(t *testing.T) {
	bun := bunOrSkip(t)
	cli := filepath.Join(serverDir(t), "stats-cli.ts")
	dir := t.TempDir()
	l := New(Options{DataDir: dir})
	l.Write("link-reader", 1, EventPlugin, exampleEvent, "")
	l.Write("link-reader", 1, EventPlugin, Event{Site: "bilibili", Route: "bili_view", URL: "https://www.example.test/video/BV1", Took: 100 * time.Millisecond, Timed: true, Result: OK}, "")
	l.Write("link-reader", 1, EventPlugin, Event{Site: "bilibili", Route: "send", Result: Skip, Note: "补录"}, "")
	l.Write("link-reader", 1, EventTool, Event{Took: 5 * time.Millisecond, Timed: true, Result: OK}, "read_link_detail")
	l.Write("link-reader", 1, EventLoad, Event{Result: OK, Note: "第 1 次启用"}, "")
	out := runBun(t, bun, cli, "--data", dir, "--json")
	var rep struct {
		Files        int `json:"files"`
		Lines        int `json:"lines"`
		SkippedLines int `json:"skippedLines"`
		Groups       []struct {
			Plugin, Site, Route string
			Count               int `json:"count"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("汇总不是 JSON：%v\n%s", err, out)
	}
	if rep.Lines != 5 || rep.SkippedLines != 0 || rep.Files != 1 {
		t.Fatalf("汇总不对：%s", out)
	}
	seen := map[string]bool{}
	for _, g := range rep.Groups {
		seen[g.Site+"/"+g.Route] = true
	}
	for _, k := range []string{"bilibili/bili_view", "bilibili/send", "host/tool:read_link_detail", "host/load"} {
		if !seen[k] {
			t.Errorf("汇总里没有 %s：%s", k, out)
		}
	}
}
