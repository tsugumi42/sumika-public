// core-check：Go 后台第 0 步的检查小工具（规格-Go后台 四点五）。
// 读配置 → 只读打开真实的 events.db、trace.db → 检查表结构认不认识 → 数条数（和 Bun 版对拍）；加 -call 时调一次模型。
// 只读：不写库、不写配置、不写数据目录。
//
//	go run ./cmd/core-check            # 在 native/ 里跑；项目根自己往上找
//	go run ./cmd/core-check -call      # 再调一次模型（花一次很便宜的请求）
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func main() {
	rootFlag := flag.String("root", "", "项目根目录（不给就从当前目录往上找有 sumika.example.yml 的那层）")
	call := flag.Bool("call", false, "调一次模型")
	model := flag.String("model", "", "调哪个模型（不给就用配置里第一个角色的 model，再没有用 defaults.backgroundModel）")
	flag.Parse()
	if err := run(*rootFlag, *call, *model); err != nil {
		fmt.Fprintln(os.Stderr, "出错："+err.Error())
		os.Exit(1)
	}
}

func run(rootFlag string, call bool, model string) error {
	root := rootFlag
	if root == "" {
		var err error
		if root, err = config.FindRoot("."); err != nil {
			return err
		}
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	fmt.Printf("配置文件：%s\n", config.File(root))
	fmt.Printf("Magpie：%s　端口：%d　角色配了模型的：%d 个　思考：本地 %s / QQ 私聊 %s / QQ 群 %s / 后台 %s\n",
		cfg.Magpie.BaseURL, cfg.Console.Port, len(cfg.Characters), cfg.Agent.Thinking.Local, cfg.Agent.Thinking.Private, cfg.Agent.Thinking.Group, cfg.Defaults.BackgroundThinking)

	p, err := paths.Open(cfg.DataDirIn(root))
	if err != nil {
		return err
	}
	fmt.Printf("库：%s　%s\n", p.BunEventsDB(), p.BunTraceDB())
	s, err := store.OpenReadOnly(p)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.CheckSchema(); err != nil {
		return err
	}
	fmt.Println("表结构：基线里的表和列真实库里都有")
	c, err := s.Count()
	if err != nil {
		return err
	}
	fmt.Printf("条数：events %d　其中消息 %d　会话 %d　model_runs %d　steps %d\n", c.Events, c.Messages, c.Conversations, c.ModelRuns, c.Steps)
	fmt.Printf("每个会话的消息数（%d 个会话）汇总哈希：%s\n", len(c.PerConversation), digest(c.PerConversation))

	if !call {
		return nil
	}
	if model == "" {
		model = firstModel(cfg)
	}
	if model == "" {
		return fmt.Errorf("配置里没有选模型：用 -model 指定一个")
	}
	cl := llm.New(llm.Magpie(cfg.Magpie.BaseURL))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ms, err := cl.Models(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("模型列表：%d 个\n", len(ms))
	for _, m := range ms {
		if m.ID == model {
			fmt.Printf("  %s：上下文 %d，能开的思考档 %d 种\n", m.ID, m.Window(), len(m.SupportedReasoningLevels))
		}
	}
	res, err := cl.StreamChat(ctx, chat.Request{
		Model:    model,
		Style:    chat.StyleBackground,
		Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("用一句不超过十个字的中文打个招呼。")}}},
		CacheKey: "go-core-check",
		Thinking: string(cfg.Defaults.BackgroundThinking),
	}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("调用 %s：回了「%s」　思考 %d 字　首字 %d ms　共 %d ms\n", model, strings.TrimSpace(res.Text), len([]rune(res.Reasoning)), res.FirstToken.Milliseconds(), res.Total.Milliseconds())
	fmt.Printf("用量：输入没命中缓存 %d、命中 %d　输出 %d（含思考 %d）　结束：%s　实际渠道：%s / %s\n", res.Usage.Input, res.Usage.CacheRead, res.Usage.Output, res.Usage.Reasoning, res.Stop, orDash(res.Route.Provider), orDash(res.Route.Model))
	return nil
}

func firstModel(cfg config.Config) string {
	ids := make([]string, 0, len(cfg.Characters))
	for id := range cfg.Characters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if m := cfg.Characters[id].Model; m != "" {
			return m
		}
	}
	return cfg.Defaults.BackgroundModel
}

// digest 把"会话 → 条数"排好序算一个哈希：对拍时只比哈希，不把会话名（里面有群号）打出来。
// 算法和 scripts/go-core-count.ts 一样：每行 "会话\t条数\n"，按会话排序，sha256 取前 16 位
func digest(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\t%d\n", k, m[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
