// Package sendlock 是 QQ 发送权的跨进程独占锁和交接记录（规格-QQ发送上线 5.2、第十一节第 3 步）。
//
// 两个文件，都在 <数据目录>/go/ 下：
//
//   - qq-sender.json：意图，只有 account 和 holder（bun / go）。没有这个文件就是 holder 是 bun。
//     它只表示主人想把发送权给谁，不授权发送：一份 JSON 挡不住已经在跑的 Bun（规格 5.2）。
//   - qq-sender.lock：锁文件，内容就是交接记录（protocol 1）：谁是持有的一方、有没有放开、什么时候放开的。
//
// 互斥靠操作系统的独占文件锁（Windows 上是 LockFileEx，不共享、拿不到就立刻失败），锁住的是锁文件里偏移 2^62 处
// 的一个字节，不跟 JSON 内容重叠，所以持锁期间别的进程照样能读记录。拿到锁之前不碰任何发送。
// 持有的一方退出（包括被杀）时句柄随进程关掉，系统自己放锁；但"交接过"还得看记录：state 是 released 才算正常交出。
// 被杀的那一方来不及写 released，记录停在 holding，这时分两种：记的是自己（上一次就是自己在发，崩溃或断电后重启）
// 就直接接着拿；记的是另一边就不拿，等主人确认——不把另一边被强杀当成交出。
//
// 宿主还要核对守卫、暂停检查和登录健康，持锁本身不等于可以发。
package sendlock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Protocol 是这一版会抢这把锁的交接协议版本。记录里不是这个数就不接（规格 5.2）
const Protocol = 1

// holder 的两种值：意图文件里主人想把发送权给谁，记录里是持有的一方自己
const (
	HolderBun = "bun"
	HolderGo  = "go"
)

// 记录里的两种状态：持有时、放开时
const (
	StateHolding  = "holding"
	StateReleased = "released"
)

// recordRetries 是读到写了一半的记录时再读的次数，间隔 recordRetryDelay（规格 5.2 锁的细节：最多 3 次）
const (
	recordRetries    = 3
	recordRetryDelay = 50 * time.Millisecond
)

// IntentFile 是意图文件的位置（<数据目录>/go/qq-sender.json）
func IntentFile(dataDir string) string { return filepath.Join(dataDir, "go", "qq-sender.json") }

// LockFile 是锁文件的位置（<数据目录>/go/qq-sender.lock），内容就是交接记录
func LockFile(dataDir string) string { return filepath.Join(dataDir, "go", "qq-sender.lock") }

// 接不过来、或者记录不对劲的几种情况。调用方用 errors.Is 分，错误文字里不带号（日志和提醒不出现号全文）
var (
	ErrIntent          = errors.New("意图文件读不懂")
	ErrIntentNotGo     = errors.New("意图文件里 holder 不是 go")
	ErrNoRecord        = errors.New("锁文件里没有交接记录")
	ErrProtocol        = errors.New("交接记录不是 protocol 1")
	ErrTorn            = errors.New("交接记录没有写完整")
	ErrAccount         = errors.New("交接记录里的号不是这个号")
	ErrUnreleasedOther = errors.New("上一个持有者是另一边，没正常交出")
	ErrLocked          = errors.New("锁在别的进程手里")
	ErrIntentMissing   = errors.New("意图文件不见了，交接记录也不是 Go 的")
)

// Intent 是 qq-sender.json 的内容：主人想把发送权给谁。只管意图，互斥由锁管
type Intent struct {
	Account string `json:"account"`
	Holder  string `json:"holder"`
}

// Record 是锁文件里的内容：交接记录
type Record struct {
	Protocol   int               `json:"protocol"`
	Account    string            `json:"account"`
	Holder     string            `json:"holder"`
	State      string            `json:"state"`
	PID        int               `json:"pid"`
	ReleasedAt int64             `json:"releasedAt,omitempty"` // 放锁时的毫秒时间；持有时没有
	Origin     string            `json:"origin,omitempty"`
	Takeover   *TakeoverEvidence `json:"takeover,omitempty"`
}

// ReadIntent 读意图文件。没有这个文件就是 holder 是 bun（规格 5.2 的默认）
func ReadIntent(dataDir string) (Intent, error) {
	b, err := os.ReadFile(IntentFile(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return Intent{Holder: HolderBun}, nil
	}
	if err != nil {
		return Intent{}, fmt.Errorf("读意图文件：%w", err)
	}
	var in Intent
	if err := strictUnmarshal(b, &in); err != nil {
		return Intent{}, ErrIntent
	}
	if !isDigits(in.Account) || (in.Holder != HolderBun && in.Holder != HolderGo) {
		return Intent{}, ErrIntent
	}
	return in, nil
}

// Take 试着把 account 这个号的发送权接过来：意图文件里 holder 是 go，锁文件里的交接记录是 protocol 1、号对得上，
// 而且抢到了独占锁。记录是 released 就正常接手；还是 holding 但记的是自己（上一次崩溃或断电后重启）也接着拿，
// 记的是另一边就不拿，等主人确认（规格 5.2：不把另一边被强杀当成交出）。
// 缺一条就返回错误，调用方保持钳制、照常收包。
// 接过来了记录就写成 holding（holder go、本进程 pid），要停发时调 Holder.Release。
func Take(dataDir, account string) (*Holder, error) {
	in, err := ReadIntent(dataDir)
	if err != nil {
		return nil, err
	}
	// 意图文件不见了（ADR-311 第 3 条）：先当作给这个号的 Go，下面读到交接记录后再核——记录里是 Go 持有或 Go 交出的，
	// 说明接管过，照常接手并把意图文件补回来；记录是另一边的，就不接（以前悄悄当成 Bun 持有，主人看不出为什么不发）
	_, statErr := os.Stat(IntentFile(dataDir))
	intentMissing := errors.Is(statErr, os.ErrNotExist)
	if intentMissing {
		in = Intent{Holder: HolderGo, Account: account}
	}
	if in.Holder != HolderGo {
		return nil, ErrIntentNotGo
	}
	if in.Account != account {
		return nil, fmt.Errorf("意图文件：%w", ErrAccount)
	}
	// 锁文件都还没有就是没有交接记录：不接，也不建出空文件（Bun 靠"文件本来不存在"识别第一次建立协议）
	if _, err := os.Stat(LockFile(dataDir)); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoRecord
	}

	f, err := openLockFile(dataDir)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		closeLock(f)
		if isLockBusy(err) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("抢锁：%w", err)
	}
	rec, err := readRecord(f)
	if err != nil {
		closeLock(f)
		return nil, err
	}
	if rec.Account != account {
		closeLock(f)
		return nil, fmt.Errorf("交接记录：%w", ErrAccount)
	}
	if intentMissing && rec.Holder != HolderGo {
		closeLock(f)
		return nil, ErrIntentMissing
	}
	resumed := false
	switch {
	case rec.State == StateReleased:
		// 正常交过来的
	case rec.State == StateHolding && rec.Holder == HolderGo:
		// 上一次就是自己在发，没来得及写 released：中间没有另一边插进来，接着发
		resumed = true
	default:
		// 记录里还是另一边持有：被强杀也是没交出，不替主人做主
		closeLock(f)
		return nil, ErrUnreleasedOther
	}
	rec.Holder, rec.State, rec.PID, rec.ReleasedAt = HolderGo, StateHolding, os.Getpid(), 0
	if err := writeRecord(f, rec); err != nil {
		closeLock(f)
		return nil, err
	}
	h := &Holder{f: f, account: account, origin: rec.Origin, takeover: rec.Takeover, Resumed: resumed}
	if intentMissing {
		h.IntentRecovered = true
		if b, err := json.Marshal(Intent{Account: account, Holder: HolderGo}); err == nil {
			h.IntentWriteErr = os.WriteFile(IntentFile(dataDir), b, 0o600)
		}
	}
	return h, nil
}

// Holder 是接过发送权的这一边：锁一直在自己手里，直到 Release
type Holder struct {
	mu       sync.Mutex
	origin   string
	takeover *TakeoverEvidence
	f        *os.File
	account  string

	// Resumed 说这次是接着上一次自己没正常交出的记录拿的（不是正常交接）。调用方可以据此记一行日志
	Resumed bool
	// IntentRecovered：意图文件不见了，凭交接记录认定接管过、补回了文件。调用方要提醒主人（ADR-311 第 3 条）
	IntentRecovered bool
	IntentWriteErr  error // 补回意图文件失败（不影响这次持锁，但重启后又会缺）
}

// Release 交出发送权：先把记录写成 released（带上放开的毫秒时刻），再放锁。顺序不能反（规格 5.2：不许先放锁）
func (h *Holder) Release() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.f == nil {
		return nil
	}
	f, account := h.f, h.account
	h.f = nil
	rec := Record{Protocol: Protocol, Account: account, Holder: HolderGo, State: StateReleased, PID: os.Getpid(), ReleasedAt: time.Now().UnixMilli(), Origin: h.origin, Takeover: h.takeover}
	writeErr := writeRecord(f, rec)
	closeErr := closeLock(f)
	return errors.Join(writeErr, closeErr)
}

// CanSend 只认 Take 核对并接过的本进程活句柄；包含自己 holding 重启，文件被换掉也要立刻收紧。
func (h *Holder) CanSend() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.f == nil {
		return false
	}
	held, err := h.f.Stat()
	if err != nil {
		return false
	}
	current, err := os.Stat(h.f.Name())
	return err == nil && os.SameFile(held, current)
}

// recordRetrySleep 是重读前的等待；测试换成"这时写的一方补全了"，不用拿定时器赌时序
var recordRetrySleep = time.Sleep

// readRecord 读句柄里的交接记录。写的一方是"截断 → 整段写 → 刷盘"，读的时候可能正好在截断和写之间。ErrTorn 就是"记录写了一半"（规格 5.2 锁的细节），只在这个情况下重读：
// 解析不了（JSON 解不开，或者只读到一半）就隔 50 毫秒重读，最多 3 次。仍读不成一份完整记录：一直空着算没有记录，解不开的算 ErrTorn（没写完整），
// 读完整了、内容不对的才是 ErrProtocol
func readRecord(f *os.File) (Record, error) {
	last := ErrNoRecord
	for attempt := 0; ; attempt++ {
		raw, err := readAll(f)
		if err != nil {
			return Record{}, fmt.Errorf("读交接记录：%w", err)
		}
		if len(raw) == 0 {
			// 写的一方可能刚截断、还没写进来
			last = ErrNoRecord
		} else if rec, err := decodeRecord(raw); err == nil {
			return rec, nil
		} else if !errors.Is(err, ErrTorn) {
			return Record{}, err
		} else {
			last = ErrTorn
		}
		if attempt == recordRetries {
			return Record{}, last
		}
		recordRetrySleep(recordRetryDelay)
	}
}

// readAll 把句柄里的全部内容读出来
func readAll(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// decodeRecord 解析并核对记录的结构。字段多了、少了、类型不对都不认：Bun 那边也是严格解析，两边必须一致
func decodeRecord(raw []byte) (Record, error) {
	var rec Record
	if err := strictUnmarshal(raw, &rec); err != nil {
		if errors.Is(err, ErrTorn) {
			return Record{}, ErrTorn
		}
		// 字段多了、类型不对：不算能读懂的交接记录
		return Record{}, ErrProtocol
	}
	switch {
	case rec.Protocol != Protocol:
		return Record{}, ErrProtocol
	case !isDigits(rec.Account):
		return Record{}, ErrProtocol
	case rec.Holder != HolderBun && rec.Holder != HolderGo:
		return Record{}, ErrProtocol
	case rec.State == StateHolding:
		return rec, nil
	case rec.State == StateReleased:
		// released 必须带放开的时刻（规格 5.2 的 JSON 字段）
		if rec.ReleasedAt == 0 {
			return Record{}, ErrProtocol
		}
		return rec, nil
	}
	return Record{}, ErrProtocol
}

// strictUnmarshal 解析 JSON，不认识的键也算读不懂；内容只读到一半时返回 ErrTorn
func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrTorn
		}
		return err
	}
	if dec.More() {
		// JSON.parse 也会拒绝后面还跟着东西
		return ErrTorn
	}
	return nil
}

// writeRecord 把记录整份改写进句柄，再刷到盘上：截断到 0 → 整段 JSON 一次写入 → FlushFileBuffers。
// 不许写临时文件再改名（改名会换掉被锁的那个文件）
func writeRecord(f *os.File, rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("写交接记录：%w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("写交接记录：%w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("写交接记录：%w", err)
	}
	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("写交接记录：%w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("写交接记录：%w", err)
	}
	return nil
}

// isDigits 说 s 是非空的十进制数字串（JSON 里的 account 是字符串）
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// closeLock 放锁、关句柄。和 Close 分开写只为了一眼看出顺序（放锁在关句柄里完成，但错误要分别报）
func closeLock(f *os.File) error {
	unlockErr := unlockExclusive(f)
	return errors.Join(unlockErr, f.Close())
}
