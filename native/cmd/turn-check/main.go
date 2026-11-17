// turn-check：Go 后台第 1 步的真调（规格-Go后台 四点六 第 5 条）。
// 在临时数据目录里放一张编的卡，走 Magpie 真模型跑一轮本地主窗口私聊（turn.Run），再把同一份请求原样发第二次，
// 看第二次是不是只剩结尾凑不满一块的那截没命中（≤ 256 token）：证明缓存键真的带上去了、前缀两次一样。
// 服务商按块缓存（2026-10-25 实测 DeepSeek / opendesign：1568 token 的请求命中 1408，尾巴 160 不中，连跑三次一样），
// 所以不按"命中率 ≥ 90%"判：请求短的时候那条线量的是请求够不够长，不是缓存键带没带
// 不碰真实的数据目录；临时目录跑完就删。
//
//	go run ./cmd/turn-check -root <仓库根>   # 在 native/ 里跑；配置从 -root 读（只读），提示词用这份代码旁边的；模型用配置里第一个角色的，-model 可换
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

func main() {
	model := flag.String("model", "", "用哪个模型（不给就用配置里第一个角色的）")
	cfgRoot := flag.String("root", "", "从哪个项目根读配置（不给就是这份代码所在的项目）")
	flag.Parse()
	if err := run(*model, *cfgRoot); err != nil {
		fmt.Fprintln(os.Stderr, "出错："+err.Error())
		os.Exit(1)
	}
}

func run(model, cfgRoot string) error {
	root, err := config.FindRoot(".")
	if err != nil {
		return err
	}
	if cfgRoot == "" {
		cfgRoot = root
	}
	cfg, err := config.Load(cfgRoot)
	if err != nil {
		return err
	}
	if model == "" {
		ids := make([]string, 0, len(cfg.Characters))
		for id := range cfg.Characters {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if m := cfg.Characters[id].Model; m != "" {
				model = m
				break
			}
		}
	}
	if model == "" {
		return fmt.Errorf("配置里没有选模型：用 -model 指定一个")
	}
	dir, err := os.MkdirTemp("", "sumika-turn-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p, err := paths.Init(dir)
	if err != nil {
		return err
	}
	card := filepath.Join(p.CardDir("tester"))
	os.MkdirAll(card, 0o755)
	os.WriteFile(filepath.Join(card, "character.yml"), []byte("name: 测试角色\n"), 0o644)
	os.WriteFile(filepath.Join(card, "character.md"), []byte("## 身份\n一个测试用的角色（编的）。\n\n## 表达\n回答尽量短。\n"), 0o644)
	// 只开本地库（主窗口只用它）和共用的 trace（规格-聊天记录按平台分库 第五节第 5 步）
	set, err := store.OpenGo(p, "local")
	if err != nil {
		return err
	}
	defer set.Close()
	st, err := set.For("console")
	if err != nil {
		return err
	}

	// 编一段二十多轮的历史，把请求拉长到真实对话的量级：太短的请求（几百 token）服务商按整块缓存，凑不满的那截不算命中，测不出缓存键
	base := time.Now().Add(-2 * time.Hour).UnixMilli()
	for i := 0; i < 24; i++ {
		kind, who, text := "owner", "owner", fmt.Sprintf("这是编的第 %d 句：今天整理了一下房间，把书架上的旧杂志按年份重新排了一遍，顺便把落灰的台灯擦干净了。", i+1)
		if i%2 == 1 {
			kind, who, text = "character", "tester", fmt.Sprintf("第 %d 句的回复：听起来收拾得很仔细，按年份排以后找起来方便多了，台灯擦干净看书也舒服。", i+1)
		}
		if _, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:tester", ActorKind: kind, ActorID: who, Origin: kind, Kind: "message", Text: text, CreatedAt: base + int64(i)*60_000}, []string{"tester"}); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := llm.New(llm.Magpie(cfg.Magpie.BaseURL))
	models, err := client.Models(ctx)
	if err != nil {
		return err
	}
	// 工作目录也放在临时目录里，再接上审批的关（手动模式、没人回答：要问的一律当取消）：
	// 真模型可能调工具，只读的在临时目录里跑，写文件、跑命令都不会真的执行
	wd := filepath.Join(dir, "work")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		return err
	}
	gate := approval.NewGate(p.Root)
	gate.Mode = "manual"
	defer gate.Analyzer.Close()
	tc := turn.Config{Root: root, Data: p, Thinking: string(cfg.Agent.Thinking.Local), RecentMessages: cfg.Agent.RecentMessages, Workdir: wd,
		Models: map[string]string{"tester": model}, Shell: turn.DetectShell(ctx), Now: time.Now, Gate: gate,
		MaxLive: cfg.Agent.MaxLive, IdleAfter: time.Duration(cfg.Agent.IdleMinutes * float64(time.Minute))}
	ac := cfg.Agent.Context
	tc.Context = turn.ContextLimits{Window: ac.Window, Reserve: ac.Reserve, SoftRatio: ac.SoftRatio, HardRatio: ac.HardRatio, KeepTokens: ac.KeepTokens, NoAutoCompact: !ac.AutoCompactChat}
	res, err := turn.Run(ctx, tc, set, client, models, "tester", "用一句话说说你是谁。")
	if err != nil {
		return err
	}
	fmt.Printf("模型 %s，思考档位 %s → 发出去的 %q，系统提示 %d 字\n", model, tc.Thinking, res.Built.Request.Thinking, store.JSLen(res.Built.System))
	ratio := func(u llm.Usage) float64 {
		if u.Input+u.CacheRead == 0 {
			return 0
		}
		return float64(u.CacheRead) / float64(u.Input+u.CacheRead) * 100
	}
	u := res.LLM.Usage
	fmt.Printf("第一次：回了「%s」　输入没命中 %d、命中 %d（%.1f%%）　输出 %d　首字 %d ms　渠道 %s\n", res.Reply, u.Input, u.CacheRead, ratio(u), u.Output, res.LLM.FirstToken.Milliseconds(), res.LLM.Route.Provider)
	again, err := client.StreamChat(ctx, res.Built.Request, nil)
	if err != nil {
		return err
	}
	u2 := again.Usage
	fmt.Printf("第二次（同一份请求）：输入没命中 %d、命中 %d（%.1f%%）　渠道 %s\n", u2.Input, u2.CacheRead, ratio(u2), again.Route.Provider)
	if u2.CacheRead == 0 || u2.Input > 256 {
		return fmt.Errorf("第二次没命中的有 %d token（命中 %d）：只该剩结尾不满一块的那截，缓存键可能没带上去，或者前缀两次不一样", u2.Input, u2.CacheRead)
	}
	fmt.Printf("缓存：第二次只有结尾 %d token 没命中\n", u2.Input)
	return nil
}
