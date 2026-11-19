package app

// 规格-模型配置 M2：配置里的模型引用（裸 ID 或 服务商/模型）在装配里按服务商分发；
// 只有一个服务商（magpie）时发给网关的 model 字段不带前缀，trace 里记规范引用。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/llm/llmtest"
)

// openWithModelLine 搭只有 magpie 的环境：先写配置，再把角色的 model 行改成 modelLine（配置 `characters.example.model` 的值），再启动
func openWithModelLine(t *testing.T, e *testEnv, extraConfig, modelLine string) (*App, *Desktop) {
	t.Helper()
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("好的")}, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure(extraConfig + "tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\n")
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	old := "    model: " + testModel + "\n"
	if !strings.Contains(string(b), old) {
		t.Fatalf("配置里没找到角色的 model 行：\n%s", b)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(b), old, "    model: "+modelLine+"\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	return a, d
}

func wireModels(e *testEnv) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, body := range e.bodies {
		var v struct {
			Model string `json:"model"`
		}
		if json.Unmarshal([]byte(body), &v) == nil {
			out = append(out, v.Model)
		}
	}
	return out
}

// 配置里写裸 ID 和写 magpie/ID 是同一个模型：目录里找得到，发给网关的 model 都是不带前缀的 ID，trace 都记 magpie/ID
func TestConfiguredModelRefBareAndPrefixedSameBehavior(t *testing.T) {
	t.Parallel()
	for _, line := range []string{testModel, "magpie/" + testModel} {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a, d := openWithModelLine(t, e, "", line)
			if _, ok := a.currentModels().Find(line); !ok {
				t.Fatalf("目录里找不到 %q", line)
			}
			if _, ok := a.currentModels().Find("other/" + testModel); ok {
				t.Fatal("没声明的服务商前缀不该找到别家的模型")
			}
			res, err := d.Reader.SelftestChat(context.Background(), selftestReq("一句话"))
			if err != nil || res.Status != SelftestDone || res.Error != "" {
				t.Fatalf("自检回合：%+v %v", res, err)
			}
			if res.Model != "magpie/"+testModel {
				t.Errorf("trace 里的模型 = %q，想要 magpie/%s", res.Model, testModel)
			}
			if got := wireModels(e); len(got) == 0 || got[len(got)-1] != testModel {
				t.Errorf("发给网关的 model 该是不带前缀的 %q：%v", testModel, got)
			}
		})
	}
}

// 配置里把 magpie 停用：引用到它的请求明确报"已停用"，不发给任何网关
func TestDisabledProviderRefusesRequests(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := openWithModelLine(t, e, "providers:\n  magpie:\n    baseUrl: http://127.0.0.1:1/v1\n    enabled: false\n", testModel)
	res, err := d.Reader.SelftestChat(context.Background(), selftestReq("一句话"))
	text := res.Error
	if err != nil {
		text += err.Error()
	}
	if res.Status == SelftestDone || !strings.Contains(text, "已停用") {
		t.Fatalf("停用的服务商该报已停用：%+v %v", res, err)
	}
	if got := wireModels(e); len(got) != 0 {
		t.Fatalf("停用的服务商不该有请求发出去：%v", got)
	}
}
