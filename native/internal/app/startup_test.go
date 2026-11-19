package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/datalock"
	"sumika-native/internal/llm"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// archiveFixture 用真实的 Go 迁移建库，不配账号、令牌或网关。
func archiveFixture(t *testing.T, name string) Options {
	t.Helper()
	// 两个环境变量已在 TestMain 里清空
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), name)
	p, err := paths.Init(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	local, _ := set.For("console")
	if _, err := local.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:invented", ActorKind: "owner", ActorID: "owner", Kind: "message", Text: "临时历史", CreatedAt: 1234}, nil); err != nil {
		t.Fatal(err)
	}
	return Options{Root: root, DataDir: data, Logf: t.Logf, DialContext: refuseLegacy}
}

func TestArchiveWithoutAccountOrGateway(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "历史")
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.opt.Mode != Archive || a.hub != nil || a.adapter != nil || a.llm != nil || a.alerts != nil || len(a.memories) != 0 || len(a.tokens) != 0 {
		t.Fatal("archive 创建了接收组件或读取了令牌")
	}
	for _, member := range a.set.Each() {
		var count int
		if err := member.Store.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = 'console:invented' AND text = '临时历史'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if member.Platform == "local" {
			want = 1
		}
		if count != want {
			t.Fatalf("%s 历史条数 %d，预期 %d", member.Platform, count, want)
		}
		if _, err := member.Store.Events.Exec(`DELETE FROM events`); err == nil {
			t.Fatal("archive 写入了业务库")
		}
	}
	if _, err := a.set.Trace().Exec(`CREATE TABLE forbidden (id INTEGER)`); err == nil {
		t.Fatal("archive 写入了 trace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("archive 退出后不能重开：%v", err)
	}
	b.Close()
}

func TestReceiveRequiresAccountBeforeStores(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	opt := e.options()
	opt.Account = ""
	a, err := Open(context.Background(), opt)
	if a != nil {
		a.Close()
	}
	if !errors.Is(err, ErrArguments) {
		t.Fatalf("未给账号必须是参数错误：%v", err)
	}
	assertNoStores(t, opt.DataDir)
}

func assertNoStores(t *testing.T, data string) {
	t.Helper()
	p := paths.Paths{Root: data}
	for _, file := range []string{p.ChatDB("local"), p.ChatDB("qq"), p.GoTraceDB()} {
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("阻断前创建了业务库 %s：%v", file, err)
		}
	}
}

func TestArchiveMissingStoreIsNotEmptyHistory(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "missing")
	file := (paths.Paths{Root: opt.DataDir}).ChatDB("qq")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), opt)
	if a != nil {
		a.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "archive 不建库") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺库未报明确错误：%v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺库被自动创建：%v", err)
	}
}

func TestArchiveUnreadableConfigStopsOpen(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "config")
	file := (paths.Paths{Root: opt.DataDir}).ConfigFile()
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), opt)
	if a != nil {
		a.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "读配置") {
		t.Fatalf("不可读配置被当成默认值：%v", err)
	}
}

func TestDataLockRefusesBeforeStores(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			lock, err := datalock.LockData(e.data)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			opt := e.options()
			opt.Mode = mode
			a, err := Open(context.Background(), opt)
			if a != nil {
				a.Close()
			}
			if !errors.Is(err, datalock.ErrDataInUse) {
				t.Fatalf("数据锁冲突未阻断：%v", err)
			}
			assertNoStores(t, e.data)
		})
	}
}

func TestArchiveStartsFromDifferentCWD(t *testing.T) {
	opt := archiveFixture(t, "中文 空格 # % &")
	t.Chdir(t.TempDir())
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.root != opt.Root || a.paths.Root != opt.DataDir {
		t.Fatalf("启动目录改变了资源或数据来源：%s %s", a.root, a.paths.Root)
	}
	st, _ := a.set.For("console")
	var text string
	if err := st.Events.QueryRow(`SELECT text FROM events WHERE conversation='console:invented'`).Scan(&text); err != nil || text != "临时历史" {
		t.Fatalf("特殊路径历史读取失败：%q %v", text, err)
	}
}

func TestArchiveQuestionMarkPath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 文件名禁止字面 ?，不能构造该数据目录；不以 %3F 冒充")
	}
	opt := archiveFixture(t, "中文 空格 # % ? &")
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
}

func TestOpenCancelReleasesDataLock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		a, err := Open(ctx, opt)
		if a != nil {
			a.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("没有进入 Open 的模型初始化")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消 Open 未返回取消错误：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消 Open 未结束")
	}
	b, err := Open(context.Background(), e.options())
	if err != nil {
		t.Fatalf("取消 Open 漏放数据锁：%v", err)
	}
	b.Close()
}
