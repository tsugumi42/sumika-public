package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sumika-native/internal/kernel"
)

type KernelMaterial struct{ ID, Kind, Text string }

// InsertKernelMaterial 与来源邮箱确认共用事务；来源编号重复不能换内容或归属。
func InsertKernelMaterial(ctx context.Context, tx *sql.Tx, in kernel.Invocation, m KernelMaterial) error {
	owner, err := json.Marshal(in.Scope)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO kernel_material_inputs(id,root_id,owner_json,run_id,kind,text) VALUES(?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET id=excluded.id WHERE kernel_material_inputs.root_id=excluded.root_id AND kernel_material_inputs.owner_json=excluded.owner_json AND kernel_material_inputs.kind=excluded.kind AND kernel_material_inputs.text=excluded.text`, m.ID, in.RootID, string(owner), in.RunID, m.Kind, m.Text)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrInputConflict
	}
	return err
}

func (s *Store) PutKernelMaterial(ctx context.Context, in kernel.Invocation, m KernelMaterial) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := InsertKernelMaterial(ctx, tx, in, m); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) KernelMaterials(ctx context.Context, in kernel.Invocation) ([]KernelMaterial, error) {
	owner, err := json.Marshal(in.Scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.Trace.QueryContext(ctx, `SELECT id,kind,text FROM kernel_material_inputs WHERE root_id=? AND owner_json=? ORDER BY rowid`, in.RootID, string(owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []KernelMaterial
	for rows.Next() {
		var m KernelMaterial
		if err := rows.Scan(&m.ID, &m.Kind, &m.Text); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
