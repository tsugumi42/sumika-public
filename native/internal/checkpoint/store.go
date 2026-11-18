package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"sumika-native/internal/kernel"
)

const metadataLimit = 8 << 20

type packKey struct{ id, name string }

type Service struct {
	dir      string
	opts     Options
	gate     chan struct{}
	buffer   []byte
	packs    map[packKey]*os.File
	progress restoreProgress
}

func New(dir string, opts Options) (*Service, error) {
	opts = opts.defaults()
	if opts.Authorize == nil || opts.MaxFileBytes < 1 || opts.MaxScanBytes < 1 || opts.MaxStoreBytes < 1 || opts.MaxFiles < 1 || opts.MaxEntries < 1 || opts.MaxDepth < 1 || opts.MaxRecords < 1 || opts.Retain < 0 {
		return nil, ErrUnavailable
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	root, _, err := openWorkspace(abs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	s := &Service{dir: abs, opts: opts, gate: make(chan struct{}, 1)}
	records, used, err := s.records(root, true)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if err := cleanupRestoreMaterials(&records[i], root); err != nil {
			// 工作区可能已被主人移动或替换；拒绝清理并留告警，不能因此挡住后台打开存储。
			slog.Warn("检查点临时材料未回收", "checkpoint_id", records[i].ID, "err", err)
		}
	}
	// 打开时按平时的规则清一遍（过期、超额；无记录的孤儿目录已在上面的 records 里清掉），不另起定时器。
	if err := s.reserve(root, records, used, 0, nil); err != nil && !errors.Is(err, ErrLimit) {
		slog.Warn("检查点打开时清理未完成", "err", err)
	}
	return s, nil
}
func (s *Service) enter(ctx context.Context) (*os.Root, func(), error) {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	root, _, err := openWorkspace(s.dir)
	if err != nil {
		<-s.gate
		return nil, nil, err
	}
	return root, func() {
		for key, f := range s.packs {
			f.Close()
			delete(s.packs, key)
		}
		root.Close()
		<-s.gate
	}, nil
}
func (s *Service) authorize(ctx context.Context, owner kernel.Scope, name string) error {
	if !ownerScope(owner) {
		return ErrUnavailable
	}
	return s.opts.Authorize(ctx, owner, name)
}
func (s *Service) records(root *os.Root, cleanup bool) ([]Record, int64, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, 0, err
	}
	defer dir.Close()
	var records []Record
	var used int64
	for {
		batch, readErr := dir.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, 0, readErr
		}
		for _, entry := range batch {
			if !entry.IsDir() || !hexID(entry.Name(), 32) {
				return nil, 0, ErrUnavailable
			}
			if err := checkParents(root, entry.Name()); err != nil {
				return nil, 0, err
			}
			recdir, err := root.OpenRoot(entry.Name())
			if err != nil {
				return nil, 0, err
			}
			rec, size, err := s.readRecord(recdir, entry.Name())
			recdir.Close()
			if errors.Is(err, os.ErrNotExist) && rec.ID == "" && cleanup {
				if err := root.RemoveAll(entry.Name()); err != nil {
					return nil, 0, err
				}
				continue
			}
			if err != nil {
				return nil, 0, err
			}
			used += size
			records = append(records, rec)
			if len(records) > s.opts.MaxRecords+1 {
				return nil, 0, ErrLimit
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Sequence < records[j].Sequence })
	for i := 1; i < len(records); i++ {
		if records[i].Sequence == records[i-1].Sequence {
			return nil, 0, ErrUnavailable
		}
	}
	return records, used, nil
}
func (s *Service) readRecord(dir *os.Root, id string) (Record, int64, error) {
	f, err := dir.Open("record.json")
	if err != nil {
		return Record{}, 0, err
	}
	if _, err := checkHandle(f, filepath.Join(dir.Name(), "record.json")); err != nil {
		f.Close()
		return Record{}, 0, err
	}
	body, err := io.ReadAll(io.LimitReader(f, metadataLimit+1))
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return Record{}, 0, err
	}
	if len(body) > metadataLimit {
		return Record{}, 0, ErrLimit
	}
	var r Record
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, 0, err
	}
	if r.Format != 1 || r.ID != id || !hexID(r.RunFirstID, 32) || r.Sequence == 0 || !ownerScope(r.Owner) || !filepath.IsAbs(r.CWD) || r.RootIdentity == "" || r.Pack != "data.bin" && r.Pack != "ready.bin" {
		return r, 0, ErrUnavailable
	}
	if r.InverseOf != "" && (!hexID(r.InverseOf, 32) || r.Tool != "checkpoint_restore") {
		return r, 0, ErrUnavailable
	}
	switch r.Status {
	case "ready", "partial", "interrupted":
	case "skipped":
		if len(r.Before) > 0 || len(r.After) > 0 || len(r.Blobs) > 0 || r.InverseOf != "" || r.SkipReason == "" {
			return r, 0, ErrUnavailable
		}
	case "pending":
		r.Status = "interrupted"
	default:
		return r, 0, ErrUnavailable
	}
	pack, err := dir.Stat(r.Pack)
	if err != nil {
		return r, 0, err
	}
	if linkInfo(pack) || !pack.Mode().IsRegular() {
		return r, 0, ErrLink
	}
	for hash, b := range r.Blobs {
		if !hexID(hash, 64) || b.Offset < 0 || b.Bytes < 0 || b.Bytes > s.opts.MaxFileBytes || b.Offset > pack.Size() || b.Bytes > pack.Size()-b.Offset {
			return r, 0, ErrUnavailable
		}
	}
	for _, states := range []map[string]FileState{r.Before, r.After} {
		if len(states) > s.opts.MaxFiles {
			return r, 0, ErrLimit
		}
		for name, state := range states {
			if err := validName(name); err != nil {
				return r, 0, err
			}
			if excludedName(name) {
				return r, 0, ErrUnavailable
			}
			if !state.Exists {
				if state != (FileState{}) {
					return r, 0, ErrUnavailable
				}
				continue
			}
			blob, ok := r.Blobs[state.Hash]
			if !ok || !hexID(state.Hash, 64) || blob.Bytes != state.Bytes || state.Mode&^os.ModePerm != 0 {
				return r, 0, ErrUnavailable
			}
		}
	}
	entries, err := dir.Open(".")
	if err != nil {
		return r, 0, err
	}
	defer entries.Close()
	files, err := entries.ReadDir(8)
	if err != nil && !errors.Is(err, io.EOF) {
		return r, 0, err
	}
	var size int64
	if len(files) > 6 {
		return r, 0, ErrUnavailable
	}
	for _, file := range files {
		switch file.Name() {
		case "record.json", "record.next", "data.bin", "ready.bin", "materials.next", "materials.jsonl":
		default:
			return r, 0, ErrUnavailable
		}
		fi, err := dir.Lstat(file.Name())
		if err != nil {
			return r, 0, err
		}
		if linkInfo(fi) || !fi.Mode().IsRegular() {
			return r, 0, ErrLink
		}
		if (file.Name() == "materials.jsonl" || file.Name() == "materials.next") && fi.Size() > metadataLimit {
			return r, 0, ErrLimit
		}
		size += fi.Size()
	}
	return r, size, nil
}
func saveRecord(dir *os.Root, r Record) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(body) > metadataLimit {
		return ErrLimit
	}
	if err := checkParents(dir, "record.next"); err != nil {
		return err
	}
	f, err := dir.OpenFile("record.next", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return durableRename(dir, "record.next", "record.json")
}
func (s *Service) reserve(root *os.Root, records []Record, used, reserved int64, pinned map[string]bool) error {
	remaining := len(records)
	cutoff := s.opts.Now().Add(-s.opts.Retain).UnixMilli()
	for _, r := range records {
		old := r.CreatedMS < cutoff
		if !old && remaining < s.opts.MaxRecords && used+reserved <= s.opts.MaxStoreBytes {
			continue
		}
		if pinned[r.ID] {
			continue
		}
		dir, err := root.OpenRoot(r.ID)
		if err != nil {
			return err
		}
		_, size, err := s.readRecord(dir, r.ID)
		dir.Close()
		if err != nil {
			return err
		}
		if err := retireRecord(root, r.ID, (*os.Root).RemoveAll); err != nil {
			return err
		}
		used -= size
		remaining--
	}
	if remaining >= s.opts.MaxRecords || used+reserved > s.opts.MaxStoreBytes {
		return ErrLimit
	}
	return nil
}

// 先持久摘除可见元数据，才允许删包；中断后只会留下无 record.json 的自有孤儿。
func retireRecord(root *os.Root, id string, remove func(*os.Root, string) error) error {
	dir, err := root.OpenRoot(id)
	if err != nil {
		return err
	}
	err = durableRename(dir, "record.json", "record.delete")
	err = errors.Join(err, dir.Close())
	if err != nil {
		return err
	}
	return remove(root, id)
}
func (s *Service) create(root *os.Root, records []Record, c Capture, identity string) (Record, *os.Root, error) {
	id, err := newID()
	if err != nil {
		return Record{}, nil, err
	}
	sequence := uint64(1)
	if len(records) > 0 {
		sequence = records[len(records)-1].Sequence + 1
	}
	if err := root.Mkdir(id, 0700); err != nil {
		return Record{}, nil, err
	}
	dir, err := root.OpenRoot(id)
	if err != nil {
		return Record{}, nil, err
	}
	r := Record{Format: 1, ID: id, RunFirstID: id, Sequence: sequence, CreatedMS: s.opts.Now().UnixMilli(), Invocation: c.Invocation, Owner: c.Owner, CWD: filepath.Clean(c.CWD), Tool: c.Tool, Whole: c.Paths == nil, Status: "pending", Pack: "data.bin", Blobs: map[string]Blob{}}
	r.RootIdentity = identity
	for i := len(records) - 1; i >= 0; i-- {
		previous := &records[i]
		if previous.Invocation.RunID == c.Invocation.RunID && previous.Invocation.RootID == c.Invocation.RootID && previous.Invocation.Scope == c.Invocation.Scope && previous.RootIdentity == identity {
			r.RunFirstID = previous.RunFirstID
			break
		}
	}
	return r, dir, nil
}

type packWriter struct {
	file   *os.File
	record *Record
	offset int64
}

func openPack(dir *os.Root, r *Record) (*packWriter, error) {
	f, err := dir.OpenFile(r.Pack, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &packWriter{file: f, record: r, offset: fi.Size()}, nil
}
func (w *packWriter) put(hash string, body []byte) error {
	if _, exists := w.record.Blobs[hash]; exists {
		return nil
	}
	n, err := w.file.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	w.record.Blobs[hash] = Blob{Offset: w.offset, Bytes: int64(n)}
	w.offset += int64(n)
	return nil
}
func (w *packWriter) close() error { return errors.Join(w.file.Sync(), w.file.Close()) }
func (s *Service) object(root *os.Root, r *Record, hash string) ([]byte, error) {
	key := packKey{r.ID, r.Pack}
	f := s.packs[key]
	if f == nil {
		var err error
		f, err = root.Open(filepath.Join(r.ID, r.Pack))
		if err != nil {
			return nil, err
		}
		if _, err := checkHandle(f, filepath.Join(root.Name(), r.ID, r.Pack)); err != nil {
			f.Close()
			return nil, err
		}
		if s.packs == nil {
			s.packs = make(map[packKey]*os.File)
		}
		// 门持有期间按包复用句柄；不按对象重开，离开本次操作即全部关闭。
		s.packs[key] = f
	}
	return readObject(f, r, hash, s.opts.MaxFileBytes, &s.buffer)
}

func readObject(f *os.File, r *Record, hash string, limit int64, buffer *[]byte) ([]byte, error) {
	blob, ok := r.Blobs[hash]
	if !ok || blob.Bytes < 0 || blob.Bytes > limit || blob.Offset < 0 {
		return nil, ErrUnavailable
	}
	if cap(*buffer) < int(blob.Bytes) {
		*buffer = make([]byte, blob.Bytes)
	}
	body := (*buffer)[:blob.Bytes]
	if _, err := f.ReadAt(body, blob.Offset); err != nil {
		return nil, err
	}
	if digestBytes(body) != hash {
		return nil, fmt.Errorf("检查点对象损坏：%s", hash)
	}
	return body, nil
}
func (s *Service) compact(root, dir *os.Root, r *Record) (bool, error) {
	f, err := dir.OpenFile("ready.bin", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	committed := *r
	next := map[string]Blob{}
	var offset int64
	for _, states := range []map[string]FileState{r.Before, r.After} {
		for _, state := range states {
			if !state.Exists {
				continue
			}
			if _, ok := next[state.Hash]; ok {
				continue
			}
			body, err := s.object(root, &committed, state.Hash)
			if err != nil {
				f.Close()
				return false, err
			}
			n, err := f.Write(body)
			if err != nil {
				f.Close()
				return false, err
			}
			if n != len(body) {
				f.Close()
				return false, io.ErrShortWrite
			}
			next[state.Hash] = Blob{Offset: offset, Bytes: int64(n)}
			offset += int64(n)
		}
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return false, err
	}
	key := packKey{r.ID, r.Pack}
	if source := s.packs[key]; source != nil {
		delete(s.packs, key)
		if err := source.Close(); err != nil {
			return false, err
		}
	}
	// 先发布新包索引，再更新返回记录；失败时仍保留 pending 引用的旧包。
	committed.Pack, committed.Blobs = "ready.bin", next
	if err := saveRecord(dir, committed); err != nil {
		return false, err
	}
	oldPack := r.Pack
	*r = committed
	return true, dir.Remove(oldPack)
}
