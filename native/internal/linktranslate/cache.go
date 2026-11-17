package linktranslate

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// DefaultCacheSize 是缓存条数上限。
const DefaultCacheSize = 500

type entry struct {
	key  string
	text string // 空串：模型判过不用翻
}

// Cache 是内存 LRU。键里有干净链接、目标语言和原文哈希，配置或原文变了自然不命中；失败不进缓存。
type Cache struct {
	mu   sync.Mutex
	max  int
	ll   *list.List
	byID map[string]*list.Element
}

func NewCache(max int) *Cache {
	if max <= 0 {
		max = DefaultCacheSize
	}
	return &Cache{max: max, ll: list.New(), byID: map[string]*list.Element{}}
}

// Key 算缓存键。
func Key(cleanURL, target, text string) string {
	h := sha256.Sum256([]byte(text))
	return cleanURL + "\x00" + target + "\x00" + hex.EncodeToString(h[:8])
}

// Get 返回 (译文, 是否命中)；命中且译文为空说明“不用翻”。
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byID[key]
	if !ok {
		return "", false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry).text, true
}

func (c *Cache) Put(key, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[key]; ok {
		el.Value.(*entry).text = text
		c.ll.MoveToFront(el)
		return
	}
	c.byID[key] = c.ll.PushFront(&entry{key, text})
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.byID, last.Value.(*entry).key)
	}
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
