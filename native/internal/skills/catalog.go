package skills

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	"golang.org/x/text/unicode/norm"
	"sumika-native/internal/approval"
	"sumika-native/internal/ecosystem"
)

type Root struct{ ID, Path, Scope string }
type Entry struct {
	ID, Name, Description, Version string
	Disabled                       bool
	UserInvocable                  bool
	root                           *os.Root
	dir                            string
	embedded                       bool
}
type Options struct {
	Enabled                                                                        bool
	Roots                                                                          []Root
	Approve                                                                        func(context.Context, ecosystem.Access, string) error
	IndexBytes, PageItems, ReadBytes, FileMaxBytes, ContextMaxBytes, ScanTimeoutMs int
}
type Catalog struct {
	opts     Options
	mu       sync.Mutex
	entries  []Entry
	cursors  map[string]cursor
	contexts map[string]*account
}
type cursor struct {
	key, entry, resource, version, query string
	offset, limit                        int
	listing                              bool
}
type account struct {
	used, reserved int
	active         bool
}

func New(o Options) (*Catalog, error) {
	defaults := []struct {
		p *int
		v int
	}{{&o.IndexBytes, 8192}, {&o.PageItems, 20}, {&o.ReadBytes, 32768}, {&o.FileMaxBytes, 1 << 20}, {&o.ContextMaxBytes, 128 << 10}, {&o.ScanTimeoutMs, 5000}}
	for _, d := range defaults {
		if *d.p == 0 {
			*d.p = d.v
		}
		if *d.p < 1 {
			return nil, fmt.Errorf("skill 限额必须为正数")
		}
	}
	if o.ReadBytes < 1024 || o.ReadBytes > 32768 || o.PageItems > 20 || o.FileMaxBytes > 1<<20 || o.ContextMaxBytes > 128<<10 || o.ScanTimeoutMs > 5000 {
		return nil, fmt.Errorf("skill 限额越界")
	}
	return &Catalog{opts: o, cursors: map[string]cursor{}, contexts: map[string]*account{}}, nil
}
func (c *Catalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	closed := map[*os.Root]bool{}
	for _, e := range c.entries {
		if e.root != nil && !closed[e.root] {
			closed[e.root] = true
			if err := e.root.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	c.entries = nil
	for _, a := range c.contexts {
		a.active = false
	}
	clear(c.contexts)
	clear(c.cursors)
	return first
}

// Scan 在授权后才读根目录和元数据；候选产物目录不会自动成为来源。
func (c *Catalog) Scan(ctx context.Context, a ecosystem.Access) ([]error, error) {
	if !c.opts.Enabled {
		return nil, ecosystem.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.opts.ScanTimeoutMs)*time.Millisecond)
	defer cancel()
	var entries []Entry
	defer func() {
		for _, e := range entries {
			if e.root != nil {
				e.root.Close()
			}
		}
	}()
	var issues []error
	seen := map[string]bool{}
	for _, r := range c.opts.Roots {
		if r.ID == "" || r.ID == "builtin" {
			return nil, fmt.Errorf("来源 ID 非法")
		}
		if err := a.Check(ctx, "skill:"+r.ID); err != nil {
			continue
		}
		if c.opts.Approve == nil {
			return nil, fmt.Errorf("没有资料读取审批")
		}
		real, err := approval.RealStrict(r.Path)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		if err = c.opts.Approve(ctx, a, real); err != nil {
			issues = append(issues, err)
			continue
		}
		root, err := os.OpenRoot(real)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		f, err := root.Open(".")
		if err != nil {
			root.Close()
			issues = append(issues, err)
			continue
		}
		dirs, err := f.ReadDir(20001)
		f.Close()
		if err != nil && err != io.EOF {
			root.Close()
			issues = append(issues, err)
			continue
		}
		if len(dirs) > 20000 {
			root.Close()
			issues = append(issues, fmt.Errorf("来源条目超过 20000"))
			continue
		}
		sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
		count := 0
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			count++
			if count > 2000 {
				issues = append(issues, fmt.Errorf("来源目录超过 2000"))
				break
			}
			if err := ctx.Err(); err != nil {
				root.Close()
				return issues, err
			}
			file := filepath.Join(real, d.Name(), "SKILL.md")
			target, err := approval.RealStrict(file)
			if err != nil {
				issues = append(issues, err)
				continue
			}
			if !inside(real, target) || seen[strings.ToLower(target)] {
				continue
			}
			packageRoot, err := root.OpenRoot(d.Name())
			if err != nil {
				issues = append(issues, err)
				continue
			}
			data, err := c.readSource(ctx, a, packageRoot, "SKILL.md", true)
			if err != nil {
				packageRoot.Close()
				issues = append(issues, err)
				continue
			}
			e, err := parse(data, d.Name())
			if err != nil {
				packageRoot.Close()
				issues = append(issues, fmt.Errorf("%s: %w", d.Name(), err))
				continue
			}
			if err = a.Check(ctx, "skill:"+r.ID); err != nil {
				packageRoot.Close()
				root.Close()
				return issues, err
			}
			e.ID = "source:" + ecosystem.Digest([]string{r.ID, r.Scope, d.Name()})
			e.root = packageRoot
			e.dir = d.Name()
			e.Version = ecosystem.Digest(data)
			entries = append(entries, e)
			seen[strings.ToLower(target)] = true
		}
		root.Close()
	}
	if a.Check(ctx, "skill:builtin") == nil {
		data, err := builtin.ReadFile("builtin/sumika/SKILL.md")
		if err != nil {
			return issues, err
		}
		e, err := parse(data, "sumika")
		if err != nil {
			return issues, err
		}
		e.ID = "builtin:sumika"
		e.embedded = true
		e.Version, err = ManualVersion()
		if err != nil {
			return issues, err
		}
		entries = append(entries, e)
	}
	if err := ctx.Err(); err != nil {
		return issues, err
	}
	c.mu.Lock()
	old := c.entries
	c.entries = entries
	entries = nil
	c.mu.Unlock()
	closed := map[*os.Root]bool{}
	for _, e := range old {
		if e.root != nil && !closed[e.root] {
			closed[e.root] = true
			e.root.Close()
		}
	}
	return issues, nil
}
func inside(root, target string) bool {
	r, err := filepath.Rel(root, target)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func (c *Catalog) readSource(ctx context.Context, a ecosystem.Access, root *os.Root, file string, metadata bool) ([]byte, error) {
	oldRoot, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	currentRoot, err := os.Stat(root.Name())
	if err != nil {
		return nil, err
	}
	if !os.SameFile(oldRoot, currentRoot) {
		return nil, ecosystem.ErrDenied
	}
	f, err := root.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	max := c.opts.FileMaxBytes
	if !info.Mode().IsRegular() || info.Size() > int64(max) {
		return nil, fmt.Errorf("skill 文件非法或超过上限")
	}
	target, err := approval.OpenedPath(f)
	if err != nil {
		return nil, err
	}
	if !inside(root.Name(), target) {
		return nil, ecosystem.ErrDenied
	}
	if err = c.opts.Approve(ctx, a, target); err != nil {
		return nil, err
	}
	if !metadata {
		data, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > max || !utf8.Valid(data) {
			return nil, fmt.Errorf("文件过大或不是 UTF-8")
		}
		return data, nil
	}
	r := bufio.NewReader(io.LimitReader(f, int64(max)+1))
	var text strings.Builder
	first := true
	for {
		line, err := r.ReadString('\n')
		text.WriteString(line)
		if text.Len() > max {
			return nil, fmt.Errorf("skill 元数据超限")
		}
		marker := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(line, "\ufeff"), "\n"), "\r")
		if first {
			if marker != "---" {
				return nil, fmt.Errorf("缺 YAML frontmatter")
			}
			first = false
		} else if marker == "---" {
			if !strings.HasSuffix(line, "\n") {
				text.WriteByte('\n')
			}
			return []byte(text.String()), nil
		}
		if err != nil {
			return nil, fmt.Errorf("skill frontmatter 未结束：%w", err)
		}
	}
}
func parse(data []byte, dir string) (Entry, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Entry{}, fmt.Errorf("缺 YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return Entry{}, fmt.Errorf("frontmatter 未结束")
	}
	raw := []byte(text[4 : 4+end])
	var fields map[string]any
	if err := yaml.UnmarshalWithOptions(raw, &fields, yaml.Strict()); err != nil {
		return Entry{}, err
	}
	e := Entry{UserInvocable: true}
	var ok bool
	e.Name, ok = fields["name"].(string)
	if !ok {
		return e, fmt.Errorf("name 类型错误")
	}
	name := norm.NFKC.String(e.Name)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 || name != norm.NFKC.String(dir) || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return e, fmt.Errorf("name 非法")
	}
	for _, r := range name {
		if r != '-' && !unicode.IsLower(r) && !unicode.IsDigit(r) {
			return e, fmt.Errorf("name 非法")
		}
	}
	e.Description, ok = fields["description"].(string)
	if !ok || strings.TrimSpace(e.Description) == "" || utf8.RuneCountInString(e.Description) > 1024 {
		return e, fmt.Errorf("description 非法")
	}
	for _, k := range []string{"license", "compatibility", "allowed-tools"} {
		if v, exists := fields[k]; exists {
			s, ok := v.(string)
			if !ok || k == "compatibility" && (len([]rune(s)) < 1 || len([]rune(s)) > 500) {
				return e, fmt.Errorf("%s 非法", k)
			}
		}
	}
	if v, exists := fields["metadata"]; exists {
		m, ok := v.(map[string]any)
		if !ok {
			return e, fmt.Errorf("metadata 非法")
		}
		for _, v := range m {
			if _, ok := v.(string); !ok {
				return e, fmt.Errorf("metadata 非字符串")
			}
		}
	}
	for _, k := range []string{"disable-model-invocation", "user-invocable"} {
		if v, exists := fields[k]; exists {
			b, ok := v.(bool)
			if !ok {
				return e, fmt.Errorf("%s 非布尔", k)
			}
			if k == "user-invocable" {
				e.UserInvocable = b
			} else {
				e.Disabled = b
			}
		}
	}
	return e, nil
}

var _ fs.FS = builtin
