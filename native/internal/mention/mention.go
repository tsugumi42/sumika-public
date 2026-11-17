// Package mention 是“按被 @ 的对象激活谁”的纯函数（规格-群聊引用和路由 6.1）：不依赖存储和模型，
// 本地群聊的 @ 解析和悄悄话（引用 = 主人对角色说的悄悄话）共用它。
// 权限只按角色 ID 认，名字只用来选候选。包里不 import 项目里别的包（archcheck 有测试）。
package mention

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Candidate 是这个会话里可以被唤起的一位角色。
type Candidate struct{ ID, Name string }

// Block 是一次发送里的一块引用卡的对象：Mentions 是角色 ID，All 表示“所有人”。
type Block struct {
	Mentions []string
	All      bool
}

// Plan 是路由结果：Targets 是要唤起的角色（按第一次出现排序、去重）；Blocks[角色 ID] 是指到她的块序号（从 0 起，升序）。
type Plan struct {
	Targets []string
	Blocks  map[string][]int
}

var (
	ErrEmptyTarget   = errors.New("没有指定对象")
	ErrNotCandidate  = errors.New("不是这个会话里能被唤起的角色")
	ErrOwner         = errors.New("对象不能是主人自己")
	ErrNoCandidates  = errors.New("这个会话里没有可以唤起的角色")
	ErrAmbiguousName = errors.New("有同名角色，请使用角色 ID")
)

// BlockError 指出是第几块的什么对象有问题；消息以“第 N 块：”开头，界面据此标在那张卡上（N 从 1 起）。
type BlockError struct {
	Block int // 从 0 起
	ID    string
	Err   error
}

func (e *BlockError) Error() string {
	if e.ID == "" {
		return fmt.Sprintf("第 %d 块：%v", e.Block+1, e.Err)
	}
	return fmt.Sprintf("第 %d 块：%q %v", e.Block+1, e.ID, e.Err)
}
func (e *BlockError) Unwrap() error { return e.Err }

// ownerIDs 是主人在身份里的写法：不是角色，不能被指到。
var ownerIDs = []string{"owner", "主人"}

// Resolve 把一次发送的各块对象展开成要唤起的角色。任何一块不合格就整个拒绝（不做一半）。
// 每个角色只出现一次，被多块指到时 Blocks 里记下所有块。“所有人”按候选顺序展开，不设人数上限。
func Resolve(blocks []Block, candidates []Candidate) (Plan, error) {
	ids := candidateIDs(candidates)
	plan := Plan{Blocks: map[string][]int{}}
	add := func(id string, block int) {
		if _, seen := plan.Blocks[id]; !seen {
			plan.Targets = append(plan.Targets, id)
		}
		if bs := plan.Blocks[id]; len(bs) == 0 || bs[len(bs)-1] != block {
			plan.Blocks[id] = append(bs, block)
		}
	}
	for i, b := range blocks {
		if len(b.Mentions) == 0 && !b.All {
			return Plan{}, &BlockError{Block: i, Err: ErrEmptyTarget}
		}
		for _, m := range b.Mentions {
			switch {
			case strings.TrimSpace(m) == "":
				return Plan{}, &BlockError{Block: i, Err: ErrEmptyTarget}
			case slices.Contains(ownerIDs, m):
				return Plan{}, &BlockError{Block: i, ID: m, Err: ErrOwner}
			case !slices.Contains(ids, m):
				return Plan{}, &BlockError{Block: i, ID: m, Err: ErrNotCandidate}
			}
			add(m, i)
		}
		if b.All {
			if len(ids) == 0 {
				return Plan{}, &BlockError{Block: i, Err: ErrNoCandidates}
			}
			for _, id := range ids {
				add(id, i)
			}
		}
	}
	return plan, nil
}

// ResolveToken 把正文里 @ 后面的一个词（角色 ID 或名字）对到候选：名字只用来选，结果是 ID。
// 没有对上返回空串和 nil（不是 @ 角色的词）；同名多于一个返回 ErrAmbiguousName。
func ResolveToken(token string, candidates []Candidate) (string, error) {
	if token == "" {
		return "", nil
	}
	var matches []string
	for _, c := range candidates {
		if token == c.ID || token == c.Name {
			matches = append(matches, c.ID)
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("@%s %w", token, ErrAmbiguousName)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", nil
}

func candidateIDs(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		if c.ID != "" && !slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out
}
