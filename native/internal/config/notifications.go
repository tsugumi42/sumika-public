package config

// Notifications 是桌面系统通知按类的开关（规格-目标和循环“提醒”一节）。后台说“这里有件事要主人看”时，
// 先看这一类开着没有；窗口在不在前台、看没看着那个会话由桌面宿主另判。默认全开，和以前一致。
type Notifications struct {
	// Goal 目标达成、目标暂停了要你看一眼
	Goal bool `yaml:"goal"`
	// Loop 定期任务有内容要看或出错（任务自己的“通知档”仍然管得更细）
	Loop bool `yaml:"loop"`
	// Approval 有操作等你批准
	Approval bool `yaml:"approval"`
	// Question 她有问题想问你
	Question bool `yaml:"question"`
	// Reminder 提醒到点了
	Reminder bool `yaml:"reminder"`
}

// DefaultNotifications 全开
func DefaultNotifications() Notifications {
	return Notifications{Goal: true, Loop: true, Approval: true, Question: true, Reminder: true}
}
