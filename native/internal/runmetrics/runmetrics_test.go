package runmetrics

import (
	"errors"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

func dur(d time.Duration) *time.Duration { return &d }

func TestStateOf(t *testing.T) {
	cases := []struct {
		name  string
		r     Reading
		limit time.Duration
		want  string
	}{
		{"全是 0 → 空闲", Reading{Backlog: I(0), Running: I(0)}, time.Minute, ctlproto.QueueIdle},
		{"有在跑 → 忙", Reading{Backlog: I(0), Running: I(2)}, time.Minute, ctlproto.QueueBusy},
		{"有积压但没超时 → 忙", Reading{Backlog: I(3), Running: I(1), OldestWait: dur(10 * time.Second)}, time.Minute, ctlproto.QueueBusy},
		{"有积压且超时 → 堵", Reading{Backlog: I(3), Running: I(1), OldestWait: dur(2 * time.Minute)}, time.Minute, ctlproto.QueueJammed},
		{"没积压，等待再久也不堵", Reading{Backlog: I(0), Running: I(1), OldestWait: dur(time.Hour)}, time.Minute, ctlproto.QueueBusy},
		{"有积压但没有最久等待的数 → 不判堵", Reading{Backlog: I(3)}, time.Minute, ctlproto.QueueBusy},
		{"时限 0 表示不判堵", Reading{Backlog: I(3), OldestWait: dur(time.Hour)}, 0, ctlproto.QueueBusy},
		{"积压没计数，在跑是 0 → 空闲", Reading{Running: I(0), Capacity: I(4)}, time.Minute, ctlproto.QueueIdle},
		{"一个数都没有 → 未知，不猜", Reading{Capacity: I(4)}, time.Minute, ctlproto.QueueUnknown},
		{"刚好等于时限不算超", Reading{Backlog: I(1), OldestWait: dur(time.Minute)}, time.Minute, ctlproto.QueueBusy},
	}
	for _, c := range cases {
		if got := StateOf(c.r, c.limit); got != c.want {
			t.Errorf("%s：得到 %s，想要 %s", c.name, got, c.want)
		}
	}
}

func TestRowsFixedOrderAndNotWired(t *testing.T) {
	r := &Registry{}
	rows := r.Rows()
	if len(rows) != len(fixedRows) {
		t.Fatalf("固定行数：%d", len(rows))
	}
	for i, row := range rows {
		if row.Key != fixedRows[i].Key || row.Wired || row.State != ctlproto.QueueUnknown || row.Backlog != nil || row.Running != nil {
			t.Fatalf("没注册的行应该是未接入、数值全是 nil（不是 0）：%+v", row)
		}
	}
}

func TestRowReadAndErrorIsolated(t *testing.T) {
	r := &Registry{}
	r.Register(KeyModelSlots, time.Minute, func() (Reading, error) {
		return Reading{Backlog: I(2), Running: I(4), Capacity: I(4), OldestWait: dur(90 * time.Second), Note: "估计"}, nil
	})
	r.Register(KeyMemory, 0, func() (Reading, error) { return Reading{}, errors.New("编造的错误") })
	r.Register(KeyLink, 0, func() (Reading, error) { panic("编造的 panic") })
	r.Register(KeyMedia, 0, func() (Reading, error) { return Reading{Backlog: I(0), Running: I(0)}, nil })
	by := map[string]ctlproto.QueueRow{}
	for _, row := range r.Rows() {
		by[row.Key] = row
	}
	m := by[KeyModelSlots]
	if !m.Wired || m.State != ctlproto.QueueJammed || *m.Backlog != 2 || *m.OldestWaitMs != 90000 || m.Note != "估计" {
		t.Fatalf("模型槽位：%+v", m)
	}
	if e := by[KeyMemory]; !e.Wired || e.Err == "" || e.State != ctlproto.QueueUnknown || e.Backlog != nil {
		t.Fatalf("读失败的行写原因、数值留空：%+v", e)
	}
	if e := by[KeyLink]; e.Err == "" || e.State != ctlproto.QueueUnknown {
		t.Fatalf("panic 当成这一行读失败：%+v", e)
	}
	if ok := by[KeyMedia]; ok.Err != "" || ok.State != ctlproto.QueueIdle {
		t.Fatalf("别的行不受影响：%+v", ok)
	}
	if v := by[KeyVision]; v.Wired {
		t.Fatalf("没注册的行仍是未接入：%+v", v)
	}
}

func TestRegisterReplaceUnregisterAndExtra(t *testing.T) {
	r := &Registry{}
	r.Register(KeyBackfill, 0, func() (Reading, error) { return Reading{Running: I(1)}, nil })
	r.Register(KeyBackfill, 0, func() (Reading, error) { return Reading{Running: I(5)}, nil }) // 重开后的新实例接替
	r.Register("z-extra", 0, func() (Reading, error) { return Reading{Running: I(0)}, nil })
	r.Register("a-extra", 0, func() (Reading, error) { return Reading{Running: I(0)}, nil })
	rows := r.Rows()
	if len(rows) != len(fixedRows)+2 || rows[len(rows)-2].Key != "a-extra" || rows[len(rows)-1].Key != "z-extra" {
		t.Fatalf("固定行之外的接在后面、按 key 排：%+v", rows)
	}
	for _, row := range rows {
		if row.Key == KeyBackfill && *row.Running != 5 {
			t.Fatalf("后注册的应当替换旧的：%+v", row)
		}
	}
	r.Unregister(KeyBackfill)
	for _, row := range r.Rows() {
		if row.Key == KeyBackfill && row.Wired {
			t.Fatal("撤销后应该回到未接入")
		}
	}
}
