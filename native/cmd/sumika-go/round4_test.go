package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backend "sumika-native/internal/app"
)

// 默认配置把数据放在另一目录时，第二次加载仍应使用原来的配置。
func TestRunDefaultConfigKeepsSource(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "sumika.example.yml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(e.data, "config", "sumika.yml"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "data", "config")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sumika.yml"), append([]byte("dataDir: '"+filepath.ToSlash(e.data)+"'\n"), cfg...), 0600); err != nil {
		t.Fatal(err)
	}
	other := newFakePeer(t)
	cfg = bytes.ReplaceAll(cfg, []byte(e.peer.url()), []byte(other.url()))
	if err := os.WriteFile(filepath.Join(e.data, "config", "sumika.yml"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-root", root, "-account", testAccount}, &stdout, &stderr, refuseLegacy)
	}()
	wrong := false
	select {
	case <-e.peer.accepted:
	case <-other.accepted:
		wrong = true
	case <-time.After(startLimit):
		cancel()
		<-done
		t.Fatalf("没有连接：%s %s", &stdout, &stderr)
	}
	cancel()
	code := <-done
	if wrong {
		t.Error("未给 -data 却连接了数据目录 B 的端点")
	}
	if code != 0 {
		t.Errorf("退出码 %d：%s %s", code, &stdout, &stderr)
	}
	if !strings.Contains(stdout.String(), backend.RedactLog("配置文件 "+filepath.Join(source, "sumika.yml"), nil)) {
		t.Errorf("启动日志没有显示配置 A：%s", &stdout)
	}
}

// 启动在建立装配日志之前失败，配置错误本身就要隐藏号码。
func TestRunConfigErrorsMaskIDs(t *testing.T) {
	for name, body := range map[string]string{
		"重复账号": "qq:\n  accounts:\n    - {id: 1234567, character: example}\n    - {id: 1234567, character: example}\n",
		"主人":   "qq:\n  owner: '1234567x'\n",
		"账号":   "qq:\n  accounts: [{id: '1234567x', character: example}]\n",
		"群":    "qq:\n  groups: ['1234567x']\n",
		"私聊":   "qq:\n  privateAllow: ['1234567x']\n",
		"超范围":  "qq:\n  owner: 123456700000000000000000000\n",
		"负数":   "qq:\n  owner: -1234567\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := os.WriteFile(filepath.Join(e.data, "config", "sumika.yml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"-account", "1234567", "-data", e.data}, &stdout, &stderr, refuseLegacy)
			if code != exitError {
				t.Errorf("错误配置退出码 %d", code)
			}
			if strings.Contains(stderr.String(), "1234567") {
				t.Errorf("配置错误泄露号码：%s", &stderr)
			}
		})
	}
}
