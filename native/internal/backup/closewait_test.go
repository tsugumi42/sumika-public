package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Close 里停调度和等这份收尾共用一个期限：卡住的备份不看取消时，两次等待加起来只有一个 CloseWait，不是各等一个
func TestCloseAndStopShareOneDeadline(t *testing.T) {
	f := newFx(t, func(o *Options) {
		o.CloseWait = 300 * time.Millisecond
		o.Media = MediaNone
		o.StartDelay = time.Millisecond
	})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		once.Do(func() { close(started) })
		<-release // 不看 ctx
		return nil
	}
	var mu sync.Mutex
	var waits []time.Duration
	f.svc.onWait = func(d time.Duration) { mu.Lock(); waits = append(waits, d); mu.Unlock() }
	f.svc.Start()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("调度没有开始备份")
	}
	done := make(chan struct{})
	go func() { f.svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("Close 卡住了")
	}
	close(release)
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 2 {
		t.Fatalf("该有两次等：停调度、等收尾：%v", waits)
	}
	// 两段等待的“决定要等多久”加起来不超过一个 CloseWait 加宽松余量（不比墙钟：各等一个 CloseWait 时加起来是 2 倍）
	if sum := waits[0] + waits[1]; sum > 300*time.Millisecond+200*time.Millisecond {
		t.Fatalf("停调度和等收尾该共用一个期限：%v（合计 %v）", waits, sum)
	}
}

// 改名重试的上限按行为测：真的把 30 秒（至少）交给底层的改名函数，不是只断言一个常量
func TestRenameGetsALongRetryBudget(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	var got time.Duration
	f.svc.replace = func(from, to string, budget time.Duration) error {
		got = budget
		return os.Rename(from, to)
	}
	f.mustRun(ReasonManual)
	if got < 30*time.Second {
		t.Fatalf("改名的重试上限太短：%v", got)
	}
}

// 一份备份的总时限到点自己取消：测试自己带超时，等事件不等墙钟；总时限设得很大（1000 小时）也照常能做
func TestRunTimesOutWithOwnGuard(t *testing.T) {
	f := newFx(t, func(o *Options) { o.RunMax = 100 * time.Millisecond })
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	errc := make(chan error, 1)
	go func() { _, err := f.run(ReasonDaily); errc <- err }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("该因为超时失败")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("没有总时限：备份卡住了（测试自己的超时）")
	}
	st := f.svc.Status()
	if st.LastFailure == "" || st.FailedAt.IsZero() {
		t.Fatalf("超时该记成失败（自检读它）：%+v", st)
	}
	if st.Running || !st.RunningSince.IsZero() {
		t.Fatalf("超时以后不再是在做：%+v", st)
	}
	if got := dirEntries(t, f); len(got) != 0 {
		t.Fatalf("超时以后不该留下文件夹：%v", got)
	}
}

func TestRunWithHugeRunMaxStillWorks(t *testing.T) {
	f := newFx(t, func(o *Options) { o.RunMax = 1000 * time.Hour; o.Media = MediaNone })
	errc := make(chan error, 1)
	go func() { _, err := f.run(ReasonManual); errc <- err }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("总时限很大时备份不该卡住")
	}
}

// 本地备份逐字节复制 config/（含明文配置、.bak、.env、脚本、BOM、CRLF、二进制），恢复出来要和数据目录一模一样；
// 只有程序自己的 .sumika-config-* 临时文件不带（规格-数据目录与备份 10.4）
func TestRunConfigFilesAreByteIdentical(t *testing.T) {
	f := newFx(t)
	cfg := f.p.ConfigDir()
	files := map[string]string{
		"sumika.yml":                  "qq:\r\n  accounts:\r\n    - id: \"10001\"\r\n      token: 令牌样本乙\r\n",
		"mcp.json":                    "\xef\xbb\xbf{\"mcpServers\":{\"s\":{\"env\":{\"API_KEY\":\"示例密钥乙\"}}}}",
		"sumika.yml.bak":              "token: 旧令牌\n",
		".env":                        "API_KEY=示例密钥丙\n",
		"notes.txt":                   "随手记\n",
		"history/sumika-20261001.yml": "token: 历史里的令牌\n",
		"context/AGENTS.md":           "用 Bearer 示例令牌 访问\n",
		"skills/a/run.py":             "import os\nos.system('curl -H \"Authorization: Bearer 示例令牌\" https://h')\r\n",
		"skills/a/icon.png":           string([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}),
		"agents/x.md":                 "子代理\n",
		"prompts/a.md":                "提示词\n",
		"secrets.bin":                 "密文",
	}
	for rel, content := range files {
		write(t, filepath.Join(cfg, filepath.FromSlash(rel)), content)
	}
	write(t, filepath.Join(cfg, ".sumika-config-123456"), "程序自己的临时文件")
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	if m.Status != StatusOK {
		t.Fatalf("%v", m.Problems)
	}
	for rel := range files {
		got, err := os.ReadFile(filepath.Join(res.Folder, "data", "config", filepath.FromSlash(rel)))
		if err != nil || string(got) != files[rel] {
			t.Errorf("config/%s 该逐字节一样：%v\n想要 %q\n实际 %q", rel, err, files[rel], got)
		}
		if _, ok := fileOf(m, "config/"+rel); !ok {
			t.Errorf("清单里该有 config/%s", rel)
		}
	}
	if _, ok := fileOf(m, "config/.sumika-config-123456"); ok || exists(filepath.Join(res.Folder, "data", "config", ".sumika-config-123456")) {
		t.Error("程序自己的临时文件不该带")
	}
}

// config/skills 这类目录整个是联接：属于要备份的内容，没法带，这份记 bad 并点名（不是悄悄 ok）
func TestRunConfigDirLinkIsProblem(t *testing.T) {
	f := newFx(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "SKILL.md"), "技能")
	makeLink(t, outside, filepath.Join(f.p.ConfigDir(), "skills"))
	res, err := f.run(ReasonManual)
	if err == nil || res.Status != StatusBad {
		t.Fatalf("%v %+v", err, res)
	}
	if !strings.Contains(strings.Join(res.Problems, "；"), "config/skills") {
		t.Fatalf("原因该点名 config/skills：%v", res.Problems)
	}
}

// isConfigTemp 的 underDir 那一半：只有 config/ 下的 .sumika-config-* 是程序自己的临时文件不带；
// 别处（go/、plugins/）碰巧叫这个名字的文件是数据，照常带
func TestConfigTempNameOnlySkippedUnderConfig(t *testing.T) {
	f := newFx(t)
	write(t, filepath.Join(f.p.ConfigDir(), ".sumika-config-1"), "临时")
	write(t, filepath.Join(f.p.ConfigDir(), "history", ".sumika-config-2"), "history 里的也是临时")
	write(t, filepath.Join(f.data, "go", ".sumika-config-3"), "go 里的是数据")
	write(t, filepath.Join(f.data, "plugins", "x", ".sumika-config-4"), "插件里的是数据")
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	for _, rel := range []string{"config/.sumika-config-1", "config/history/.sumika-config-2"} {
		if _, ok := fileOf(m, rel); ok {
			t.Errorf("%s 是程序自己的临时文件，不该带", rel)
		}
	}
	for _, rel := range []string{"go/.sumika-config-3", "plugins/x/.sumika-config-4"} {
		if _, ok := fileOf(m, rel); !ok {
			t.Errorf("%s 不在 config/ 下，是数据，该带", rel)
		}
	}
}
