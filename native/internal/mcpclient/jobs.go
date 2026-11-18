package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
)

// Job 是一次超过等待时限、转到后台继续跑的 MCP 调用。
// 它用自己的 context（父运行寿命 + 取消键）跑，取消即断开那一次请求；归属按会话身份认。
type Job struct {
	ID     string
	owner  kernel.Scope
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	result    *mcp.CallToolResult
	err       error
	cancelled bool
	// Out 由 onDone 回调在 done 关闭前写入，用来放调用方存好的结果引用等。
	Out any
}

func (j *Job) Done() <-chan struct{} { return j.done }

// State 是 running / done / failed / cancelled。
func (j *Job) State() string {
	select {
	case <-j.done:
	default:
		return "running"
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.cancelled:
		return "cancelled"
	case j.err != nil:
		return "failed"
	}
	return "done"
}

func (j *Job) Outcome() (*mcp.CallToolResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.err
}

const finishedJobsKept = 64

// Start 把已审批的调用放进后台：parent 是父运行寿命（不是工具调用的短 context），Lifetime 登记活动，排空会等它。
// onDone 在调用结束、done 关闭前执行（可为 nil），返回值放进 Job.Out。
func (c *Client) Start(p *ResolvedCall, inv kernel.Invocation, parent context.Context, lt kernel.Lifetime, onDone func(*mcp.CallToolResult, error) any) (*Job, error) {
	jobCtx, cancel := context.WithCancel(kernel.WithInvocation(parent, inv))
	jobCtx, activity, err := lt.Begin(jobCtx, inv)
	if err != nil {
		cancel()
		return nil, err
	}
	j := &Job{ID: ecosystem.ID(), owner: p.access.Scope(), cancel: cancel, done: make(chan struct{})}
	c.mu.Lock()
	if c.jobs == nil {
		c.jobs = map[string]*Job{}
	}
	c.jobs[j.ID] = j
	if len(c.jobs) > finishedJobsKept {
		for id, old := range c.jobs {
			select {
			case <-old.done:
				delete(c.jobs, id)
			default:
			}
		}
	}
	c.mu.Unlock()
	go func() {
		result, err := p.Execute(jobCtx)
		j.mu.Lock()
		j.result, j.err = result, err
		j.mu.Unlock()
		if onDone != nil {
			out := onDone(result, err)
			j.mu.Lock()
			j.Out = out
			j.mu.Unlock()
		}
		close(j.done)
		cancel()
		activity.End(err)
	}()
	return j, nil
}

// Job 按会话身份找后台调用；不存在和别人的返回同一个 nil。
func (c *Client) Job(scope kernel.Scope, id string) *Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j := c.jobs[id]; j != nil && j.owner == scope {
		return j
	}
	return nil
}

var ErrNoJob = errors.New("没有这个后台调用")

// CancelJob 取消还在跑的后台调用：context 取消会断开那一次请求，等它真的退出（最多 wait）再返回状态。
// 已经结束的不动，原样报告最终状态。
func (c *Client) CancelJob(scope kernel.Scope, id string, wait time.Duration) (string, error) {
	j := c.Job(scope, id)
	if j == nil {
		return "", ErrNoJob
	}
	select {
	case <-j.done:
		return j.State(), nil
	default:
	}
	j.mu.Lock()
	j.cancelled = true
	j.mu.Unlock()
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(wait):
		return "", fmt.Errorf("已发出取消，但调用还没退出，稍后再查")
	}
	return j.State(), nil
}

func (c *Client) cancelAllJobs() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, j := range c.jobs {
		j.cancel()
	}
}
