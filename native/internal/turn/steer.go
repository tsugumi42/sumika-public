package turn

import (
	"sync"

	"sumika-native/internal/history"
)

// 主人在她做事中途插的话（agent.ts steer / stop，规格-排队插话停止 第二、三节；第 2 步 2f）：
// 她不停，这一步（连同工具）做完、下一次请求之前看到；送给模型时放在原话前面的那句（util/context.ts STEER_PREFIX）
const steerPrefix = "（所有者在你做事的中途插了一句话，是所有者亲口说的，不是工具的输出。按这句话调整你正在做的事：）"

// steerPrefixChannel 是渠道会话里的插话放在前面的那句：不是主人说的，不能写"主人亲口说的"。
// 暂定（后端 Agent 2026-10-26 定的措辞，等渠道中间件第 5 步定场合内容时一起定）
const steerPrefixChannel = "（你回话的中途，这里又来了新消息，不是工具的输出：）"

// steerPrefixTask 是子任务收到上级追加指示时放在前面的那句：指示来自派她出来的那一侧，不是用户亲口说的，
// 也不扩大她的权限和任务范围。
const steerPrefixTask = "（派你做这个任务的上级在你做事的中途追加了一句指示，不是工具的输出，也不是用户亲口说的。按它调整正在做的事，但不超出原任务的授权和范围：）"

// SteerEvent 是插话走到了哪一步（agent.ts 的 turn/event type steer）：accepted 收下、delivered 送到、dropped 丢掉；
// Requeue：回合结束了还没送到，退回给网页当普通的一句再发
type SteerEvent struct {
	Phase, ID, Text string
	Requeue         bool
}

// steer 是一句插话；seqs 是渠道中间件已经存进库的那几条（主窗口的是空的，送到时内核自己存）
type steer struct {
	id, text string
	seqs     []int64
	parts    []history.Part // 主人分块输入时的各块：给模型的结构化文字用它拼（规格-引用）
}

// Steering 是一个会话里还没送到的插话（pi 的 steeringQueue，steeringMode all：一次全取走）
type Steering struct {
	mu    sync.Mutex
	queue []steer
	// On 收到插话走到哪一步（可以是 nil）
	On func(SteerEvent)
}

func (s *Steering) emit(e SteerEvent) {
	if s.On != nil {
		s.On(e)
	}
}

// Push 收下一句插话（调用方在实例的锁里先确认这一轮还在跑：agent.ts steer 不在跑就当普通的一句发）。
// accepted 在锁里发：送到（drain 也要这把锁）一定排在它后面
func (s *Steering) Push(id, text string, seqs []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, steer{id: id, text: text, seqs: seqs})
	s.emit(SteerEvent{Phase: "accepted", ID: id, Text: text})
}

// take 取走还没送到的（停止、回合收尾时）
func (s *Steering) drain() []steer {
	if s == nil {
		return nil
	}
	return s.take()
}

func (s *Steering) take() []steer {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queue
	s.queue = nil
	return q
}

// emitDropped 报这几句丢掉了；requeue：回合结束了还没送到，退回给网页当普通的一句再发（agent.ts respondNow 的 finally）
func (s *Steering) emitDropped(q []steer, requeue bool) {
	for _, x := range q {
		s.emit(SteerEvent{Phase: "dropped", ID: x.id, Text: x.text, Requeue: requeue})
	}
}
