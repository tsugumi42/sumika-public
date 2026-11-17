package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/ecosystem"
)

type Request struct {
	Name, Resource, Query, Cursor string
	Limit                         int
	Listing                       bool
	DirectUser                    bool
}
type Item struct{ ID, Name, Description, Version string }
type Page struct {
	Source, Resource, Version string
	Start, End                int
	Complete                  bool
	Text                      string
	Items                     []Item               `json:",omitempty"`
	NextCursor                string               `json:",omitempty"`
	Disposition               *ecosystem.Finalizer `json:"-"`
	Valid                     func() bool          `json:"-"`
}

func (c *Catalog) allowed(ctx context.Context, a ecosystem.Access, e Entry) error {
	cap := "skill:builtin"
	if !e.embedded {
		cap = "skill:" + c.rootID(e)
	}
	return a.Check(ctx, cap)
}
func (c *Catalog) rootID(e Entry) string {
	for _, r := range c.opts.Roots {
		if e.ID == "source:"+ecosystem.Digest([]string{r.ID, r.Scope, e.dir}) {
			return r.ID
		}
	}
	return ""
}
func (c *Catalog) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.contexts[id]; a != nil {
		a.active = false
		delete(c.contexts, id)
	}
}
func (c *Catalog) Usage(id string) (used, reserved int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.contexts[id]; a != nil {
		return a.used, a.reserved
	}
	return 0, 0
}
func (c *Catalog) Read(ctx context.Context, a ecosystem.Access, contextID string, q Request) (Page, error) {
	if !c.opts.Enabled || contextID == "" {
		return Page{}, ecosystem.ErrDenied
	}
	if q.Limit < 0 || q.Name != "" && q.Listing || q.Name == "" && !q.Listing {
		return Page{}, fmt.Errorf("skill 参数分支错误")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var prev cursor
	if q.Cursor != "" {
		var ok bool
		prev, ok = c.cursors[q.Cursor]
		if !ok || prev.key != a.Key() || prev.listing != q.Listing {
			return Page{}, fmt.Errorf("cursor_invalidated")
		}
	}
	if q.Listing {
		return c.list(ctx, a, q, prev)
	}
	var e Entry
	found := false
	// 内置保留名只精确寻址；其他裸名沿扫描时的来源优先级。
	for _, x := range c.entries {
		if x.ID == q.Name || x.Name == q.Name && !strings.HasPrefix(q.Name, "builtin:") {
			e = x
			found = true
			break
		}
	}
	if !found || e.Disabled && !(q.DirectUser && e.UserInvocable) || c.allowed(ctx, a, e) != nil {
		return Page{}, ecosystem.ErrDenied
	}
	resource := q.Resource
	if resource == "" {
		resource = "SKILL.md"
	}
	if !safeResource(resource) {
		return Page{}, fmt.Errorf("资源必须是包内相对路径")
	}
	if q.Cursor != "" && (prev.entry != e.ID || prev.resource != resource || prev.limit != q.Limit) {
		return Page{}, fmt.Errorf("cursor_invalidated")
	}
	var data []byte
	var err error
	if e.embedded {
		data, err = readBuiltin(resource)
	} else {
		data, err = c.readSource(ctx, a, e.root, resource, false)
	}
	if err != nil {
		return Page{}, err
	}
	if !utf8.Valid(data) || len(data) > c.opts.FileMaxBytes {
		return Page{}, fmt.Errorf("非 UTF-8 或文件过大")
	}
	version := ecosystem.Digest(data)
	start := 0
	if q.Cursor != "" {
		if prev.version != version {
			return Page{}, fmt.Errorf("cursor_invalidated")
		}
		start = prev.offset
	}
	p := Page{Source: e.ID, Resource: resource, Version: version, Start: start}
	limit := q.Limit
	if limit == 0 {
		limit = c.opts.ReadBytes
	}
	limit = min(limit, c.opts.ReadBytes)
	// JSON 包装也计单次预算；cursor 固定长度，留给正文的字节数用实际序列化收紧。
	end := min(len(data), start+limit)
	for end > start && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	nextID := ecosystem.ID()
	for {
		p.End = end
		p.Text = string(data[start:end])
		p.Complete = end == len(data)
		if !p.Complete {
			p.NextCursor = nextID
		} else {
			p.NextCursor = ""
		}
		raw, _ := json.Marshal(p)
		if len(raw) <= limit {
			break
		}
		over := len(raw) - limit
		end = max(start, end-over)
		for end > start && end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
		if end == start {
			return Page{}, fmt.Errorf("预算装不下框架和下一个字符")
		}
	}
	if end == start && start < len(data) {
		return Page{}, fmt.Errorf("分页无法前进")
	}
	if err = c.allowed(ctx, a, e); err != nil {
		return Page{}, err
	}
	acc := c.contexts[contextID]
	if acc == nil {
		acc = &account{active: true}
		c.contexts[contextID] = acc
	}
	n := end - start
	if acc.used+acc.reserved+n > c.opts.ContextMaxBytes {
		return Page{}, fmt.Errorf("skill 上下文额度不足")
	}
	acc.reserved += n
	p.Valid = func() bool { return c.allowed(context.Background(), a, e) == nil }
	p.Disposition = ecosystem.NewFinalizer(func(accepted bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		acc.reserved -= n
		if accepted && acc.active && p.Valid() {
			acc.used += n
		}
	})
	if p.NextCursor != "" {
		c.cursors[nextID] = cursor{key: a.Key(), entry: e.ID, resource: resource, version: version, offset: end, limit: q.Limit}
	}
	return p, nil
}
func safeResource(r string) bool {
	if strings.ContainsAny(r, "\\:\x00") || strings.HasPrefix(r, "/") || path.Clean(r) != r {
		return false
	}
	for _, s := range strings.Split(r, "/") {
		if s == ".." || s == "." || s == "" {
			return false
		}
	}
	return true
}
func (c *Catalog) list(ctx context.Context, a ecosystem.Access, q Request, prev cursor) (Page, error) {
	limit := q.Limit
	if limit == 0 {
		limit = c.opts.PageItems
	}
	if limit < 1 || limit > c.opts.PageItems {
		return Page{}, fmt.Errorf("目录条数越界")
	}
	var items []Item
	for _, e := range c.entries {
		if e.Disabled || c.allowed(ctx, a, e) != nil {
			continue
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(e.Name+" "+e.Description), strings.ToLower(q.Query)) {
			continue
		}
		desc := []rune(e.Description)
		if len(desc) > 512 {
			desc = desc[:512]
		}
		items = append(items, Item{e.ID, e.Name, string(desc), e.Version})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	version := ecosystem.Digest(items)
	start := 0
	if q.Cursor != "" {
		if prev.version != version || prev.query != q.Query || prev.limit != q.Limit {
			return Page{}, fmt.Errorf("cursor_invalidated")
		}
		start = prev.offset
	}
	p := Page{Version: version, Start: start}
	end := start
	for end < len(items) && end-start < limit {
		p.Items = append(p.Items, items[end])
		raw, _ := json.Marshal(p)
		if len(raw) > c.opts.IndexBytes-128 {
			p.Items = p.Items[:len(p.Items)-1]
			break
		}
		end++
	}
	if end == start && end < len(items) {
		return Page{}, fmt.Errorf("目录条目超过预算")
	}
	p.End = end
	p.Complete = end == len(items)
	if !p.Complete {
		p.NextCursor = ecosystem.ID()
		c.cursors[p.NextCursor] = cursor{key: a.Key(), listing: true, version: version, query: q.Query, offset: end, limit: q.Limit}
	}
	return p, nil
}
