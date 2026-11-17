// memory-shadow 是影子整理工具（规格-记忆写入上线 W6）：把真实的聊天库和记忆库拷一份到 -work 下，在副本上把后台整理跑一遍，
// 出一份报告给主人抽检。全程只在拷贝那一段读原库，拷完不再打开它。
//
//	go run ./cmd/memory-shadow -data <仓库>\data -char <角色 id>                 估算：不调一次模型，只数批数和字数
//	go run ./cmd/memory-shadow -data … -char … -gen fake                                          假生成器把整条流水线走一遍（报告里的新事实是假的）
//	go run ./cmd/memory-shadow -data … -char … -gen real -spend <次数>                            真跑（现在关着：缺真实生成器、W5 合 main、主人同意的 -spend，见 gen.go 的 realGenReady）
//	go run ./cmd/memory-shadow -score <out 目录>\marks.csv                                         读你标好的清单算精确率，对着过线门槛说话
//
// 估算和真跑都读真实库的拷贝，由主会话跑，不交给实现子代理；报告原文（report.md、marks.csv）只给主人自己打开，
// 会话和子代理只读 summary.json（只有计数、编号、范围，没有聊天正文和名字）。
//
// 拷贝期间要拿数据锁：桌面程序或 Go 后台持锁时拒绝，先退出它们。拷贝耗时就是 QQ 的空档，开始前会打印。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(realMain(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// realMain 解析命令行、跑一次、打印结果。返回退出码：0 成功，1 出错，2 参数不对
func realMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory-shadow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		data       = fs.String("data", "", "数据目录（只在拷贝那一段读）")
		char       = fs.String("char", "", "角色 id")
		work       = fs.String("work", "", "工作目录，必须在 <数据目录>/dev/ 下且被 git 忽略；默认 <数据目录>/dev/memory-shadow/<时间>/")
		root       = fs.String("root", "", "项目根（找 prompts/summarize-v2.md）；默认从当前目录往上找")
		name       = fs.String("name", "", "她的名字（填进整理提示词）；默认用角色 id")
		places     = fs.String("places", "", "放行哪些场合，逗号分隔：owner-private、local-private、local-group、platform-private、platform-group；默认只有 owner-private")
		cursors    = fs.String("cursors", "real", "real = 用副本里的真实游标（默认）；seed = 给每个放行的会话种游标，提示词 A/B 用（报告里写明不是真实游标）")
		last       = fs.Int("last", 0, "-cursors seed 用：窗口是最近几天")
		since      = fs.String("since", "", "-cursors seed 用：窗口从哪天起，写 2006-01-02（本地时间）")
		maxBatches = fs.Int("max-batches", 50, "每个会话最多跑几批")
		gen        = fs.String("gen", "", "不给 = 估算（一次模型都不调）；fake = 假生成器；real = 真的调后台模型（必须同时给 -spend）")
		spend      = fs.Int("spend", 0, "-gen real 用：最多调几次后台模型。工具自己不设默认值")
		prompt     = fs.String("prompt", "", "换一份整理提示词（提示词 A/B 用）")
		keep       = fs.Bool("keep", false, "跑完不删副本（默认删）")
		seed       = fs.Int64("seed", 1, "漏记抽查抽样的随机种子")
		score      = fs.String("score", "", "读你标好的 marks.csv 算精确率，对着过线门槛说话（只给这一个参数就行，不拷库、不调模型）")
		fixture    = fs.String("fixture", "", "issue17：只在新建的 -work 中造库，不读取 -data")
		sample     = fs.Int("sample", 0, "issue17 小样本：1 或 2；零为完整夹具")
		rounds     = fs.Int("rounds", 1, "issue17 独立新库轮数，1～3")
		model      = fs.String("model", "agnes-ai/agnes-2.5-flash", "issue17 整理与回答模型")
		gateway    = fs.String("gateway", "http://127.0.0.1:3425/v1", "issue17 模型网关")
		embedURL   = fs.String("embed-url", "", "issue17 独立嵌入服务地址，含 /v1；real 必填")
		embedModel = fs.String("embed-model", "", "issue17 嵌入模型；real 必填")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fixture != "" {
		if *fixture != "issue17" || *data != "" || *char != "" || *score != "" || *places != "" || *cursors != "real" || *last != 0 || *since != "" || *prompt != "" {
			fmt.Fprintln(stderr, "-fixture 只认 issue17；不接受 -data / -char / -score / -places / 游标 / -prompt 参数")
			return 2
		}
		o := issue17Options{Work: *work, Root: *root, Model: *model, Gateway: *gateway, Mode: mode(*gen), Spend: *spend, Sample: *sample, Rounds: *rounds, EmbedURL: *embedURL, EmbedModel: *embedModel}
		if err := o.validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if _, err := runIssue17(ctx, o, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *score != "" {
		raw, err := os.ReadFile(*score)
		if err != nil {
			fmt.Fprintln(stderr, "读不了 marks.csv：", err)
			return 1
		}
		res, err := parseMarks(raw)
		if err != nil {
			fmt.Fprintln(stderr, "marks.csv 不对：", err)
			return 1
		}
		fmt.Fprint(stdout, scoreText(res))
		return 0
	}
	pl, err := parsePlaces(*places)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg := runConfig{
		Data: *data, Char: *char, Work: *work, Root: *root, Name: *name, Places: pl, Cursors: *cursors,
		MaxBatches: *maxBatches, Mode: mode(*gen), Spend: *spend, PromptFile: *prompt, Keep: *keep, Seed: *seed,
		Logf: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) },
	}
	if *last > 0 {
		cfg.SeedLast = time.Duration(*last) * 24 * time.Hour
	}
	if *since != "" {
		t, err := time.ParseInLocation("2006-01-02", *since, time.Local)
		if err != nil {
			fmt.Fprintf(stderr, "-since 要写成 2006-01-02：%v\n", err)
			return 2
		}
		cfg.SeedSince = t
	}
	if err := cfg.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stderr, startupNotice)
	res, err := runShadow(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "失败：", err)
		return 1
	}
	if err := writeReport(res); err != nil {
		fmt.Fprintln(stderr, "写报告失败：", err)
		return 1
	}
	fmt.Fprint(stdout, estimateText(res))
	fmt.Fprintf(stdout, "\n报告写在 %s（report.md 只由你自己打开；会话和子代理只读 summary.json）\n", res.Out)
	if res.Kept != "" {
		fmt.Fprintf(stdout, "副本留在 %s（用完请删）\n", res.Kept)
	}
	return 0
}
