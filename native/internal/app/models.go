package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/modelref"
	"sumika-native/internal/persona"
	"sumika-native/internal/turn"
)

// ModelRuntime 发布后不可变；模型页字段和当版模型能力一起原子替换。
type ModelRuntime struct {
	Values        config.ModelValues
	Models        []llm.Model
	Version       string
	ReadAt        time.Time
	CatalogError  string
	ApplyErrors   []string
	summaryErrors map[string]string // 每个角色未生效的原因；无关保存保留，不隐式重试
	turn          turn.Config
	ReviewModels  map[string]string // 审阅用途独立发布，不替换角色模型。
	Refs          modelref.Resolver // 引用解析器（服务商和 defaultProvider 来自配置，启动时定）
}

// Find 按引用在目录里找模型：裸引用归 defaultProvider，`服务商/模型` 认已声明的服务商。
// 找不到不拿别的顶替（规格-模型配置 3.2）
func (s *ModelRuntime) Find(ref string) (llm.Model, bool) {
	if s == nil {
		return llm.Model{}, false
	}
	want := s.Refs.Canonical(ref)
	for _, m := range s.Models {
		if m.Ref() == want {
			return m, true
		}
	}
	return llm.Model{}, false
}

func (a *App) currentModels() *ModelRuntime {
	return a.modelRuntime.Load()
}

// CatalogState 只读当前这份模型目录：来源地址、读到的时间、取不到的原因（空 = 目录是好的）。
// 不重新列模型、不写缓存：桌面宿主隔一会看一次，把“模型来源连不上”带给界面（规格-模型配置 6.1 第 7 条）。
func (a *App) CatalogState() (source string, readAt time.Time, catalogErr string) {
	s := a.currentModels()
	if s == nil {
		return "", time.Time{}, ""
	}
	return a.catalogSource(), s.ReadAt, s.CatalogError
}
func (a *App) initModelRuntime(models []llm.Model) error {
	readAt := time.Time{}
	if a.opt.Mode == Receive {
		readAt = a.now()
	}
	return a.initModelRuntimeCatalog(models, readAt, "")
}

// initModelRuntimeCatalog 同 initModelRuntime，把目录是几时读到的、取不到的原因一起发布（规格-模型配置 6.1）
func (a *App) initModelRuntimeCatalog(models []llm.Model, readAt time.Time, catalogErr string) error {
	version := ""
	if a.cfg.Source != "" {
		b, err := os.ReadFile(a.cfg.Source)
		if err != nil {
			return err
		}
		version = config.ContentVersion(b)
	}
	s := &ModelRuntime{Values: config.ModelsOf(a.cfg), Models: a.tagProvider(models), Version: version, ReadAt: readAt, CatalogError: catalogErr, Refs: a.cfg.ModelRefs()}
	s.ReviewModels = reviewRoutes(a.cfg.Agent.ReviewConfig)
	s.turn = s.turnConfig()
	s.turn.ModelVersion = version
	a.modelRuntime.Store(s)
	return nil
}
func (s *ModelRuntime) turnConfig() turn.Config {
	v := s.Values
	x := v.Context
	models := make(map[string]string, len(v.Characters))
	fallbacks := make(map[string][]string, len(v.Characters))
	for id, ch := range v.Characters {
		if ch.Model != "" {
			models[id] = ch.Model
		}
		fallbacks[id] = slices.Clone(ch.FallbackModels)
	}
	return turn.Config{Models: models, FallbackModels: fallbacks, DefaultFallbackModels: slices.Clone(v.FallbackModels), Thinking: string(v.Group), PrivateThinking: string(v.Private), LocalThinking: string(v.Local), Context: turn.ContextLimits{Window: x.Window, Reserve: x.Reserve, SoftRatio: x.SoftRatio, HardRatio: x.HardRatio, KeepTokens: x.KeepTokens, NoAutoCompact: !x.AutoCompactChat}}
}

var _ api.ModelSettingsWriter = (*DesktopReader)(nil)

func (r *DesktopReader) enterModels(ctx context.Context) (context.Context, func(), error) {
	d := r.desk
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil, api.ErrModelSettingsClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	d.chatWork.Add(1)
	run, cancel := context.WithCancel(d.chatCtx)
	stop := context.AfterFunc(ctx, cancel)
	return run, func() { stop(); cancel(); d.chatWork.Done() }, nil
}
func (r *DesktopReader) ModelSettings(ctx context.Context) (api.ModelSettingsView, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	defer done()
	a := r.desk.app
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	// 打开页时拉一次；普通保存返回视图不重复拉列表。
	if a.opt.Mode == Receive {
		a.refreshModels(ctx)
	}
	return a.modelSettingsView(ctx)
}

// RefreshModels 是模型页的"刷新"：重新读模型列表，也立刻联网刷新一次 models.dev 的资料（不受刷新间隔和退避限制；
// 联网放在锁外，取不到只记在资料库状态里，不拦模型列表）
func (r *DesktopReader) RefreshModels(ctx context.Context) (api.ModelSettingsView, error) {
	if a := r.desk.app; a.modelDev != nil && a.cfg.ModelMeta.ModelsDev.Enabled && a.opt.Mode == Receive && a.opt.LLM == nil {
		if err := a.modelDev.Refresh(ctx, true); err != nil {
			a.logf("模型资料库（models.dev）手动刷新没成功：%v", err)
		}
	}
	return r.ModelSettings(ctx)
}

// refreshModels 重新列一次模型并发布（成功写缓存）；返回列模型的结果，失败只记在 CatalogError 里、旧目录留着
func (a *App) refreshModels(ctx context.Context) error {
	models, readAt, _, err := a.loadModelCatalog(ctx)
	if err != nil && ctx.Err() != nil {
		return err // 调用方取消/到时限（关机、关页面）：目录状态不动，不记成来源坏了
	}
	old := a.currentModels()
	next := *old
	if err != nil {
		next.CatalogError = err.Error()
	} else {
		next.Models = a.resolveModels(models, old.Values.Overrides)
		next.ReadAt = readAt
		next.CatalogError = ""
	}
	a.modelRuntime.Store(&next)
	if err != nil {
		a.startCatalogRetry() // 手动刷新没取到：后台接着按退避重试，来源回来不用重启
	}
	return err
}
func (r *DesktopReader) SetModelSettings(ctx context.Context, ch api.ModelSettingsChange, base string) (api.ModelSettingsView, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	defer done()
	a := r.desk.app
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	_, _, model, err := ch.Target()
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	if ch.Character != "" {
		ids, e := a.listCharacterIDs()
		if e != nil {
			return api.ModelSettingsView{}, e
		}
		if !slices.Contains(ids, ch.Character) {
			return api.ModelSettingsView{}, errors.New("角色 ID 不存在")
		}
	}
	old := a.currentModels()
	if model {
		if a.opt.Mode != Receive || len(old.Models) == 0 {
			return api.ModelSettingsView{}, api.ErrModelsUnavailable
		}
		value := ch.StringValue()
		_, listedModel := old.Find(value)
		found := value == "" || listedModel
		if !found {
			return api.ModelSettingsView{}, errors.New("模型不在当前列表里")
		}
	}
	// 重新定位遵循 SUMIKA_CONFIG；Source 为空时不创建文件。
	before, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	candidate, err := config.SaveModel(ctx, before.Source, filepath.Join(a.paths.Root, "config", "history"), base, ch, a.now())
	if err != nil {
		if errors.Is(err, config.ErrConfigVersion) {
			v, e := a.modelSettingsView(ctx)
			return v, errors.Join(err, e)
		}
		return api.ModelSettingsView{}, err
	}
	b, err := os.ReadFile(candidate.Source)
	if err != nil {
		return api.ModelSettingsView{}, &api.SavedModelSettingsError{Reasons: []string{err.Error()}}
	}
	next := *old
	next.Values = config.ModelsOf(candidate)
	// 改了模型窗口或单次输出的覆盖：用新的覆盖重算列表（Resolve 可对同一列表反复算），下一轮用
	next.Models = a.resolveModels(old.Models, next.Values.Overrides)
	next.Version = config.ContentVersion(b)
	next.turn = next.turnConfig()
	next.turn.ModelVersion = next.Version
	if a.opt.Mode == Receive {
		var changed bool
		next.summaryErrors, changed = a.transitionSummaries(ctx, old, &next)
		if changed {
			next.ApplyErrors = modelApplyErrors(next.summaryErrors)
		}
	}
	a.modelRuntime.Store(&next)
	view, err := a.modelSettingsView(ctx)
	if err != nil {
		return view, &api.SavedModelSettingsError{Reasons: []string{err.Error()}}
	}
	if len(next.ApplyErrors) > 0 {
		return view, &api.SavedModelSettingsError{Reasons: slices.Clone(next.ApplyErrors)}
	}
	return view, nil
}
func (a *App) modelSettingsView(ctx context.Context) (api.ModelSettingsView, error) {
	if err := ctx.Err(); err != nil {
		return api.ModelSettingsView{}, err
	}
	c, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	s := a.currentModels()
	v := api.ModelSettingsView{ModelsDev: a.modelsDevState(), Configured: config.ModelsOf(c), Running: s.Values.Clone(), RunningKnown: a.opt.Mode == Receive, RunningVersion: s.Version, Source: c.Source, ModelsReadAt: s.ReadAt, ModelsError: s.CatalogError, ApplyErrors: slices.Clone(s.ApplyErrors)}
	if a.opt.Mode == Receive {
		v.RunningReason = "已发布，下一轮使用；正在运行的回合沿用各自版本"
		for _, active := range a.hub.ActiveModels() {
			v.Active = append(v.Active, api.ModelUse(active))
		}
	}
	if c.Source != "" {
		b, e := os.ReadFile(c.Source)
		if e != nil {
			return v, e
		}
		v.Version = config.ContentVersion(b)
	}
	v.ModelsEditable = a.opt.Mode == Receive && len(s.Models) > 0
	if !v.ModelsEditable {
		v.ModelsDisabledReason = api.ErrModelsUnavailable.Error()
	}
	if a.opt.Mode == Receive && !v.ModelsEditable {
		v.ModelsDisabledReason = "模型列表为空，请刷新模型列表"
	}
	groups := map[string][]api.ModelOption{}
	listed := func(ref string) bool { _, ok := s.Find(ref); return ok }
	for _, m := range s.Models {
		option := modelOption(m)
		v.Models = append(v.Models, option)
		groups[m.OwnedBy] = append(groups[m.OwnedBy], option)
	}
	for provider, options := range groups {
		sort.Slice(options, func(i, j int) bool { return options[i].Name < options[j].Name })
		v.Groups = append(v.Groups, api.ModelGroup{Provider: provider, Models: options})
	}
	sort.Slice(v.Groups, func(i, j int) bool { return v.Groups[i].Provider < v.Groups[j].Provider })
	ids, err := a.listCharacterIDs()
	if err != nil {
		return v, err
	}
	for _, id := range ids {
		ch := c.Characters[id]
		run := s.Values.Characters[id]
		actual := run.Model
		if a.opt.Mode == Receive && actual == "" {
			_, _, actual, err = persona.Display(a.paths.CharactersDir(), id, run.Model)
			if err != nil {
				return v, err
			}
		}
		v.Characters = append(v.Characters, api.ModelCharacterSettings{ID: id, ConfiguredModel: ch.Model, ConfiguredBackground: ch.Background, RunningModel: actual, RunningBackground: s.Values.Background(id), ModelMissing: !s.ReadAt.IsZero() && !listed(ch.Model) && ch.Model != "", BackgroundMissing: !s.ReadAt.IsZero() && !listed(v.Configured.Background(id)) && v.Configured.Background(id) != ""})
		v.ReadOnly = append(v.ReadOnly, api.ModelReadOnly{Key: "characters." + id + ".topic", Value: ch.Topic, Reason: "话题还没做"})
	}
	v.ReadOnly = append(v.ReadOnly, api.ModelReadOnly{Key: "agent.context.topicCompactAt", Value: fmt.Sprint(c.Agent.Context.TopicCompactAt), Reason: "话题还没做"}, api.ModelReadOnly{Key: "vision.model", Value: c.Vision.Model, Reason: "Go 还没做识图"}, api.ModelReadOnly{Key: "magpie.baseUrl", Value: c.MagpieBaseURL(), Reason: "模型页只读；连接结果见 ModelsError / ModelsReadAt"})
	return v, nil
}
func modelOption(m llm.Model) api.ModelOption {
	o := api.ModelOption{ID: m.ID, Name: m.DisplayName, Provider: m.OwnedBy, Label: m.Label, ContextWindow: m.Window(), ContextReported: m.WindowSource() != llm.SourceDefault, ReasoningLevels: slices.Clone(m.ReasoningLevels)}
	o.MaxOutput, o.WindowSource, o.OutputSource = m.OutputLimit(), string(m.WindowSource()), string(m.OutputSource())
	l := m.Limits
	o.ProviderWindow, o.ProviderOutput, o.DevWindow, o.DevOutput, o.UserWindow, o.UserOutput, o.DevMixed = l.ProviderWindow, l.ProviderOutput, l.DevWindow, l.DevOutput, l.UserWindow, l.UserOutput, l.DevMixed
	if o.Name == "" {
		o.Name = m.ID
	}
	if m.Modalities != nil {
		o.Input = slices.Clone(m.Modalities.Input)
		o.Output = slices.Clone(m.Modalities.Output)
		o.VisionReported = m.Modalities.Input != nil
	}
	if len(o.ReasoningLevels) == 0 {
		for _, l := range m.SupportedReasoningLevels {
			o.ReasoningLevels = append(o.ReasoningLevels, l.Effort)
		}
	}
	o.ReasoningReported = m.Reasoning != nil || len(o.ReasoningLevels) > 0
	vision := "看图：未报告"
	if o.VisionReported {
		vision = "不能看图"
		if slices.Contains(o.Input, "image") {
			vision = "能看图"
		}
	}
	window := "上下文未知（按 128K 算）"
	if o.ContextReported {
		window = fmt.Sprintf("%gK 上下文", float64(o.ContextWindow)/1000)
	}
	thinking := "思考：未报告"
	if len(o.ReasoningLevels) > 0 {
		labels := make([]string, len(o.ReasoningLevels))
		for i, l := range o.ReasoningLevels {
			labels[i] = map[string]string{"off": "关", "minimal": "极低", "low": "低", "medium": "中", "high": "高", "xhigh": "更高", "max": "最高"}[l]
			if labels[i] == "" {
				labels[i] = l
			}
		}
		thinking = "思考：" + strings.Join(labels, "/")
	} else if m.Reasoning != nil && !*m.Reasoning {
		thinking = "不支持思考"
	}
	o.Subtitle = vision + " · " + window + " · " + thinking
	return o
}
