package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/embed"
	"sumika-native/internal/embed/embedtest"
)

// 本包的测试进程也能当假的 sumika-models.exe 起来（embedtest）
func TestMain(m *testing.M) {
	if embedtest.Wanted() {
		embedtest.Run()
		return
	}
	os.Exit(m.Run())
}

// writeConfig 在临时数据目录里写 config/sumika.yml（embed 一节由调用方给），返回数据目录
func writeConfig(t *testing.T, embedYAML string) string {
	t.Helper()
	t.Setenv("SUMIKA_CONFIG", "")
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "config", "sumika.yml"), []byte("embed:\n"+embedYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// GM7 验收 3c：embed.backend 是 go-models、不带 -embed-url 时，memory-eval 起的是 sumika-models；
// 配置里同时写着已删的旧键 python / serverDir 也读得进来、不被采用（那两个路径不存在，真被采用的话起不来）；
// 条目和查询分别带 input_type=document / query；用的是 config 包的解析（embedLocalConfig），不再自己 yaml
func TestMemoryEvalUsesConfigEmbed(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	root := filepath.Join(tmp, "models")
	if err := embedtest.WriteModelFiles(embed.ModelFiles(root, "gte-multilingual-base")...); err != nil {
		t.Fatal(err)
	}
	reqLog, argsLog := filepath.Join(tmp, "requests.jsonl"), filepath.Join(tmp, "args.jsonl")
	t.Setenv(embedtest.EnvOn, "1")
	t.Setenv(embedtest.EnvEmbedModels, "gte-multilingual-base")
	t.Setenv(embedtest.EnvRequestLog, reqLog)
	t.Setenv(embedtest.EnvArgsFile, argsLog)
	data := writeConfig(t, fmt.Sprintf("  enabled: true\n  backend: go-models\n  model: gte-multilingual-base\n  modelsRoot: %q\n  modelsExe: %q\n  python: %q\n  serverDir: %q\n",
		root, exe, filepath.Join(tmp, "不存在的-python.exe"), filepath.Join(tmp, "不存在的-server")))

	cfg, err := embedLocalConfig(embedSetup{dataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != embed.BackendGoModels || cfg.Exe != exe || cfg.ModelsRoot != root || cfg.Model != "gte-multilingual-base" {
		t.Fatalf("go-models 的配置没读对：%+v", cfg)
	}

	ev, stop := setUpEmbedder(embedSetup{dataDir: data}, t.Logf)
	defer stop()
	if _, isLocal := ev.inner.(*embed.Local); !isLocal || ev.typed == nil {
		t.Fatalf("不带 -embed-url 该起本机子进程，而且是带 input_type 的那种：%T", ev.inner)
	}
	if _, err := ev.Embed(context.Background(), []string{"编的条目"}); err != nil { // Open 期间：条目
		t.Fatal(err)
	}
	ev.setQuery()
	if _, err := ev.Embed(context.Background(), []string{"编的查询"}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil || !strings.Contains(string(args), `"--models","gte-multilingual-base"`) || !strings.Contains(string(args), "--exit-on-stdin-eof") {
		t.Fatalf("起的该是 sumika-models（--models gte-multilingual-base）：%s %v", args, err)
	}
	var kinds []string
	b, _ := os.ReadFile(reqLog)
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec struct {
			Body map[string]json.RawMessage `json:"body"`
		}
		if json.Unmarshal([]byte(ln), &rec) == nil {
			kinds = append(kinds, string(rec.Body["input_type"]))
		}
	}
	// 预热一次（document）、条目一次（document）、查询一次（query）
	if strings.Join(kinds, ",") != `"document","document","query"` {
		t.Fatalf("input_type 该依次是 document（预热）、document（条目）、query：%v", kinds)
	}
}

// legacy 已删：配置读得进来、后端原样交给 Local 报原因；go-models 没有 modelsExe 又没给 -models-exe：Exe 是空，Local 起不来并说清缺 embed.modelsExe；
// 相对路径按数据目录解析；-models-exe、-ort-library、-models-root 盖过配置（取绝对路径）；没有配置文件是错误
func TestEmbedLocalConfigVariants(t *testing.T) {
	t.Run("legacy 配置读得进来、不拼路径", func(t *testing.T) {
		data := writeConfig(t, "  backend: legacy\n  model: m\n  python: py.exe\n  serverDir: sd\n  modelsRoot: D:/Models\n  modelsExe: D:/x/sumika-models.exe\n  ortLibrary: D:/x/onnxruntime.dll\n")
		cfg, err := embedLocalConfig(embedSetup{dataDir: data, modelsExe: "D:/y/覆盖.exe"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Backend != "legacy" || cfg.Exe != "" || cfg.ModelsRoot != "" || cfg.OrtLibrary != "" {
			t.Fatalf("legacy 下不该采用 go-models 的键（-models-exe 也不该盖进来）：%+v", cfg)
		}
		l := embed.NewLocal(cfg, nil)
		l.Start()
		if s, why := l.Status(); s != embed.StatusDown || !strings.Contains(why, "legacy 已移除") {
			t.Fatalf("该报 legacy 已移除：%s %q", s, why)
		}
	})
	t.Run("相对路径按数据目录解析", func(t *testing.T) {
		data := writeConfig(t, "  enabled: true\n  backend: go-models\n  model: gte-multilingual-base\n  modelsRoot: models\n  modelsExe: bin/sumika-models.exe\n  ortLibrary: bin/onnxruntime.dll\n")
		cfg, err := embedLocalConfig(embedSetup{dataDir: data})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ModelsRoot != filepath.Join(data, "models") || cfg.Exe != filepath.Join(data, "bin", "sumika-models.exe") || cfg.OrtLibrary != filepath.Join(data, "bin", "onnxruntime.dll") {
			t.Fatalf("相对路径该按数据目录解析：%+v", cfg)
		}
	})
	t.Run("没写 modelsExe 也没给 -models-exe", func(t *testing.T) {
		data := writeConfig(t, "  backend: go-models\n  model: gte-multilingual-base\n  modelsRoot: D:/Models\n")
		cfg, err := embedLocalConfig(embedSetup{dataDir: data})
		if err != nil || cfg.Exe != "" {
			t.Fatalf("评测没有资源根，没写就是空：%+v %v", cfg, err)
		}
		l := embed.NewLocal(cfg, nil)
		l.Start()
		if s, why := l.Status(); s != embed.StatusOff || !strings.Contains(why, "embed.modelsExe") {
			t.Fatalf("该说清缺 embed.modelsExe：%s %q", s, why)
		}
	})
	t.Run("命令行盖过配置", func(t *testing.T) {
		data := writeConfig(t, "  backend: go-models\n  model: gte-multilingual-base\n  modelsRoot: D:/Models\n  modelsExe: D:/x/a.exe\n  ortLibrary: D:/x/a.dll\n")
		// 用本机的绝对路径：写死 D:/y/... 在 Linux 上是相对路径，会被拼到当前目录下
		y := t.TempDir()
		exe, dll, models := filepath.Join(y, "b.exe"), filepath.Join(y, "b.dll"), filepath.Join(y, "models")
		cfg, err := embedLocalConfig(embedSetup{dataDir: data, modelsExe: exe, ortLibrary: dll, modelsRoot: models})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range [][2]string{{cfg.Exe, exe}, {cfg.OrtLibrary, dll}, {cfg.ModelsRoot, models}} {
			if !strings.EqualFold(filepath.ToSlash(p[0]), filepath.ToSlash(p[1])) || !filepath.IsAbs(p[0]) {
				t.Fatalf("命令行没盖过配置：%q 要 %q", p[0], p[1])
			}
		}
	})
	t.Run("没有配置文件", func(t *testing.T) {
		t.Setenv("SUMIKA_CONFIG", "")
		_, err := embedLocalConfig(embedSetup{dataDir: filepath.Join(t.TempDir(), "没有")})
		if err == nil || !strings.Contains(err.Error(), "config/sumika.yml") {
			t.Fatalf("没有配置该报错并说清找的是哪儿：%v", err)
		}
	})
	t.Run("配置里后端写错", func(t *testing.T) {
		data := writeConfig(t, "  backend: go_models\n")
		if _, err := embedLocalConfig(embedSetup{dataDir: data}); err == nil || !strings.Contains(err.Error(), "embed.backend") {
			t.Fatalf("config 包的检查该拦下：%v", err)
		}
	})
}
