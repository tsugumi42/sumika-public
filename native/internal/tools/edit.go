package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"sumika-native/internal/edittext"
	"sumika-native/internal/jsdiff"
	"sumika-native/internal/jsjson"
)

// 编辑定位与替换由 edittext 统一计算，这里只负责文件队列、取消和写入。

// prepareEdit 照 pi 的 prepareEditArguments（校验参数之前先整理）：edits 写成了 JSON 字符串的解开；
// 只给了一个 {oldText, newText} 对象的包成一项的列表；老写法顶层的 oldText / newText 接到 edits 末尾
func prepareEdit(args jsjson.Value) jsjson.Value {
	if args.Kind != jsjson.Object {
		return args
	}
	if e, ok := args.Obj["edits"]; ok {
		if e.Kind == jsjson.String {
			if parsed, err := jsjson.Parse(e.Str); err == nil {
				if parsed.Kind == jsjson.Array {
					args = withKey(args, "edits", parsed)
				} else if isSingleEdit(parsed) {
					args = withKey(args, "edits", jsjson.Value{Kind: jsjson.Array, Arr: []jsjson.Value{parsed}})
				}
			}
		} else if isSingleEdit(e) {
			args = withKey(args, "edits", jsjson.Value{Kind: jsjson.Array, Arr: []jsjson.Value{e}})
		}
	}
	oldText, okOld := argString(args, "oldText")
	newText, okNew := argString(args, "newText")
	if !okOld || !okNew {
		return args
	}
	var edits []jsjson.Value
	if e := args.Obj["edits"]; e.Kind == jsjson.Array {
		edits = append(edits, e.Arr...)
	}
	edits = append(edits, jsjson.Value{Kind: jsjson.Object, Keys: []string{"oldText", "newText"}, Obj: map[string]jsjson.Value{
		"oldText": {Kind: jsjson.String, Str: oldText}, "newText": {Kind: jsjson.String, Str: newText}}})
	// { ...rest, edits }：去掉 oldText、newText，edits 原来有就留在原位、没有就放最后
	return withKey(jsjson.Without(jsjson.Without(args, "oldText"), "newText"), "edits", jsjson.Value{Kind: jsjson.Array, Arr: edits})
}

func isSingleEdit(v jsjson.Value) bool {
	return v.Kind == jsjson.Object && v.Obj["oldText"].Kind == jsjson.String && v.Obj["newText"].Kind == jsjson.String &&
		hasKey(v, "oldText") && hasKey(v, "newText")
}

func hasKey(v jsjson.Value, k string) bool { _, ok := v.Obj[k]; return ok }

// withKey 像 JS 的 obj.k = v：已有的键留在原位，没有的加在最后
func withKey(v jsjson.Value, k string, x jsjson.Value) jsjson.Value {
	obj := make(map[string]jsjson.Value, len(v.Obj)+1)
	for kk, vv := range v.Obj {
		obj[kk] = vv
	}
	keys := v.Keys
	if _, ok := v.Obj[k]; !ok {
		keys = append(append([]string(nil), v.Keys...), k)
	}
	obj[k] = x
	return jsjson.Value{Kind: jsjson.Object, Keys: keys, Obj: obj}
}

type editDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine int    `json:"firstChangedLine"`
}

type editCommitKey struct{}

// EditCommit 只在写文件和发布成功状态时持锁；到点读取不会等文件队列或差分。
type EditCommit struct {
	mu      sync.Mutex
	result  Result
	written bool
}

// WithEditCommit 把一次调用独占的写入状态交给 edit。
func WithEditCommit(ctx context.Context, committed *EditCommit) context.Context {
	return context.WithValue(ctx, editCommitKey{}, committed)
}

// Finish 最多等正在进行的写入返回；取消也在锁内，挡住到点后才开始的写入。
func (c *EditCommit) Finish(cancel context.CancelFunc) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cancel()
	return c.result, c.written
}

type editHintsKey struct{}

// WithEditHints 打开"编辑未命中时附上最接近的几行"。内核只对读它本来就允许的文件打开（受保护的路径不开）：
// 提示里带着文件内容，开了等于让没批准读的人读到它（opencode 同款取舍）
func WithEditHints(ctx context.Context) context.Context {
	return context.WithValue(ctx, editHintsKey{}, true)
}

// EditHintsEnabled 说这个 ctx 是不是开着编辑提示（内核的测试用）
func EditHintsEnabled(ctx context.Context) bool {
	on, _ := ctx.Value(editHintsKey{}).(bool)
	return on
}

// withEditHint 在原报错后面追加提示，原报错的字不变；没开、不是未命中、没有可说的就原样返回
func withEditHint(ctx context.Context, content string, edits []edittext.Pair, err error) error {
	if on, _ := ctx.Value(editHintsKey{}).(bool); !on {
		return err
	}
	miss := edittext.AsMiss(err)
	if miss == nil {
		return err
	}
	hint := edittext.Hint(content, edits, miss)
	if hint == "" {
		return err
	}
	return fmt.Errorf("%s\n\n%s", err.Error(), hint)
}

// 和差分函数一样留出写函数，测试用屏障覆盖 WriteFile 跨过总时限。
var writeEditFile = os.WriteFile

// 单独留出差分函数，测试用屏障卡住它，验证写入成功后不会被详情拖成失败。
var generateEditDetails = func(path, before, after string) editDetails {
	diff, first, _ := jsdiff.GenerateDiffString(before, after, editContextLines)
	return editDetails{Diff: diff, Patch: editPatch(path, before, after), FirstChangedLine: first}
}

func edit(ctx context.Context, cwd string, args jsjson.Value) (string, any, error) {
	p, _ := argString(args, "path")
	edits, err := edittext.Parse(args)
	if err != nil {
		return "", nil, err
	}
	abs := resolveToCwd(p, cwd)
	var details *editDetails
	text, err := withFileMutation(abs, func() (string, error) { return editLocked(ctx, abs, p, edits, &details) })
	if err != nil {
		return "", nil, err
	}
	if details == nil {
		return text, nil, nil
	}
	return text, *details, nil
}

// editLocked 在写入前检查取消；写入成功以后只取消详情计算，工具仍回成功，避免模型重做已完成的修改。
func editLocked(ctx context.Context, abs, p string, edits []edittext.Pair, details **editDetails) (string, error) {
	if err := aborted(ctx); err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if err := aborted(ctx); err != nil {
			return "", err
		}
		// fs.access 的报错只取 code（edit.js）：不存在、上级是文件都是 ENOENT
		return "", fmt.Errorf("Could not edit file: %s. Error code: ENOENT.", p)
	}
	if fi.IsDir() {
		return "", errors.New("EISDIR: illegal operation on a directory, read")
	}
	if err := aborted(ctx); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if err := aborted(ctx); err != nil {
		return "", err
	}
	change, err := edittext.Apply(string(raw), edits, p)
	if err != nil {
		return "", withEditHint(ctx, string(raw), edits, err)
	}
	text := fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), p)
	data := []byte(change.Content)
	committed, _ := ctx.Value(editCommitKey{}).(*EditCommit)
	err = func() error {
		if committed != nil {
			committed.mu.Lock()
			defer committed.mu.Unlock()
		}
		if err := aborted(ctx); err != nil {
			return err
		}
		if err := writeEditFile(abs, data, 0o666); err != nil {
			return err
		}
		rememberFileAt(ctx, abs)
		if committed != nil {
			committed.result, committed.written = Result{Text: text}, true
		}
		return nil
	}()
	if err != nil {
		return "", err
	}
	// 差分只读局部字符串；迟到的结果留在缓冲通道，不再碰文件、详情指针或库。
	// jsdiff 不支持中断，取消只放弃等待；已开始的差分仍持有全文并计算到结束（步数有上限，见 jsdiff.maxEditLength）。
	ready := make(chan editDetails, 1)
	generate := generateEditDetails
	go func() { ready <- generate(p, change.Before, change.After) }()
	select {
	case result := <-ready:
		*details = &result
	case <-ctx.Done():
		slog.Warn("文件已改好，差分超过工具时限或被取消，跳过详情")
	}
	return text, nil
}

const editContextLines = 4

// editPatch 照 jsdiff 的 structuredPatch / formatPatch（pi 的 FILE_HEADERS_ONLY）。
// 差分仍用现有的 jsdiff，避免别的库在等长改法里选了另一种顺序。
func editPatch(path, oldContent, newContent string) string {
	parts := append(jsdiff.DiffLines(oldContent, newContent), jsdiff.Part{})
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)
	oldLine, newLine := 1, 1
	oldStart, newStart := 0, 0
	var previous, lines []string
	var hunk []string
	addContext := func(lines []string) {
		for _, line := range lines {
			hunk = append(hunk, " "+line)
		}
	}
	for i, part := range parts {
		lines = strings.SplitAfter(part.Value, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if part.Added || part.Removed {
			if oldStart == 0 {
				context := min(len(previous), editContextLines)
				oldStart, newStart = oldLine-context, newLine-context
				addContext(previous[len(previous)-context:])
			}
			prefix := "-"
			if part.Added {
				prefix = "+"
				newLine += len(lines)
			} else {
				oldLine += len(lines)
			}
			for _, line := range lines {
				hunk = append(hunk, prefix+line)
			}
		} else {
			if oldStart != 0 {
				if len(lines) <= editContextLines*2 && i < len(parts)-2 {
					addContext(lines)
				} else {
					context := min(len(lines), editContextLines)
					addContext(lines[:context])
					oldCount, newCount := oldLine-oldStart+context, newLine-newStart+context
					if oldCount == 0 {
						oldStart--
					}
					if newCount == 0 {
						newStart--
					}
					fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
					for _, line := range hunk {
						out.WriteString(line)
						if !strings.HasSuffix(line, "\n") {
							out.WriteString("\n\\ No newline at end of file\n")
						}
					}
					oldStart, newStart, hunk = 0, 0, hunk[:0]
				}
			}
			oldLine += len(lines)
			newLine += len(lines)
		}
		previous = lines
	}
	return out.String()
}
