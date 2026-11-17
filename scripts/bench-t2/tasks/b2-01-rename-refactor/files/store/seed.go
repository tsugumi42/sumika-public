package store

// Seed 返回一个装了演示数据的 Store。
func Seed() *Store {
	s := New()
	for _, it := range []Item{
		{"A-100", "bolt", 120, 25},
		{"B-200", "washer", 8, 10},
		{"C-300", "spring", 3, 180},
		{"D-400", "bearing", 9, 1250},
		{"E-500", "tape", 15, 399},
		{"F-600", "label", 0, 5},
		{"G-700", "glove", 10, 799},
		{"H-800", "cable tie", 4, 60},
	} {
		s.Add(it)
	}
	return s
}
