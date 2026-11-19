package linktranslate

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultCacheSize 是缓存条数上限。
const DefaultCacheSize = 500

// UnneededTTL 是“不用翻”结论的保留时间：过了就重新问模型（ADR-391）；
// 成功译文照旧不设期限，靠容量淘汰。
const UnneededTTL = 24 * time.Hour

type entry struct {
	key  string
	text string    // 空串：模型判过不用翻
	at   time.Time // 写进来的时候
}

// Cache 是内存 LRU。键里有干净链接、目标语言、翻译模型和原文哈希，换模型或原文变了自然不命中；失败不进缓存。
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

// Key 算缓存键：干净链接、目标语言、翻译模型 ID、原文哈希。
func Key(cleanURL, target, model, text string) string {
	h := sha256.Sum256([]byte(text))
	return cleanURL + "\x00" + target + "\x00" + model + "\x00" + hex.EncodeToString(h[:8])
}

// Get 返回 (译文, 是否命中)；命中且译文为空说明“不用翻”。
// “不用翻”只保留 UnneededTTL（ADR-391），过期的当没命中并删掉；成功译文不过期。
func (c *Cache) Get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byID[key]
	if !ok {
		return "", false
	}
	e := el.Value.(*entry)
	if e.text == "" && now.Sub(e.at) >= UnneededTTL {
		c.ll.Remove(el)
		delete(c.byID, key)
		return "", false
	}
	c.ll.MoveToFront(el)
	return e.text, true
}

func (c *Cache) Put(key, text string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[key]; ok {
		e := el.Value.(*entry)
		e.text, e.at = text, now
		c.ll.MoveToFront(el)
		return
	}
	c.byID[key] = c.ll.PushFront(&entry{key: key, text: text, at: now})
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
