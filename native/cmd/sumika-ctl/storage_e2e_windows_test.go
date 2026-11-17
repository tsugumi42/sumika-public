package main

// stats storage 端到端（规格-运行页 第十二节第 2 步）：命令行 → 带测试名的真命名管道 → internal/ctlserver 的校验 →
// 真的 storagestat.Service 量临时数据目录，再走回来变成退出码和输出。目录只在 t.TempDir() 里，不碰真实 data/。

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/paths"
	"sumika-native/internal/storagestat"
)

type storageBackend struct {
	stubBackend
	svc *storagestat.Service
}

func (b *storageBackend) Storage(ctx context.Context) (ctlproto.StorageReport, error) {
	return b.svc.Storage(ctx)
}

func TestStatsStorageEndToEnd(t *testing.T) {
	p, err := paths.Init(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel string, n int) {
		f := filepath.Join(p.Root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go/media/qq/ab/h", 2048)
	write("logs/desk-1.log", 100)
	write("dev/x", 500)
	svc := storagestat.New(p, storagestat.Options{})
	t.Cleanup(func() { svc.Close() })

	name := testPipeName("storage")
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	srv := ctlserver.New(&storageBackend{stubBackend: stubBackend{quit: make(chan struct{})}, svc: svc}, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb, dial)
		return code, out.String(), errb.String()
	}

	// 没统计过：--json 可解析，大目录没数、小目录有数，命令行不触发统计
	code, out, errs := do("--json", "stats", "storage")
	var r ctlproto.Reply
	var rep ctlproto.StorageReport
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil || !r.OK || json.Unmarshal(r.Data, &rep) != nil {
		t.Fatalf("退出码 %d，输出 %q，错误 %q", code, out, errs)
	}
	if !rep.NeverScanned || rep.Scan.Running {
		t.Errorf("命令行不该触发统计：%+v", rep)
	}
	byKey := map[string]ctlproto.StorageCategory{}
	for _, c := range rep.Categories {
		byKey[c.Key] = c
	}
	if c := byKey[ctlproto.StoreLogs]; c.Bytes != 100 || c.Source != ctlproto.SourceLive {
		t.Errorf("日志要现量：%+v", c)
	}
	if c := byKey[ctlproto.StoreDev]; c.Source != ctlproto.SourceNone || c.Bytes != 0 {
		t.Errorf("开发数据没统计过不能写 0 当数：%+v", c)
	}
	code, out, _ = do("stats", "storage")
	if strings.Contains(out, "\r") {
		t.Errorf("输出里不该有回车符：%q", out)
	}
	for _, want := range []string{"还没有统计过", "日志  100 字节", "开发数据  还没有统计", "清理：没有自动清理"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("文字输出缺 %q：\n%s", want, out)
		}
	}

	// 界面那边统计过以后：数字出来，带"约"
	if err := svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, out, _ = do("stats", "storage")
	for _, want := range []string{"上次统计", "媒体  约 2.0 KB  1 个文件", "开发数据  约 500 字节", "qq  2.0 KB  1 个文件"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("统计后缺 %q：\n%s", want, out)
		}
	}

	// 形状：存储没有范围，带范围或分组选项、多余参数都被拒
	for _, bad := range [][]string{
		{"stats", "storage", "extra"},
		{"stats", "storage", "--days", "7"},
		{"stats", "storage", "--all"},
		{"stats", "storage", "--by", "task"},
		{"stats", "storage", "--full"},
	} {
		if code, _, errs := do(bad...); code != 3 {
			t.Errorf("%v 应被拒（退出码 3），得到 %d，错误 %q", bad, code, errs)
		}
	}
}
