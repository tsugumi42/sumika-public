package main

// 拷库（规格-记忆写入上线 W6 技术路线第 1 点）：把 Go 的聊天库组和这个角色的记忆库拷进 -work 下的副本目录。
//
//	拿数据锁（拿不到就不拷）→ 路径守卫 → 拷贝前记一次指纹 → 拷 → 拷贝后再记一次 → 对副本核对 → 放锁
//
// 锁只持到拷完并核对完。工具只证明"拷贝那一刻的一致"（前后两次指纹相同、副本的记忆库和拷贝前一字节不差、副本 integrity_check 通过），
// 不声称原库"全程没动"：拷完以后这个工具再不打开原库，这一点用测试证明（拷完把原目录改名，照跑），不靠指纹。
// 副本里不放 memory-v2.db：它是从 memory.db 派生的全文和向量索引，拷旧的会让向量和库对不上，Open 会在副本旁重建全文那部分

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/datalock"
)

// fileFP 是一个源文件的指纹：记忆库（和它的 -wal）算 SHA-256，聊天库只记大小和修改时间（几百 MB 以上的库在锁里算哈希，拷贝的时间就更长）
type fileFP struct {
	Size    int64
	ModNano int64
	SHA     string
}

func (a fileFP) same(b fileFP) bool {
	if a.Size != b.Size {
		return false
	}
	if a.SHA != "" || b.SHA != "" {
		return a.SHA == b.SHA
	}
	return a.ModNano == b.ModNano
}

type snapOpts struct {
	Data, Copy, Char string
	Guard            pathGuard
	MaxAttempts      int // 前后指纹对不上时最多重拷几次，零用 3
	Logf             func(string, ...any)
	// hookMid 只给测试：拷完、记"拷贝后"指纹之前调用，用来模拟拷的时候有人在写
	hookMid func(attempt int)
}

// snapshot 是一次拷贝的记录（不含聊天正文）
type snapshot struct {
	MemoryRel   string
	Files       []string // 拷了哪些（相对数据目录，斜杠写法）
	Attempts    int      // 第几次才拷到一致的
	Elapsed     time.Duration
	Before      map[string]fileFP
	After       map[string]fileFP
	IntegrityOK bool
}

// errNoStill：重拷到上限，前后指纹还是对不上
var errNoStill = errors.New("拷贝的时候库一直在变，拷不到静止的版本")

// sourceFiles 列出要拷的源文件：记忆库、trace 库、go/chat 下每个 .db，各带 -wal、-shm（有就拷）。返回记忆库的相对路径和全部相对路径
func sourceFiles(data, char string, guard pathGuard) (memRel string, rels []string, err error) {
	memRel, err = checkMemorySource(data, char)
	if err != nil {
		return "", nil, err
	}
	bases := []string{memRel, "go/trace.db"}
	entries, err := os.ReadDir(filepath.Join(data, "go", "chat"))
	if err != nil {
		return "", nil, fmt.Errorf("读 go/chat：%w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			bases = append(bases, "go/chat/"+e.Name())
		}
	}
	sort.Strings(bases)
	if _, err := os.Stat(filepath.Join(data, "go", "trace.db")); err != nil {
		return "", nil, fmt.Errorf("没有 go/trace.db：%w", err)
	}
	for _, b := range bases {
		for _, ext := range []string{"", "-wal", "-shm"} {
			rel := b + ext
			if _, err := os.Lstat(filepath.Join(data, filepath.FromSlash(rel))); err != nil {
				if ext == "" {
					return "", nil, fmt.Errorf("没有 %s：%w", rel, err)
				}
				continue
			}
			if err := guard(data, rel); err != nil {
				return "", nil, err
			}
			rels = append(rels, rel)
		}
	}
	return memRel, rels, nil
}

func fingerprint(data string, rels []string, memRel string) (map[string]fileFP, error) {
	out := map[string]fileFP{}
	for _, rel := range rels {
		p := filepath.Join(data, filepath.FromSlash(rel))
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		fp := fileFP{Size: fi.Size(), ModNano: fi.ModTime().UnixNano()}
		if rel == memRel || rel == memRel+"-wal" {
			if fp.SHA, err = sha256File(p); err != nil {
				return nil, err
			}
		}
		out[rel] = fp
	}
	return out, nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// takeSnapshot 做完整的一轮。Copy 目录必须已经存在（上一层已经在里面拿了它自己的锁）
func takeSnapshot(ctx context.Context, o snapOpts) (*snapshot, error) {
	if o.Guard == nil {
		o.Guard = strictGuard
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	lock, err := datalock.LockExisting(o.Data)
	switch {
	case errors.Is(err, datalock.ErrDataInUse):
		return nil, fmt.Errorf("%w：请先退出桌面程序和 Go 后台，再跑影子整理（它要拷到静止的库）", err)
	case errors.Is(err, datalock.ErrLocalInUse):
		return nil, fmt.Errorf("%w：Bun 后台还在写本地私聊和记忆，请先退出它，再跑影子整理（它要拷到静止的库）", err)
	case err != nil:
		return nil, fmt.Errorf("拿不到数据锁（%w）：这个数据目录从没跑过 Go 后台？不拷", err)
	}
	defer lock.Close()

	started := time.Now()
	memRel, rels, err := sourceFiles(o.Data, o.Char, o.Guard)
	if err != nil {
		return nil, err
	}
	logf("拷贝开始：%d 个文件。拷贝期间后台是退出的，拷贝耗时就是 QQ 的空档", len(rels))
	snap := &snapshot{MemoryRel: memRel, Files: rels}
	still := false
	var badCopy error // 最近一次试的副本 integrity_check 没过的原因；这一次试过得去就清掉
	for attempt := 1; attempt <= o.MaxAttempts; attempt++ {
		snap.Attempts = attempt
		badCopy = nil
		before, err := fingerprint(o.Data, rels, memRel)
		if err != nil {
			return nil, fmt.Errorf("记指纹：%w", err)
		}
		for _, rel := range rels {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := copyFile(filepath.Join(o.Data, filepath.FromSlash(rel)), filepath.Join(o.Copy, filepath.FromSlash(rel))); err != nil {
				return nil, fmt.Errorf("拷 %s：%w", rel, err)
			}
		}
		if o.hookMid != nil {
			o.hookMid(attempt)
		}
		after, err := fingerprint(o.Data, rels, memRel)
		if err != nil {
			return nil, fmt.Errorf("记指纹：%w", err)
		}
		snap.Before, snap.After = before, after
		if !sameFPs(before, after) {
			logf("第 %d 次拷的时候库变了，重拷", attempt)
			continue
		}
		// 副本和拷贝前的源逐字节一致（只比记忆库和它的 -wal，聊天库靠前后指纹）
		for rel, fp := range before {
			if fp.SHA == "" {
				continue
			}
			got, err := sha256File(filepath.Join(o.Copy, filepath.FromSlash(rel)))
			if err != nil || got != fp.SHA {
				return nil, fmt.Errorf("副本 %s 和源对不上（拷坏了？），不继续", rel)
			}
		}
		// 副本自己的完整性：-wal 拷到半截、拷坏的页在这里看得出来，不过就重拷（规格 W6 第 1 点）。
		// 放在放锁之前：这一轮不过还要再拷，数据锁得一直拿着，桌面程序和后台才不会趁空档又去动库
		if err := checkCopies(o.Copy, rels); err != nil {
			badCopy = err
			logf("第 %d 次拷的副本 integrity_check 没过，重拷：%v", attempt, err)
			continue
		}
		snap.Elapsed = time.Since(started)
		// 锁到这里放：核对副本不需要原库
		if err := lock.Close(); err != nil {
			return nil, fmt.Errorf("放数据锁：%w", err)
		}
		still = true
		break
	}
	if !still {
		if badCopy != nil {
			return nil, fmt.Errorf("副本的 integrity_check 试了 %d 次都没过：%w", o.MaxAttempts, badCopy)
		}
		return nil, fmt.Errorf("%w（试了 %d 次）", errNoStill, o.MaxAttempts)
	}
	snap.IntegrityOK = true
	return snap, nil
}

// checkCopies 对副本里每个库文件（不含 -wal、-shm）做 integrity_check
func checkCopies(copyDir string, rels []string) error {
	for _, rel := range rels {
		if strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") {
			continue
		}
		if err := integrityCheck(filepath.Join(copyDir, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("副本 %s 的 integrity_check 没过：%w", rel, err)
		}
	}
	return nil
}

func sameFPs(a, b map[string]fileFP) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !v.same(w) {
			return false
		}
	}
	return true
}

// sqliteDSN：照 memory.openReadOnly 的 URI 转义。副本是我们自己的，可写打开
func sqliteDSN(file string, readOnly bool) string {
	p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
	if !readOnly {
		return "file:" + p
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + p + "?" + q.Encode()
}

func integrityCheck(file string) error {
	db, err := sql.Open("sqlite", sqliteDSN(file, false))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if s != "ok" {
			bad = append(bad, s)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s", strings.Join(bad[:min(len(bad), 3)], "；"))
	}
	return nil
}
