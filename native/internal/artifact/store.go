// Package artifact 保存工具的规范正文；引用是索引键，不能作为路径或授权。
package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

var ErrUnavailable = errors.New("结果不可访问或不存在")
var ErrLimit = errors.New("结果采集已达硬限；生产者已取消，原文不完整")

type Store struct {
	DB            *store.Store
	Dir           string
	Limits        config.Work
	Now           func() time.Time
	SourceAllowed func(kernel.Scope, string) bool
	mu            sync.Mutex
	active        map[string]bool
	activeTools   map[string]bool
	verified      map[string]fileVersion
	// 文件系统故障注入只用于真实文件路径上的失败验收。
	create    func(string, string) (*os.File, error)
	rename    func(string, string) error
	closeFile func(*os.File) error
	writeFile func(*os.File, []byte) (int, error)
}
type fileVersion struct {
	size     int64
	modified time.Time
}

func New(db *store.Store, dir string, limits config.Work) *Store {
	if limits == (config.Work{}) {
		limits = config.DefaultWork()
	}
	return &Store{DB: db, Dir: dir, Limits: limits, Now: time.Now, active: map[string]bool{}, activeTools: map[string]bool{}, verified: map[string]fileVersion{}, create: os.CreateTemp, rename: os.Rename, closeFile: func(f *os.File) error { return f.Close() }, writeFile: func(f *os.File, p []byte) (int, error) { return f.Write(p) }}
}

// ValidScope 只接受宿主完整身份；正文和 PrivateOK 不能补发 authority。
func ValidScope(s kernel.Scope) bool {
	for _, v := range []string{s.CharacterID, s.Conversation, s.ScopeID, s.Platform, s.Channel, s.AccountID, s.ActorID, s.AuthorityID} {
		if v == "" || v == "0" {
			return false
		}
	}
	if s.ActorKind != "owner" && s.ActorKind != "human" {
		return false
	}
	if s.Scene != "private" && s.Scene != "group" {
		return false
	}
	// 发言人是接收账号自己的身份不收，不论渠道（kernel.Scope.ActorIsAccount 归一后比较）
	if s.ActorIsAccount() {
		return false
	}
	if s.Channel == "console" {
		return s.Platform == "local"
	}
	return s.Channel == "qq" && s.Platform == "qq" && !s.ActorIsAccount()
}
func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

type Capture struct {
	store          *Store
	inv            kernel.Invocation
	name, id       string
	file           *os.File
	path           string
	hash           hash.Hash
	bytes          int64
	limited, ended bool
	err            error
}

func (s *Store) Begin(ctx context.Context, inv kernel.Invocation, media string) (*Capture, error) {
	if !ValidScope(inv.Scope) || inv.RootID == "" || inv.RunID == "" || inv.ToolID == "" {
		return nil, ErrUnavailable
	}
	if err := s.Limits.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := s.DB.ReserveResult(ctx, inv, int64(s.Limits.ResultMaxBytes), int64(s.Limits.RootMaxBytes))
	if err != nil {
		return nil, err
	}
	release := func(e error) (*Capture, error) {
		return nil, errors.Join(e, s.DB.ReleaseResult(context.WithoutCancel(ctx), inv.RootID, inv.ToolID))
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return release(err)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return release(err)
	}
	f, err := s.create(s.Dir, ".collect-")
	if err != nil {
		return release(err)
	}
	c := &Capture{store: s, inv: inv, name: name, id: hex.EncodeToString(id[:]), file: f, path: f.Name(), hash: sha256.New()}
	s.mu.Lock()
	s.active[filepath.Base(c.path)] = true
	s.active[c.id] = true
	s.activeTools[inv.ToolID] = true
	s.mu.Unlock()
	return c, nil
}
func (c *Capture) Write(p []byte) (int, error) {
	if c.ended {
		return 0, os.ErrClosed
	}
	if c.err != nil {
		return 0, c.err
	}
	if c.limited {
		return 0, ErrLimit
	}
	remaining := int64(c.store.Limits.ResultMaxBytes) - c.bytes
	n := len(p)
	if int64(n) >= remaining {
		n = int(remaining)
		c.limited = true
	}
	if n > 0 {
		w, err := c.store.writeFile(c.file, p[:n])
		c.hash.Write(p[:w])
		c.bytes += int64(w)
		if err != nil {
			c.err = err
			return w, err
		}
		if w != n {
			c.err = io.ErrShortWrite
			return w, c.err
		}
	}
	if c.limited {
		return n, ErrLimit
	}
	return n, nil
}
func (c *Capture) Finish(ctx context.Context, media string, complete bool) (ref kernel.ResultRef, err error) {
	c.ended = true
	defer func() {
		c.store.mu.Lock()
		delete(c.store.active, filepath.Base(c.path))
		delete(c.store.active, c.id)
		delete(c.store.activeTools, c.inv.ToolID)
		c.store.mu.Unlock()
		if err != nil {
			err = errors.Join(err, c.store.DB.ReleaseResult(context.WithoutCancel(ctx), c.inv.RootID, c.inv.ToolID))
		}
	}()
	closeErr := c.store.closeFile(c.file)
	if err = errors.Join(c.err, closeErr); err != nil {
		return ref, errors.Join(err, os.Remove(c.path))
	}
	if err = ctx.Err(); err != nil {
		return ref, errors.Join(err, os.Remove(c.path))
	}
	// 硬限可能切在多字节字符中间；规范文本只保留完整 UTF-8 前缀。
	if c.limited && c.bytes > 0 {
		f, e := os.OpenFile(c.path, os.O_RDWR, 0)
		if e != nil {
			return ref, e
		}
		last := make([]byte, min(int64(4), c.bytes))
		_, e = f.ReadAt(last, c.bytes-int64(len(last)))
		cut := len(last)
		start := len(last) - 1
		for start > 0 && !utf8.RuneStart(last[start]) {
			start--
		}
		if !utf8.FullRune(last[start:]) {
			cut = start
		}
		if cut < len(last) {
			c.bytes -= int64(len(last) - cut)
			e = errors.Join(e, f.Truncate(c.bytes))
		}
		if _, e2 := f.Seek(0, io.SeekStart); e2 != nil {
			e = errors.Join(e, e2)
		}
		c.hash.Reset()
		_, e2 := io.Copy(c.hash, f)
		e = errors.Join(e, e2, f.Close())
		if e != nil {
			return ref, e
		}
	}
	final := filepath.Join(c.store.Dir, c.id)
	if err = c.store.rename(c.path, final); err != nil {
		return ref, errors.Join(err, os.Remove(c.path))
	}
	ref = kernel.ResultRef{ID: c.id, SHA256: hex.EncodeToString(c.hash.Sum(nil)), MediaType: media, Owner: c.inv.Scope, RunID: c.inv.RunID, StepID: c.inv.StepID, ToolID: c.inv.ToolID, Bytes: c.bytes, Complete: complete && !c.limited}
	if err = c.store.DB.CommitResult(ctx, c.inv.RootID, c.name, ref, int64(c.store.Limits.ResultMaxBytes), c.store.Now().UnixMilli()); err != nil {
		return kernel.ResultRef{}, err
	}
	return ref, nil
}
func (s *Store) Put(ctx context.Context, inv kernel.Invocation, media string, r io.Reader, complete bool) (kernel.ResultRef, error) {
	c, err := s.Begin(ctx, inv, media)
	if err != nil {
		return kernel.ResultRef{}, err
	}
	_, err = io.Copy(c, r)
	if err != nil && !errors.Is(err, ErrLimit) {
		c.err = err
	}
	return c.Finish(ctx, media, complete)
}

func Preview(text string, ref kernel.ResultRef, limit int) string {
	suffix := fmt.Sprintf("\n[result id=%s bytes=%d sha256=%s complete=%t; result_read({id,offset,limit}) 续读]", ref.ID, ref.Bytes, ref.SHA256, ref.Complete)
	if len(text)+len(suffix) <= limit {
		return text + suffix
	}
	n := max(0, limit-len(suffix))
	if n < len(text) {
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
	}
	return text[:n] + suffix
}
