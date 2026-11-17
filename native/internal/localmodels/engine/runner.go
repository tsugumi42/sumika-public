package engine

import (
	"context"
	"fmt"
	"strings"

	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/tokenizer"
)

// Tokenizer 是引擎要用的分词器（*tokenizer.Tokenizer 满足它；测试用假的）。
type Tokenizer interface {
	EncodeSingle(text string, maxTokens int) ([]int, error)
	EncodePair(query, document string, maxTokens int) ([]int, error)
	Count(text string) (int, error)
}

var _ Tokenizer = (*tokenizer.Tokenizer)(nil)

// rowLock 是每个模型一把的锁，只围住一行的 Run（规格 2.5"锁"一行，复核后改，和 server.py 整请求持锁不同）：
// 后台补向量的大批不该把守卫的单句挤掉，单句最多等一行的时间。容量 1 的 channel 当锁，
// 等锁时也看 ctx，先到先得（Go 的 channel 等待队列是先进先出）。
type rowLock chan struct{}

func newRowLock() rowLock { return make(rowLock, 1) }

func (l rowLock) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil { // 已经取消了就别去抢：select 在两边都能走时是随机选的
		return err
	}
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l rowLock) release() { <-l }

// runner 管一个会话：校验图的输入输出、摆张量、一次一行地跑、取回点名的那个输出。
// 一次前向只有一行，形状 [1, n]，不补 pad：量化图对批次大小和 padding 敏感（server.py 的 _forward 注释、规格 2.5）。
type runner struct {
	sess   ort.Session
	lock   rowLock
	feeds  []string // 图声明的输入里要喂的，按图里的顺序
	output string   // 要取的输出
}

// newRunner 核对会话的输入输出。图的输入只认 input_ids、attention_mask、position_ids（int64）：
// 带 past_key_values.* 的图（Qwen 那种）这一版没实现，别的名字也不认，加载时就报错，不留到第一次请求。
// outputs 是按优先顺序的候选名，取第一个图里有的。
func newRunner(id string, sess ort.Session, outputs ...string) (*runner, error) {
	if sess == nil {
		return nil, fmt.Errorf("%s: 没有会话", id)
	}
	r := &runner{sess: sess, lock: newRowLock()}
	hasIDs := false
	for _, in := range sess.Inputs() {
		switch {
		case in.Name == "input_ids" || in.Name == "attention_mask" || in.Name == "position_ids":
			if in.Type != ort.TypeInt64 {
				return nil, fmt.Errorf("%s: 输入 %s 的类型是 %s，这里只会喂 int64", id, in.Name, in.Type)
			}
			hasIDs = hasIDs || in.Name == "input_ids"
			r.feeds = append(r.feeds, in.Name)
		case strings.HasPrefix(in.Name, "past_key_values."):
			return nil, fmt.Errorf("%s: 图要喂 %s，这一版没实现带 KV 缓存的模型", id, in.Name)
		default:
			return nil, fmt.Errorf("%s: 不认识的模型输入 %s", id, in.Name)
		}
	}
	if !hasIDs {
		return nil, fmt.Errorf("%s: 图没有 input_ids 输入", id)
	}
	have := map[string]bool{}
	for _, o := range sess.Outputs() {
		have[o.Name] = true
	}
	for _, name := range outputs {
		if have[name] {
			r.output = name
			break
		}
	}
	if r.output == "" {
		return nil, fmt.Errorf("%s: 图里没有 %s，取不到要用的输出", id, strings.Join(outputs, " / "))
	}
	return r, nil
}

// run 跑一行。摆张量在锁外，锁里只有 Run；ctx 取消了就不跑（已经在跑的那一行 Run 中途打断不了，跑完再停）。
func (r *runner) run(ctx context.Context, ids []int) (ort.FloatTensor, error) {
	n := len(ids)
	if n == 0 {
		return ort.FloatTensor{}, fmt.Errorf("一行前向没有 token")
	}
	shape := []int64{1, int64(n)}
	inputs := make([]ort.Int64Tensor, 0, len(r.feeds))
	for _, name := range r.feeds {
		data := make([]int64, n)
		for i := range data {
			switch name {
			case "input_ids":
				data[i] = int64(ids[i])
			case "attention_mask":
				data[i] = 1
			case "position_ids":
				data[i] = int64(i)
			}
		}
		inputs = append(inputs, ort.Int64Tensor{Name: name, Shape: shape, Data: data})
	}
	if err := r.lock.acquire(ctx); err != nil {
		return ort.FloatTensor{}, err
	}
	out, err := r.runLocked(inputs)
	if err != nil {
		return ort.FloatTensor{}, err
	}
	t, ok := out[r.output]
	if !ok {
		return ort.FloatTensor{}, fmt.Errorf("前向的结果里没有 %s", r.output)
	}
	return t, nil
}

// runLocked 在持锁时跑 Run，锁用 defer 放开：会话的 Run 万一 panic（HTTP 层 recover 之后进程还活着），
// 行锁不能跟着永远占下去，不然这个模型以后每个请求都排在一把没人放的锁后面。
func (r *runner) runLocked(inputs []ort.Int64Tensor) (map[string]ort.FloatTensor, error) {
	defer r.lock.release()
	return r.sess.Run(inputs, []string{r.output})
}
