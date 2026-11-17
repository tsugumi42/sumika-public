// harness-check：在临时数据目录里用真实装配（app.Open）跑一轮本地私聊，逐项看工具链通不通。
// 只用于开发时验收：模型走配置里的 Magpie；遇到审批一律放行，所以数据目录和工作目录必须是临时的。
// 不读写真实 data/；跑完把这一轮的工具步骤从 trace 库里打出来。
//
//	go run ./cmd/harness-check -root .. -data <临时数据目录> -char tester -msg "看看 README 讲了什么"
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/api"
	"sumika-native/internal/app"
	"sumika-native/internal/config"
)

func main() {
	root := flag.String("root", "", "资源根（有 sumika.example.yml 的目录）；不给就从当前目录往上找")
	data := flag.String("data", "", "临时数据目录（必填，不许是真实 data/）")
	char := flag.String("char", "tester", "角色 ID")
	msg := flag.String("msg", "", "发给角色的话")
	account := flag.String("account", "10001", "配置里占位的 QQ 小号")
	answer := flag.Int("answer", 0, "她用 ask_owner 提问时替主人点第几个选项（从 1 数）；0 = 不点，等到期")
	wait := flag.Duration("timeout", 5*time.Minute, "单轮最长等待")
	flag.Parse()
	if *data == "" || *msg == "" {
		fmt.Fprintln(os.Stderr, "需要 -data 和 -msg")
		os.Exit(2)
	}
	if err := run(*root, *data, *char, *msg, *account, *answer, *wait); err != nil {
		fmt.Fprintln(os.Stderr, "出错："+err.Error())
		os.Exit(1)
	}
}

func run(root, data, char, msg, account string, answer int, wait time.Duration) error {
	abs, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Base(abs), "data") && !strings.Contains(strings.ToLower(abs), "scratchpad") && !strings.Contains(strings.ToLower(abs), "temp") {
		return fmt.Errorf("拒绝：%s 看起来是真实数据目录", abs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if root == "" {
		if root, err = config.FindRoot("."); err != nil {
			return err
		}
	}
	a, err := app.Open(ctx, app.Options{Root: root, DataDir: abs, Mode: app.Receive, Account: account, Logf: func(f string, v ...any) { fmt.Printf("[日志] "+f+"\n", v...) }})
	if err != nil {
		return err
	}
	d := a.Desktop()
	defer func() { d.Close(); a.Close() }() // 先收桌面入口（等回合和订阅退出），再关后台
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + char}

	started := time.Now().UnixMilli()
	stop := make(chan struct{})
	go func() { // 审批一律放行
		seen := map[string]bool{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(300 * time.Millisecond):
			}
			sub, err := d.Reader.WatchLocalApprovals(ctx)
			if err != nil {
				continue
			}
			select {
			case snap := <-sub.Updates():
				for _, v := range snap.Value {
					if !seen[v.ID] {
						seen[v.ID] = true
						fmt.Printf("[审批] %s %s：%s → 允许\n", v.Tool, v.Subject, v.Why)
						d.Reader.DecideLocal(ctx, v.Key, v.ID, api.ApproveOnce)
					}
				}
			case <-time.After(200 * time.Millisecond):
			}
			sub.Close()
		}
	}()
	if answer > 0 {
		go func() { // 她提问就替主人点第 answer 个选项（只给真实模型试验用，真界面里是主人自己点）
			seen := map[string]bool{}
			for {
				select {
				case <-stop:
					return
				case <-time.After(300 * time.Millisecond):
				}
				sub, err := d.Reader.WatchLocalQuestions(ctx)
				if err != nil {
					continue
				}
				select {
				case snap := <-sub.Updates():
					for _, v := range snap.Value {
						if seen[v.ID] || answer > len(v.Options) {
							continue
						}
						seen[v.ID] = true
						fmt.Printf("[提问] %s 选项 %v → 点第 %d 个\n", v.Text, v.Options, answer)
						d.Reader.AnswerLocal(ctx, v.Key, v.ID, answer-1)
					}
				case <-time.After(200 * time.Millisecond):
				}
				sub.Close()
			}
		}()
	}
	var last api.LocalDraft
	err = d.Reader.SendLocal(ctx, key, msg, func(v api.LocalDraft) { last = v })
	close(stop)
	fmt.Printf("\n[发送结果] err=%v\n[最后草稿] 步骤 %d：%s\n", err, last.Step, last.Text)
	msgs, e := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 6}, "")
	if e == nil {
		for _, m := range msgs.Value.Items {
			fmt.Printf("[消息] %s/%s：%s\n", m.Kind, m.ActorKind, m.Text)
		}
	}
	dumpSteps(filepath.Join(abs, "go", "trace.db"), started)
	return err
}

func dumpSteps(file string, since int64) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Println("打不开 trace：", err)
		return
	}
	defer db.Close()
	rows, err := db.Query(`SELECT kind, COALESCE(name,''), CASE WHEN kind = 'recall' THEN '' ELSE COALESCE(args_json,'') END, status, is_error, COALESCE(preview,'') FROM steps WHERE started_at >= ? ORDER BY started_at, id`, since)
	if err != nil {
		fmt.Println("读 steps：", err)
		return
	}
	defer rows.Close()
	cut := func(s string, n int) string {
		r := []rune(strings.ReplaceAll(s, "\n", " "))
		if len(r) > n {
			return string(r[:n]) + "…"
		}
		return string(r)
	}
	for rows.Next() {
		var kind, name, args, status, preview string
		var isErr int
		rows.Scan(&kind, &name, &args, &status, &isErr, &preview)
		fmt.Printf("[步骤] %s %s %s err=%d 参数=%s 结果=%s\n", kind, name, status, isErr, cut(args, 160), cut(preview, previewLimit()))
	}
}

// previewLimit：HC_PREVIEW 指定步骤结果显示多少字，默认 300
func previewLimit() int {
	n := 300
	if v := os.Getenv("HC_PREVIEW"); v != "" {
		fmt.Sscan(v, &n)
	}
	return n
}
