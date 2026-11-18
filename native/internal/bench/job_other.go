//go:build !windows

package bench

import (
	"os/exec"
	"time"
)

// 别的系统上只跑、不量子孙（表二只在 Windows 上跑；这里让包能编译、CI 能跑别的测试）：CPU 只算直接起的那个进程

// Job 是起了、还没等完的一个进程
type Job struct{ cmd *exec.Cmd }

// StartInJob 起 cmd
func StartInJob(cmd *exec.Cmd, _ time.Duration) (*Job, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Job{cmd: cmd}, nil
}

// Wait 等进程结束
func (j *Job) Wait() (Usage, error) {
	err := j.cmd.Wait()
	var u Usage
	if s := j.cmd.ProcessState; s != nil {
		u.CPUMs = (s.UserTime() + s.SystemTime()).Milliseconds()
		u.Processes = 1
	}
	return u, err
}

// RunInJob 是 StartInJob + Wait
func RunInJob(cmd *exec.Cmd, interval time.Duration) (Usage, error) {
	j, err := StartInJob(cmd, interval)
	if err != nil {
		return Usage{}, err
	}
	return j.Wait()
}
