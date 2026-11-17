package store

import (
	"encoding/json"
	"strings"
)

// localPartQuoteIDs 取主人分块消息（segments.local_parts，规格-引用）里各引用块指向的事件 id，按块的顺序。
func localPartQuoteIDs(segments string) []string {
	if !strings.Contains(segments, `"local_parts"`) {
		return nil
	}
	var seg struct {
		Parts []struct {
			Quote *struct {
				ID string `json:"id"`
			} `json:"quote"`
		} `json:"local_parts"`
	}
	if json.Unmarshal([]byte(segments), &seg) != nil {
		return nil
	}
	var ids []string
	for _, p := range seg.Parts {
		if p.Quote != nil && p.Quote.ID != "" {
			ids = append(ids, p.Quote.ID)
		}
	}
	return ids
}
