package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/scrypt"
)

// 文件里的长度和附加数据（规格 5.3.3）：包裹不能互换、数据部分和包裹也不能互换。
const (
	saltLen     = 16
	ivLen       = 12
	tagLen      = 16
	dekLen      = 32
	aadData     = "data"
	aadPassword = "password"
	aadRecovery = "recovery"
)

// 以下包内变量只给测试替换；生产路径没有开关，也不会读环境变量。
var (
	// 本机加密（Windows 是 DPAPI，非 Windows 返回 ErrUnsupported）。
	protectLocal   = protect
	unprotectLocal = unprotect
	// scrypt 派生 32 字节钥匙。
	deriveKey = func(secret, salt []byte, n, r, p int) ([]byte, error) {
		return scrypt.Key(secret, salt, n, r, p, dekLen)
	}
	// 新写主密码 / 恢复码包裹用的代价（规格：N = 2¹⁷、r = 8、p = 1）。
	writeCost = scryptCost{N: 1 << 17, R: 8, P: 1}
	// 读文件时接受的代价上下限，超出就是格式错误（防改过的文件让解锁吃掉几 GB 内存）。
	acceptCost = scryptLimits{MinN: 1 << 14, MaxN: 1 << 20, MaxR: 16, MaxP: 4}
	// 原子写：测试换成会失败的函数，验证原文件不动。
	writeTmp  = writeTempFile
	renameTmp = os.Rename
	// 替换文件被临时拒绝时两次重试之间的等待；测试换成不睡的替身，不真等。
	renameRetryWait = time.Sleep
	// 取文件状态；测试注入一次失败，验证拒写标记在文件没变时会清掉。
	statFile = os.Stat
)

type scryptCost struct {
	N, R, P int
}

type scryptLimits struct {
	MinN, MaxN, MaxR, MaxP int
}

// scryptMu 保证同一时间只跑一个 scrypt：N = 2¹⁷ 一次要 128 MB 和零点几秒，同时跑几个会
// 把内存吃光和互相拖慢（规格 5.3.3：包内串行）。
var scryptMu sync.Mutex

// derive 串行调用 deriveKey；派生参数由调用方查过上下限。
func derive(secret, salt []byte, n, r, p int) ([]byte, error) {
	scryptMu.Lock()
	defer scryptMu.Unlock()
	return deriveKey(secret, salt, n, r, p)
}

type sealed struct {
	IV  string `json:"iv"`
	Tag string `json:"tag"`
	CT  string `json:"ct"`
}

// kdfSlot 是 password / recovery 的一份包裹：参数和长度都合规时 wrap 非 nil；文件里有这一份
// 但不合规时 raw 原样留着，Open 走本机那份时不挡读、写回不改，Unlock 用到它才报格式错误。
type kdfSlot struct {
	wrap *kdfWrap
	raw  json.RawMessage
}

func (s kdfSlot) present() bool { return s.wrap != nil || s.raw != nil }

type kdfWrap struct {
	KDF  string `json:"kdf"`
	N    int    `json:"N"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt string `json:"salt"`
	IV   string `json:"iv"`
	Tag  string `json:"tag"`
	CT   string `json:"ct"`
}

// 读文件的上限（规格 5.3.3）：超过 4 MB 直接算格式错误，不读进内存。
const maxFileBytes = 4 << 20

// 读包裹时 scrypt 参数的成本上限：内存 128·N·r 不超过 512 MiB、工作量 N·r·p 不超过 2²⁴，
// 防一个改过的文件让解锁吃掉几 GB 内存、跑几十秒。
const (
	maxScryptMemory = 512 << 20
	maxScryptWork   = 1 << 24
)

type envelope struct {
	Version int                        `json:"v"`
	Wraps   map[string]json.RawMessage `json:"wraps"`
	Data    sealed                     `json:"data"`
}

// vault 是文件解析出来的样子；wraps 里不认识的键原样留着，写回时不动。
type vault struct {
	path     string
	stamp    fileStamp // 读到它时文件的样子
	wraps    map[string]json.RawMessage
	dpapi    []byte
	password kdfSlot
	recovery kdfSlot
	data     sealed
}

func load(path string) (*vault, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件失败：%w", err)
	}
	if fi.Size() > maxFileBytes {
		return nil, ErrFormat
	}
	b, err := readFile(path)
	if err != nil {
		return nil, err
	}
	v, err := parse(b)
	if err != nil {
		return nil, err
	}
	v.path = path
	v.stamp = fileStamp{mod: fi.ModTime(), size: int64(len(b)), hash: sha256.Sum256(b)}
	return v, nil
}

// readFile 读密钥文件，读的时候再限一次大小：Stat 之后文件变大了也读不爆内存，超过 4 MB
// 算格式错误（规格 5.3.3）。
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件失败：%w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件失败：%w", err)
	}
	if len(b) > maxFileBytes {
		return nil, ErrFormat
	}
	return b, nil
}

func parse(b []byte) (*vault, error) {
	var shape struct {
		Version int                        `json:"v"`
		Wraps   map[string]json.RawMessage `json:"wraps"`
		Data    *sealed                    `json:"data"`
	}
	// 不拒绝多余的字段：Bun 或以后的版本多写的字段要能读；写回只保留 wraps 里的键，
	// 顶层的多余字段不保留。
	if json.Unmarshal(b, &shape) != nil {
		return nil, ErrFormat
	}
	if shape.Version != 1 || len(shape.Wraps) == 0 || shape.Data == nil {
		return nil, ErrFormat
	}
	v := &vault{wraps: shape.Wraps, data: *shape.Data}
	if raw, ok := shape.Wraps["dpapi"]; ok {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, ErrFormat
		}
		if text != "" {
			blob, err := base64.StdEncoding.DecodeString(text)
			if err != nil || len(blob) == 0 {
				return nil, ErrFormat
			}
			v.dpapi = blob
		}
	}
	// password / recovery：有这一份就收下；参数或长度不合规只标记成坏包裹，Open 走本机那份时
	// 不挡读，Unlock 用到那一份时才报格式错误（5.3.3 复核第 6 条）。
	for key, dst := range map[string]*kdfSlot{"password": &v.password, "recovery": &v.recovery} {
		raw, ok := shape.Wraps[key]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		dst.raw = raw
		var w kdfWrap
		if json.Unmarshal(raw, &w) == nil && w.validate() == nil {
			dst.wrap = &w
		}
	}
	// password 和 recovery 要么都有、要么都没有：只留一份多半是改坏的文件。
	if v.password.present() != v.recovery.present() {
		return nil, ErrFormat
	}
	// 三份包裹一项都没有也是格式错误（不认识的键不算数）：那样的文件解不开任何东西。
	if v.dpapi == nil && !v.password.present() {
		return nil, ErrFormat
	}
	if err := v.data.check(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *vault) open(dek []byte, local bool) (*Store, error) {
	if len(dek) != dekLen {
		return nil, ErrFormat
	}
	values, err := openData(dek, v.data)
	if err != nil {
		return nil, err
	}
	return &Store{
		path:      v.path,
		wraps:     v.wraps,
		dpapi:     v.dpapi,
		password:  v.password,
		recovery:  v.recovery,
		values:    values,
		dek:       dek,
		local:     local,
		file:      v.stamp,
		fileKnown: true,
		fileLocal: v.dpapi,
	}, nil
}

func (d sealed) check() error {
	iv, ivErr := base64.StdEncoding.DecodeString(d.IV)
	tag, tagErr := base64.StdEncoding.DecodeString(d.Tag)
	ct, ctErr := base64.StdEncoding.DecodeString(d.CT)
	if ivErr != nil || tagErr != nil || ctErr != nil || len(iv) != ivLen || len(tag) != tagLen || len(ct) == 0 {
		return ErrFormat
	}
	return nil
}

// validate 照规格查 scrypt 参数的上下限：超出的文件不去算，直接回格式错误。
func (w *kdfWrap) validate() error {
	switch {
	case w.KDF != "scrypt",
		w.N < acceptCost.MinN, w.N > acceptCost.MaxN, w.N&(w.N-1) != 0,
		w.R < 1, w.R > acceptCost.MaxR,
		w.P < 1, w.P > acceptCost.MaxP:
		return ErrFormat
	}
	if int64(128)*int64(w.N)*int64(w.R) > maxScryptMemory || int64(w.N)*int64(w.R)*int64(w.P) > maxScryptWork {
		return ErrFormat
	}
	for _, field := range []struct {
		text string
		size int
	}{{w.Salt, saltLen}, {w.IV, ivLen}, {w.Tag, tagLen}, {w.CT, dekLen}} {
		b, err := base64.StdEncoding.DecodeString(field.text)
		if err != nil || len(b) != field.size {
			return ErrFormat
		}
	}
	return nil
}

// unwrap 用口令派生的钥匙解开数据密钥；认证失败就是口令不对。
func (w *kdfWrap) unwrap(secret []byte, use string) ([]byte, error) {
	// 再查一遍：长度不对的 IV 交给 gcm.Open 会 panic，这里挡住。
	if err := w.validate(); err != nil {
		return nil, err
	}
	salt, saltErr := base64.StdEncoding.DecodeString(w.Salt)
	iv, ivErr := base64.StdEncoding.DecodeString(w.IV)
	tag, tagErr := base64.StdEncoding.DecodeString(w.Tag)
	ct, ctErr := base64.StdEncoding.DecodeString(w.CT)
	if saltErr != nil || ivErr != nil || tagErr != nil || ctErr != nil {
		return nil, ErrFormat
	}
	key, err := derive(secret, salt, w.N, w.R, w.P)
	if err != nil {
		return nil, fmt.Errorf("密钥派生失败：%w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, ErrFormat
	}
	dek, err := gcm.Open(nil, iv, append(ct, tag...), []byte(use))
	if err != nil {
		return nil, ErrWrongSecret
	}
	return dek, nil
}

// sealWrap 用口令派生的钥匙包数据密钥；salt、IV 每次新生成。
func sealWrap(secret []byte, use string, dek []byte) (*kdfWrap, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("生成盐失败：%w", err)
	}
	key, err := derive(secret, salt, writeCost.N, writeCost.R, writeCost.P)
	if err != nil {
		return nil, fmt.Errorf("密钥派生失败：%w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, fmt.Errorf("加密数据密钥失败：%w", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("生成 IV 失败：%w", err)
	}
	sealedText := gcm.Seal(nil, iv, dek, []byte(use))
	return &kdfWrap{
		KDF:  "scrypt",
		N:    writeCost.N,
		R:    writeCost.R,
		P:    writeCost.P,
		Salt: base64.StdEncoding.EncodeToString(salt),
		IV:   base64.StdEncoding.EncodeToString(iv),
		Tag:  base64.StdEncoding.EncodeToString(sealedText[len(sealedText)-tagLen:]),
		CT:   base64.StdEncoding.EncodeToString(sealedText[:len(sealedText)-tagLen]),
	}, nil
}

func openData(dek []byte, d sealed) (map[string]string, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if len(dek) != dekLen {
		return nil, ErrFormat
	}
	iv, _ := base64.StdEncoding.DecodeString(d.IV)
	tag, _ := base64.StdEncoding.DecodeString(d.Tag)
	ct, _ := base64.StdEncoding.DecodeString(d.CT)
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, ErrFormat
	}
	plain, err := gcm.Open(nil, iv, append(ct, tag...), []byte(aadData))
	if err != nil {
		return nil, fmt.Errorf("%w：数据认证失败", ErrFormat)
	}
	var values map[string]*string
	if json.Unmarshal(plain, &values) != nil || values == nil {
		return nil, ErrFormat
	}
	out := make(map[string]string, len(values))
	for name, value := range values {
		if value == nil {
			return nil, ErrFormat
		}
		out[name] = *value
	}
	return out, nil
}

func sealData(dek, plain []byte) (sealed, error) {
	if len(dek) != dekLen {
		return sealed{}, ErrFormat
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return sealed{}, ErrFormat
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return sealed{}, fmt.Errorf("生成 IV 失败：%w", err)
	}
	ct := gcm.Seal(nil, iv, plain, []byte(aadData))
	return sealed{
		IV:  base64.StdEncoding.EncodeToString(iv),
		Tag: base64.StdEncoding.EncodeToString(ct[len(ct)-tagLen:]),
		CT:  base64.StdEncoding.EncodeToString(ct[:len(ct)-tagLen]),
	}, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// save 把内存里的名字和值重新加密、连同各种包裹写回；每个写操作调用一次。落盘前先确认
// 文件没被别的程序改过，写成功后记下文件的新样子。
func (s *Store) save() error {
	if !s.loaded {
		// 对象已经被卸载（重建失败、外部改动没能重读）：不能拿一份内存里的旧快照覆盖文件。
		return ErrChangedOnDisk
	}
	if err := s.checkWritable(); err != nil {
		return err
	}
	values := make(map[string]string, len(s.values))
	for name, value := range s.values {
		values[name] = value
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("序列化密钥失败：%w", err)
	}
	data, err := sealData(s.dek, plain)
	if err != nil {
		return err
	}
	wraps := make(map[string]json.RawMessage, len(s.wraps)+3)
	for key, raw := range s.wraps {
		wraps[key] = raw
	}
	if len(s.dpapi) > 0 {
		if wraps["dpapi"], err = encodeRaw(base64.StdEncoding.EncodeToString(s.dpapi)); err != nil {
			return fmt.Errorf("序列化密钥文件失败：%w", err)
		}
	} else {
		delete(wraps, "dpapi")
	}
	for _, item := range []struct {
		key  string
		slot kdfSlot
	}{{"password", s.password}, {"recovery", s.recovery}} {
		switch {
		case item.slot.wrap != nil:
			if wraps[item.key], err = encodeRaw(*item.slot.wrap); err != nil {
				return fmt.Errorf("序列化密钥文件失败：%w", err)
			}
		case item.slot.raw != nil:
			// 坏包裹原样写回，一个字节都不改（5.3.3 复核第 6 条）。
			wraps[item.key] = item.slot.raw
		}
	}
	if s.dpapi == nil && s.password.wrap == nil {
		return ErrNeedPassphrase
	}
	b, err := json.Marshal(envelope{Version: 1, Wraps: wraps, Data: data})
	if err != nil {
		return fmt.Errorf("序列化密钥文件失败：%w", err)
	}
	stamp, err := writeAtomic(s.path, b)
	if err != nil {
		return err
	}
	s.file, s.fileKnown = stamp, true
	s.fileLocal = bytes.Clone(s.dpapi)
	return nil
}

func encodeRaw(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// writeAtomic 先写同目录的 secrets.bin.tmp-<随机后缀>、刷盘，再改名替换；中途失败原文件不动。
// 返回写完的样子：改名不改修改时间和大小，临时文件的就够了。
func writeAtomic(path string, b []byte) (fileStamp, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fileStamp{}, fmt.Errorf("建密钥目录失败：%w", err)
		}
	}
	suffix, err := tempSuffix()
	if err != nil {
		return fileStamp{}, err
	}
	tmp := path + ".tmp-" + suffix
	if err := writeTmp(tmp, b); err != nil {
		os.Remove(tmp)
		return fileStamp{}, err
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		os.Remove(tmp)
		return fileStamp{}, fmt.Errorf("读取密钥临时文件失败：%w", err)
	}
	if err := renameReplacing(tmp, path); err != nil {
		os.Remove(tmp)
		return fileStamp{}, fmt.Errorf("替换密钥文件失败：%w", err)
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size(), hash: sha256.Sum256(b)}, nil
}

// renameRetryWindow 是替换文件被临时拒绝时的重试总时长，照 Go 源码 cmd/internal/robustio
// 的 arbitraryTimeout（约 2 秒）。
const renameRetryWindow = 2 * time.Second

// renameReplacing 调 renameTmp 并照 robustio 重试：Windows 上杀毒、索引程序正开着文件时
// 改名会被拒（拒绝访问、共享冲突、临时文件一时找不到），短时间内指数退避再试约 2 秒；
// 其余错误立刻返回。哪些错误码值得重试只在 Windows 上认（rename_windows.go），非 Windows
// 一律不重试（rename_other.go）：errno 数字在两个平台上含义不同。
func renameReplacing(oldpath, newpath string) error {
	var (
		bestErr     error
		lowestErrno syscall.Errno
		start       time.Time
		nextSleep   = time.Millisecond
	)
	for {
		err := renameTmp(oldpath, newpath)
		if err == nil || !ephemeralRenameErr(err) {
			return err
		}
		// 照 robustio：重试用尽时优先返回 errno 最小的那个（最像根因）。
		if errno, ok := errnoOf(err); ok && (lowestErrno == 0 || errno < lowestErrno) {
			bestErr, lowestErrno = err, errno
		} else if bestErr == nil {
			bestErr = err
		}
		if start.IsZero() {
			start = time.Now()
		} else if time.Since(start)+nextSleep >= renameRetryWindow {
			return bestErr
		}
		renameRetryWait(nextSleep)
		nextSleep += nextSleep
	}
}

// errnoOf 取出错误里的系统错误码（os.Rename 返回 *os.LinkError，包着 Errno）。
func errnoOf(err error) (syscall.Errno, bool) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno, true
	}
	return 0, false
}

// tempSuffix 是临时文件名的随机后缀：同一个库的写入在锁里串行，随机后缀防外来的同名文件撞上。
func tempSuffix() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成临时文件名失败：%w", err)
	}
	return hex.EncodeToString(b), nil
}

// writeTempFile 写临时文件并刷盘；Windows 上 0600 只影响 ACL 继承，照常写。
func writeTempFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("写密钥临时文件失败：%w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("写密钥临时文件失败：%w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("刷盘密钥临时文件失败：%w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("写密钥临时文件失败：%w", err)
	}
	return nil
}
