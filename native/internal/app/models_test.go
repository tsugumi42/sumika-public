package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
)

func modelTestServer(t *testing.T, e *testEnv, hold <-chan struct{}, entered chan<- struct{}, catalog *atomic.Int32) *httptest.Server {
	t.Helper()
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","owned_by":"one","reasoning":true,"context_length":32768,"modalities":{"output":["text"]}}`), json.RawMessage(`{"id":"fake/new","display_name":"编的新版","magpie_label":"标签","owned_by":"two","context_length":512000,"reasoning_levels":["low","medium","high"],"modalities":{"input":["text","image"],"output":["text"]}}`)}
	script := llmtest.Script{Models: models, Reply: llmtest.Text("编的回复")}
	if hold != nil {
		script.Replies = []llmtest.Reply{llmtest.Call("fixture-plan", "todo_write", `{"todos":[{"content":"编造的任务","status":"pending"}]}`)}
	}
	handler := llmtest.Handler(script, &e.bodies, &e.mu)
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			catalog.Add(1)
			if catalog.Load() < 0 {
				json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
				return
			}
		}
		if r.Method == http.MethodPost && hold != nil {
			once.Do(func() { entered <- struct{}{}; <-hold })
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}
func modelSave(t *testing.T, r *DesktopReader, v api.ModelSettingsView, ch config.ModelChange) api.ModelSettingsView {
	t.Helper()
	next, err := r.SetModelSettings(context.Background(), ch, v.Version)
	if err != nil {
		t.Fatalf("保存 %s：%v", ch.Field, err)
	}
	return next
}

// 模型配置测试直接调用 Hub，由夹具宿主明确四场合身份，不让正文补发身份。
func modelSceneInput(conv, channel, scene, text string) turn.Input {
	platform := "local"
	if channel == "qq" {
		platform = "qq"
	}
	return turn.Input{Identity: kernel.Invocation{Scope: kernel.Scope{CharacterID: testChar, Conversation: conv, ScopeID: "fixture", Platform: platform, Channel: channel, AccountID: testAccount, Scene: scene, ActorKind: "owner", ActorID: testOwner}, Kind: kernel.KindForeground}, CharacterID: testChar, Conversation: conv, Text: text, Audience: audience.Audience{Channel: channel, Conversation: conv, Scene: scene}, Scope: "fixture"}
}
func TestModelSettingsValidationAndCatalog(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var gets atomic.Int32
	e.llm = modelTestServer(t, e, nil, nil, &gets)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 2 || !v.ModelsEditable || !v.RunningKnown {
		t.Fatalf("列表请求=%d editable=%v", gets.Load(), v.ModelsEditable)
	}
	if len(v.Groups) != 2 || v.Groups[1].Models[0].Name != "编的新版" || v.Groups[1].Models[0].Label != "标签" || v.Groups[1].Models[0].Subtitle != "能看图 · 512K 上下文 · 思考：低/中/高" {
		t.Fatalf("模型能力：%+v", v.Groups)
	}
	if v.Models[0].VisionReported || !strings.Contains(v.Models[0].Subtitle, "看图：未报告") {
		t.Fatal(v.Models[0])
	}
	file := filepath.Join(e.data, "config", "sumika.yml")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []config.ModelChange{
		{Field: config.CharacterModel, Character: testChar, String: new("not/listed")},
		{Field: config.CharacterModel, Character: testChar, String: new("")},
		{Field: config.CharacterModel, Character: "../other", String: new(testModel)},
		{Field: config.ContextSoftRatio, Float: new(0.99)},
		{Field: config.ThinkingPrivate, String: new("unknown")},
		{Field: "memory.write.characters", String: new(testChar)},
		{Field: config.ContextWindow, String: new("1000")},
	} {
		if _, err := r.SetModelSettings(context.Background(), ch, v.Version); err == nil {
			t.Fatalf("接受了 %+v", ch)
		}
		b, err := os.ReadFile(file)
		if err != nil || string(b) != string(before) {
			t.Fatalf("拒绝后文件变化 %v", err)
		}
	}
	v = modelSave(t, r, v, config.ModelChange{Field: config.CharacterModel, Character: testChar, String: new("fake/new")})
	if v.Running.Characters[testChar].Model != "fake/new" || gets.Load() != 2 {
		t.Fatal("未发布或保存重复联网")
	}
	gets.Store(-10)
	v, err = r.RefreshModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Characters[0].ModelMissing || v.Configured.Characters[testChar].Model != "fake/new" {
		t.Fatalf("列表消失未标记：%+v", v.Characters)
	}
	// 文件版本不是秒级时间；外部同秒编辑也必须冲突并重读。
	if err := os.WriteFile(file, append(before, []byte("# external\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := r.SetModelSettings(context.Background(), config.ModelChange{Field: config.ThinkingLocal, String: new("high")}, v.Version)
	if !errors.Is(err, api.ErrModelSettingsVersion) || next.Version == v.Version {
		t.Fatalf("外部修改 %v %+v", err, next)
	}
}
func TestModelSettingsRuntimeSnapshot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var gets atomic.Int32
	e.llm = modelTestServer(t, e, release, entered, &gets)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := a.hub.Send(testChar, "旧轮")
	<-entered
	v = modelSave(t, r, v, config.ModelChange{Field: config.CharacterModel, Character: testChar, String: new("fake/new")})
	v = modelSave(t, r, v, config.ModelChange{Field: config.ThinkingLocal, String: new("high")})
	v = modelSave(t, r, v, config.ModelChange{Field: config.ContextWindow, Int: new(20000)})
	v = modelSave(t, r, v, config.ModelChange{Field: config.ContextReserve, Int: new(2000)})
	active, err := a.modelSettingsView(context.Background())
	if err != nil || len(active.Active) != 1 || active.Active[0].Model != testModel || active.Active[0].Version == v.RunningVersion {
		t.Fatalf("在途版本：%+v %v", active.Active, err)
	}
	releaseOnce.Do(func() { close(release) })
	old := <-first
	if old.Err != nil {
		t.Fatal(old.Err)
	}
	fresh := <-a.hub.Send(testChar, "新轮")
	if fresh.Err != nil {
		t.Fatal(fresh.Err)
	}
	if old.Result.Built.Model.ID != testModel || old.Result.Built.Effort != "medium" || old.Result.Built.Window != 32768 || old.Result.Built.Hard != 16384 {
		t.Fatalf("旧轮被改 %+v", old.Result.Built)
	}
	if fresh.Result.Built.Model.ID != "fake/new" || fresh.Result.Built.Effort != "high" || fresh.Result.Built.Window != 20000 || fresh.Result.Built.Hard != 17000 {
		t.Fatalf("新轮未生效 %+v", fresh.Result.Built)
	}
	e.mu.Lock()
	bodies := append([]string(nil), e.bodies...)
	e.mu.Unlock()
	for i, want := range []string{testModel, testModel, "fake/new"} {
		var body struct {
			Model  string `json:"model"`
			Effort string `json:"reasoning_effort"`
		}
		if err := json.Unmarshal([]byte(bodies[i]), &body); err != nil {
			t.Fatal(err)
		}
		if body.Model != want || body.Effort != []string{"medium", "medium", "high"}[i] {
			t.Fatalf("请求 %d %+v", i, body)
		}
	}
	if !strings.Contains(bodies[2], "旧轮") {
		t.Fatal("换模型后历史丢失")
	}
}
func TestModelSettingsThinkingScenes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var gets atomic.Int32
	e.llm = modelTestServer(t, e, nil, nil, &gets)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v = modelSave(t, r, v, config.ModelChange{Field: config.ThinkingLocal, String: new("high")})
	v = modelSave(t, r, v, config.ModelChange{Field: config.ThinkingPrivate, String: new("medium")})
	modelSave(t, r, v, config.ModelChange{Field: config.ThinkingGroup, String: new("low")})
	for _, tt := range []struct{ conv, channel, scene, effort string }{{"console:" + testChar, "console", "private", "high"}, {"console:group:fiction", "console", "group", "high"}, {"qq:private:12345:23456", "qq", "private", "medium"}, {"qq:group:34567", "qq", "group", "low"}} {
		in := modelSceneInput(tt.conv, tt.channel, tt.scene, "编的输入")
		out := <-a.hub.Respond(in)
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		if out.Result.Built.Effort != tt.effort {
			t.Fatalf("%s：%s", tt.conv, out.Result.Built.Effort)
		}
	}
}
func TestModelSettingsArchive(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "model-settings")
	dir := filepath.Join(opt.DataDir, "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sumika.yml"), []byte("magpie:\n  baseUrl: http://127.0.0.1:1\ncharacters: {}\nqq:\n  memoryAll: [91001]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gets.Add(1); t.Error("archive 联网") }))
	defer srv.Close()
	// 配置地址无效，仍应只读本地完成；LLM 注入也不能被调用。
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.ModelsEditable || v.RunningKnown || len(v.Models) != 0 || v.ModelsDisabledReason == "" {
		t.Fatalf("archive %+v", v)
	}
	v = modelSave(t, r, v, config.ModelChange{Field: config.ThinkingPrivate, String: new("high")})
	if v.Configured.Private != "high" || v.RunningKnown {
		t.Fatal("archive 思考没有保存")
	}
	// 旧文件里的未知键不重新变成可用开关，也不出现在设置读写后的投影。
	for _, setting := range v.ReadOnly {
		if setting.Key == "qq.memoryAll" {
			t.Fatalf("已退役白名单重新出现在设置：%+v", setting)
		}
	}
	if _, err := r.SetModelSettings(context.Background(), config.ModelChange{Field: config.BackgroundModel, String: new("")}, v.Version); !errors.Is(err, api.ErrModelsUnavailable) {
		t.Fatal(err)
	}
	if gets.Load() != 0 {
		t.Fatal(gets.Load())
	}
}
func TestModelSettingsConcurrentSavesAndTurns(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var gets atomic.Int32
	e.llm = modelTestServer(t, e, nil, nil, &gets)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	start := make(chan struct{})
	for _, effort := range []string{"high", "low"} {
		wg.Add(1)
		go func(level string) {
			defer wg.Done()
			<-start
			_, err := r.SetModelSettings(context.Background(), config.ModelChange{Field: config.ThinkingLocal, String: &level}, v.Version)
			results <- err
		}(effort)
	}
	for _, conv := range []string{"qq:group:34567", "qq:group:45678"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out := <-a.hub.Respond(modelSceneInput(conv, "qq", "group", "并发输入"))
			results <- out.Err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	conflicts := 0
	for err := range results {
		if errors.Is(err, api.ErrModelSettingsVersion) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("冲突数=%d", conflicts)
	}
}
