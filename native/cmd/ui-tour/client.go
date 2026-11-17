package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

type rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}
type node struct {
	ID          string          `json:"id"`
	Parent      string          `json:"parent,omitempty"`
	Role        string          `json:"role"`
	Key         json.RawMessage `json:"key,omitempty"`
	Label       string          `json:"label,omitempty"`
	Value       string          `json:"value,omitempty"`
	Bounds      rect            `json:"bounds"`
	States      []string        `json:"states,omitempty"`
	Actions     []string        `json:"actions,omitempty"`
	SetSize     int             `json:"setSize,omitempty"`
	Placeholder string          `json:"placeholder,omitempty"`
	Description string          `json:"description,omitempty"`
	Min         *float64        `json:"min,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	Now         *float64        `json:"now,omitempty"`
	Step        *float64        `json:"step,omitempty"`
	Selection   *[2]int         `json:"selection,omitempty"`
	Level       int             `json:"level,omitempty"`
	PosInSet    int             `json:"posInSet,omitempty"`
	WebView     bool            `json:"webView,omitempty"`
}
type tree struct {
	Window        int      `json:"window"`
	Nodes         []node   `json:"nodes"`
	Focus         string   `json:"focus,omitempty"`
	Announcements []string `json:"announcements,omitempty"`
}
type window struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	ContentBounds rect   `json:"contentBounds"`
	Kind          string `json:"kind"`
	URL           string `json:"url,omitempty"`
	Bounds        rect   `json:"bounds"`
	Focused       bool   `json:"focused"`
	Visible       bool   `json:"visible"`
}
type client struct {
	conn       net.Conn
	dec        *json.Decoder
	enc        *json.Encoder
	seq        int
	win        int
	waits      []string
	idleFailed bool
}

type protocolError struct{ Method, Code, Message string }

func (e *protocolError) Error() string { return e.Method + ": " + e.Code + ": " + e.Message }

func connect(path string) (*client, error) {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil, err
	}
	return &client{conn: c, dec: json.NewDecoder(bufio.NewReader(c)), enc: json.NewEncoder(c)}, nil
}
func (c *client) call(method string, params any, out any) error {
	c.seq++
	if err := c.conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	if err := c.enc.Encode(map[string]any{"id": c.seq, "method": method, "params": params}); err != nil {
		return err
	}
	var r struct {
		ID     int                             `json:"id"`
		Result json.RawMessage                 `json:"result"`
		Error  *struct{ Code, Message string } `json:"error"`
	}
	if err := c.dec.Decode(&r); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if r.ID != c.seq {
		return fmt.Errorf("%s: response id %d != %d", method, r.ID, c.seq)
	}
	if r.Error != nil {
		return &protocolError{method, r.Error.Code, r.Error.Message}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}
func (c *client) idle() error {
	if c.idleFailed {
		return nil
	}
	err := c.call("idle", map[string]any{"window": c.win, "timeout": 10000}, nil)
	var failure *protocolError
	if errors.As(err, &failure) && failure.Code == "timeout" {
		// 留下失败再截当前画面，最后整次巡视仍退出非零；不让一个活跃页阻断其余证据。
		c.waits = append(c.waits, err.Error())
		c.idleFailed = true
		return nil
	}
	return err
}
func (c *client) tree() (tree, error) {
	var t tree
	err := c.call("tree", map[string]any{"window": c.win}, &t)
	return t, err
}
func (c *client) key(key string) error {
	c.idleFailed = false
	return c.call("input", map[string]any{"window": c.win, "events": []any{map[string]any{"type": "key", "key": key}}}, nil)
}
func (c *client) act(n node, action, text string) error {
	c.idleFailed = false
	return c.call("act", map[string]any{"window": c.win, "id": n.ID, "action": action, "text": text}, nil)
}
func (c *client) find(label, action, scope string) (node, error) {
	t, err := c.tree()
	if err != nil {
		return node{}, err
	}
	return findNode(t, label, action, scope)
}

func (c *client) click(n node) error {
	c.idleFailed = false
	return c.call("input", map[string]any{"window": c.win, "events": []any{map[string]any{"type": "click", "x": n.Bounds.X + n.Bounds.Width/2, "y": n.Bounds.Y + n.Bounds.Height/2}}}, nil)
}

func findNode(t tree, label, action, scope string) (node, error) {
	byID := index(t)
	scopeID := ""
	if scope != "" {
		for _, n := range t.Nodes {
			if n.Label == scope && visible(n) {
				if scopeID != "" {
					return node{}, fmt.Errorf("%q: 多个可见容器", scope)
				}
				scopeID = n.ID
			}
		}
		if scopeID == "" {
			return node{}, fmt.Errorf("%q: 没有可见容器", scope)
		}
	}
	var matches []node
	for _, n := range t.Nodes {
		if n.Label != label || !has(n.Actions, action) || !visible(n) || scopeID != "" && !under(n, scopeID, byID) {
			continue
		}
		// 同一命中框的祖先和后代属于一个入口；保留外层，避免把它误判成同名冲突。
		nested := false
		for p := n.Parent; p != ""; p = byID[p].Parent {
			parent := byID[p]
			if parent.Label == label && has(parent.Actions, action) && visible(parent) && (scopeID == "" || under(parent, scopeID, byID)) {
				nested = true
				break
			}
		}
		if !nested {
			matches = append(matches, n)
		}
	}
	if len(matches) != 1 {
		return node{}, fmt.Errorf("%q action %s: %d visible matches: %+v", label, action, len(matches), matches)
	}
	return matches[0], nil
}
func (c *client) press(label string) error {
	return c.pressWithin(label, "")
}

func (c *client) pressWithin(label, scope string) error {
	n, err := c.find(label, "press", scope)
	if err != nil {
		return err
	}
	if err = c.click(n); err != nil {
		return err
	}
	return c.idle()
}
func (c *client) value(label, text string) error {
	n, err := c.find(label, "setValue", "")
	if err != nil {
		return err
	}
	if err = c.act(n, "setValue", text); err != nil {
		return err
	}
	return c.idle()
}
func has(ss []string, s string) bool {
	for _, v := range ss {
		if s == v {
			return true
		}
	}
	return false
}
