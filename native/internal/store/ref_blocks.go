package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// RefBlock 是一块参考信息（时间、对面是谁、记忆召回、审批说法……）的原字节，见 0006_ref_blocks.sql。
// ScopeKey 是这一块能被谁看见的档：必须非空，缺了不当通配（读和写都拒）。
type RefBlock struct {
	CharacterID, Conversation, ScopeKey string
	BeforeSeq                           int64  // 块后面第一条事件的 seq
	Ordinal                             int    // 同一 BeforeSeq 内的序号，从 0 起
	CardHash                            string // 角色卡加系统段的哈希，不等于当前值的块不插
	MemoryRefsJSON                      string // 块引用的记忆：[{"id":…,"hash":…}]，没有就是 []；不合法的整批拒绝（见 checkMemoryRefs）
	Text                                string // 空串不写行（和 buildContext 返回 "" 一致）
	CreatedAt                           int64
}

// memoryRef 是块引用的一条记忆：ID 和内容哈希（哈希怎么算、范围和过期怎么进去，见规格 7.3）。
type memoryRef struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// checkMemoryRefs 严格解成 [{id, hash}]：null、标量、对象、缺 id 或 hash 都不通过。
// 只看是不是合法 JSON 不够：null 解出来是“没有引用”，那块永远不会因记忆变动而失效，等于静默丢了核对。
func checkMemoryRefs(js string) error {
	var refs []memoryRef
	if err := json.Unmarshal([]byte(js), &refs); err != nil {
		return fmt.Errorf("参考块的记忆引用不是 [{id,hash}] 数组（解析失败不当空）：%w", err)
	}
	if refs == nil {
		return errors.New("参考块的记忆引用是 null（没有引用要写 []）")
	}
	for _, r := range refs {
		if r.ID == "" || r.Hash == "" {
			return errors.New("参考块的记忆引用缺 id 或 hash")
		}
	}
	return nil
}

// PutRefBlocksTx 在调用方的事务里写一批块：交回时和事件同一个事务（规格 7.2），事务失败整体回滚，库里不留半截。
// 任一条不合法就返回错误，调用方回滚；同一主键再写一次就覆盖，Commit 重做时幂等。空 Text 不写行，不报错。
func PutRefBlocksTx(tx *sql.Tx, blocks []RefBlock) error {
	for _, b := range blocks {
		if b.CharacterID == "" || b.Conversation == "" || b.ScopeKey == "" {
			return errors.New("参考块缺角色、会话或档（缺档不当通配，不写）")
		}
		if b.Ordinal < 0 || b.BeforeSeq < 0 {
			return fmt.Errorf("参考块位置不合法：before_seq=%d ordinal=%d", b.BeforeSeq, b.Ordinal)
		}
		if err := checkMemoryRefs(b.MemoryRefsJSON); err != nil {
			return err
		}
	}
	for _, b := range blocks {
		if b.Text == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO ref_blocks
			(character_id, conversation, scope_key, before_seq, ordinal, card_hash, memory_refs_json, text, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.CharacterID, b.Conversation, b.ScopeKey, b.BeforeSeq, b.Ordinal, b.CardHash, b.MemoryRefsJSON, b.Text, b.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// PruneRefBlocksTx 在调用方的事务里删掉同一（角色、会话）下 before_seq 小于窗口起点的块，所有档一起删（规格 7.3 窗口裁剪）。
// 窗口是按会话锚定的，起点之前的块对任何档都不会再插回；别的会话的行不动。角色或会话缺了报错，不当通配。
func PruneRefBlocksTx(tx *sql.Tx, characterID, conversation string, fromSeq int64) error {
	if characterID == "" || conversation == "" {
		return errors.New("清理参考块缺角色或会话（不当通配）")
	}
	_, err := tx.Exec(`DELETE FROM ref_blocks WHERE character_id = ? AND conversation = ? AND before_seq < ?`,
		characterID, conversation, fromSeq)
	return err
}

// ListRefBlocks 取同一角色、会话、档下 before_seq 不小于 fromSeq 的块，按（before_seq, ordinal）排。
// 档为空直接报错（不当通配）；其他档的块不会出现在结果里。记忆引用校验不过的行整块跳过（失败关闭，结果仍是库里的子集）。
func (s *Store) ListRefBlocks(characterID, conversation, scopeKey string, fromSeq int64) ([]RefBlock, error) {
	if characterID == "" || conversation == "" || scopeKey == "" {
		return nil, errors.New("读参考块缺角色、会话或档（缺档不当通配）")
	}
	rows, err := s.Events.Query(`SELECT character_id, conversation, scope_key, before_seq, ordinal, card_hash, memory_refs_json, text, created_at
		FROM ref_blocks WHERE character_id = ? AND conversation = ? AND scope_key = ? AND before_seq >= ?
		ORDER BY before_seq, ordinal`, characterID, conversation, scopeKey, fromSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RefBlock
	for rows.Next() {
		var b RefBlock
		if err := rows.Scan(&b.CharacterID, &b.Conversation, &b.ScopeKey, &b.BeforeSeq, &b.Ordinal, &b.CardHash, &b.MemoryRefsJSON, &b.Text, &b.CreatedAt); err != nil {
			return nil, err
		}
		if checkMemoryRefs(b.MemoryRefsJSON) != nil {
			continue
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveRefBlocks 在一个事务里写一批块并清掉窗口起点之前的块（交回时用，规格 7.3）：任一步出错整体回滚，库里不留半截。
// blocks 可以是空的（这一轮没有可存的块，照样清理）；fromSeq 为 0 不清理
func (s *Store) SaveRefBlocks(blocks []RefBlock, characterID, conversation string, fromSeq int64) error {
	tx, err := s.Events.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := PutRefBlocksTx(tx, blocks); err != nil {
		return err
	}
	if fromSeq > 0 {
		if err := PruneRefBlocksTx(tx, characterID, conversation, fromSeq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PurgeRefBlocksByMemory 删掉引用了这些记忆的块（记忆删了、被取代、改了文字或范围，规格 7.3）。
// characterID 为空是所有角色的块（共用池的记忆谁都能引用）。ref 为空的列表什么都不删
func (s *Store) PurgeRefBlocksByMemory(characterID string, memoryRefs []string) error {
	if len(memoryRefs) == 0 {
		return nil
	}
	js, _ := json.Marshal(memoryRefs)
	// json_valid 先挡坏 JSON：直接改库留下的坏行不能让整条清理报错（读的时候本来就整块跳过）
	_, err := s.Events.Exec(`DELETE FROM ref_blocks WHERE (? = '' OR character_id = ?) AND json_valid(memory_refs_json) AND EXISTS (
		SELECT 1 FROM json_each(CASE WHEN json_valid(ref_blocks.memory_refs_json) THEN ref_blocks.memory_refs_json ELSE '[]' END) j
		WHERE json_extract(j.value, '$.id') IN (SELECT value FROM json_each(?)))`,
		characterID, characterID, string(js))
	return err
}
