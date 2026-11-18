package channel

import "sync"

// lanes 是"按键排队"：同一个键的活一个接一个做，不同键的互不等待（规格-渠道中间件 第六节第 4 步：按（渠道、会话）一条队列）。
// Bun 用一个全局队列（qq.ts:100，并发 1），一个会话慢了所有会话都堵；NoneBot 每个事件一个任务、没有排队，水位推不准（清单⑬ be786e20）。
//
// 每个有活的键有自己的 goroutine，活做完就退出，不常驻。关闭以后不再收新活，排着没开始的丢掉（调用方拿到通知），已经在做的做完。
type lanes struct {
	mu      sync.Mutex
	lane    map[string]*lane
	closed  bool
	pending int           // 收下了、还没做完（也没丢掉）的活
	idle    chan struct{} // pending 为 0 时是关着的反面：nil 表示现在就闲着
	running sync.WaitGroup
}

type lane struct {
	jobs []job
}

// job 是一件活：run 在这个键的队列里做；drop 在它被丢掉时调（关闭时排着没开始的），让等结果的人知道没做
type job struct {
	run  func()
	drop func()
}

func newLanes() *lanes { return &lanes{lane: map[string]*lane{}} }

// submit 把活排进 key 的队列。已经关闭返回 false（活没有收下，drop 不会被调用）
func (q *lanes) submit(key string, j job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	l := q.lane[key]
	start := l == nil
	if start {
		l = &lane{}
		q.lane[key] = l
	}
	l.jobs = append(l.jobs, j)
	if q.pending == 0 {
		q.idle = make(chan struct{})
	}
	q.pending++
	if start {
		q.running.Add(1)
		go q.work(key, l)
	}
	return true
}

func (q *lanes) work(key string, l *lane) {
	defer q.running.Done()
	for {
		q.mu.Lock()
		if q.closed {
			// 排着没开始的丢掉。在这里丢而不是在 close 里丢：close 不能碰正在做的这一件，丢的时候要按顺序、每件只丢一次
			dropped := l.jobs
			l.jobs = nil
			delete(q.lane, key)
			q.mu.Unlock()
			for _, j := range dropped {
				j.drop()
				q.done()
			}
			return
		}
		if len(l.jobs) == 0 {
			delete(q.lane, key)
			q.mu.Unlock()
			return
		}
		j := l.jobs[0]
		l.jobs = l.jobs[1:]
		q.mu.Unlock()
		j.run()
		q.done()
	}
}

// done 是一件活做完（或被丢掉）了
func (q *lanes) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending--
	if q.pending == 0 {
		close(q.idle)
		q.idle = nil
	}
}

// idleChan 返回一个在"所有收下的活都做完"时关闭的通道；现在就闲着，返回已经关闭的
func (q *lanes) idleChan() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == 0 {
		c := make(chan struct{})
		close(c)
		return c
	}
	return q.idle
}

// isClosed 队列是不是已经关闭（测试等 close 生效用）
func (q *lanes) isClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

// stop 只关入口，正在做的由 idleChan 或 close 等完。
func (q *lanes) stop() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}

// close 不再收新活；等正在做的做完、排着的丢完再返回
func (q *lanes) close() {
	q.stop()
	q.running.Wait()
}
