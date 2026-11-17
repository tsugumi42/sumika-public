// Package projectcontext 读取宿主已授权的项目资料；信任、工具执行和受控资料封装由宿主负责。
package projectcontext

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"sumika-native/internal/approval"
)

const (
	DefaultSourceBytes = 1 << 20
	DefaultTotalBytes  = 32 << 10
	DefaultDepth       = 5
	DefaultImports     = 64
	DefaultConcurrency = 8
)

// Options 的路径都是宿主冻结的绝对路径。Root 为空时只读 CWD，不向上探测。
// Open 可用于宿主计量读取；它必须打开收到的真实路径，不能另找替代文件。
type Options struct {
	UserRoot, Root, CWD                                        string
	RootIdentity                                               *RootIdentity
	Fallback                                                   []string
	Approve                                                    func(context.Context, string) error
	Open                                                       func(string) (io.ReadCloser, error)
	SourceBytes, TotalBytes, MaxDepth, MaxImports, Concurrency int
}

type State string

const (
	Present     State = "present"
	Absent      State = "absent"
	Unavailable State = "unavailable"
)

// Issue 同时给出来源、失败原因和恢复条件；完整列表不受模型文字预算截断。
type Issue struct{ Path, Reason, Recovery string }

func (i Issue) String() string {
	return fmt.Sprintf("%s：%s；恢复条件：%s", i.Path, i.Reason, i.Recovery)
}

// File 包含原始来源与 import 的核对结果。审批失败是 present 但不完整。
type File struct {
	Path, Real string
	State      State
	Complete   bool
}

// Source 是一个目录的一组候选（基础或 local），ID 不随候选文件名变化。
// Rendered 是本次实际给出的版本；不完整版本永远不能满足写前检查。
type Source struct {
	ID, Path, Scope string
	State           State
	Complete        bool
	Rendered        string
	Digest          [32]byte
	Files           []File
	Issues          []Issue
}

type Snapshot struct {
	Sources     []Source
	Text        string
	Complete    bool
	Issues      []Issue
	diagnostics string
}

type cachedFile struct {
	info os.FileInfo
	id   fileIdentity
	text string
	refs []reference
}

type cachedPath struct {
	real string
	id   fileIdentity
}

// 核对词法路径当前的文件身份；链接换目标时必须重新严格解析。
func (r *Resolver) resolve(path string) (string, os.FileInfo, fileIdentity, error) {
	info, id, err := identify(path)
	if err != nil {
		return "", nil, id, err
	}
	if old, ok := r.paths[path]; ok && sameIdentity(id, old.id) {
		return old.real, info, id, nil
	}
	real, err := approval.RealStrict(path)
	if err != nil {
		return "", nil, id, err
	}
	r.paths[path] = cachedPath{real: real, id: id}
	return real, info, id, nil
}

// Resolver 的扫描串行化，因此实际正文读取并发为 1，始终小于等于 Concurrency。
// 回调不能重入同一个 Resolver。不同工作区分别创建 Resolver。
type Resolver struct {
	mu    sync.Mutex
	opts  Options
	cache map[string]cachedFile
	paths map[string]cachedPath
}

func New(o Options) (*Resolver, error) {
	if o.Root == "" {
		o.Root = o.CWD
	}
	for _, p := range []string{o.Root, o.CWD} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("工作区路径必须是绝对路径：%q", p)
		}
	}
	if !inside(o.Root, o.CWD) {
		return nil, fmt.Errorf("cwd 不在工作区根内：%s", o.CWD)
	}
	if o.UserRoot != "" && !filepath.IsAbs(o.UserRoot) {
		return nil, fmt.Errorf("用户说明根必须是绝对路径")
	}
	for _, name := range o.Fallback {
		if !safeName(name) {
			return nil, fmt.Errorf("fallback 必须是普通文件名：%q", name)
		}
	}
	if o.SourceBytes == 0 {
		o.SourceBytes = DefaultSourceBytes
	}
	if o.TotalBytes == 0 {
		o.TotalBytes = DefaultTotalBytes
	}
	if o.MaxDepth == 0 {
		o.MaxDepth = DefaultDepth
	}
	if o.MaxImports == 0 {
		o.MaxImports = DefaultImports
	}
	if o.Concurrency == 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.SourceBytes < 1 || o.TotalBytes < 1 || o.MaxDepth < 0 || o.MaxImports < 1 || o.Concurrency < 1 || o.Concurrency > DefaultConcurrency {
		return nil, fmt.Errorf("说明限额无效（读取并发必须在 1 到 8 之间）")
	}
	if o.SourceBytes == int(^uint(0)>>1) {
		return nil, fmt.Errorf("单源限额过大")
	}
	if o.Open == nil {
		o.Open = func(p string) (io.ReadCloser, error) { return os.Open(p) }
	}
	o.Fallback = append([]string(nil), o.Fallback...)
	return &Resolver{opts: o, cache: make(map[string]cachedFile), paths: make(map[string]cachedPath)}, nil
}

func inside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeName(s string) bool {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00:<>\"|?*") || strings.TrimRight(s, " .") != s {
		return false
	}
	base, _, _ := strings.Cut(s, ".")
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if len(base) >= 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		n := strings.TrimPrefix(strings.TrimPrefix(base, "COM"), "LPT")
		if utf8.RuneCountInString(n) == 1 && strings.Contains("123456789¹²³", n) {
			return false
		}
	}
	return true
}

// MarkDirty 使下一次核对重读正文；空路径清空全部正文缓存。不会改变接纳账本。
func (r *Resolver) MarkDirty(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// watcher 可能报告别名或刚删除的路径；保守失效避免漏掉同身份缓存。
	clear(r.cache)
}

func (r *Resolver) Discover(ctx context.Context) Snapshot { return r.DiscoverTo(ctx, r.opts.CWD) }

// DiscoverTo 用于成功 read 的父目录、成功 ls 的目录，以及写前目标父目录。
// 不存在的子目录只核对已有祖先；任何其他解析错误均失败关闭。
func (r *Resolver) DiscoverTo(ctx context.Context, dir string) Snapshot {
	return r.discover(ctx, dir, false)
}

func (r *Resolver) discover(ctx context.Context, dir string, force bool) Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := scan{r: r, ctx: ctx, force: force}
	snap := Snapshot{Complete: true}
	if !filepath.IsAbs(dir) || !inside(r.opts.Root, dir) {
		snap.Issues = append(snap.Issues, Issue{dir, "目标目录不在工作区内", "选择工作区内的目标"})
	} else {
		root, err := r.realDirectory(r.opts.Root)
		if err == nil && r.opts.RootIdentity != nil {
			err = r.opts.RootIdentity.Check(r.opts.Root)
		}
		if err != nil {
			snap.Issues = append(snap.Issues, Issue{r.opts.Root, "无法确认工作区真实路径：" + err.Error(), "恢复工作区访问后重新核对"})
		} else {
			// 按调用方的目录链扫描，不沿链接的目标去继承另一条目录链。
			dirs := []string{}
			for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
				dirs = append(dirs, d)
				if samePath(d, r.opts.Root) {
					break
				}
			}
			if r.opts.UserRoot != "" {
				user, e := r.realDirectory(r.opts.UserRoot)
				if e != nil {
					snap.Sources = append(snap.Sources, failedSource("user", r.opts.UserRoot, "无法确认用户说明根："+e.Error()))
				} else {
					snap.Sources = append(snap.Sources, s.selectSource(r.opts.UserRoot, user, "user", []string{"AGENTS.override.md", "AGENTS.md"}))
				}
			}
			for i := len(dirs) - 1; i >= 0; i-- {
				d := dirs[i]
				real, e := r.realDirectory(d)
				if e != nil && !os.IsNotExist(e) {
					snap.Sources = append(snap.Sources, failedSource(d, d, "无法确认目录真实路径："+e.Error()))
					break
				}
				if e == nil && !inside(root, real) {
					snap.Sources = append(snap.Sources, failedSource(d, d, "目录真实路径越出工作区"))
					break
				}
				names := append([]string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"}, r.opts.Fallback...)
				snap.Sources = append(snap.Sources, s.selectSource(d, root, "base", names), s.selectSource(d, root, "local", []string{"AGENTS.local.md", "CLAUDE.local.md"}))
			}
		}
	}
	r.budget(&snap)
	return snap
}

func (r *Resolver) realDirectory(path string) (string, error) {
	real, info, _, err := r.resolve(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("路径不是目录：%s", path)
	}
	return real, nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func failedSource(id, path, reason string) Source {
	return Source{ID: id, Path: path, State: Unavailable, Issues: []Issue{{path, reason, "恢复路径、文件类型或访问权限后重新核对"}}}
}

type seenFile struct {
	info   os.FileInfo
	id     fileIdentity
	real   string
	active bool
}
type scan struct {
	r       *Resolver
	ctx     context.Context
	force   bool
	seen    []*seenFile
	imports int
}

func (s *scan) selectSource(dir, root, kind string, names []string) Source {
	src := Source{ID: filepath.Join(dir, "#"+kind), Scope: dir, State: Absent, Complete: true}
	for _, name := range names {
		path := filepath.Join(dir, name)
		// Lstat 区分真正不存在和悬空链接；坏的高优先级候选不能回退。
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		src.Path = path
		if err != nil {
			src.State = Unavailable
			src.Complete = false
			src.Issues = append(src.Issues, Issue{path, "发现失败：" + err.Error(), "恢复候选文件访问权限"})
			return src
		}
		src.State = Present
		body, ok := s.expand(path, root, kind == "user", 0, &src)
		src.Complete = ok
		if len(src.Files) > 0 {
			src.State = src.Files[0].State
		}
		if src.State == Present {
			src.Rendered = header(src) + body + "\n[说明结束]\n"
		}
		return src
	}
	return src
}

func header(src Source) string {
	return fmt.Sprintf("[项目说明 来源 %q；作用域 %q]\n", src.Path, src.Scope)
}

// load 在打开正文之前做严格解析、边界、类型、当次审批和缓存核对。
func (s *scan) load(path, root string, depth int) (File, cachedFile, error) {
	f := File{Path: path, State: Unavailable}
	if err := s.ctx.Err(); err != nil {
		return f, cachedFile{}, err
	}
	if depth > 0 {
		if s.imports >= s.r.opts.MaxImports {
			return f, cachedFile{}, fmt.Errorf("超出 import 文件数：超过 %d", s.r.opts.MaxImports)
		}
		s.imports++
	}
	real, info, id, err := s.r.resolve(path)
	if err != nil {
		if os.IsNotExist(err) {
			if _, e := os.Lstat(path); os.IsNotExist(e) {
				f.State = Absent
			}
		}
		return f, cachedFile{}, fmt.Errorf("严格真实路径解析失败：%w", err)
	}
	f.Real = real
	if !inside(root, real) || strings.HasPrefix(real, `\\`) {
		return f, cachedFile{}, fmt.Errorf("真实路径越界或为网络共享")
	}
	if !info.Mode().IsRegular() {
		return f, cachedFile{}, fmt.Errorf("只读普通 UTF-8 文件")
	}
	f.State = Present
	if s.r.opts.Approve != nil {
		if err := s.r.opts.Approve(s.ctx, real); err != nil {
			return f, cachedFile{}, fmt.Errorf("审批未通过：%w", err)
		}
	}
	if err := s.ctx.Err(); err != nil {
		return f, cachedFile{}, err
	}
	if depth > s.r.opts.MaxDepth {
		return f, cachedFile{}, fmt.Errorf("import 超过 %d 跳", s.r.opts.MaxDepth)
	}
	for _, seen := range s.seen {
		if sameIdentity(id, seen.id) {
			f.Complete = true
			return f, cachedFile{info: info, id: id}, nil
		}
	}
	if old, ok := s.r.cache[real]; ok && !s.force && sameIdentity(id, old.id) && info.Size() == old.info.Size() && info.ModTime() == old.info.ModTime() {
		f.Complete = true
		return f, old, nil
	}
	if info.Size() > int64(s.r.opts.SourceBytes) {
		return f, cachedFile{}, fmt.Errorf("超过单源 %d 字节上限", s.r.opts.SourceBytes)
	}
	rd, err := s.r.opts.Open(real)
	if err != nil {
		f.State = Unavailable
		return f, cachedFile{}, err
	}
	data, err := io.ReadAll(io.LimitReader(rd, int64(s.r.opts.SourceBytes)+1))
	closeErr := rd.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		f.State = Unavailable
		return f, cachedFile{}, err
	}
	if len(data) > s.r.opts.SourceBytes {
		return f, cachedFile{}, fmt.Errorf("超过单源 %d 字节上限", s.r.opts.SourceBytes)
	}
	if !utf8.Valid(data) {
		f.State = Unavailable
		return f, cachedFile{}, fmt.Errorf("文件不是有效 UTF-8")
	}
	after, afterID, err := identify(real)
	if err != nil || !sameIdentity(id, afterID) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		f.State = Unavailable
		return f, cachedFile{}, fmt.Errorf("读取期间文件发生变化，无法核对")
	}
	c := cachedFile{info: info, id: id, text: string(data)}
	c.refs = references(c.text)
	s.r.cache[real] = c
	f.Complete = true
	return f, c, nil
}

func (s *scan) expand(path, root string, user bool, depth int, src *Source) (string, bool) {
	f, c, err := s.load(path, root, depth)
	src.Files = append(src.Files, f)
	fail := func(reason string) (string, bool) {
		src.Issues = append(src.Issues, Issue{path, reason, "修复引用、精简来源、调整限额或取得本次审批后，完整接纳新版"})
		return "", false
	}
	if err != nil {
		return fail(err.Error())
	}
	for _, seen := range s.seen {
		if sameIdentity(c.id, seen.id) {
			if seen.active {
				return fail("import 循环")
			}
			return fmt.Sprintf("[已引用 %q]", seen.real), true
		}
	}
	seen := &seenFile{info: c.info, id: c.id, real: f.Real, active: true}
	s.seen = append(s.seen, seen)
	defer func() { seen.active = false }()
	if len(c.refs) == 0 {
		return c.text, true
	}
	var b strings.Builder
	complete, pos := true, 0
	for _, ref := range c.refs {
		b.WriteString(c.text[pos:ref.start])
		if s.imports >= s.r.opts.MaxImports {
			src.Issues = append(src.Issues, Issue{path, fmt.Sprintf("超出 import 文件数：超过 %d", s.r.opts.MaxImports), "精简引用或调整限额后完整接纳新版"})
			b.WriteString(c.text[ref.start:])
			return b.String(), false
		}
		target, e := importPath(ref.path, filepath.Dir(path), root, user)
		var text string
		var ok bool
		if e != nil {
			s.imports++
			src.Issues = append(src.Issues, Issue{path, e.Error(), "改用授权根内的本地相对文件引用"})
		} else {
			text, ok = s.expand(target, root, user, depth+1, src)
		}
		if !ok {
			complete = false
		}
		if !ok && text == "" {
			b.WriteString(c.text[ref.start:ref.end])
		} else {
			fmt.Fprintf(&b, "[import %q]\n%s\n[/import]", target, text)
		}
		pos = ref.end
	}
	b.WriteString(c.text[pos:])
	return b.String(), complete
}

const incompleteMark = "[项目说明不完整；详见来源诊断，相关写入已阻塞]\n"
const updateMark = "此来源已更新，以下版本替代旧版本\n"
const retryMark = "本次没有写入，请按这些资料重新调用\n"

func (r *Resolver) budget(snap *Snapshot) {
	// 为更新提示和写前返回提示留出实际框架字节，避免正文刚好装满后超支。
	remaining := r.opts.TotalBytes - len(retryMark)
	for _, src := range snap.Sources {
		if src.Rendered != "" {
			remaining -= len(updateMark)
		}
	}
	total := 0
	for _, src := range snap.Sources {
		total += len(src.Rendered)
		if !src.Complete {
			snap.Complete = false
		}
		snap.Issues = append(snap.Issues, src.Issues...)
	}
	if len(snap.Issues) > 0 {
		snap.Complete = false
	}
	overflow := total > remaining
	if overflow {
		snap.Complete = false
	}
	diagnosticBytes := 0
	for _, issue := range snap.Issues {
		diagnosticBytes += len(issue.String()) + 1
	}
	// 截断后新增的诊断也预留空间；恢复条件与正文共用预算。
	if !snap.Complete {
		for _, src := range snap.Sources {
			if src.Rendered != "" {
				diagnosticBytes += len(budgetIssue(src.Path).String()) + 1
			}
		}
		remaining -= diagnosticBytes
	}
	if !snap.Complete {
		remaining -= len(incompleteMark)
	}
	if remaining < 0 {
		remaining = 0
	}
	// 倒序分配：local、较深目录、祖先、用户说明；展示仍按原顺序。
	for i := len(snap.Sources) - 1; i >= 0; i-- {
		src := &snap.Sources[i]
		if len(src.Rendered) > remaining {
			src.Complete = false
			issue := budgetIssue(src.Path)
			src.Issues = append(src.Issues, issue)
			snap.Issues = append(snap.Issues, issue)
			h, tail := header(*src), "\n[正文已截断]\n[说明结束]\n"
			if remaining >= len(h)+len(tail) {
				body := strings.TrimSuffix(strings.TrimPrefix(src.Rendered, h), "\n[说明结束]\n")
				src.Rendered = h + prefixUTF8(body, remaining-len(h)-len(tail)) + tail
			} else {
				src.Rendered = ""
			}
		}
		remaining -= len(src.Rendered)
		src.Digest = sha256.Sum256([]byte(src.Rendered))
	}
	var diagnostic strings.Builder
	for _, issue := range snap.Issues {
		diagnostic.WriteString(issue.String() + "\n")
	}
	// 极小限额下仍通过 Issues 返回完整诊断，模型文字按 UTF-8 限制。
	diagnosticLimit := max(0, min(diagnosticBytes, r.opts.TotalBytes-len(incompleteMark)))
	snap.diagnostics = prefixUTF8(diagnostic.String(), diagnosticLimit)
	var b strings.Builder
	if !snap.Complete {
		b.WriteString(prefixUTF8(incompleteMark, r.opts.TotalBytes))
	}
	for _, src := range snap.Sources {
		b.WriteString(src.Rendered)
	}
	b.WriteString(snap.diagnostics)
	snap.Text = b.String()
}

func budgetIssue(path string) Issue {
	return Issue{path, "总渲染预算不足，来源已截断或跳过", "精简整组说明或调整总预算后，完整接纳全部适用来源"}
}

func prefixUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
