package tools

import (
	"context"
	"io"
	"strings"

	"sumika-native/internal/jsstr"
)

// ───────────── 命令输出的累积和截断（output-accumulator.js、truncate.js 的 truncateTail、utils/output-files.js） ─────────────

// outputAcc 边解码边写宿主的有界 sink，内存只留预览尾部；不再向系统临时目录复制全文。
type outputAcc struct {
	dec             *jsstr.Decoder
	sink            io.Writer
	err             error
	tail            string
	tailBytes       int
	tailAtLineStart bool
	totalRaw        int
	totalDecoded    int
	completedLines  int
	totalLines      int
	curLineBytes    int
	openLine        bool
	finished        bool
}

const maxRollingBytes = maxBytes * 2

func newOutputAcc() *outputAcc {
	return &outputAcc{dec: jsstr.NewDecoder(), tailAtLineStart: true}
}

func (o *outputAcc) append(data []byte) {
	o.totalRaw += len(data)
	o.appendDecoded(o.dec.Decode(data))
}

func (o *outputAcc) finish() {
	if o.finished {
		return
	}
	o.finished = true
	o.appendDecoded(o.dec.Flush())
}

type tailTruncation struct {
	content         string
	truncated       bool
	byLines         bool
	totalLines      int
	outputLines     int
	outputBytes     int
	lastLinePartial bool
}

// snapshot 是现在给她看的那一段和截断的情况（总行数、总字节数按收到的全部算）
func (o *outputAcc) snapshot() tailTruncation {
	tail := truncateTail(o.snapshotText())
	t := tail
	t.truncated = o.totalLines > maxLines || o.totalDecoded > maxBytes
	if t.truncated && !tail.truncated {
		// 留下的这一段本身没超（尾巴修剪过），按总数看是哪个超了：字节超了算按字节，不然算按行
		t.byLines = o.totalDecoded <= maxBytes
	}
	t.totalLines = o.totalLines
	return t
}

func (o *outputAcc) appendDecoded(text string) {
	if text == "" {
		return
	}
	if o.sink != nil && o.err == nil {
		_, o.err = io.WriteString(o.sink, text)
	}
	n := len(text)
	o.totalDecoded += n
	o.tail += text
	o.tailBytes += n
	if o.tailBytes > maxRollingBytes*2 {
		o.trimTail()
	}
	if nl := strings.Count(text, "\n"); nl == 0 {
		o.curLineBytes += n
		o.openLine = true
	} else {
		o.completedLines += nl
		rest := text[strings.LastIndexByte(text, '\n')+1:]
		o.curLineBytes = len(rest)
		o.openLine = rest != ""
	}
	o.totalLines = o.completedLines
	if o.openLine {
		o.totalLines++
	}
}

func (o *outputAcc) trimTail() {
	if len(o.tail) <= maxRollingBytes {
		o.tailBytes = len(o.tail)
		return
	}
	start := len(o.tail) - maxRollingBytes
	for start < len(o.tail) && o.tail[start]&0xC0 == 0x80 {
		start++
	}
	if start != 0 {
		o.tailAtLineStart = o.tail[start-1] == '\n'
	}
	o.tail = o.tail[start:]
	o.tailBytes = len(o.tail)
}

func (o *outputAcc) snapshotText() string {
	if o.tailAtLineStart {
		return o.tail
	}
	if i := strings.IndexByte(o.tail, '\n'); i != -1 {
		return o.tail[i+1:]
	}
	return o.tail
}

// truncateTail 照 truncate.js：从尾巴往前整行整行地留，行数和字节哪个先到算哪个；
// 最后一行自己就超过字节上限时，留这一行的最后那么多字节（从字的开头切）
func truncateTail(content string) tailTruncation {
	var lines []string
	if content != "" {
		lines = strings.Split(content, "\n")
		if strings.HasSuffix(content, "\n") {
			lines = lines[:len(lines)-1]
		}
	}
	t := tailTruncation{totalLines: len(lines)}
	if len(lines) <= maxLines && len(content) <= maxBytes {
		t.content, t.outputLines, t.outputBytes = content, len(lines), len(content)
		return t
	}
	t.truncated = true
	var out []string
	n := 0
	t.byLines = true
	for i := len(lines) - 1; i >= 0 && len(out) < maxLines; i-- {
		add := len(lines[i])
		if len(out) > 0 {
			add++
		}
		if n+add > maxBytes {
			t.byLines = false
			if len(out) == 0 {
				s := lines[i]
				start := len(s) - maxBytes
				for start < len(s) && s[start]&0xC0 == 0x80 {
					start++
				}
				out = []string{s[start:]}
				t.lastLinePartial = true
			}
			break
		}
		out = append([]string{lines[i]}, out...)
		n += add
	}
	if len(out) >= maxLines && n <= maxBytes {
		t.byLines = true
	}
	t.content = strings.Join(out, "\n")
	t.outputLines, t.outputBytes = len(out), len(t.content)
	return t
}

type outputSinkKey struct{}

func WithOutputSink(ctx context.Context, sink io.Writer) context.Context {
	return context.WithValue(ctx, outputSinkKey{}, sink)
}
