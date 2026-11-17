package rank

import "sort"

// Entry 是排行榜上的一行。
type Entry struct {
	Customer string
	Total    int
}

// TopCustomers 返回消费最多的前 n 位：金额从高到低，金额相同按名字从小到大。
// 客户不足 n 位就全部返回；n 不是正数返回空。
func TopCustomers(sums map[string]int, n int) []Entry {
	entries := make([]Entry, 0, len(sums))
	for name, total := range sums {
		entries = append(entries, Entry{name, total})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Total != entries[j].Total {
			return entries[i].Total > entries[j].Total
		}
		return entries[i].Customer < entries[j].Customer
	})
	return entries[:n]
}
