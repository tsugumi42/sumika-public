package memory

// Visibility 是一个场合里想不想得起
type Visibility string

const (
	VisYes  Visibility = "yes"  // 这个场合里的任何一种听众都想得起
	VisNo   Visibility = "no"   // 都想不起
	VisWhen Visibility = "when" // 看是谁在听，When 里是想得起的那几种
)

// PlaceView 是一个场合的结果
type PlaceView struct {
	Place   Place
	Visible Visibility
	When    []string // Visible == VisWhen 时，想得起的那几种听众
}

// ScopePlaces 只描述场合召回资格；范围标签保留展示，不再用它筛场合（ADR-242）。
func ScopePlaces([]string) []PlaceView {
	return []PlaceView{
		{Place: LocalPrivate, Visible: VisYes},
		{Place: LocalGroup, Visible: VisYes},
		{Place: PlatformPrivate, Visible: VisYes},
		{Place: PlatformGroup, Visible: VisYes},
	}
}
