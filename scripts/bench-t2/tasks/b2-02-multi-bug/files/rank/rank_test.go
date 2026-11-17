package rank

import (
	"testing"

	"example.com/ledger/parse"
)

func mustParse(t *testing.T, line string) parse.Order {
	t.Helper()
	o, err := parse.ParseLine(line)
	if err != nil {
		t.Fatalf("%q：%v", line, err)
	}
	return o
}

func TestSumByCustomer(t *testing.T) {
	orders := []parse.Order{
		mustParse(t, "2026-03-01,Ann,12.5,0"),
		mustParse(t, "2026-03-02, ann ,7,0"),
		mustParse(t, "2026-03-02,bob,10,0"),
	}
	got := SumByCustomer(orders, 700)
	if got["ann"] != 2087 || got["bob"] != 1070 || len(got) != 2 {
		t.Errorf("得到 %v，想要 ann=2087 bob=1070", got)
	}
}

func TestTopCustomers(t *testing.T) {
	sums := map[string]int{"carol": 500, "ann": 900, "bob": 500}
	got := TopCustomers(sums, 2)
	if len(got) != 2 || got[0].Customer != "ann" || got[1].Customer != "bob" {
		t.Errorf("前两名不对：%+v", got)
	}
}

func TestTopCustomersMoreThanExist(t *testing.T) {
	sums := map[string]int{"carol": 500, "ann": 900, "bob": 500}
	got := TopCustomers(sums, 10)
	if len(got) != 3 || got[2].Customer != "carol" {
		t.Errorf("客户不足 n 位应该全部返回：%+v", got)
	}
}

func TestTopCustomersNonPositive(t *testing.T) {
	sums := map[string]int{"ann": 900}
	if got := TopCustomers(sums, 0); len(got) != 0 {
		t.Errorf("n=0 应该返回空：%+v", got)
	}
}
