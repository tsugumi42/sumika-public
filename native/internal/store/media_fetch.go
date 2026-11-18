package store

// 收到的图补下载要用的库方法（规格-QQ收图下载和断线补录 4.1、4.3）：
//   - media_fetch 表的读写（状态记在这里，不改段）；
//   - SetSegHash：把下载好的文件的 hash 填回事件里那一个段，只动这一个段的 hash 字段，别的字节原样不动；
//   - 补扫要的查询：近几天里"有地址、没 hash、没有终局状态"的图片段所在的事件。
// 只在 Go 自己的平台库上用（有 chat/0006 建的表）。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sumika-native/internal/channel/types"
)

// media_fetch.state 的取值
const (
	MediaPending  = "pending"
	MediaDone     = "done"
	MediaExpired  = "expired"         // 地址过期、协议端也换不出新的
	MediaTooBig   = "too_big"         // 超过单张上限
	MediaNotImage = "not_image"       // 认不出是位图（HTML 错误页、SVG、压缩包……）
	MediaNotVideo = "not_video"       // 认不出支持的视频容器
	MediaBlocked  = "blocked"         // 地址的主机不在白名单，或解析到内网
	MediaFailed   = "failed"          // 重试用完
	MediaCanceled = "canceled"        // 用户或宿主取消，允许手动重新请求
	MediaManual   = "awaiting_manual" // 第二档文件，必须显式请求
)

// MediaFetch 是 media_fetch 的一行
type MediaFetch struct {
	EventSeq      int64
	Idx           int // 段在 $.segs 里的下标
	Kind          string
	URLSig        string // MediaURLSig(入队时的地址)
	State         string
	Tries         int
	NextAt        int64 // 下次最早时刻（毫秒）
	UpdatedAt     int64
	Hash          string // done 时的 sha256
	LastError     string // 短原因，不含地址和查询串
	Width, Height int
	Animated      bool
	CoverHash     string
	DurationMS    int64
	Size          int64
	Format        string // 文件的实头种类；展示不重复打开原件
}

// MediaURLSig 是地址的指纹：sha256 前 16 位十六进制。库里不存地址本身（查询串里有 rkey），
// 回填 hash 时拿它核对"事件里那个段的地址还是入队时的那个"
func MediaURLSig(url string) string {
	s := sha256.Sum256([]byte(url))
	return hex.EncodeToString(s[:8])
}

// 文件可能没有 URL，绑定原文件名、ID 和取址令牌，避免迟到结果填进另一份文件。
func MediaSegSig(seg types.Seg) string {
	if seg.Kind == types.SegFile {
		return MediaURLSig(seg.URL + "\x00" + seg.File + "\x00" + seg.FileID + "\x00" + seg.FileHash + "\x00" + seg.FileGroup)
	}
	return MediaURLSig(seg.URL)
}

// PutMediaFetch 写一行（没有就插入，有就整行换成这一份）。状态怎么走由调用方（下载器）管
func (s *Store) PutMediaFetch(m MediaFetch) error {
	tx, err := s.Events.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := putMediaFetch(tx, m); err != nil {
		return err
	}
	var conv string
	err = tx.QueryRow(`SELECT conversation FROM events WHERE seq=?`, m.EventSeq).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	return s.commitChanged(tx, conv)
}

// execer 是 *sql.DB 和 *sql.Tx 共有的写法
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putMediaFetch(db execer, m MediaFetch) error {
	if m.EventSeq <= 0 || m.Idx < 0 || m.State == "" {
		return errors.New("media_fetch 的行没有事件、下标或状态")
	}
	_, err := db.Exec(`INSERT INTO media_fetch (event_seq, idx, kind, url_sig, state, tries, next_at, updated_at, hash, last_error, width, height, animated, cover_hash, duration_ms, size, format)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_seq, idx) DO UPDATE SET kind = excluded.kind, url_sig = excluded.url_sig, state = excluded.state,
			tries = excluded.tries, next_at = excluded.next_at, updated_at = excluded.updated_at, hash = excluded.hash, last_error = excluded.last_error,
			width = excluded.width, height = excluded.height, animated = excluded.animated,
			cover_hash = excluded.cover_hash, duration_ms = excluded.duration_ms, size = excluded.size, format = excluded.format`,
		m.EventSeq, m.Idx, m.Kind, m.URLSig, m.State, m.Tries, m.NextAt, m.UpdatedAt, nullable(m.Hash), nullable(m.LastError), m.Width, m.Height, m.Animated, m.CoverHash, m.DurationMS, m.Size, m.Format)
	return err
}

// MediaFetchRows 是一条事件的全部 media_fetch 行，按段下标排
func (s *Store) MediaFetchRows(seq int64) (map[int]MediaFetch, error) {
	rows, err := s.Events.Query(`SELECT event_seq, idx, kind, url_sig, state, tries, next_at, updated_at, coalesce(hash, ''), coalesce(last_error, ''), width, height, animated, cover_hash, duration_ms, size, format
		FROM media_fetch WHERE event_seq = ?`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]MediaFetch{}
	for rows.Next() {
		var m MediaFetch
		if err := rows.Scan(&m.EventSeq, &m.Idx, &m.Kind, &m.URLSig, &m.State, &m.Tries, &m.NextAt, &m.UpdatedAt, &m.Hash, &m.LastError, &m.Width, &m.Height, &m.Animated, &m.CoverHash, &m.DurationMS, &m.Size, &m.Format); err != nil {
			return nil, err
		}
		out[m.Idx] = m
	}
	return out, rows.Err()
}

// EventRef 是一条事件的会话串和平台消息号（没有映射的事件，号是空串）。取新链接要用平台消息号向协议端问
func (s *Store) EventRef(seq int64) (conversation, platformID string, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT e.conversation, coalesce(m.platform_id, '') FROM events e
		LEFT JOIN channel_msgs m ON m.event_seq = e.seq WHERE e.seq = ?`, seq).Scan(&conversation, &platformID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return conversation, platformID, err == nil, err
}

// MediaEvent 是补扫挑出来的一条事件：它至少有一个"有地址、没 hash、没有终局状态"的图片 / 表情段
type MediaEvent struct {
	Seq          int64
	Conversation string
	CreatedAt    int64
	Segments     string // segments_json 原文
}

// MediaCandidates 按 seq 翻页挑待下载段。图片与文件受 sinceMs 窗口限制；videoSinceSeq>0
// 还恢复本次新收到的视频和文件（含旧时间戳的补录），0 不批量补旧视频。
// 没有行、pending、failed 都算还要做；failed 是否额外重试由调用方判断。
// 不要求角色观察过：陌生人私聊、自检期间的消息也补下载。
// 导入图片没有 URL 不挑；文件可以凭原事件身份取址。坏 JSON 跳过。
func (s *Store) MediaCandidates(sinceMs, afterSeq int64, limit int, videoSinceSeq int64) ([]MediaEvent, error) {
	rows, err := s.Events.Query(`SELECT e.seq, e.conversation, e.created_at, e.segments_json FROM events e
		WHERE e.seq > ?1 AND e.segments_json IS NOT NULL AND json_valid(e.segments_json)
		AND EXISTS (SELECT 1 FROM json_each(e.segments_json, '$.segs') j
			WHERE (e.created_at >= ?2 AND json_extract(j.value, '$.kind') IN ('image', 'emoji', 'card', 'file')
			OR ?4 > 0 AND e.seq >= ?4 AND json_extract(j.value, '$.kind') IN ('video','file'))
			AND (coalesce(json_extract(j.value, '$.url'), '') <> '' OR json_extract(j.value, '$.kind') = 'file')
			AND coalesce(json_extract(j.value, '$.hash'), '') = ''
			AND NOT EXISTS (SELECT 1 FROM media_fetch m WHERE m.event_seq = e.seq AND m.idx = CAST(j.key AS INTEGER) AND m.state NOT IN ('pending', 'failed')))
		ORDER BY e.seq LIMIT ?3`, afterSeq, sinceMs, limit, videoSinceSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaEvent
	for rows.Next() {
		var e MediaEvent
		if err := rows.Scan(&e.Seq, &e.Conversation, &e.CreatedAt, &e.Segments); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetSegHash 把 hash 填回事件 seq 的 $.segs[idx].hash。三个条件同时成立才写，否则什么也不改、返回 false：
// 那一行还在；下标上的段是 wantKind（image / emoji / video / card 封面）；段里的地址指纹等于 urlSig；段现在没有 hash。
// 只改这一个字段：整段 segments_json 按字节拼接（不读出来再整体编码，Seg 以后加字段，旧代码重新编码会丢不认识的字段），
// 别的段、别的键（from、tags、line、import_meta……）的字节一个都不动。
// 写成功后通知界面这个会话变了
func (s *Store) SetSegHash(seq int64, idx int, wantKind, urlSig, hash string) (bool, error) {
	return s.setSegHash(seq, idx, wantKind, urlSig, hash, nil)
}

// FinishSegHash 同 SetSegHash，并且在同一个事务里把 row 写进 media_fetch（不管回填成没成都写）：
// 下载好一张图只提交一次，入库一站和下载同用一个库连接，提交次数多了会互相拖慢
func (s *Store) FinishSegHash(seq int64, idx int, wantKind, urlSig, hash string, row MediaFetch) (bool, error) {
	return s.setSegHash(seq, idx, wantKind, urlSig, hash, &row)
}

func (s *Store) setSegHash(seq int64, idx int, wantKind, urlSig, hash string, row *MediaFetch) (applied bool, err error) {
	if !validSHA256(hash) {
		return false, fmt.Errorf("hash 不是 64 位小写十六进制")
	}
	tx, err := s.Events.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var conv string
	var body sql.NullString
	err = tx.QueryRow(`SELECT conversation, segments_json FROM events WHERE seq = ?`, seq).Scan(&conv, &body)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return false, err
	}
	if !missing && body.Valid {
		if next, ok := spliceSegHash(body.String, idx, wantKind, urlSig, hash); ok {
			// 读到写之间这一行被别的路径改了就不写（库只有一个连接，事务里本来不会，这是第二道）
			res, err := tx.Exec(`UPDATE events SET segments_json = ? WHERE seq = ? AND segments_json = ?`, next, seq, body.String)
			if err != nil {
				return false, err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				applied = true
			}
		}
	}
	if row != nil {
		if err := putMediaFetch(tx, *row); err != nil {
			return false, err
		}
	}
	if applied {
		return true, s.commitChanged(tx, conv)
	}
	return false, tx.Commit()
}

func validSHA256(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, r := range h {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// spliceSegHash 在 body（{"segs":[…],…}）里找 segs[idx]，条件都对就返回只改了那个段的 hash 的新 body。
// 条件不成立、JSON 形状不对都返回 false
func spliceSegHash(body string, idx int, wantKind, urlSig, hash string) (string, bool) {
	if idx < 0 || (wantKind != "image" && wantKind != "emoji" && wantKind != "video" && wantKind != "card" && wantKind != "file") || !validSHA256(hash) {
		return "", false
	}
	segStart, segEnd, ok := findSegRange(body, idx)
	if !ok {
		return "", false
	}
	seg := body[segStart:segEnd]
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(seg), &fields) != nil || fields == nil {
		return "", false
	}
	var kind, url string
	_ = json.Unmarshal(fields["kind"], &kind)
	_ = json.Unmarshal(fields["url"], &url)
	sig := MediaURLSig(url)
	if kind == types.SegFile {
		var media types.Seg
		if json.Unmarshal([]byte(seg), &media) != nil {
			return "", false
		}
		sig = MediaSegSig(media)
	}
	if kind != wantKind || (url == "" && kind != "file") || sig != urlSig {
		return "", false
	}
	// 已经有 hash（任何非空的值，数字也算）不盖；缺、空串、null 才填
	if raw, has := fields["hash"]; has && string(raw) != `""` && string(raw) != "null" {
		return "", false
	}
	quoted := `"hash":"` + hash + `"`
	var newSeg string
	if vs, ve, has := findKeyRange(seg, "hash"); has {
		newSeg = seg[:vs] + `"` + hash + `"` + seg[ve:]
	} else {
		end := strings.LastIndexByte(seg, '}') // 值已经读过是对象，最后一个非空白字符一定是 }
		if end < 0 {
			return "", false
		}
		head := strings.TrimRight(seg[:end], " \t\r\n")
		sep := ","
		if strings.HasSuffix(head, "{") {
			sep = ""
		}
		newSeg = head + sep + quoted + seg[len(head):]
	}
	return body[:segStart] + newSeg + body[segEnd:], true
}

// findSegRange 是顶层键 "segs"（数组）里第 idx 个元素在 body 里的字节范围
func findSegRange(body string, idx int) (start, end int, ok bool) {
	dec := json.NewDecoder(strings.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return 0, 0, false
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return 0, 0, false
		}
		key, _ := kt.(string)
		if key != "segs" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return 0, 0, false
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return 0, 0, false
		}
		for i := 0; dec.More(); i++ {
			var raw json.RawMessage
			if dec.Decode(&raw) != nil {
				return 0, 0, false
			}
			if i == idx {
				e := int(dec.InputOffset())
				return e - len(raw), e, true
			}
		}
		return 0, 0, false
	}
	return 0, 0, false
}

// findKeyRange 是对象 obj 的顶层键 key 的值在 obj 里的字节范围
func findKeyRange(obj, key string) (start, end int, ok bool) {
	dec := json.NewDecoder(strings.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return 0, 0, false
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return 0, 0, false
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return 0, 0, false
		}
		if k, _ := kt.(string); k == key {
			e := int(dec.InputOffset())
			return e - len(raw), e, true
		}
	}
	return 0, 0, false
}
