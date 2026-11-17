package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/tokenizer"
)

// TokenizerFile 是模型目录里分词器文件的名字（server.py 的 TOKENIZER_FILE）。
const TokenizerFile = "tokenizer.json"

// FileLoader 是真的加载函数：从模型根目录读 onnx 和 tokenizer.json，核 SHA-256，建分词器和 ORT 会话，装成 Embedder 或
// Reranker。池拿它的 Load 和 Estimate；sumika-models 启动时拿它的 Check（文件在不在）和 Verify（哈希对不对）。
//
// 错误里只写相对模型根目录的路径（dir/文件），绝对路径换成 <模型根目录>：错误会原样进 HTTP 500 的正文（规格第三节"加载失败"）。
type FileLoader struct {
	root          string
	rt            ort.Runtime
	session       ort.Options
	maxLineTokens int

	newTokenizer func(tokenizer.Spec, []byte) (engine.Tokenizer, error) // 测试注入假的
	hash         func(context.Context, string) (string, error)          // 同上，测试里数它被调了几次

	mu       sync.Mutex
	verified map[string]fileStamp // onnx 哈希核过的文件，大小和修改时间没变就不再算（首次哈希 gte 约 0.6 秒）
}

type fileStamp struct {
	size  int64
	mtime time.Time
}

// NewFileLoader root 必须是绝对路径。rt 可以是 nil（只做 Check、Verify、Estimate 的时候，比如 --verify 不加载 dll）。
// session 是 ORT 会话选项，零值就是生产值；maxLineTokens 是嵌入的单行 token 上限（--max-line-tokens，0 = 登记表的值）。
func NewFileLoader(root string, rt ort.Runtime, session ort.Options, maxLineTokens int) (*FileLoader, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("模型根目录必须是绝对路径：%q", root)
	}
	return &FileLoader{
		root: filepath.Clean(root), rt: rt, session: session, maxLineTokens: maxLineTokens,
		newTokenizer: loadRealTokenizer,
		hash:         hashFile,
		verified:     map[string]fileStamp{},
	}, nil
}

func loadRealTokenizer(spec tokenizer.Spec, raw []byte) (engine.Tokenizer, error) {
	tk, err := tokenizer.Load(spec, raw)
	if err != nil {
		return nil, err
	}
	return tk, nil
}

func (l *FileLoader) onnxPath(spec engine.Spec) string {
	return filepath.Join(l.root, filepath.FromSlash(spec.Dir), filepath.FromSlash(spec.Onnx))
}

func (l *FileLoader) tokenizerPath(spec engine.Spec) string {
	return filepath.Join(l.root, filepath.FromSlash(spec.Dir), TokenizerFile)
}

func relOnnx(spec engine.Spec) string      { return spec.Dir + "/" + spec.Onnx }
func relTokenizer(spec engine.Spec) string { return spec.Dir + "/" + TokenizerFile }

// RowTokens 是这个模型一行最多会前向多少个 token：嵌入 = min(登记表的 max_tokens, 单行上限)，重排 = 登记表的 max_tokens。
func (l *FileLoader) RowTokens(spec engine.Spec) int {
	if spec.Kind == engine.KindEmbed && l.maxLineTokens > 0 && l.maxLineTokens < spec.MaxTokens {
		return l.maxLineTokens
	}
	return spec.MaxTokens
}

// Estimate 读 onnx 文件的大小，按 EstimateBytes 估计占多少字节。文件不在就返回错误。
func (l *FileLoader) Estimate(spec engine.Spec) (uint64, error) {
	info, err := statFile(l.onnxPath(spec), relOnnx(spec))
	if err != nil {
		return 0, err
	}
	return EstimateBytes(spec, info.Size(), l.RowTokens(spec)), nil
}

// Check 看 onnx 和 tokenizer.json 两个文件在不在、是不是文件。不核哈希、不读内容；一次列出所有缺的。
// --lazy-models 里的模型启动时只做这一步，决定 /v1/models 里的 ready。
func (l *FileLoader) Check(spec engine.Spec) error {
	var problems []string
	for _, f := range []struct{ abs, rel string }{{l.onnxPath(spec), relOnnx(spec)}, {l.tokenizerPath(spec), relTokenizer(spec)}} {
		if _, err := statFile(f.abs, f.rel); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "；"))
	}
	return nil
}

// Verify 核两个文件的 SHA-256 是否和登记表一致（onnx 的结果按大小加修改时间缓存，Load 不用再算）。
// --models 点名的模型启动时必须过这一步，--verify 对全部模型做。返回花在哈希上的毫秒数。
func (l *FileLoader) Verify(ctx context.Context, spec engine.Spec) (int64, error) {
	ms, err := l.verifyOnnx(ctx, spec)
	if err != nil {
		return ms, err
	}
	t0 := time.Now()
	got, err := l.hash(ctx, l.tokenizerPath(spec))
	ms += time.Since(t0).Milliseconds()
	if err != nil {
		return ms, l.fileProblem(relTokenizer(spec), err)
	}
	if got != spec.TokenizerSHA256 {
		return ms, fmt.Errorf("%s 的 SHA-256 是 %s，登记的是 %s", relTokenizer(spec), got, spec.TokenizerSHA256)
	}
	return ms, nil
}

// verifyOnnx 核 onnx 的哈希，返回花了多少毫秒（缓存命中是 0）。
func (l *FileLoader) verifyOnnx(ctx context.Context, spec engine.Spec) (int64, error) {
	abs, rel := l.onnxPath(spec), relOnnx(spec)
	info, err := statFile(abs, rel)
	if err != nil {
		return 0, err
	}
	stamp := fileStamp{size: info.Size(), mtime: info.ModTime()}
	key := abs + "|" + spec.OnnxSHA256
	l.mu.Lock()
	known, ok := l.verified[key]
	l.mu.Unlock()
	if ok && known == stamp {
		return 0, nil
	}
	t0 := time.Now()
	got, err := l.hash(ctx, abs)
	ms := time.Since(t0).Milliseconds()
	if err != nil {
		return ms, l.fileProblem(rel, err)
	}
	if got != spec.OnnxSHA256 {
		return ms, fmt.Errorf("%s 的 SHA-256 是 %s，登记的是 %s", rel, got, spec.OnnxSHA256)
	}
	l.mu.Lock()
	l.verified[key] = stamp
	l.mu.Unlock()
	return ms, nil
}

// Load 是池的加载函数：核 onnx 哈希 → 读分词器（Load 里核它的哈希）→ 建 ORT 会话 → 装成引擎。
// 每一步之间看 ctx（池关闭时取消）；任何一步失败，已经建出来的会话都关掉。
func (l *FileLoader) Load(ctx context.Context, spec engine.Spec) (Loaded, error) {
	if l.rt == nil {
		return Loaded{}, errors.New("没有 ONNX Runtime")
	}
	hashMs, err := l.verifyOnnx(ctx, spec)
	if err != nil {
		return Loaded{}, err
	}
	raw, err := os.ReadFile(l.tokenizerPath(spec))
	if err != nil {
		return Loaded{}, l.fileProblem(relTokenizer(spec), err)
	}
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	tok, err := l.newTokenizer(tokenizer.Spec{SHA256: spec.TokenizerSHA256}, raw)
	if err != nil {
		return Loaded{}, fmt.Errorf("分词器 %s：%s", relTokenizer(spec), l.clean(err))
	}
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	sess, err := l.rt.NewSession(l.onnxPath(spec), l.session)
	if err != nil {
		return Loaded{}, errors.New(l.clean(err))
	}
	ld := Loaded{HashMs: hashMs, Close: sess.Close}
	switch spec.Kind {
	case engine.KindEmbed:
		ld.Embedder, err = engine.NewEmbedder(spec, tok, sess, engine.EmbedderOptions{MaxLineTokens: l.maxLineTokens})
	case engine.KindRerank:
		ld.Reranker, err = engine.NewReranker(spec, tok, sess)
	default:
		err = fmt.Errorf("不认识的 kind %q", spec.Kind)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = sess.Close()
		return Loaded{}, errors.New(l.clean(err))
	}
	return ld, nil
}

// clean 把错误文字里的模型根目录绝对路径换掉。
func (l *FileLoader) clean(err error) string {
	s := err.Error()
	for _, root := range []string{l.root, filepath.ToSlash(l.root)} {
		s = strings.ReplaceAll(s, root, "<模型根目录>")
	}
	return s
}

// fileProblem 把读文件的错误翻成不带绝对路径的一句话。
func (l *FileLoader) fileProblem(rel string, err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("找不到文件 %s", rel)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("没有权限读 %s", rel)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("读 %s 失败：%v", rel, err)
}

// statFile 取文件信息；不存在、是目录、读不了都翻成不带绝对路径的话。
func statFile(abs, rel string) (fs.FileInfo, error) {
	info, err := os.Stat(abs)
	switch {
	case err == nil && info.IsDir():
		return nil, fmt.Errorf("%s 是目录，不是文件", rel)
	case err == nil:
		return info, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("找不到文件 %s", rel)
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("没有权限读 %s", rel)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return nil, fmt.Errorf("读 %s 失败：%v", rel, err)
}

// hashFile 流式算文件的 SHA-256（小写十六进制）；每读 1 MiB 看一次 ctx。
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
