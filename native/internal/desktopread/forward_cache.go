package desktopread

import (
	"container/list"
	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

type forwardBriefKey struct {
	id string
	at int64
}
type forwardBriefEntry struct {
	key   forwardBriefKey
	brief *api.ForwardBrief
}
type forwardBriefCache struct {
	items map[forwardBriefKey]*list.Element
	order list.List
}

func (c *forwardBriefCache) get(r store.ForwardRecord) *api.ForwardBrief {
	key := forwardBriefKey{r.Tree.ID, r.Fetched}
	if e := c.items[key]; e != nil {
		c.order.MoveToFront(e)
		return e.Value.(forwardBriefEntry).brief
	}
	brief := forwardBrief(r)
	if c.items == nil {
		c.items = make(map[forwardBriefKey]*list.Element)
	}
	if len(c.items) == 2000 {
		last := c.order.Back()
		delete(c.items, last.Value.(forwardBriefEntry).key)
		c.order.Remove(last)
	}
	c.items[key] = c.order.PushFront(forwardBriefEntry{key, brief})
	return brief
}
