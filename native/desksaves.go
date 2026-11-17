package main

import "sync"

// deskSaves 在后台按调用顺序写桌面设置，界面线程不等。
// 每次改设置各起一个协程的话，连点两下可能后点的先写完，文件里留下的是前一个值；也没法等它们写完（退出、测试收尾）。
type deskSaves struct {
	mu      sync.Mutex
	queue   []func()
	running bool
	done    chan struct{} // 有人在等时才建，排空时关掉
}

// add 排进队列，后台一个一个写。
func (q *deskSaves) add(save func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queue = append(q.queue, save)
	if !q.running {
		q.running = true
		go q.run()
	}
}

func (q *deskSaves) run() {
	for {
		q.mu.Lock()
		if len(q.queue) == 0 {
			q.running = false
			if q.done != nil {
				close(q.done)
				q.done = nil
			}
			q.mu.Unlock()
			return
		}
		save := q.queue[0]
		q.queue = q.queue[1:]
		q.mu.Unlock()
		save()
	}
}

// wait 等已经排队的都写完。
func (q *deskSaves) wait() {
	q.mu.Lock()
	if !q.running {
		q.mu.Unlock()
		return
	}
	if q.done == nil {
		q.done = make(chan struct{})
	}
	done := q.done
	q.mu.Unlock()
	<-done
}
