// Command snowluma-record 连一个 OneBot 正向 WS 端点，把收到的每一帧原样录成 JSONL。
// 默认只收不发；给 -act 时改成"核验以后发一个动作、录它的回包"（act.go），不给 -send 只核验不发。
//
// 录真实帧要主人点头、每样单独问（规格-渠道中间件第十九节 A10，ADR-187 第 8 条）；
// 第 6 步只写这个工具和它的测试（假协议端），真实数据由主会话在主人开着 SnowLuma 时跑。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/channel/snowluma"
)

func main() {
	var (
		url      = flag.String("url", "", "OneBot 正向 WS 地址（例如 ws://127.0.0.1:3001/）")
		token    = flag.String("token", "", "access token；不会打印出来")
		tokenEnv = flag.String("token-env", "", "从哪个环境变量取 token（-token 为空时用）")
		out      = flag.String("out", "", "写到哪个文件；默认 data/dev/snowluma-frames/<年月日-时分秒>.jsonl")
		forDur   = flag.Duration("for", 30*time.Minute, "录多久就停（Ctrl+C 也可以停）")
		actPath  = flag.String("act", "", "要发的一个动作（JSON 文件：action、params）；不给就只收不发")
		account  = flag.String("account", "", "-act 时必填：期望协议端登录的号，对不上不发")
		send     = flag.Bool("send", false, "-act 时真发；不给只核验、打印要发什么")
		wait     = flag.Duration("wait", 15*time.Second, "-act 发出去以后再录多久")
	)
	flag.Parse()
	if strings.TrimSpace(*url) == "" {
		fmt.Fprintln(os.Stderr, "要给 -url（OneBot 正向 WS 地址）")
		os.Exit(2)
	}
	if err := snowluma.CheckURL(*url); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	tok := *token
	if tok == "" && *tokenEnv != "" {
		tok = os.Getenv(*tokenEnv)
	}
	path := *out
	if path == "" {
		path = defaultOut(time.Now())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *forDur > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *forDur)
		defer cancel()
	}
	if *actPath != "" {
		act, err := loadAct(*actPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if strings.TrimSpace(*account) == "" {
			fmt.Fprintln(os.Stderr, "-act 要同时给 -account（期望登录的号）")
			os.Exit(2)
		}
		n, err := actOnce(ctx, snowluma.Config{URL: *url, Token: tok}, act, actOptions{account: *account, send: *send, wait: *wait}, path, os.Stdout)
		fmt.Fprintln(os.Stdout, endLine(n, path))
		if err != nil {
			fmt.Fprintln(os.Stderr, "出错:", scrub(err.Error(), tok))
			os.Exit(1)
		}
		return
	}
	n, err := recordFor(ctx, snowluma.Config{URL: *url, Token: tok}, path, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "录帧出错:", scrub(err.Error(), tok))
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, endLine(n, path))
}

// recordFor 连上去一直录到 ctx 取消（-for 到点或 Ctrl+C）；断了按和适配器一样的退避重连。
// 只收不发：除了 WebSocket 握手和协议层的 ping/pong，不发任何帧。每帧一行 JSONL、写完就落盘
// （文件不缓冲，进程被杀最多丢半行）。文件已经有了就接着往后写，不清空（同一个 -out 再录一次不会把上次的冲掉）。
// 没有的目录先建。返回录了几帧。
func recordFor(ctx context.Context, cfg snowluma.Config, path string, progress io.Writer) (int, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("建目录 %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("建文件 %s: %w", path, err)
	}
	defer f.Close()
	// 上次录到一半被杀、最后一行没写完：先补一个换行，这次的第一帧不和那半行粘成一行坏的
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err == nil && last[0] != '\n' {
			if _, err := f.Write([]byte("\n")); err != nil {
				return 0, fmt.Errorf("写 %s: %w", path, err)
			}
		}
	}
	fmt.Fprintln(progress, startLine(cfg, path))

	n := 0
	backoff := snowluma.DefaultBackoff
	for ctx.Err() == nil {
		c, err := snowluma.Dial(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if !reconnect(ctx, progress, "连不上", err, cfg.Token, &backoff) {
				break
			}
			continue
		}
		backoff = snowluma.DefaultBackoff
		var readErr error
		for {
			frame, err := c.Read(ctx)
			if err != nil {
				readErr = err
				break
			}
			if _, err := f.Write(line(time.Now(), frame)); err != nil {
				c.Close()
				return n, fmt.Errorf("写 %s: %w", path, err)
			}
			n++
		}
		c.Close()
		if ctx.Err() != nil {
			break
		}
		if !reconnect(ctx, progress, "连接断了", readErr, cfg.Token, &backoff) {
			break
		}
	}
	return n, nil
}

// reconnect 是掉线以后的等待：打一行、按退避等一等。返回 false 表示 ctx 取消了。
func reconnect(ctx context.Context, progress io.Writer, what string, err error, token string, backoff *time.Duration) bool {
	fmt.Fprintf(progress, "%s，约 %s 后重连: %s\n", what, waitText(*backoff), scrub(err.Error(), token))
	if !sleep(ctx, snowluma.Jitter(*backoff)) {
		return false
	}
	*backoff = snowluma.NextBackoff(*backoff)
	return true
}

// waitText 是给输出看的等待时长（实际带抖动，所以只说个范围）。
func waitText(d time.Duration) string {
	return (d / 2).Round(time.Millisecond).String() + "～" + d.String()
}

// sleep 等到 d 过完；ctx 取消就立刻返回 false。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// line 是一行 JSONL：at 是本地收到这一帧的时刻，frame 是原帧（原样嵌进去，一个字节都不改）。
// 原帧不是 JSON 时按字符串存，不丢，也不让这一行坏掉。
func line(at time.Time, frame []byte) []byte {
	var b bytes.Buffer
	b.WriteString(`{"at":"`)
	b.WriteString(at.Format(time.RFC3339Nano))
	b.WriteString(`","frame":`)
	if json.Valid(frame) {
		b.Write(frame)
	} else {
		q, _ := json.Marshal(string(frame))
		b.Write(q)
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// startLine 是启动那一行：地址和文件照给，令牌只写"***"。
func startLine(cfg snowluma.Config, path string) string {
	return fmt.Sprintf("在录：%s（token %s）→ %s", scrub(cfg.URL, cfg.Token), maskToken(cfg.Token), path)
}

func maskToken(token string) string {
	if token == "" {
		return "没设"
	}
	return "***"
}

// scrub 把令牌从文字里抹掉：地址和错误里万一带了令牌，任何输出都不能漏出去。
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

// endLine 是结束时那一行：录了几帧、存在哪。
func endLine(n int, path string) string {
	return fmt.Sprintf("录了 %d 帧，存在 %s", n, path)
}

// defaultOut 是没给 -out 时的文件名：data/dev/snowluma-frames/<年月日-时分秒>.jsonl（data/ 不进 git）。
func defaultOut(now time.Time) string {
	return filepath.Join("data", "dev", "snowluma-frames", now.Format("20060102-150405")+".jsonl")
}
