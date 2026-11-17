package store

// 群档位的整表读法：控制接口（sumika-ctl gate list）要把各群的档位列出来。单个会话的读法是 gate.go 的 GateModeOf。

// GateModeRows 读 gate_modes 的全部行：会话 → 这个会话自己的档位设置（off / dry / speak）。
// "*" 是"所有群"的那一行。只读，archive 打开的只读库也能用
func (s *Store) GateModeRows() (map[string]string, error) {
	rows, err := s.Trace.Query(`SELECT conversation, mode FROM gate_modes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var c, m string
		if err := rows.Scan(&c, &m); err != nil {
			return nil, err
		}
		out[c] = m
	}
	return out, rows.Err()
}
