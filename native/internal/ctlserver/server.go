// Package ctlserver 是桌面程序的本机控制接口的服务端（规格-本机控制接口）：在一条只给当前用户的命名管道上，
// 一次连接收一个请求、回一个回复，把命令交给 Backend（桌面程序里接的是托盘用的同一批函数）。
//
// 这里是"谁能让程序做什么"的唯一关口，所以参数全在这一层校验，Backend 拿到的已经是收紧过的值：
//   - gate set 只许 off、dry，而且必须一字不差（speak、SPEAK、" speak"、空、别的值一律 invalid）；
//   - gate 只认 qq:group:<群号> 或群句柄；checkpoint 只认本地主窗口私聊，恢复仍经过桌面单次审批；
//   - 群号不往外给：gate list 和 gate set 的回复里是打码的会话和句柄；
//   - 不接受任意 SQL、任意文件路径、任意配置键：命令表是写死的，没有"自由参数"。
//
// 不开任何网络端口。管道的安全描述只给当前用户，go-winio 建管道时固定带拒绝远程客户端的标志（listen_windows.go）。
package ctlserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/ctlproto"
)

// Backend 是桌面程序交给控制接口的一组操作。每个方法对应托盘上能点的一件事，实现不另写一套逻辑。
// 失败用 *ctlproto.Error 带上错误码（readonly、demo、timeout……）；别的错误一律按 failed
type Backend interface {
	Status(ctx context.Context) (ctlproto.Status, error)
	// ReceiveStart wait 为 0：立即回当时的阶段；大于 0：等阶段落定（接收中 / 受阻 / 回到只看历史），最多等这么久
	ReceiveStart(ctx context.Context, wait time.Duration) (ctlproto.Receive, error)
	ReceiveStop(ctx context.Context) (ctlproto.Receive, error)
	ShowWindow(ctx context.Context) error
	Logs(ctx context.Context, n int, f ctlproto.LogFilter) (ctlproto.Logs, error)
	GateList(ctx context.Context) (ctlproto.GateList, error)
	// GateSet 拿到的 mode 只会是 "off" 或 "dry"，conversation 只会是 "qq:group:<数字>"（本层校验过）
	GateSet(ctx context.Context, conversation, mode string) (ctlproto.GateChange, error)
	SetAutoReceive(ctx context.Context, on bool) error
	SetAutostart(ctx context.Context, on bool) error
	// Quit 在"正在退出"的回复发出去、连接关掉以后才调
	Quit()
}

// SDDL 是只给一个用户的安全描述：保护的 DACL（不继承）、只有一条允许项，给这个 SID 全部权限。
// 别的用户、Administrators 组、SYSTEM 都不在里面；管理员要用也得是同一个用户
func SDDL(sid string) string { return "D:P(A;;GA;;;" + sid + ")" }

// Options 服务端的选项
type Options struct {
	// Demo：演示数据模式。receive、gate、auto-receive 回 code demo，不碰后端
	Demo bool
	Logf func(string, ...any) // nil = 不记日志
	// IOTimeout 读请求、写回复各自的时限；零值用协议里的 5 秒。测试缩短
	IOTimeout time.Duration
	// HandleKey 给群句柄算 HMAC 的本机密钥（见 gatehandle.go）。桌面程序给设置文件里存着的那个（重启不变）；
	// nil 用进程里随机生成的一个（测试用）
	HandleKey func() ([]byte, error)
	// AcceptBackoff：收连接出错后第一次等多久再试，之后每次加倍；零值 50 毫秒。测试缩短
	AcceptBackoff time.Duration
	// CloseGrace：Close 以后，还没做完的连接再等这么久就强行关掉；零值 2 秒
	CloseGrace time.Duration
}

// maxAcceptFailures 是连着收不到连接几次以后放弃（中间有一次收到就从头数）。按默认退避（50 毫秒起每次加倍，最多 1 秒）合计等 3 秒多
const maxAcceptFailures = 8

// Server 一个控制接口。Serve 在一个监听器上收连接，Close 不等在途的处理（可以在界面线程调）
type Server struct {
	b   Backend
	opt Options

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	ln     net.Listener
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

// New 建服务端；还没开始收连接
func New(b Backend, opt Options) *Server {
	if opt.IOTimeout <= 0 {
		opt.IOTimeout = ctlproto.IOTimeout
	}
	if opt.HandleKey == nil {
		opt.HandleKey = defaultHandleKey()
	}
	if opt.AcceptBackoff <= 0 {
		opt.AcceptBackoff = 50 * time.Millisecond
	}
	if opt.CloseGrace <= 0 {
		opt.CloseGrace = 2 * time.Second
	}
	s := &Server{b: b, opt: opt, conns: map[net.Conn]struct{}{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

func (s *Server) logf(format string, args ...any) {
	if s.opt.Logf != nil {
		s.opt.Logf(format, args...)
	}
}

// Serve 在 ln 上收连接，直到 Close（返回 nil）或监听器坏了（返回错误）。一个服务端只服务一个监听器
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return nil
	}
	s.ln = ln
	s.mu.Unlock()
	failures := 0
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// 偶发的错误（比如建下一个管道实例时资源不足）：退避后接着收，不让控制接口从此没人接；
			// 连着出错到上限才放弃，放弃时把监听器也关掉（不留一个有名字却没人接的管道）
			failures++
			if failures > maxAcceptFailures {
				ln.Close()
				return err
			}
			s.logf("控制接口：收连接出错，稍后重试（第 %d 次）：%v", failures, err)
			wait := min(s.opt.AcceptBackoff<<(failures-1), time.Second)
			select {
			case <-time.After(wait):
			case <-s.ctx.Done():
				return nil
			}
			continue
		}
		failures = 0
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

// Close 退出流程第一步：不再收新连接，在途的处理收到取消（它们回"桌面程序正在退出"）。不等处理做完（Wait 才等）；
// 过了 CloseGrace 还没关的连接强行关掉。可以重复调用
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	s.cancel()
	if ln != nil {
		ln.Close()
	}
	time.AfterFunc(s.opt.CloseGrace, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for c := range s.conns {
			c.Close()
		}
	})
}

// Wait 等在途的处理都做完，最多等 d；返回是不是都做完了
func (s *Server) Wait(d time.Duration) bool {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func (s *Server) handle(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	conn.SetReadDeadline(time.Now().Add(s.opt.IOTimeout))
	line, err := ctlproto.ReadLine(bufio.NewReaderSize(conn, 4096), ctlproto.MaxRequestBytes)
	var reply ctlproto.Reply
	var after func()
	switch {
	case errors.Is(err, ctlproto.ErrTooLong):
		reply = fail(ctlproto.CodeInvalid, fmt.Sprintf("请求超过 %d KB", ctlproto.MaxRequestBytes>>10))
		s.logEarly(reply)
	case err != nil:
		return // 对方没发完就断了或超时：没有请求可回
	default:
		conn.SetReadDeadline(time.Time{}) // 命令在宿主里做多久，照实等
		reply, after = s.serve(line)
	}
	conn.SetWriteDeadline(time.Now().Add(s.opt.IOTimeout))
	if err := ctlproto.WriteJSON(conn, reply); err != nil {
		s.logf("控制接口：回复写不出去：%v", err)
	}
	conn.Close() // 先关，再做 after：quit 的客户端拿到回复就能走
	if after != nil {
		after()
	}
}

// serve 解一行请求、校验、交给后端，返回回复和回复发出以后才做的事（quit）
func (s *Server) serve(line []byte) (reply ctlproto.Reply, after func()) {
	var head struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return s.early(fail(ctlproto.CodeInvalid, "请求不是一行 JSON 对象"))
	}
	if head.V == nil || *head.V != ctlproto.Version {
		return s.early(fail(ctlproto.CodeVersion, fmt.Sprintf("协议版本对不上：这边是 %d，请更新 sumika-ctl 或桌面程序", ctlproto.Version)))
	}
	var req ctlproto.Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return s.early(fail(ctlproto.CodeInvalid, "请求里有不认识的字段或类型不对"))
	}
	if req.Cmd == ctlproto.CmdBackfill {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return s.early(fail(ctlproto.CodeInvalid, "补录请求格式错误"))
		}
		for _, name := range []string{"wait", "n", "cursor"} {
			if raw, present := fields[name]; present {
				if string(raw) == "null" || (name == "n" && req.N == 0) || (name == "wait" && req.Wait == "") {
					return s.early(fail(ctlproto.CodeInvalid, "补录请求包含无效的可选参数"))
				}
			}
		}
	}
	defer func() {
		if r := recover(); r != nil {
			s.logf("控制接口：处理 %s 时出了意外：%v", req.Cmd, r)
			reply, after = fail(ctlproto.CodeFailed, "桌面程序处理这条命令时出了意外"), nil
		}
	}()
	return s.dispatch(req)
}

var (
	// 群会话只认这一种写法：渠道、群、纯数字的群号，不带空白、不带别的段
	groupConv = regexp.MustCompile(`^qq:group:[0-9]{1,20}$`)
)

func fail(code, msg string) ctlproto.Reply { return ctlproto.Reply{Code: code, Error: msg} }

func invalid(format string, args ...any) (ctlproto.Reply, func()) {
	return fail(ctlproto.CodeInvalid, fmt.Sprintf(format, args...)), nil
}

func success(data any) ctlproto.Reply {
	if data == nil {
		return ctlproto.Reply{OK: true}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return fail(ctlproto.CodeFailed, "回复编不出来")
	}
	return ctlproto.Reply{OK: true, Data: b}
}

// fromErr 后端的错误变成回复：带码的原样用（failed、timeout 除外，见下），别的按 failed；服务端正在关时说正在退出
func (s *Server) fromErr(err error) ctlproto.Reply {
	var ce *ctlproto.Error
	if errors.As(err, &ce) {
		// 退出开始以后，"做不成"（failed、timeout）的真正原因是程序在退出，不是命令自己带的那句（比如退出时被取消的
		// receive start 会带着上一个阶段的原因）；readonly、invalid、demo 这些和退出无关的码照原样
		if s.ctx.Err() != nil && (ce.Code == ctlproto.CodeFailed || ce.Code == ctlproto.CodeTimeout) {
			return fail(ctlproto.CodeFailed, "桌面程序正在退出")
		}
		r := fail(ce.Code, ctlproto.Clean(ce.Msg))
		if ce.Data != nil {
			data := ce.Data
			if rc, ok := data.(ctlproto.Receive); ok {
				data = cleanReceive(rc)
			}
			if cr, ok := data.(ctlproto.ChatResult); ok {
				data = cleanChat(cr)
			}
			if b, merr := json.Marshal(data); merr == nil {
				r.Data = b
			}
		}
		return r
	}
	if s.ctx.Err() != nil {
		return fail(ctlproto.CodeFailed, "桌面程序正在退出")
	}
	return fail(ctlproto.CodeFailed, ctlproto.Clean(err.Error()))
}

func (s *Server) demo(cmd string) (ctlproto.Reply, bool) {
	if s.opt.Demo {
		return fail(ctlproto.CodeDemo, "这是演示数据模式，没有后台，"+cmd+"做不了"), true
	}
	return ctlproto.Reply{}, false
}

// dispatch 办一个请求，并给会改状态的请求记一行"做了什么、结果怎样"。记在办完以后，被拒的也记：
// 事后要查"有没有人（或被提示词注入的 Agent）试过让群开口"，日志里得有被拒的那一笔，而且和成功的分得开
func (s *Server) dispatch(req ctlproto.Request) (ctlproto.Reply, func()) {
	reply, after := s.route(req)
	if !readOnly(req) && !(req.Cmd == ctlproto.CmdBackfill && reply.OK) {
		s.logf("控制接口：%s → %s", describe(req), outcome(reply))
	}
	return reply, after
}

// early 请求读不懂（不是 JSON、版本不对、有不认识的字段）就被拒了，没走到命令表：也记一笔，只写固定的话
func (s *Server) early(r ctlproto.Reply) (ctlproto.Reply, func()) {
	s.logEarly(r)
	return r, nil
}

func (s *Server) logEarly(r ctlproto.Reply) {
	s.logf("控制接口：收到读不懂的请求 → %s", outcome(r))
}

// outcome 回复在日志里的写法：只用固定的词和我们自己的错误码，不带回复里的任何文字（那里面可能有后端交回来的外来文字）
func outcome(r ctlproto.Reply) string {
	if r.OK {
		return "成功"
	}
	switch r.Code {
	case ctlproto.CodeInvalid, ctlproto.CodeDenied, ctlproto.CodeDemo, ctlproto.CodeVersion:
		return "被拒（" + r.Code + "）"
	case ctlproto.CodeReadOnly, ctlproto.CodeFailed, ctlproto.CodeTimeout:
		return "没做成（" + r.Code + "）"
	}
	return "没做成"
}

// route 命令表。每条命令先把参数校验完，再碰后端；校验不过的一律不调后端
func (s *Server) route(req ctlproto.Request) (ctlproto.Reply, func()) {
	// 这两个字段只对各自的一条命令有意义，别处出现就是写错了，不悄悄忽略
	if req.Wait != "" && !(req.Cmd == ctlproto.CmdReceive && len(req.Args) == 1 && req.Args[0] == "start") && req.Cmd != ctlproto.CmdChat && !(req.Cmd == ctlproto.CmdWhisper && len(req.Args) == 1 && req.Args[0] == "create") && !(req.Cmd == ctlproto.CmdBackup && len(req.Args) >= 1 && (req.Args[0] == "now" || req.Args[0] == "restore" || req.Args[0] == "verify")) {
		return invalid("--wait 只用于 receive start、backup now|restore|verify（chat、whisper create 的 --timeout 也走这个字段）")
	}
	if req.Stats != nil && req.Cmd != ctlproto.CmdStats {
		return invalid("统计参数只用于 stats")
	}
	if req.Link != nil && req.Cmd != ctlproto.CmdLink {
		return invalid("--template、--site、--sample 只用于 link")
	}
	if req.Backup != nil && req.Cmd != ctlproto.CmdBackup {
		return invalid("--media、--to 只用于 backup")
	}
	if req.MCP != nil && req.Cmd != ctlproto.CmdMCP {
		return invalid("--reason、--accept-unknown-risk 只用于 mcp release")
	}
	if req.Artist != nil && req.Cmd != ctlproto.CmdArtist {
		return invalid("--platform、--status、--undo 只用于 artist")
	}
	if req.Loop != nil && req.Cmd != ctlproto.CmdLoop {
		return invalid("--every、--daily、--weekly、--at、--self、--new-chat、--same-chat、--notify 只用于 loop add 和 loop edit")
	}
	if req.Whisper != nil && req.Cmd != ctlproto.CmdWhisper {
		return invalid("--selftest、--source-text、--quote-name、--say 只用于 whisper create")
	}
	if req.WhisperLog != nil && req.Cmd != ctlproto.CmdWhisper {
		return invalid("--conversation、--batch、--entry、--with-thinking 只用于 whisper log")
	}
	if req.Secrets != nil && req.Cmd != ctlproto.CmdSecrets {
		return invalid("值、主密码、恢复码只用于 secrets")
	}
	isSlash := req.Cmd == ctlproto.CmdGoal || req.Cmd == ctlproto.CmdLoop || req.Cmd == ctlproto.CmdTrust
	if (req.Text != "" || req.New || (req.Character != "" && !isSlash)) && req.Cmd != ctlproto.CmdChat && !(req.Cmd == ctlproto.CmdWhisper && req.Text == "" && !req.New) {
		return invalid("--character、--new 和要说的话只用于 chat（--character 也用于 goal、loop、trust）")
	}
	if req.N != 0 && req.Cmd != ctlproto.CmdLogs && req.Cmd != ctlproto.CmdProactive && req.Cmd != ctlproto.CmdArtist && !(req.Cmd == ctlproto.CmdBackfill && len(req.Args) == 1 && req.Args[0] == "gaps") {
		return invalid("-n 只用于 logs、proactive log、artist list 或 backfill gaps")
	}
	if req.Cursor != nil && !(req.Cmd == ctlproto.CmdBackfill && len(req.Args) == 1 && req.Args[0] == "gaps") {
		return invalid("--cursor 只用于 backfill gaps")
	}
	ctx := s.ctx
	switch req.Cmd {
	case ctlproto.CmdDoctor:
		return s.doctor(req)
	case ctlproto.CmdCheckpoint:
		return s.checkpoint(req.Args)
	case ctlproto.CmdChat:
		return s.chat(req)
	case ctlproto.CmdWhisper:
		return s.whisper(req)
	case ctlproto.CmdGoal, ctlproto.CmdLoop, ctlproto.CmdTrust:
		return s.slash(req)
	case ctlproto.CmdStats:
		return s.stats(req)
	case ctlproto.CmdLink:
		return s.link(req)
	case ctlproto.CmdProactive:
		return s.proactive(req)
	case ctlproto.CmdArtist:
		return s.artist(req)
	case ctlproto.CmdBackup:
		return s.backup(req)
	case ctlproto.CmdKernel:
		return s.kernel(req)
	case ctlproto.CmdMCP:
		return s.mcp(req)
	case ctlproto.CmdDB:
		return s.db(req)
	case ctlproto.CmdSecrets:
		return s.secrets(req)
	case ctlproto.CmdStatus:
		if len(req.Args) != 0 {
			return invalid("status 不带参数")
		}
		d, err := s.b.Status(ctx)
		return s.result(cleanStatus(d), err), nil

	case ctlproto.CmdReceive:
		if len(req.Args) != 1 || (req.Args[0] != "start" && req.Args[0] != "stop") {
			return invalid("用法：receive start|stop")
		}
		var wait time.Duration
		if req.Wait != "" {
			d, err := time.ParseDuration(req.Wait)
			if err != nil || d <= 0 || d > ctlproto.MaxWait {
				return invalid("--wait 要写成大于 0、不超过 %v 的时长，比如 60s", ctlproto.MaxWait)
			}
			wait = d
		}
		if r, ok := s.demo("receive"); ok {
			return r, nil
		}
		var d ctlproto.Receive
		var err error
		if req.Args[0] == "start" {
			d, err = s.b.ReceiveStart(ctx, wait)
		} else {
			d, err = s.b.ReceiveStop(ctx)
		}
		return s.result(cleanReceive(d), err), nil

	case ctlproto.CmdWindow:
		if len(req.Args) != 1 || req.Args[0] != "show" {
			return invalid("用法：window show")
		}
		return s.result(nil, s.b.ShowWindow(ctx)), nil

	case ctlproto.CmdLogs:
		if len(req.Args) != 0 {
			return invalid("logs 不带参数，行数用 -n")
		}
		n := req.N
		if n == 0 {
			n = ctlproto.DefaultLogLines
		}
		if n < 0 || n > ctlproto.MaxLogLines {
			return invalid("-n 要在 1 到 %d 之间", ctlproto.MaxLogLines)
		}
		d, err := s.b.Logs(ctx, n, ctlproto.LogFilter{Level: req.Level, Module: req.Module})
		return s.result(cleanLogs(d), err), nil

	case ctlproto.CmdBackfill:
		return s.backfill(req)

	case ctlproto.CmdGate:
		return s.gate(req.Args)

	case ctlproto.CmdAutoReceive, ctlproto.CmdAutostart:
		on, ok := onOff(req.Args)
		if !ok {
			return invalid("用法：%s on|off", req.Cmd)
		}
		if req.Cmd == ctlproto.CmdAutoReceive {
			if r, ok := s.demo("auto-receive"); ok {
				return r, nil
			}
			if err := s.b.SetAutoReceive(ctx, on); err != nil {
				return s.fromErr(err), nil
			}
		} else if err := s.b.SetAutostart(ctx, on); err != nil {
			return s.fromErr(err), nil
		}
		return success(ctlproto.Toggle{On: on}), nil

	case ctlproto.CmdQuit:
		if len(req.Args) != 0 {
			return invalid("quit 不带参数")
		}
		return success(ctlproto.Quitting{Quitting: true}), s.b.Quit
	}
	return invalid("不认识的命令 %q", truncate(req.Cmd, 40))
}

func (s *Server) gate(args []string) (ctlproto.Reply, func()) {
	if len(args) == 1 && args[0] == "list" {
		if r, ok := s.demo("gate list"); ok {
			return r, nil
		}
		d, err := s.b.GateList(s.ctx)
		if err == nil {
			d, err = s.exposeGateList(d)
		}
		return s.result(d, err), nil
	}
	if len(args) == 3 && args[0] == "set" {
		ref, mode := args[1], args[2]
		// 档位：只许往"不开口"的方向调。speak 不进这个接口，哪种写法都不行（大小写、空白、别的值），留给主人在界面上亲手点
		if mode != "off" && mode != "dry" {
			if strings.EqualFold(strings.TrimSpace(mode), "speak") {
				return invalid("speak（让群真开口）不能通过命令行改，请在界面上亲手改；这里只能调成 off（只记录）或 dry（试运行）")
			}
			return invalid("档位只能是 off（只记录）或 dry（试运行）")
		}
		// 指哪个群：完整的 qq:group:<群号>，或者 gate list 给的句柄（8 个小写十六进制字符）。别的写法一律 invalid
		isConv, isHandle := groupConv.MatchString(ref), ctlproto.IsHandle(ref)
		if !isConv && !isHandle {
			return invalid("会话要写完整的群会话身份 qq:group:<群号>，或者 gate list 给的句柄（8 个小写十六进制字符）；打码的写法、私聊、本地会话和别的写法都不行")
		}
		if r, ok := s.demo("gate set"); ok {
			return r, nil
		}
		conv := ref
		if isHandle {
			var err error
			if conv, err = s.resolveHandle(s.ctx, ref); err != nil {
				return s.fromErr(err), nil
			}
		}
		d, err := s.b.GateSet(s.ctx, conv, mode)
		if err == nil {
			d, err = s.exposeGateChange(d)
		}
		return s.result(d, err), nil
	}
	return invalid("用法：gate list，或 gate set <qq:group:群号 或 gate list 给的句柄> off|dry")
}

func (s *Server) result(data any, err error) ctlproto.Reply {
	if err != nil {
		return s.fromErr(err)
	}
	return success(data)
}

func onOff(args []string) (on, ok bool) {
	if len(args) != 1 {
		return false, false
	}
	switch args[0] {
	case "on":
		return true, true
	case "off":
		return false, true
	}
	return false, false
}

// readOnly 是只读的请求：不改程序的任何状态
func readOnly(req ctlproto.Request) bool {
	switch req.Cmd {
	case ctlproto.CmdStatus, ctlproto.CmdDoctor, ctlproto.CmdLogs, ctlproto.CmdStats, ctlproto.CmdLink, ctlproto.CmdProactive:
		return true
	case ctlproto.CmdArtist:
		return len(req.Args) == 1 && req.Args[0] == "list" // 只有 list 只读；import、confirm、remove 会改表
	case ctlproto.CmdBackup:
		return len(req.Args) == 1 && req.Args[0] == "list" // backup now 会写备份目录
	case ctlproto.CmdKernel:
		return len(req.Args) == 1 && req.Args[0] == "faults" // kernel retry 会改库
	case ctlproto.CmdMCP:
		return len(req.Args) == 1 && req.Args[0] == "status" // mcp release 会改隔离记录
	case ctlproto.CmdDB:
		return len(req.Args) == 1 && req.Args[0] == ctlproto.DBStatusCmd // db status 只读
	case ctlproto.CmdSecrets:
		// 只有查看是只读；set、delete、set-passphrase、unlock 都会改密钥文件
		return len(req.Args) == 1 && (req.Args[0] == ctlproto.SecretsStatusCmd || req.Args[0] == ctlproto.SecretsListCmd)
	case ctlproto.CmdGate:
		return len(req.Args) == 1 && req.Args[0] == "list"
	case ctlproto.CmdBackfill:
		return len(req.Args) == 1 && (req.Args[0] == "status" || req.Args[0] == "gaps")
	case ctlproto.CmdCheckpoint:
		return len(req.Args) > 0 && (req.Args[0] == "list" || req.Args[0] == "preview" || req.Args[0] == "status")
	case ctlproto.CmdWhisper:
		return len(req.Args) == 2 && req.Args[0] == "status"
	case ctlproto.CmdGoal:
		return len(req.Args) == 0 || req.Args[0] == "show"
	case ctlproto.CmdLoop:
		return len(req.Args) == 0 || req.Args[0] == "list"
	}
	return false
}

// describe 日志里记的请求：只有认识的命令名和子命令，不带会话号、群号（后端自己写带打码的细节），
// 也不把请求里别人给的任何文字原样写进日志（换行能伪造日志行）
func describe(req ctlproto.Request) string {
	switch req.Cmd {
	case ctlproto.CmdStatus, ctlproto.CmdDoctor, ctlproto.CmdReceive, ctlproto.CmdWindow, ctlproto.CmdLogs, ctlproto.CmdGate,
		ctlproto.CmdAutoReceive, ctlproto.CmdAutostart, ctlproto.CmdQuit, ctlproto.CmdBackfill, ctlproto.CmdCheckpoint, ctlproto.CmdChat, ctlproto.CmdWhisper, ctlproto.CmdStats, ctlproto.CmdGoal, ctlproto.CmdLoop, ctlproto.CmdTrust, ctlproto.CmdLink, ctlproto.CmdProactive, ctlproto.CmdArtist, ctlproto.CmdBackup, ctlproto.CmdMCP, ctlproto.CmdDB, ctlproto.CmdKernel, ctlproto.CmdSecrets:
	default:
		return "收到不认识的命令"
	}
	d := req.Cmd
	if len(req.Args) > 0 {
		switch req.Args[0] {
		case "start", "stop", "show", "list", "set", "on", "off", "status", "gaps", "retry", "run", "preview", "restore", "usage", "edit", "add", "pause", "resume", "clear", "remove", "template", "log", "create", "cancel", "import", "confirm", "now", "release", "reconnect", "faults", "verify", "delete", "unlock", "set-passphrase":
			d += " " + req.Args[0]
		}
	}
	return d
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
