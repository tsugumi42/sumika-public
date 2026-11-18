package watchdog

import (
	"os"
	"os/exec"
	"sync"
)

// execChild 是真的子进程
type execChild struct {
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	code int
}

// StartExec 拉起 exe。env 是在当前环境之上追加的几项（KEY=VALUE）；子进程的标准输入输出都不接
func StartExec(exe string, args, env []string) (Child, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &execChild{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.code = -1
		if cmd.ProcessState != nil {
			c.code = cmd.ProcessState.ExitCode()
		}
		c.mu.Unlock()
		_ = err
		close(c.done)
	}()
	return c, nil
}

func (c *execChild) Pid() int              { return c.cmd.Process.Pid }
func (c *execChild) Done() <-chan struct{} { return c.done }
func (c *execChild) Kill() error           { return c.cmd.Process.Kill() }
func (c *execChild) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}
