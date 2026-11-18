package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
)

// DialContextFunc 允许测试替换拨号，不用占真实的 7720 端口。
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type StartupReason string

const (
	LegacyPortOccupied StartupReason = "legacy_port_occupied"
	LegacyPortUnknown  StartupReason = "legacy_port_unknown"
)

// StartupError 在打开任何业务库或接收组件前返回。
// Code 给桌面端用，含义保持稳定；Cause 保留原始错误，方便诊断。
type StartupError struct {
	Code     StartupReason
	Endpoint string
	Cause    error
}

func (e *StartupError) Error() string {
	if e.Code == LegacyPortOccupied {
		return fmt.Sprintf("启动受阻 [%s]：旧后台端口 %s 能连接，请先停止旧后台", e.Code, e.Endpoint)
	}
	return fmt.Sprintf("启动受阻 [%s]：旧后台端口 %s 探测超时或连接出错，无法确认已停写", e.Code, e.Endpoint)
}

func (e *StartupError) Unwrap() error { return e.Cause }

const legacyProbeTimeout = 500 * time.Millisecond

// checkLegacyListeners 持有 backend.lock 时只探测一次，先探测再开库。
// TCP 握手成功就够了，不发 HTTP、不查进程、不轮询。
// 只有明确拒绝连接才算没有监听；其他错误一律阻止启动。
func (a *App) checkLegacyListeners(ctx context.Context) error {
	ports := a.opt.ProbePorts
	if ports == nil {
		ports = []int{7720, a.cfg.Console.Port}
	}
	if len(ports) == 0 {
		return fmt.Errorf("%w：启动探测端口不能为空", ErrArguments)
	}
	dial := a.opt.DialContext
	if dial == nil {
		dialer := &net.Dialer{Timeout: legacyProbeTimeout}
		dial = dialer.DialContext
	}
	for i, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%w：启动探测端口不合法", ErrArguments)
		}
		duplicate := false
		for _, previous := range ports[:i] {
			if previous == port {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		probe, cancel := context.WithTimeout(ctx, legacyProbeTimeout)
		conn, err := dial(probe, "tcp", endpoint)
		cancel()
		if conn != nil {
			conn.Close()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err == nil {
			return &StartupError{Code: LegacyPortOccupied, Endpoint: endpoint}
		}
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, errConnRefused) {
			continue
		}
		return &StartupError{Code: LegacyPortUnknown, Endpoint: endpoint, Cause: err}
	}
	return nil
}

// receiveProbeTimeout 是自动接收前试连协议端的时限：只做一次 TCP 握手，不连真服务、不发 WebSocket
const receiveProbeTimeout = 2 * time.Second

// CheckReceiveReady 是自动接收在拆掉本地后台之前做的便宜检查：号在配置里、有 URL、取得到令牌，
// 协议端地址能连上（TCP 连一下就断，2 秒）。令牌和 URL 只用于检查，错误里不回显令牌。
// 它不建库、不改数据目录、不装适配器；检查过了才值得关掉本地可用去开完整接收。
func CheckReceiveReady(ctx context.Context, opt Options, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	location, err := ResourceRoot(opt.Root)
	if err != nil {
		return err
	}
	cfg, err := config.LoadIn(location.Dir, opt.DataDir)
	if err != nil {
		return fmt.Errorf("读配置：%w", err)
	}
	dataDir := opt.DataDir
	if dataDir == "" {
		dataDir = cfg.DataDirIn(location.Dir)
	}
	p, err := paths.Open(dataDir)
	if err != nil {
		return err
	}
	acct, err := pickAccount(cfg, account)
	if err != nil {
		return err
	}
	raw := strings.TrimSpace(acct.URL)
	if raw == "" {
		return fmt.Errorf("账号 %s 没有写 qq.accounts[].url：这一版不自动找协议端地址", config.MaskID(string(acct.ID)))
	}
	if _, err := tokenFor(p, acct); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("账号 %s 的 qq.accounts[].url 读不懂", config.MaskID(string(acct.ID)))
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("账号 %s 的 qq.accounts[].url 只认 ws:// 或 wss://", config.MaskID(string(acct.ID)))
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	}
	endpoint := net.JoinHostPort(u.Hostname(), port)
	dial := opt.DialContext
	if dial == nil {
		d := &net.Dialer{Timeout: receiveProbeTimeout}
		dial = d.DialContext
	}
	probe, cancel := context.WithTimeout(ctx, receiveProbeTimeout)
	defer cancel()
	conn, err := dial(probe, "tcp", endpoint)
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		return fmt.Errorf("协议端地址连不上：%w", err)
	}
	return nil
}
