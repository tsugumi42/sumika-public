package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"sumika-native/internal/procreg"
)

// inheritedEnv 是子进程从 Sumika 继承的环境变量：够 npx、uvx、node、python 找到用户目录、缓存和临时目录，不带 API 密钥之类。
// 思路和官方 MCP SDK、Claude Desktop 的默认继承集一样；别的变量要在配置的 env 里自己写。
var inheritedEnv = []string{"SystemRoot", "WINDIR", "ComSpec", "PATH", "PATHEXT", "TEMP", "TMP",
	"APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "USERNAME", "USERDOMAIN", "SystemDrive",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData", "CommonProgramFiles", "CommonProgramFiles(x86)", "ALLUSERSPROFILE",
	"PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS", "OS", "HOME", "LOGNAME", "USER", "SHELL", "LANG"}

type processTransport struct {
	config ServerConfig
	diag   *startDiag
	logDir string // 空 = 不写 stderr 日志文件
	// onTree 在关连接时被告知“进程树没能确认退出”（强杀失败、等不到全部退出）；管道已经关了之类的小错不报。空 = 不通知
	onTree func(error)
}

// startDiag 记下启动这一次的线索：自己的启动错误、进程退出情况、stderr 末尾。握手失败时拼成原因给用户看。
type startDiag struct {
	mu        sync.Mutex
	launchErr error
	exited    atomic.Value // 进程退出后才有；值是 exitInfo
	stderr    tailBuffer
}

type exitInfo struct{ err error }

func (d *startDiag) setLaunch(err error) {
	d.mu.Lock()
	d.launchErr = err
	d.mu.Unlock()
}

// reason 是握手没成时写给人看的原因，一句话到三句话，凭据值抹掉。
func (d *startDiag) reason(cfg ServerConfig, timedOut bool) string {
	d.mu.Lock()
	launch := d.launchErr
	d.mu.Unlock()
	var parts []string
	switch {
	case launch != nil:
		parts = append(parts, redact(launch.Error(), cfg))
	case timedOut:
		parts = append(parts, fmt.Sprintf("等了 %d 秒还没握手成功（第一次用 npx、uvx 要先下载包，可以在这个服务里写 sumika.startupTimeoutMs 调大）", cfg.Sumika.StartupTimeoutMs/1000))
	default:
		parts = append(parts, "它启动了但没有按 MCP 协议回话（可能不是 MCP 服务，或者启动参数不对）")
	}
	if x, ok := d.exited.Load().(exitInfo); ok && launch == nil {
		if x.err != nil {
			parts = append(parts, "进程已退出："+x.err.Error())
		} else {
			parts = append(parts, "进程已正常退出")
		}
	}
	if tail := redact(d.stderr.String(), cfg); tail != "" {
		parts = append(parts, "它最后说的是："+tail)
	}
	return strings.Join(parts, "；")
}

// tailBuffer 只留 stderr 的最后几 KB。
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

const tailKeep = 4096

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailKeep {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-tailKeep:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

var secretish = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+|basic\s+|token\s+)?)\S+`),
	regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)\S+`),
	regexp.MustCompile(`(?i)(\b(?:token|secret|password|passwd|api[-_ ]?key|access[-_ ]?key)\s*[:=]\s*)\S+`),
}

// redact 把配置里的凭据值（env、请求头的值）和看着像密钥的字抹成 ***，去掉控制字符，只留末尾几行、最多 400 个字。
func redact(text string, cfg ServerConfig) string {
	text = scrub(text, cfg)
	var kept []string
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > 4 {
		kept = kept[len(kept)-4:]
	}
	out := strings.Join(kept, " ⏎ ")
	if r := []rune(out); len(r) > 400 {
		out = "…" + string(r[len(r)-400:])
	}
	return out
}

// scrub 把配置里的凭据值（env、请求头的值）和看着像密钥的字抹成 ***，去掉换行和制表以外的控制字符；
// 握手失败的原因（redact）和 stderr 日志文件（stderrLog）共用这一套。
func scrub(text string, cfg ServerConfig) string {
	for _, m := range []map[string]string{cfg.Env, cfg.Headers} {
		for _, v := range m {
			if len(v) >= 3 {
				text = strings.ReplaceAll(text, v, "***")
			}
		}
	}
	for _, re := range secretish {
		text = re.ReplaceAllString(text, "${1}***")
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}

// resolveProgram 把配置里的 command 变成一个能启动的文件：绝对路径要存在；光写程序名（npx、uvx、node）按 PATH 和 PATHEXT 找，
// 和 Codex（rmcp-client 的 program_resolver）、Claude Desktop 一样。找不到说清楚缺什么。
func resolveProgram(command string) (string, error) {
	if filepath.IsAbs(command) {
		info, err := os.Stat(command)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("找不到程序 %s（路径不对或已经被删了）", command)
		}
		return command, nil
	}
	path, err := exec.LookPath(command)
	if err != nil {
		if errors.Is(err, exec.ErrDot) {
			return "", fmt.Errorf("命令 %q 只在当前目录里找到，不会从那里运行；写完整路径，或把它放进 PATH", command)
		}
		return "", fmt.Errorf("找不到命令 %q：电脑的 PATH 里没有它。npx 要装 Node.js、uvx 要装 uv，装好后重启接收；也可以在 command 里写它的完整路径", command)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, nil
}

func (t *processTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	cfg := t.config
	if t.diag == nil {
		t.diag = &startDiag{}
	}
	fail := func(err error) (mcp.Connection, error) {
		t.diag.setLaunch(err)
		return nil, err
	}
	program, err := resolveProgram(cfg.Command)
	if err != nil {
		return fail(err)
	}
	cmd, err := launchCommand(program, cfg.Args)
	if err != nil {
		return fail(err)
	}
	cmd.Dir = cfg.CWD
	env := map[string]string{}
	for _, name := range inheritedEnv {
		if v, ok := os.LookupEnv(name); ok {
			env[strings.ToUpper(name)] = name + "=" + v
		}
	}
	for k, v := range cfg.Env {
		if strings.ContainsAny(k+v, "\x00\r\n") {
			return fail(fmt.Errorf("MCP 环境变量 %q 的值里有换行或空字符", k))
		}
		env[strings.ToUpper(k)] = k + "=" + v
	}
	for _, v := range env {
		cmd.Env = append(cmd.Env, v)
	}
	// 不进主日志，防服务器回显凭据：末尾几 KB 留在内存里，握手失败时抹掉凭据再给用户看原因；完整的另写进单独的日志文件（也抹过凭据）。stdout 只交给 SDK 解码。
	// 另外按服务写一份带时间、抹过凭据的日志文件（stderrlog.go）；进程结束、启动失败时收尾
	elog := newStderrLog(t.logDir, cfg)
	cmd.Stderr = io.MultiWriter(&t.diag.stderr, elog)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		elog.Close()
		return fail(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		elog.Close()
		return fail(err)
	}
	group, err := startProcess(cmd)
	if err != nil {
		elog.Close()
		return fail(fmt.Errorf("启动 %s 失败：%w", filepath.Base(program), err))
	}
	conn, err := (&mcp.IOTransport{Reader: stdout, Writer: stdin, MaxLineLength: 16 << 20}).Connect(ctx)
	if err != nil {
		group.kill()
		cmd.Wait()
		group.close()
		elog.Close()
		return fail(err)
	}
	// 登记到运行页的进程表：标签只写配置里的服务 id，不带命令行和路径；进程退出就撤销
	unregister := procreg.Register(procreg.CatMCP, cfg.ID, cmd.Process.Pid)
	done := make(chan error, 1)
	diag := t.diag
	go func() {
		err := cmd.Wait()
		elog.Close() // Wait 返回时 stderr 已经读完
		diag.exited.Store(exitInfo{err})
		done <- err
		unregister()
	}()
	return &processConnection{Connection: conn, group: group, done: done, onTree: t.onTree}, nil
}

type processConnection struct {
	mcp.Connection
	group *processGroup
	done  chan error
	once  sync.Once
	err   error
	// onTree 见 processTransport.onTree
	onTree func(error)
}

// treeConfirm 等整棵进程树退出（最多 3 秒）；测试里换成会失败的，验证“旧进程没能确认退出”这条路
var treeConfirm = func(g *processGroup) error { return g.waitExit(3 * time.Second) }

func (c *processConnection) Close() error {
	c.once.Do(func() {
		closeErr := c.Connection.Close()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		var treeErr error
		select {
		case <-c.done:
		case <-timer.C:
			if err := c.group.kill(); err != nil {
				c.err, treeErr = err, err
			}
			<-c.done
		}
		// 即使根正常结束也收掉还留在 Job 的孙进程。
		killErr := c.group.kill()
		waitErr := treeConfirm(c.group)
		c.err = errors.Join(c.err, closeErr, killErr, waitErr, c.group.close())
		// 只有“进程树没能确认退出”才要通知：管道已经关了（closeErr）这类小错不代表进程还在
		if treeErr = errors.Join(treeErr, killErr, waitErr); treeErr != nil && c.onTree != nil {
			c.onTree(treeErr)
		}
	})
	return c.err
}
