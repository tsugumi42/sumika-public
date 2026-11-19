package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

// RefDigest 是一条记忆「给她看的样子」的指纹。参考信息块按档存库时，块里引用的每条记忆带一份（规格-提示词前缀稳定 7.2），
// 重建时用 VerifyRefs 重算再比：记忆删了、改了、改了范围、过期了、被隔离了，指纹对不上，整块不插。
type RefDigest struct {
	Ref  string `json:"id"`
	Hash string `json:"hash"`
}

// HitDigest 算这一条命中给她看的样子的指纹。只取不随时间变的字段：文字、来源、类型、待确认、范围、被取代的旧版本（各自的文字和时间），
// 加上这一档的「主人私下说的」标记。渲染出来的「3 天前」不进指纹，不然隔一天所有块都对不上；过期、隔离不是字段，由 VerifyRefs 单独判。
func HitDigest(h Hit) RefDigest {
	type prior struct {
		Text string `json:"t"`
		At   int64  `json:"at"`
	}
	scopes := slices.Clone(h.Scopes)
	slices.Sort(scopes)
	var prev []prior
	for _, p := range h.Previous {
		prev = append(prev, prior{p.Text, p.CreatedAt})
	}
	raw, _ := json.Marshal(struct {
		Ref       string   `json:"ref"`
		Type      string   `json:"type"`
		Kind      string   `json:"kind"`
		Text      string   `json:"text"`
		Source    string   `json:"source"`
		Hearsay   bool     `json:"hearsay"`
		OwnerOnly bool     `json:"owner_only"`
		Scopes    []string `json:"scopes"`
		CreatedAt int64    `json:"created"`
		Previous  []prior  `json:"previous"`
	}{h.Ref, h.Type, h.Kind, h.Text, h.Source, h.Hearsay, h.OwnerOnly, scopes, h.CreatedAt, prev})
	sum := sha256.Sum256(raw)
	return RefDigest{Ref: h.Ref, Hash: hex.EncodeToString(sum[:])}
}

// VerifyRefs 问：这些记忆现在还是原样吗。全部满足才返回真，任何一条不满足、或读不出来，都返回假（失败关闭）：
//   - 还在条目表里（没删、没被取代、没被隔离），且没过期（now 是重建的这一刻，过期随时间变，只在写入时看不够）；
//   - 用这一档的听众 a 重算出的指纹和块里存的一样（文字、来源、范围、「主人私下说的」标记任何一样变了都对不上）。
//
// 来源要回查聊天原文（labelSources）：读不出来时来源退成「未核实」，指纹自然对不上，块不插。
func (m *Memory) VerifyRefs(ctx context.Context, a Audience, now time.Time, refs []RefDigest) bool {
	if len(refs) == 0 {
		return true
	}
	_, byRef := m.snapshotForRead()
	ownerOnly := ownerOnlyFn(a, DisclosureScopes(a))
	hits := make([]Hit, 0, len(refs))
	for _, r := range refs {
		it := byRef[r.Ref]
		if it == nil || it.Expired(now) {
			return false
		}
		hits = append(hits, Hit{Item: *it, Hearsay: it.Status == "candidate"})
	}
	m.labelSources(ctx, hits)
	for i := range hits {
		hits[i].OwnerOnly = ownerOnly(hits[i].Scopes)
		if HitDigest(hits[i]).Hash != refs[i].Hash {
			return false
		}
	}
	return true
}

// itemSig 是一条记忆整体的指纹（变没变的判断，不管给她看成什么样）
func itemSig(it *Item) string {
	raw, _ := json.Marshal(it)
	sum := sha256.Sum256(raw)
	return string(sum[:])
}

// changedRefs 是 old 到 now 之间删了、改了的条目（新增的不算：没有块引用它们）
func changedRefs(old, now map[string]*Item) []string {
	var out []string
	for ref, it := range old {
		if n := now[ref]; n == nil || itemSig(n) != itemSig(it) {
			out = append(out, ref)
		}
	}
	slices.Sort(out)
	return out
}
