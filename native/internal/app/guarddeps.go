package app

import (
	"context"
	"strings"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/api"
	"sumika-native/internal/embed"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/memory"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// warnRemovedConfigKeys 对配置里还写着的已删键警告一次（规格-嵌入不依赖外部程序 第 2 条）：读配置不失败，但别让主人以为还在用。
// 现在只有 embed.python、embed.serverDir
func (a *App) warnRemovedConfigKeys() {
	if len(a.cfg.RemovedKeys) == 0 {
		return
	}
	a.logf("sumika.yml 里的 %s 已删掉（旧版借 MaiBot 嵌入的退路整体移除了），这几项被忽略；嵌入现在只走自带的 sumika-models，模型目录是 embed.modelsRoot（空 = <数据目录>/models）",
		strings.Join(a.cfg.RemovedKeys, "、"))
}

// LocalEmbedding 是 receive 持有的本地嵌入；记忆只用 Embedder，宿主管起停和提醒。
type LocalEmbedding interface {
	embed.Embedder
	Start()
	Stop()
	Status() (embed.Status, string)
	MissingModel() *embed.MissingModel
}

// embedMissingModel 是设置页缺模型入口要的状态：没接嵌入、或不缺模型文件时为空。
func (a *App) embedMissingModel() *api.EmbedMissingModel {
	if a.embedding == nil {
		return nil
	}
	m := a.embedding.MissingModel()
	if m == nil {
		return nil
	}
	return &api.EmbedMissingModel{Model: m.Model, Path: m.Path, DownloadURL: m.DownloadURL}
}

// openEmbedding 异步加载本地模型；未就绪时守卫报告 literal-pending，接收照常启动。
// 本块只准备依赖，发送钳制仍由下一步核对 GuardLevel 后决定。
func (a *App) openEmbedding() {
	if !a.cfg.Embed.Enabled {
		a.embeddingStatus(embed.StatusOff)
		return
	}
	// 只走自带的 sumika-models：modelsRoot 空 = <数据目录>/models，相对路径相对数据目录（规格-嵌入不依赖外部程序 第 4 条）
	cfg := embed.LocalConfigFrom(a.cfg.Embed, a.cfg.Memory.Rerank.Model, a.root, a.paths.Root)
	cfg.OnStatus = a.embeddingStatus
	newLocal := a.opt.NewEmbed
	if newLocal == nil {
		newLocal = func(c embed.LocalConfig, logf func(string, ...any)) LocalEmbedding {
			return embed.NewLocal(c, logf)
		}
	}
	a.embedding = newLocal(cfg, a.logfFor(applog.Memory))
	a.noteUncalibratedModel(cfg.Model)
	a.embedding.Start()
}

// noteUncalibratedModel 在配的嵌入模型没和守卫门槛对过时说一声（启动时一次）：守卫的 0.70 是在 gte-multilingual-base 上量的
// （memory.GuardCalibrated），别的模型即使嵌入就绪，GuardLevel 也只报 literal-pending，QQ 发送留在试运行。
// 这不是嵌入故障（guard:embed 管那个），不说的话就是一个没有任何提示的"发送放不开"（规格-Go本地模型 2.7）
func (a *App) noteUncalibratedModel(model string) {
	if model == "" || memory.GuardCalibrated(model) {
		return
	}
	a.logf("嵌入模型 %s 没和守卫的相似度门槛对过：守卫只比对原文，QQ 发送留在试运行", model)
	a.alerts.Raise(alert.Alert{
		Key: "guard:uncalibrated", Class: "guard", Title: "发送前检查的嵌入模型没标定过",
		Body: "嵌入模型 " + model + " 没和发送前检查的相似度门槛 0.70 对过（门槛是在 gte-multilingual-base 上量的），所以只比对原文，QQ 发送先留在试运行。要放开发送，把 embed.model 换成 gte-multilingual-base（embed.backend 用 go-models）。",
	})
}

func (a *App) embeddingStatus(status embed.Status) {
	if a.closing.Load() {
		return
	}
	// 没接嵌入或没配模型由装配提醒 literal-pending；已接的服务才报故障。
	if a.embedding == nil || a.embedding.Model() == "" {
		return
	}
	if status == embed.StatusReady {
		a.embedAlert.Store(false)
		a.alerts.Clear("guard:embed", alert.Note{Title: "发送前检查的嵌入已恢复", Body: "相似度检查的嵌入已就绪；发送启用仍需核对其余条件。"})
		a.alerts.Clear("guard:literal-pending", alert.Note{Title: "发送前检查的相似度已接上", Body: "相似度检查的嵌入已就绪；发送启用仍需核对其余条件。"})
		a.embeddingReady()
		return
	}
	if status != embed.StatusOff && status != embed.StatusDown {
		return
	}
	if a.embedAlert.Swap(true) {
		return
	}
	body := "本地嵌入未就绪或未启用，当前只能比对原文；其余依赖齐全时守卫能力为 literal-pending。接收照常运行，发送钳制保持。"
	// 带上第一行原因（模型 id 不在服务里 / 找不到可执行文件 / 依赖没装……）；stderr 末尾在状态里，不进提醒
	if _, why := a.embedding.Status(); why != "" {
		first, _, _ := strings.Cut(why, "\n")
		if r := []rune(first); len(r) > 160 {
			first = string(r[:160]) + "…"
		}
		body += "原因：" + first
	}
	a.alerts.Raise(alert.Alert{Key: "guard:embed", Class: "guard", Title: "发送前检查的嵌入尚不可用", Body: body})
}

// embeddingReady 在嵌入就绪（含挂了又恢复）时给每个已经挂上的记忆排一次补向量。
// 记忆是在嵌入还没起来时打开的（openMemories 紧跟在 openEmbedding 后面），那时只补得了全文和别名，向量要等嵌入就绪才有。
// 回调在嵌入看管的协程上：EmbeddingReady 只排队、不等算完，不会卡住它的健康检查。还没挂上的记忆由 openMemories 挂上以后自己再看一眼状态
func (a *App) embeddingReady() {
	a.memMu.Lock()
	defer a.memMu.Unlock()
	for _, id := range a.memoryIDs {
		a.memories[id].EmbeddingReady()
	}
	if a.ownerMemory != nil {
		a.ownerMemory.EmbeddingReady()
	}
}

func (a *App) backgroundModel(character string) string {
	return a.currentModels().Values.Background(character)
}

// memoryGenerator 和内核共用同一个网关客户端，不另建来源、不写入记忆库。
type memoryGenerator struct {
	app       *App
	runtime   *ModelRuntime
	caller    turn.ModelCaller
	budget    kernel.Budget
	character string
	// kind 是这次调用记进模型调用记录的用途（空是 memory）；conversation 记进它的会话
	kind, conversation string
	// system 非空就作为第一条系统消息；thinking 非空就用它这一档思考，代替后台思考档（翻译这类短活关思考省时间）
	system, thinking string
}

func (g memoryGenerator) Call(ctx context.Context, model, prompt, cacheKey string) (memory.Reply, error) {
	a := g.app
	s := g.runtime
	if s == nil {
		s = a.currentModels()
	}
	ctx, identity, caller, identityErr := g.callIdentity(ctx, cacheKey)
	if identityErr != nil {
		return memory.Reply{}, identityErr
	}
	chain := s.Values.Fallback(g.character)
	var selector *llm.Failover
	if c, ok := caller.(*modelcall.Client); ok {
		selector = c.Failover
	}
	if selector == nil {
		selector = &llm.Failover{}
	}
	plan := selector.Plan(model, chain)
	if plan.Model == "" {
		return memory.Reply{}, plan.Err(nil)
	}
	blocks := []chat.Block{chat.Text(prompt)}
	msgs := []chat.Message{{Role: chat.RoleUser, Blocks: blocks}}
	if g.system != "" {
		msgs = []chat.Message{{Role: chat.RoleSystem, Blocks: []chat.Block{chat.Text(g.system)}}, {Role: chat.RoleUser, Blocks: blocks}}
	}
	req := chat.Request{Messages: msgs, CacheKey: cacheKey, NoStore: true, Style: chat.StyleBackground}
	for {
		req.Model = plan.Model
		req.Thinking = ""
		chosen := llm.Model{ID: plan.Model}
		for _, m := range s.Models {
			if m.ID == plan.Model {
				chosen = m
				level := string(s.Values.BackgroundThinking)
				if g.thinking != "" {
					level = g.thinking
				}
				req.Thinking = turn.Effort(m, level)
				break
			}
		}
		// 单次输出跟模型，但不能把请求顶出窗口：输入大时按窗口余量往下压（没报输出上限的按兜底）
		req.MaxOutputTokens = chosen.ClampOutput(len(prompt), int64(chosen.MaxOutputTokens))
		if plan.Info != nil {
			req = chat.StripThinking(req)
		}
		result, err := caller.StreamChat(llm.WithFailover(ctx, plan.Info), identity, req, nil)
		if err == nil {
			return memory.Reply{Text: result.Text}, nil
		}
		if !plan.Next(ctx, err) {
			return memory.Reply{}, plan.Exhausted(err)
		}
		identity.AttemptID = "attempt:" + store.NewID(time.Now())
		ctx = kernel.WithInvocation(ctx, identity)
	}
}
