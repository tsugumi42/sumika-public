// Command rpc-bench 量界面协议的开销（规格-Bun后台第1批 第十节）：同一份数据，比"直接调 Reader""进程内经协议编码（管道）""经本机 WebSocket"三条路。
// 只读写 -data 指定的目录（用 cmd/desk-fixture 造的编造数据），不碰真实 data/。
//
//	go run ./cmd/desk-fixture -out <新目录>
//	go run ./cmd/rpc-bench -data <同一个目录> [-rounds 3]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/store"
)

const group = "qq:group:90002"

func main() {
	dir := flag.String("data", "", "desk-fixture 造的数据目录")
	rounds := flag.Int("rounds", 3, "每条路跑几轮")
	streamOnly := flag.Bool("stream-only", false, "只量流式首字（第 2 批，规格第五节）：假模型，不需要数据目录")
	flag.Parse()
	if *streamOnly {
		if err := streamBench(50); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(*dir, *rounds); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type path struct {
	name string
	r    api.Reader
}

func run(dir string, rounds int) error {
	if dir == "" {
		return fmt.Errorf("要给 -data")
	}
	// 这个工具会往库里写消息（量通知延迟）：只认 desk-fixture 造的目录，防止指到真实数据
	if _, err := os.Stat(filepath.Join(dir, ".desk-fixture")); err != nil {
		return fmt.Errorf("%s 不是 desk-fixture 造的目录（没有 .desk-fixture 标记），不碰", dir)
	}
	p, err := paths.Open(dir)
	if err != nil {
		return err
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		return err
	}
	defer set.Close()
	entries, _ := os.ReadDir(p.CharactersDir())
	var chars []api.CharacterView
	for _, e := range entries {
		chars = append(chars, api.CharacterView{ID: e.Name(), Name: e.Name()})
	}
	svc := desktopread.New(set, chars, nil).WithData(p.Root)
	defer svc.Close()
	direct := &desktopread.Reader{Service: svc}
	srv := rpcserve.New(direct, "bench")
	ctx := context.Background()

	st, ct := rpcserve.Pipe()
	go srv.Serve(ctx, st)
	pc, err := rpcclient.New(ctx, ct, "bench")
	if err != nil {
		return err
	}
	defer pc.Close()
	l, err := rpcserve.Listen(ctx, srv, "")
	if err != nil {
		return err
	}
	defer l.Close()
	wc, err := rpcclient.Dial(ctx, l.URL, l.Token(), "bench")
	if err != nil {
		return err
	}
	defer wc.Close()
	routes := []path{{"直接", direct}, {"管道", &rpcclient.Reader{C: pc}}, {"WebSocket", &rpcclient.Reader{C: wc}}}

	key := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: group}
	fmt.Println("## 1. 万条滚动：从最新往前翻完（每页 200）")
	fmt.Println("| 路 | 页数 | 每页中位数 | 每页 p95 | 合计 |")
	fmt.Println("|---|---|---|---|---|")
	var media *api.MediaRef
	for _, rt := range routes {
		var all []time.Duration
		var pages int
		var total time.Duration
		for range rounds {
			cursor := api.Cursor("")
			pages = 0
			start := time.Now()
			for {
				t0 := time.Now()
				res, err := rt.r.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 200}, cursor)
				if err != nil {
					return fmt.Errorf("%s 读消息：%w", rt.name, err)
				}
				all = append(all, time.Since(t0))
				pages++
				if media == nil {
					for _, m := range res.Value.Items {
						for _, s := range m.Segments {
							if s.Media != nil && !s.Animated {
								ref := *s.Media
								media = &ref
							}
						}
					}
				}
				if !res.Value.HasMore {
					break
				}
				cursor = res.Value.NextCursor
			}
			total += time.Since(start)
		}
		fmt.Printf("| %s | %d | %s | %s | %s |\n", rt.name, pages, ms(pct(all, 50)), ms(pct(all, 95)), ms(total/time.Duration(rounds)))
	}

	fmt.Println()
	fmt.Println("## 2. 看图：同一张图，每档 20 次")
	if media == nil {
		fmt.Println("没找到静态图")
	} else {
		fmt.Println("| 路 | 档 | 中位数 | p95 | 字节 |")
		fmt.Println("|---|---|---|---|---|")
		for _, tier := range []api.MediaTier{0, 512} {
			for _, rt := range routes {
				var d []time.Duration
				var size int
				for range 20 {
					t0 := time.Now()
					res, err := rt.r.ReadMedia(ctx, *media, tier)
					if err != nil {
						return fmt.Errorf("%s 读图：%w", rt.name, err)
					}
					d = append(d, time.Since(t0))
					size = len(res.Value.Bytes)
				}
				fmt.Printf("| %s | %d | %s | %s | %d |\n", rt.name, tier, ms(pct(d, 50)), ms(pct(d, 95)), size)
			}
		}
	}

	fmt.Println()
	fmt.Println("## 3. 通知延迟：订阅会话后写一条新消息，到收到含它的快照（各 10 次）")
	fmt.Println("| 路 | 中位数 | p95 |")
	fmt.Println("|---|---|---|")
	qq, _ := set.Platform("qq")
	for _, rt := range routes {
		sub, err := rt.r.WatchConversation(ctx, api.ConversationRequest{Key: key, Limit: 200})
		if err != nil {
			return err
		}
		<-sub.Updates() // 初始快照
		var d []time.Duration
		for i := range 10 {
			text := fmt.Sprintf("量通知延迟 %s %d %d", rt.name, i, time.Now().UnixNano())
			t0 := time.Now()
			if _, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: group, Kind: "message", ActorKind: "human", ActorID: "90001", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{chars[0].ID}); err != nil {
				return err
			}
		wait:
			for {
				select {
				case s := <-sub.Updates():
					for _, m := range s.Value.Page.Items {
						if m.Text == text {
							d = append(d, time.Since(t0))
							break wait
						}
					}
				case <-time.After(10 * time.Second):
					return fmt.Errorf("%s 等不到新消息的快照", rt.name)
				}
			}
		}
		sub.Close()
		fmt.Printf("| %s | %s | %s |\n", rt.name, ms(pct(d, 50)), ms(pct(d, 95)))
	}
	abs, _ := filepath.Abs(dir)
	fmt.Printf("\n数据目录：%s；角色 %d 个\n", abs, len(chars))
	fmt.Println()
	return streamBench(50)
}

// benchChat 是假模型：请求一到就吐第一个字，之后每 10 毫秒再吐一个，吐完 6 段返回。不碰任何库、不连任何模型
type benchChat struct{ api.Reader }

func (benchChat) SendLocal(ctx context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	for i := range 6 {
		if i > 0 {
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if draft != nil {
			draft(api.LocalDraft{TurnID: "bench", Text: strings.Repeat("字", i+1)})
		}
	}
	return nil
}
func (benchChat) StopLocal(context.Context, api.ConversationKey) error { return nil }
func (benchChat) WatchLocalApprovals(context.Context) (api.Subscription[[]api.LocalApproval], error) {
	return nil, api.ErrLocalChatUnsupported
}
func (benchChat) DecideLocal(context.Context, api.ConversationKey, string, api.ApprovalChoice) error {
	return nil
}

// streamBench 量流式首字（规格-Bun后台第2批 第五节）：从调用 SendLocal 到第一次草稿回调到达调用方的时间，
// 直接调、经进程内管道、经 WebSocket 各 n 次。假模型在请求到达后 0 毫秒吐第一个字，所以差值就是协议的开销
func streamBench(n int) error {
	ctx := context.Background()
	srv := rpcserve.New(benchChat{}, "bench", rpcserve.WithWrites())
	st, ct := rpcserve.Pipe()
	go srv.Serve(ctx, st)
	pc, err := rpcclient.New(ctx, ct, "bench")
	if err != nil {
		return err
	}
	defer pc.Close()
	l, err := rpcserve.Listen(ctx, srv, "")
	if err != nil {
		return err
	}
	defer l.Close()
	wc, err := rpcclient.Dial(ctx, l.URL, l.Token(), "bench")
	if err != nil {
		return err
	}
	defer wc.Close()
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:bench"}
	routes := []struct {
		name string
		chat api.LocalChat
	}{{"直接", benchChat{}}, {"管道", rpcclient.NewFull(pc)}, {"WebSocket", rpcclient.NewFull(wc)}}
	fmt.Printf("## 4. 流式首字：调用 SendLocal 到第一次草稿回调（假模型请求到达即吐字，每路 %d 次）\n", n)
	fmt.Println("| 路 | 首字中位数 | 首字 p95 | 整轮（6 段，间隔 10 ms）中位数 |")
	fmt.Println("|---|---|---|---|")
	for _, rt := range routes {
		var firsts, totals []time.Duration
		for range 3 { // 预热，不计
			rt.chat.SendLocal(ctx, key, "预热", func(api.LocalDraft) {})
		}
		for range n {
			var first atomic.Int64
			t0 := tick()
			err := rt.chat.SendLocal(ctx, key, "量首字", func(api.LocalDraft) { first.CompareAndSwap(0, int64(tick()-t0)+1) })
			if err != nil {
				return fmt.Errorf("%s 发送：%w", rt.name, err)
			}
			totals = append(totals, tick()-t0)
			firsts = append(firsts, time.Duration(first.Load()-1))
		}
		fmt.Printf("| %s | %s | %s | %s |\n", rt.name, us(pct(firsts, 50)), us(pct(firsts, 95)), ms(pct(totals, 50)))
	}
	return nil
}

func pct(d []time.Duration, p int) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s) == 0 {
		return 0
	}
	return s[(len(s)-1)*p/100]
}

// us 用微秒报：首字的差别在几十微秒，毫秒两位小数看不出
func us(d time.Duration) string { return fmt.Sprintf("%d µs", d.Microseconds()) }

func ms(d time.Duration) string { return fmt.Sprintf("%.2f ms", float64(d.Microseconds())/1000) }
