package engine

import (
	"container/list"
	"sync"
)

// lru 是每个模型的向量缓存（server.py 的 OrderedDict，256 条）：键是加了前缀的文本，值是归一化后的全维向量和 token 数。
// 有自己的小锁，不借模型锁；取出和放进都拷贝，调用方拿到的切片和缓存里的互不相干
// （截断 dimensions 在拷贝上做，先要 128 维再要全维不会拿到被改过的缓存）。
type lru struct {
	mu  sync.Mutex
	cap int
	ll  *list.List // 最近用的在前
	m   map[string]*list.Element
}

type lruEntry struct {
	key    string
	vec    []float32
	tokens int
}

func newLRU(capacity int) *lru {
	return &lru{cap: capacity, ll: list.New(), m: make(map[string]*list.Element, capacity)}
}

// get 命中时把它挪到最前，返回向量的拷贝和 token 数。
func (c *lru) get(key string) ([]float32, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, 0, false
	}
	c.ll.MoveToFront(el)
	e := el.Value.(*lruEntry)
	return append([]float32(nil), e.vec...), e.tokens, true
}

// put 放进去的是 vec 的拷贝；超过容量就丢最久没用的。
func (c *lru) put(key string, vec []float32, tokens int) {
	cp := append([]float32(nil), vec...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		e := el.Value.(*lruEntry)
		e.vec, e.tokens = cp, tokens
		c.ll.MoveToFront(el)
	} else {
		c.m[key] = c.ll.PushFront(&lruEntry{key: key, vec: cp, tokens: tokens})
	}
	for c.ll.Len() > c.cap {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.m, last.Value.(*lruEntry).key)
	}
}

func (c *lru) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	clear(c.m)
}

func (c *lru) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
