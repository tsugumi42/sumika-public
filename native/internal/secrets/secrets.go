// Package secrets 是 Sumika 的密钥库（规格-数据目录与备份 5.3.3）：信封加密，随机 32 字节
// 数据密钥加密全部“名字 → 值”；数据密钥本身再用本机 DPAPI、主密码（scrypt）、恢复码（scrypt）
// 各包一份。正常启动用本机那一份，不用输入；本机那份解不开时用主密码或恢复码解开一次，
// 再用这台电脑的 DPAPI 包一份写回。值、主密码、恢复码不出现在错误和日志里。
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrNotFound    = errors.New("没有密钥文件")
	ErrLocked      = errors.New("密钥还锁着")
	ErrFormat      = errors.New("不认识或已损坏的密钥文件格式")
	ErrUnsupported = errors.New("不支持本机密钥解密：需要 Windows DPAPI")
	// ErrWrongSecret 是主密码或恢复码不对；解开失败时文件一个字节都不改。
	ErrWrongSecret = errors.New("主密码或恢复码不对")
	// ErrNeedPassphrase 是这台电脑没有本机加密（非 Windows）又没设主密码时写不进去。
	ErrNeedPassphrase = errors.New("这台电脑没有可用的本机加密，先设主密码再存密钥")
	// ErrChangedOnDisk 是写盘前发现密钥文件被别的程序改过、删了，或我们以为不存在时它已经
	// 被人建好：这次写不落盘，内存和文件都不变，重新打开拿到最新内容再改。
	ErrChangedOnDisk = errors.New("密钥文件被别的程序改过，请重新打开再改")
)

// LockedError 说明锁着时能不能用主密码或恢复码解开；errors.Is(err, ErrLocked) 成立，
// 调用方用 errors.As 取 HasPassphrase。
type LockedError struct {
	HasPassphrase bool
}

func (e *LockedError) Error() string {
	if e.HasPassphrase {
		return "密钥还锁着：可以用主密码或恢复码解开"
	}
	return "密钥还锁着：这台电脑解不开本机那一份，也没有可用的主密码或恢复码"
}

func (e *LockedError) Is(target error) bool { return target == ErrLocked }

// Kind 是 Unlock 的口令种类。
type Kind int

const (
	// Passphrase 是主密码（先做 Unicode NFKC 规范化、至少 8 个字符）。
	Passphrase Kind = iota
	// RecoveryCode 是设主密码时只显示一次的恢复码。
	RecoveryCode
)

// 密钥名和值的上限（规格 5.3.3）。
const (
	maxNameBytes  = 128
	maxValueBytes = 64 << 10
	minPassChars  = 8
	recoveryBytes = 20
	recoveryChars = 32
)

// Open 打开密钥文件：本机那份在、解得开就返回已解开的库；缺或解不开是 ErrLocked
// （带 HasPassphrase），文件不存在是 ErrNotFound，格式坏是 ErrFormat。这个路径上已经登记着
// 库时先看文件有没有被别的程序改过：没改就沿用那个库，改过就在同一个库上重读。
// 每个调用拿到这个路径的对象后在它的锁里做自己这一次：没加载过就加载，加载过就照外面有没
// 有改动处理（规格 5.3.3）。
func Open(path string) (*Store, error) {
	s, err := storeForPath(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resyncLocked(localAccess, s.localOpen); err != nil {
		return nil, err
	}
	return s, nil
}

// access 从刚读到的文件取出数据密钥和本机那份的状态；解不开的原因照第一次打开的规则返回。
type access func(*vault) (dek []byte, local bool, err error)

// localAccess 照 Open 的规则：文件里本机那一份在、这台电脑解得开才算打开，否则是锁着。
func localAccess(v *vault) ([]byte, bool, error) {
	if v.dpapi == nil {
		return nil, false, &LockedError{HasPassphrase: v.password.present()}
	}
	dek, err := unprotectLocal(v.dpapi)
	if err != nil {
		// 非 Windows、换了 Windows 用户、重装了系统都是这里解不开；照规格统一报锁着。
		return nil, false, &LockedError{HasPassphrase: v.password.present()}
	}
	return dek, true, nil
}

// Unlock 用主密码或恢复码解开锁着的库。口令不对是 ErrWrongSecret、文件不变；恢复码格式不对
// 直接报“恢复码格式不对”、不跑 scrypt。解开后本机加密可用就重包本机副本写回，写失败不影响
// 这次解开，原因放在 Store.LocalCopyError。
func Unlock(path, secret string, kind Kind) (*Store, error) {
	from := func(v *vault) ([]byte, bool, error) {
		slot, input, use, err := unlockSlot(v.password, v.recovery, secret, kind)
		if err != nil {
			return nil, false, err
		}
		dek, err := slot.wrap.unwrap(input, use)
		return dek, false, err
	}
	s, err := storeForPath(path)
	if err != nil {
		return nil, err
	}
	check := func() error {
		slot, input, use, err := unlockSlot(s.password, s.recovery, secret, kind)
		if err != nil {
			return err
		}
		_, err = slot.wrap.unwrap(input, use)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resyncLocked(from, check); err != nil {
		return nil, err
	}
	// 重包本机副本和这次解开在同一段临界区里：中间被别的协程卸载 / 重读时不能把
	// “没包上”记到一个已经不是刚解开的库上（复核第 4 轮）。
	s.rewrapLocalLocked()
	return s, nil
}

// unlockSlot 挑出这次解锁要用的那份包裹、口令字节和附加数据。
func unlockSlot(password, recovery kdfSlot, secret string, kind Kind) (kdfSlot, []byte, string, error) {
	switch kind {
	case Passphrase:
		if !password.present() {
			return kdfSlot{}, nil, "", errors.New("这个密钥库没有设主密码")
		}
		if password.wrap == nil {
			// 文件里那一份的参数或长度不合规：这条文件坏了，不拿它去跑 scrypt。
			return kdfSlot{}, nil, "", ErrFormat
		}
		input, err := unlockPassphraseBytes(secret)
		if err != nil {
			return kdfSlot{}, nil, "", err
		}
		return password, input, aadPassword, nil
	case RecoveryCode:
		if !recovery.present() {
			return kdfSlot{}, nil, "", errors.New("这个密钥库没有恢复码")
		}
		if recovery.wrap == nil {
			return kdfSlot{}, nil, "", ErrFormat
		}
		code, ok := recoveryCodeSecret(secret)
		if !ok {
			return kdfSlot{}, nil, "", errRecoveryFormat
		}
		return recovery, code, aadRecovery, nil
	}
	return kdfSlot{}, nil, "", errors.New("不认识的解锁口令种类")
}

// Create 在文件不存在时新建空库：本机加密可用就用它包一份落盘；不可用（非 Windows）时
// 先不落盘，Set 返回 ErrNeedPassphrase，设了主密码才写得进去。文件已存在时照 Open 处理，
// 不覆盖原有内容；文件被外面删了就在同一个已登记对象上重建（规格 5.3.3）。
func Create(path string) (*Store, error) {
	s, err := storeForPath(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 进锁以后再查一次文件：两个 goroutine 同时 Create 时，先拿到锁的那个建好库、后一个
	// 必须照 Open 沿用，不能把对方刚建好的库卸载重来（复核第 4 轮）。
	if _, err := statOf(path); err == nil {
		if err := s.resyncLocked(localAccess, s.localOpen); err != nil {
			return nil, err
		}
		return s, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !s.loaded {
		if err := s.resetLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err := s.recreateLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// recreateLocked 处理“对象加载过、文件却被外面删了”的 Create：在同一个对象上重置成新的
// 空库，旧快照不写回。这期间文件又被别人建好了（或重置后落盘前发现它已经在了）就返回
// ErrChangedOnDisk、对象退回没加载，不覆盖别人的文件；调用方再 Open 读外面那份。
func (s *Store) recreateLocked() error {
	if _, err := statOf(s.path); err == nil {
		s.unloadLocked()
		return ErrChangedOnDisk
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := s.resetLocked(); err != nil {
		return err
	}
	return nil
}

// resetLocked 把对象换成一个全新的空库；本机加密可用就落盘，不可用就等 SetPassphrase。
// 重建中途出错或本机加密 panic，对象都退回“没加载”（持有人之后的写操作返回
// ErrChangedOnDisk，下一次调用从文件重新加载），不能停在和文件对不上的空库上。
func (s *Store) resetLocked() (err error) {
	done := false
	defer func() {
		if !done {
			s.unloadLocked()
		}
	}()
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return fmt.Errorf("生成数据密钥失败：%w", err)
	}
	s.loaded = true
	s.wraps = map[string]json.RawMessage{}
	s.dpapi = nil
	s.password = kdfSlot{}
	s.recovery = kdfSlot{}
	s.values = map[string]string{}
	s.dek = dek
	s.local = false
	s.file = fileStamp{}
	s.fileKnown = false
	s.fileLocal = nil
	s.stale = nil
	s.localCopyErr = nil
	wrapped, err := protectLocal(dek)
	switch {
	case errors.Is(err, ErrUnsupported):
		done = true
		return nil
	case err != nil:
		return fmt.Errorf("本机加密失败：%w", err)
	}
	s.dpapi, s.local = wrapped, true
	if err := s.save(); err != nil {
		return err
	}
	done = true
	return nil
}

// unloadLocked 把对象退回“没加载”：重建空库失败时不能停在和文件对不上的空库上，
// 下一次调用从文件重新加载（规格 5.3.3）。
func (s *Store) unloadLocked() {
	s.loaded = false
	s.wraps = nil
	s.dpapi = nil
	s.password = kdfSlot{}
	s.recovery = kdfSlot{}
	s.values = nil
	clear(s.dek)
	s.dek = nil
	s.local = false
	s.file = fileStamp{}
	s.fileKnown = false
	s.fileLocal = nil
	s.stale = nil
	s.localCopyErr = nil
}

// Store 是一个路径上的密钥库；并发安全，每个写操作当场落盘。登记表先放进一个没加载的空壳，
// 第一次调用在 mu 里加载；加载失败就留在没加载，下一次再试。
type Store struct {
	mu       sync.Mutex
	path     string
	loaded   bool                       // 为假表示还没成功加载过（或重建失败被清空），下面只有 path 有意义
	wraps    map[string]json.RawMessage // 原样保留不认识的包裹（例如以后的版本加的包裹）
	dpapi    []byte
	password kdfSlot
	recovery kdfSlot
	values   map[string]string
	dek      []byte
	local    bool // 本机那份可写且已包好
	// file 是上次读或写完时文件的修改时间和大小；fileKnown 为假表示这个库还没读过也没写过
	// 文件（非 Windows 上 Create 之后还没设主密码就是这样）。
	file      fileStamp
	fileKnown bool
	// fileLocal 是文件里本机那一份的原样字节（nil 表示文件里没有）。外面的程序改过文件后，
	// 它和 dpapi 可能不一样：写回失败时 dpapi 是新的、文件里还是旧的。
	fileLocal []byte
	// stale 是外面改过 / 删了文件又没能重读的原因；有它时写操作直接报错、不落盘，
	// 不能拿旧快照盖掉别人的新文件。
	stale error
	// localCopyErr 是解开后重包本机副本没成功的原因；nil 表示包好了或本机加密不可用。
	// 用 LocalCopyError 在锁里读：别的协程可能正在重包。
	localCopyErr error
}

// storeLabel 是 *Store 打印出来的唯一内容：值和数据密钥不能经 %v、%#v 或日志带出去。
const storeLabel = "<密钥库>"

func (s *Store) String() string { return storeLabel }

func (s *Store) GoString() string { return storeLabel }

func (s *Store) LogValue() slog.Value { return slog.StringValue(storeLabel) }

// 每个文件路径在进程里只有一个库对象：同一路径的 Open / Unlock / Create 都拿到它，再拿它
// 的 mu 做这一次的事，两个库各写一份会互相覆盖（规格 5.3.3）。键是清理过的绝对路径。
var stores = struct {
	mu     sync.Mutex
	byPath map[string]*Store
}{byPath: map[string]*Store{}}

// storeForPath 按路径取出这个路径的对象，没有就登记一个还没加载的空壳，然后立刻放开登记表
// 的锁。加载（含 Unlock 的 scrypt）、重读、重建都由调用方拿着对象的锁自己做；登记表的锁里
// 不读文件、不跑 scrypt（规格 5.3.3）。
func storeForPath(path string) (*Store, error) {
	key, err := pathKey(path)
	if err != nil {
		return nil, err
	}
	stores.mu.Lock()
	defer stores.mu.Unlock()
	if s, ok := stores.byPath[key]; ok {
		return s, nil
	}
	s := &Store{path: path}
	stores.byPath[key] = s
	return s, nil
}

// pathKey 是登记用的键：清理过的绝对路径；Windows 上不分大小写。
func pathKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析密钥文件路径失败：%w", err)
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}

// fileStamp 是文件在磁盘上的样子：修改时间、大小和内容的 SHA-256。修改时间和大小一样不够——
// 同一毫秒里改两次、大小正好一样时只靠这两样会漏，所以两样都一样还要比内容（文件最大 4 MB）。
type fileStamp struct {
	mod  time.Time
	size int64
	hash [sha256.Size]byte
}

// metaSame 报告修改时间和大小一样，可以进一步比内容。
func (a fileStamp) metaSame(b fileStamp) bool { return a.size == b.size && a.mod.Equal(b.mod) }

// statOf 取文件现在的修改时间和大小；文件不在是 ErrNotFound。
func statOf(path string) (fileStamp, error) {
	fi, err := statFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, ErrNotFound
	}
	if err != nil {
		return fileStamp{}, fmt.Errorf("读取密钥文件失败：%w", err)
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size()}, nil
}

// unchanged 报告磁盘上的文件和上次读 / 写的还是不是一个：修改时间或大小一变就是改过，两样
// 都一样才读文件比内容哈希（规格 5.3.3）。文件不在是 ErrNotFound。
func (s *Store) unchanged() (bool, error) {
	stamp, err := statOf(s.path)
	if err != nil {
		return false, err
	}
	if !s.fileKnown || !stamp.metaSame(s.file) {
		return false, nil
	}
	b, err := readFile(s.path)
	if err != nil {
		return false, err
	}
	return sha256.Sum256(b) == s.file.hash, nil
}

// resyncLocked 是 Open / Unlock / Create 在对象的锁里做的那一步：还没加载过就加载；加载过就
// 处理外面的改动——文件还是上次的样子就按 check（可为 nil）验一遍再沿用，变了就在这个对象上
// 重读、换掉内存里的值；删了是 ErrNotFound，解不开照第一次打开的规则返回。后面几种都把对象
// 标成需要重新打开，期间的写操作不落盘。
func (s *Store) resyncLocked(from access, check func() error) error {
	if !s.loaded {
		return s.loadLocked(from)
	}
	unchanged, err := s.unchanged()
	if err != nil {
		s.markStale(err)
		return err
	}
	if unchanged {
		s.stale = nil
		if check != nil {
			return check()
		}
		return nil
	}
	v, err := load(s.path)
	if err != nil {
		s.markStale(err)
		return err
	}
	if err := s.adopt(v, from); err != nil {
		if staleOn(err) {
			s.stale = err
		}
		return err
	}
	return nil
}

// loadLocked 第一次加载这个路径的文件。失败时对象留在“没加载”，下一次调用再试；不缓存失败。
func (s *Store) loadLocked(from access) error {
	v, err := load(s.path)
	if err != nil {
		return err
	}
	return s.adopt(v, from)
}

// adopt 用刚读到的文件换掉内存里的快照，文件的样子和本机那一份一起记下。
func (s *Store) adopt(v *vault, from access) error {
	dek, local, err := from(v)
	if err != nil {
		return err
	}
	fresh, err := v.open(dek, local)
	if err != nil {
		return err
	}
	s.loaded = true
	s.wraps = fresh.wraps
	s.dpapi = fresh.dpapi
	s.password = fresh.password
	s.recovery = fresh.recovery
	s.values = fresh.values
	s.dek = fresh.dek
	s.local = fresh.local
	s.file = fresh.file
	s.fileKnown = true
	s.fileLocal = fresh.fileLocal
	s.stale = nil
	s.localCopyErr = nil
	return nil
}

// markStale 记下没能重读的原因；文件本来就不存在（这个库还没落过盘）时不算被删。
func (s *Store) markStale(reason error) {
	if errors.Is(reason, ErrNotFound) && !s.fileKnown {
		return
	}
	s.stale = reason
}

// staleOn 报告这个错误是不是说明文件本身不能用（改坏了 / 解不开 / 没了）；那种情况之后的
// 写操作都要拒绝，不能拿旧快照盖别人的文件。口令输错不算，文件可能好好地在。
func staleOn(err error) bool {
	return errors.Is(err, ErrLocked) || errors.Is(err, ErrFormat) || errors.Is(err, ErrNotFound)
}

// localOpen 照 Open 的规则查文件里本机那一份：没有，或这台电脑解不开，都是锁着。文件没变时
// Open 靠它回答，不用把文件再读一遍。
func (s *Store) localOpen() error {
	if s.fileLocal == nil {
		return &LockedError{HasPassphrase: s.password.present()}
	}
	if _, err := unprotectLocal(s.fileLocal); err != nil {
		return &LockedError{HasPassphrase: s.password.present()}
	}
	return nil
}

// checkWritable 落盘前确认文件还是上次读到的样子：外面改过、删了，或我们以为不存在时它
// 已经被人建好，都拒绝这次写。
func (s *Store) checkWritable() error {
	if s.stale != nil {
		return s.stale
	}
	unchanged, err := s.unchanged()
	switch {
	case errors.Is(err, ErrNotFound):
		if !s.fileKnown {
			return nil
		}
		return ErrChangedOnDisk
	case errors.Is(err, ErrFormat):
		// Stat 之后文件变大了（超过 4 MB）也和改过一样拒写。
		return ErrChangedOnDisk
	case err != nil:
		return err
	}
	if !unchanged {
		return ErrChangedOnDisk
	}
	return nil
}

// rewrapLocalLocked 用这台电脑的本机加密重包数据密钥并写回；写失败不影响这次解开，原因记在
// localCopyErr。非 Windows 只有主密码那一份，不算出错（规格 5.3.3）。调用方必须拿着 s.mu。
func (s *Store) rewrapLocalLocked() {
	wrapped, err := protectLocal(s.dek)
	if errors.Is(err, ErrUnsupported) {
		return
	}
	if err == nil {
		s.dpapi, s.local = wrapped, true
		if err = s.save(); err == nil {
			s.localCopyErr = nil
			return
		}
	}
	s.localCopyErr = fmt.Errorf("本机副本没包上：%w", err)
}

// LocalCopyError 说明解开后重包本机副本没成功的原因；nil 表示包好了或本机加密不可用。
// 读的时候拿对象的锁：别的协程可能正在重包。
func (s *Store) LocalCopyError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localCopyErr
}

// Get 的 bool 区分缺少名字和存了空字符串。
func (s *Store) Get(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[name]
	return value, ok
}

// Names 返回排好序的名字，不给值。
func (s *Store) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.values))
	for name := range s.values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasPassphrase 报告库里有没有设过主密码（能不能用主密码 / 恢复码解开）。
func (s *Store) HasPassphrase() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password.present()
}

// Set 存一条并当场落盘；落盘失败时内存里的值也回退，和文件保持一致。
func (s *Store) Set(name, value string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if !utf8.ValidString(value) {
		// 不合法的 UTF-8 存进 JSON 会被悄悄换成替换字符，重启后读出来不一样。
		return errors.New("密钥的值必须是合法 UTF-8")
	}
	if len(value) > maxValueBytes {
		return fmt.Errorf("密钥的值最长 %d 字节", maxValueBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return ErrChangedOnDisk
	}
	if !s.local && s.password.wrap == nil {
		return ErrNeedPassphrase
	}
	old, had := s.values[name]
	s.values[name] = value
	if err := s.save(); err != nil {
		if had {
			s.values[name] = old
		} else {
			delete(s.values, name)
		}
		return err
	}
	return nil
}

// Delete 删一条并当场落盘；名字本来就不在时什么也不做。
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return ErrChangedOnDisk
	}
	if !s.local && s.password.wrap == nil {
		return ErrNeedPassphrase
	}
	old, had := s.values[name]
	if !had {
		return nil
	}
	delete(s.values, name)
	if err := s.save(); err != nil {
		s.values[name] = old
		return err
	}
	return nil
}

// SetPassphrase 设或改主密码，返回新的恢复码（只返回这一次），旧的恢复码同时作废。
func (s *Store) SetPassphrase(passphrase string) (string, error) {
	secret, err := passphraseBytes(passphrase)
	if err != nil {
		return "", err
	}
	code, err := newRecoveryCode()
	if err != nil {
		return "", fmt.Errorf("生成恢复码失败：%w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return "", ErrChangedOnDisk
	}
	passWrap, err := sealWrap(secret, aadPassword, s.dek)
	if err != nil {
		return "", err
	}
	recoveryWrap, err := sealWrap([]byte(norm.NFKC.String(code.secret)), aadRecovery, s.dek)
	if err != nil {
		return "", err
	}
	oldPass, oldRecovery := s.password, s.recovery
	s.password, s.recovery = kdfSlot{wrap: passWrap}, kdfSlot{wrap: recoveryWrap}
	if err := s.save(); err != nil {
		s.password, s.recovery = oldPass, oldRecovery
		return "", err
	}
	return code.display, nil
}

// checkName 照规格 5.3.3：非空、只许字母数字和 . _ -、最长 128。
func checkName(name string) error {
	if len(name) == 0 || len(name) > maxNameBytes {
		return fmt.Errorf("密钥名只能是 1 到 %d 个字母、数字和 . _ -", maxNameBytes)
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return errors.New("密钥名只能用字母、数字和 . _ -")
		}
	}
	return nil
}

// passphraseBytes 先做 Unicode NFKC 规范化再取 UTF-8 字节；不去首尾空格。
func passphraseBytes(passphrase string) ([]byte, error) {
	normalized := norm.NFKC.String(passphrase)
	if utf8.RuneCountInString(normalized) < minPassChars {
		return nil, fmt.Errorf("主密码至少 %d 个字符", minPassChars)
	}
	return []byte(normalized), nil
}

// unlockPassphraseBytes 只做 NFKC、拒绝空串，不按长度拒：旧代码按 UTF-16 码元数、在规范化前
// 数长度，和 Go 数法不同，4 个 emoji 设的旧主密码按长度拒就解不开了（复核第 2 条）。
func unlockPassphraseBytes(passphrase string) ([]byte, error) {
	normalized := norm.NFKC.String(passphrase)
	if normalized == "" {
		return nil, ErrWrongSecret
	}
	return []byte(normalized), nil
}

var errRecoveryFormat = errors.New("恢复码格式不对")

// recoveryCodeSecret 转大写、去掉 A-Z2-7 以外的所有字符。剩下不是 32 个就是格式不对；
// 这 32 个字符本身（不解码成字节）经 NFKC 后作为 scrypt 的口令，和旧守护进程一致。
func recoveryCodeSecret(code string) ([]byte, bool) {
	cleaned := strings.Map(func(r rune) rune {
		r = unicode.ToUpper(r)
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			return r
		}
		return -1
	}, code)
	if len(cleaned) != recoveryChars {
		return nil, false
	}
	return []byte(norm.NFKC.String(cleaned)), true
}

type recovery struct {
	display string // 4 个一组、短横线隔开，只显示一次
	secret  string // 32 个 Base32 字符，作为 scrypt 的口令
}

// newRecoveryCode 生成 20 个随机字节，显示成 4 个一组、短横线隔开的 32 个 Base32 字符。
func newRecoveryCode() (recovery, error) {
	b := make([]byte, recoveryBytes)
	if _, err := rand.Read(b); err != nil {
		return recovery{}, err
	}
	text := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	groups := make([]string, 0, len(text)/4)
	for i := 0; i < len(text); i += 4 {
		groups = append(groups, text[i:i+4])
	}
	return recovery{display: strings.Join(groups, "-"), secret: text}, nil
}
