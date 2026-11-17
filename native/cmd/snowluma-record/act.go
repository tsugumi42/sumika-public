package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/channel/snowluma"
)

// 发一个动作录回包（规格-QQ发送上线 3.4、第十一节第 8 步）：连上以后先照第四节核验登录号和
// 收发健康，再把 -act 文件里的那一个动作写出去，接着把这段时间收到的每一帧照常录下来，到 -wait 为止。
// 不重连、不重试：一次只发一个，发完就走。不给 -send 时只核验、打印要发什么，一个动作也不发。
// 真发之前每一条都要主人在对话里点头（ADR-187 第 8 条），这里不替人判断能不能发。

// actEcho 是这次动作的 echo；核验的两个查询用别的 echo，回包不会认错。
const (
	actEcho    = "sumika-act-1"
	loginEcho  = "sumika-act-login"
	statusEcho = "sumika-act-status"
	queryLimit = 10 * time.Second
)

// actFile 是 -act 指的文件：OneBot 的 action 名和参数，原样发出去。号码只在这个文件里（放 data/dev 或 scratchpad，不进 git）。
type actFile struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

type actOptions struct {
	account string        // 期望登录的号（小号）；对不上就不发
	send    bool          // 没有就只核验、不发
	wait    time.Duration // 发出去以后再录多久
}

func loadAct(path string) (actFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return actFile{}, err
	}
	var a actFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return actFile{}, fmt.Errorf("读 %s: %w", path, err)
	}
	if strings.TrimSpace(a.Action) == "" {
		return actFile{}, fmt.Errorf("%s 里没有 action", path)
	}
	if len(a.Params) == 0 || !json.Valid(a.Params) || a.Params[0] != '{' {
		return actFile{}, fmt.Errorf("%s 的 params 要是一个 JSON 对象", path)
	}
	return a, nil
}

// actOnce 连一次、核验、（-send 时）发一个动作，把收到的帧和发出去的那一帧都录进 path。
// 返回录了几帧（含发出去的那一帧）。核验没过、连不上都返回错误，此时一个动作也没发。
func actOnce(ctx context.Context, cfg snowluma.Config, act actFile, opt actOptions, path string, progress io.Writer) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	c, err := snowluma.Dial(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	rctx, stopRead := context.WithCancel(ctx)
	defer stopRead()

	// 读循环：每帧落盘，带 echo 的回包另外交给等它的人
	var mu sync.Mutex // 读循环和发送都往文件写，n 也两边加
	n := 0
	record := func(b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if _, err := f.Write(b); err != nil {
			return err
		}
		n++
		return nil
	}
	replies := make(chan []byte, 16)
	readDone := make(chan error, 1)
	go func() {
		for {
			frame, err := c.Read(rctx)
			if err != nil {
				readDone <- err
				return
			}
			if err := record(line(time.Now(), frame)); err != nil {
				readDone <- err
				return
			}
			if echoOf(frame) != "" {
				select {
				case replies <- frame:
				default:
				}
			}
		}
	}()
	finish := func() int {
		stopRead()
		<-readDone
		mu.Lock()
		defer mu.Unlock()
		return n
	}

	call := func(action string, params any, echo string) (map[string]any, error) {
		b, err := json.Marshal(map[string]any{"action": action, "params": params, "echo": echo})
		if err != nil {
			return nil, err
		}
		if err := c.Write(ctx, b); err != nil {
			return nil, err
		}
		t := time.NewTimer(queryLimit)
		defer t.Stop()
		for {
			select {
			case fr := <-replies:
				if echoOf(fr) != echo {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal(fr, &m); err != nil {
					return nil, err
				}
				return m, nil
			case err := <-readDone:
				readDone <- err
				return nil, fmt.Errorf("等 %s 回包时连接断了: %w", action, err)
			case <-t.C:
				return nil, fmt.Errorf("%s 等了 %s 没回包", action, queryLimit)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	// 第四节：号对得上、online 且 good 才发。不打印两边的号
	login, err := call("get_login_info", map[string]any{}, loginEcho)
	if err != nil {
		return finish(), err
	}
	if got, ok := loginID(login); !ok || got != opt.account {
		return finish(), errors.New("协议端登录的号和 -account 对不上，没发")
	}
	st, err := call("get_status", map[string]any{}, statusEcho)
	if err != nil {
		return finish(), err
	}
	if !healthy(st) {
		return finish(), errors.New("协议端报收发不健康（online / good 不是 true），没发")
	}
	fmt.Fprintln(progress, "核验过了：登录号对得上，收发健康")

	if !opt.send {
		fmt.Fprintf(progress, "没给 -send，不发。要发的是 %s（参数见 -act 文件）\n", act.Action)
		return finish(), nil
	}
	out, err := json.Marshal(map[string]any{"action": act.Action, "params": act.Params, "echo": actEcho})
	if err != nil {
		return finish(), err
	}
	// 发出去的那一帧也录下来，标 dir:"out"，回放时分得清哪帧是我们发的
	if err := record(outLine(time.Now(), out)); err != nil {
		return finish(), err
	}
	if err := c.Write(ctx, out); err != nil {
		return finish(), fmt.Errorf("写 %s 失败（可能没发出去）: %w", act.Action, err)
	}
	fmt.Fprintf(progress, "已发 %s，再录 %s\n", act.Action, opt.wait)
	got := false
	t := time.NewTimer(opt.wait)
	defer t.Stop()
	for {
		select {
		case fr := <-replies:
			if echoOf(fr) == actEcho && !got {
				got = true
				fmt.Fprintln(progress, replySummary(fr))
			}
			continue
		case <-readDone:
			readDone <- nil
		case <-t.C:
		case <-ctx.Done():
		}
		break
	}
	if !got {
		fmt.Fprintln(progress, "录完了也没等到这个动作的回包")
	}
	return finish(), nil
}

func echoOf(frame []byte) string {
	var m struct {
		Echo json.RawMessage `json:"echo"`
	}
	if json.Unmarshal(frame, &m) != nil || len(m.Echo) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(m.Echo, &s) == nil {
		return s
	}
	return string(m.Echo)
}

// loginID 取 get_login_info 回包的 user_id，按十进制字符串比（和适配器一样，不去前导零）。
func loginID(m map[string]any) (string, bool) {
	d, _ := m["data"].(map[string]any)
	switch v := d["user_id"].(type) {
	case float64:
		if v <= 0 || v != float64(int64(v)) {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case string:
		return v, v != ""
	}
	return "", false
}

func healthy(m map[string]any) bool {
	d, _ := m["data"].(map[string]any)
	return d["online"] == true && d["good"] == true
}

// replySummary 只说回包的形状（status、retcode、data 里有哪些键），不打印号码和内容；全文在录下的文件里。
func replySummary(frame []byte) string {
	var m map[string]any
	if json.Unmarshal(frame, &m) != nil {
		return "回包不是 JSON（全文在文件里）"
	}
	keys := []string{}
	if d, ok := m["data"].(map[string]any); ok {
		for k := range d {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return fmt.Sprintf("回包：status=%v retcode=%v data 的键=%v", m["status"], m["retcode"], keys)
}

func outLine(at time.Time, frame []byte) []byte {
	b := line(at, frame)
	// line 的结尾是 "}\n"；插一个 dir 字段
	return append(append(b[:len(b)-2:len(b)-2], `,"dir":"out"}`...), '\n')
}
