package desktopread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

func TestSettingsWhitelistConfiguredAndEffective(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "settings.yml")
	token := strings.Join([]string{"fixture", "only", "credential"}, "-")
	account := "90001"
	group := "90002"
	yaml := fmt.Sprintf(`magpie:
  baseUrl: https://user:%s@example.invalid/private/%s?token=%s
characters:
  a:
    model: configured-model
agent:
  context:
    reserve: 9000
memory:
  topK: 12
expression:
  learnGroups: false
vision:
  concurrency: 3
tools:
  approvalMode: auto
qq:
  accounts:
    - id: %q
      character: a
      token: %q
      url: wss://example.invalid?token=%s
  groups: [%q]
grants: %q
unknown: %q
`, token, token, token, account, token, token, group, token, token)
	if e := os.WriteFile(file, []byte(yaml), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SUMIKA_CONFIG", file)
	reader := SettingsReader{Load: func(context.Context) (config.Config, error) { return config.LoadIn(root, root) }, Running: map[string]any{"characters.a.model": "running-model", "agent.context.reserve": 8000, "appearance.theme": "dark", "data.mode": "archive", "platform.selectedAccount": account, "magpie.baseUrl": "https://example.invalid/?key=" + token, "unapproved.secret": token}}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	result, e := reader.ReadSettings(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	items := map[string]api.Result[api.Setting]{}
	for _, item := range result.Value.Items.Items {
		items[item.Value.Key] = item
	}
	model := items["characters.a.model"].Value
	if model.Value != "configured-model" || model.EffectiveValue != "running-model" || !model.Configured || !model.Effective || !model.Supported || model.Source != file {
		t.Fatalf("模型来源与生效值: %+v", model)
	}
	reserve := items["agent.context.reserve"].Value
	if reserve.Value != 9000 || reserve.EffectiveValue != 8000 {
		t.Fatalf("配置与运行值混合: %+v", reserve)
	}
	unset := items["characters.a.topic"].Value
	if unset.Configured || unset.Effective || unset.Source != "default" {
		t.Fatalf("未配置项伪装生效: %+v", unset)
	}
	unsupported := items["memory.topK"]
	if unsupported.State != api.Unsupported || unsupported.Value.Value != 12 || !unsupported.Value.Configured || unsupported.Value.Effective || unsupported.Value.Supported || unsupported.Err == nil || unsupported.Err.Error() != "配置存在，Go 尚未启用" {
		t.Fatalf("未实现服务状态: %+v", unsupported)
	}
	if theme := items["appearance.theme"].Value; theme.Value != "dark" || !theme.Effective || !theme.Supported || theme.Source != "runtime" {
		t.Fatalf("实际外观来源: %+v", theme)
	}
	for _, key := range []string{"occupation", "tools.uiApproval", "notifications.alerts", "shortcuts.registered"} {
		if items[key].State != api.Unsupported {
			t.Fatalf("编造已启用: %s", key)
		}
	}
	encoded, e := json.Marshal(result)
	if e != nil {
		t.Fatal(e)
	}
	all := string(encoded) + logs.String() + fmt.Sprint(result)
	for _, secret := range []string{token, account, group, "grants", "unapproved.secret", "wss://", "private/made-up"} {
		if strings.Contains(all, secret) {
			t.Fatalf("设置或日志泄露编造秘密类型 %q", strings.Split(secret, "-")[0])
		}
	}
	if got := items["magpie.baseUrl"].Value; got.Value != "https://example.invalid" || got.EffectiveValue != "https://example.invalid" {
		t.Fatalf("网关安全来源: %+v", got)
	}
	t.Log("编造令牌、账号、群号与未授权键扫描：泄漏 0 个")
}

func TestSettingsFailureDoesNotExposeLoaderError(t *testing.T) {
	t.Parallel()
	reader := SettingsReader{Load: func(context.Context) (config.Config, error) {
		return config.Config{}, errors.New("made-up-secret-token 90001")
	}}
	v, e := reader.ReadSettings(context.Background())
	if e == nil || v.State != api.Failed || strings.Contains(fmt.Sprint(v, e), "made-up-secret-token") || strings.Contains(fmt.Sprint(v, e), "90001") {
		t.Fatal("读取失败泄露原始错误或伪装成功")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	reader.Load = func(context.Context) (config.Config, error) { called = true; return config.Defaults(), nil }
	if _, e = reader.ReadSettings(ctx); !errors.Is(e, context.Canceled) || called {
		t.Fatal("取消后仍读取配置")
	}
}

// 记忆重排和嵌入后端三个键（W1a）：Go 读，所以显示为已启用；写在配置里的值读得出来，没写的是默认值、来源是 default
func TestSettingsRerankAndBackendKeys(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "settings.yml")
	yaml := `memory:
  rerank:
    model: 编的重排模型
    k: 20
`
	if e := os.WriteFile(file, []byte(yaml), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SUMIKA_CONFIG", file)
	reader := SettingsReader{Load: func(context.Context) (config.Config, error) { return config.LoadIn(root, root) }}
	result, e := reader.ReadSettings(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	items := map[string]api.Result[api.Setting]{}
	for _, item := range result.Value.Items.Items {
		items[item.Value.Key] = item
	}
	for key, want := range map[string]any{"memory.rerank.model": "编的重排模型", "memory.rerank.k": 20, "memory.rerank.timeoutSeconds": 5.0, "embed.backend": "go-models"} {
		it, ok := items[key]
		if !ok || it.State != api.Ready || !it.Value.Supported || it.Value.Value != want {
			t.Fatalf("%s：%+v（要已启用、值 %v）", key, it, want)
		}
	}
	if it := items["memory.rerank.k"].Value; !it.Configured || it.Source != file {
		t.Fatalf("写了的键要标出处：%+v", it)
	}
	if it := items["memory.rerank.timeoutSeconds"].Value; it.Configured || it.Source != "default" {
		t.Fatalf("没写的键是默认值：%+v", it)
	}
}

// 设置页的键表里有 memory.write 的 8 个键：值取对字段、配置过的标"已配置"、Go 已经读它们所以不是"尚未启用"（W5 验收 3）。
// 没配的键给的是默认值（最保守的），不能是别的字段的值
func TestSettingsListsMemoryWriteKeys(t *testing.T) {
	read := func(t *testing.T, yaml string) map[string]api.Result[api.Setting] {
		t.Helper()
		root := t.TempDir()
		file := filepath.Join(root, "settings.yml")
		if err := os.WriteFile(file, []byte(yaml), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SUMIKA_CONFIG", file)
		reader := SettingsReader{Load: func(context.Context) (config.Config, error) { return config.LoadIn(root, root) }}
		result, err := reader.ReadSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items := map[string]api.Result[api.Setting]{}
		for _, item := range result.Value.Items.Items {
			items[item.Value.Key] = item
		}
		return items
	}
	keys := []string{"memory.write.characters", "memory.write.summarize", "memory.write.places", "memory.write.catchUp",
		"memory.write.autoConfirm", "memory.write.confirmOccasions", "memory.write.confirmKinds", "memory.write.quarantineSince"}
	t.Run("配了的：值对得上，已配置，不是尚未启用", func(t *testing.T) {
		items := read(t, `memory:
  write:
    characters: [a]
    summarize: [a]
    places: [owner-private, local-private]
    catchUp: latest
    autoConfirm: true
    confirmOccasions: 4
    confirmKinds: [fact]
    quarantineSince: "1762041600000"
`)
		want := map[string]any{
			"memory.write.characters": []string{"a"}, "memory.write.summarize": []string{"a"}, "memory.write.places": []string{"owner-private", "local-private"},
			"memory.write.catchUp": "latest", "memory.write.autoConfirm": true, "memory.write.confirmOccasions": 4,
			"memory.write.confirmKinds": []string{"fact"}, "memory.write.quarantineSince": "1762041600000",
		}
		for _, key := range keys {
			item, ok := items[key]
			if !ok {
				t.Fatalf("键表里没有 %s", key)
			}
			if !reflect.DeepEqual(item.Value.Value, want[key]) {
				t.Errorf("%s 的值 %#v，应该是 %#v", key, item.Value.Value, want[key])
			}
			if !item.Value.Configured || !item.Value.Supported || item.State != api.Ready || item.Err != nil {
				t.Errorf("%s 配了就该是已配置、已支持、就绪：%+v 状态 %v 错误 %v", key, item.Value, item.State, item.Err)
			}
		}
	})
	t.Run("没配的：默认值，未配置", func(t *testing.T) {
		items := read(t, "memory:\n  topK: 12\n")
		for _, key := range keys {
			item, ok := items[key]
			if !ok {
				t.Fatalf("键表里没有 %s", key)
			}
			if item.Value.Configured || item.Value.Source != "default" {
				t.Errorf("%s 没配却标成已配置：%+v", key, item.Value)
			}
			if !item.Value.Supported || item.State != api.Ready {
				t.Errorf("%s 不该说尚未启用：%+v 状态 %v", key, item.Value, item.State)
			}
		}
		if v := items["memory.write.autoConfirm"].Value.Value; v != false {
			t.Errorf("autoConfirm 默认应该是关：%#v", v)
		}
		if v := items["memory.write.confirmOccasions"].Value.Value; v != 3 {
			t.Errorf("confirmOccasions 默认应该是 3：%#v", v)
		}
		if v := items["memory.write.characters"].Value.Value; !reflect.DeepEqual(v, []string{"全部角色"}) {
			t.Errorf("characters 默认的 [*] 该显示成“全部角色”：%#v", v)
		}
		if v, ok := items["memory.write.summarize"].Value.Value.([]string); !ok || len(v) != 0 {
			t.Errorf("summarize 默认该是空名单：%#v", items["memory.write.summarize"].Value.Value)
		}
	})
}
