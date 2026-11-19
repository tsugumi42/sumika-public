package store

import "testing"

// 整表读法：没设过是空表；设过的一行一个；"*" 就是所有群那一行；删掉以后不在了
func TestGateModeRows(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	if rows, err := s.GateModeRows(); err != nil || len(rows) != 0 {
		t.Fatalf("没设过应是空表：%v %v", rows, err)
	}
	for conv, mode := range map[string]string{"qq:group:5001": "off", "qq:group:5002": "speak", "*": "dry"} {
		if err := s.SetGateMode(conv, mode, 100); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.GateModeRows()
	if err != nil || len(rows) != 3 || rows["qq:group:5001"] != "off" || rows["qq:group:5002"] != "speak" || rows["*"] != "dry" {
		t.Fatalf("%v %v", rows, err)
	}
	if err := s.SetGateMode("qq:group:5001", "", 200); err != nil {
		t.Fatal(err)
	}
	if rows, _ = s.GateModeRows(); len(rows) != 2 || rows["qq:group:5001"] != "" {
		t.Fatalf("删掉以后：%v", rows)
	}
}
