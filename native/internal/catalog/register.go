package catalog

// 登记表：新加一个组件族的例子，写好 Demo 函数，在这里加一条 Add。顺序就是画廊左边列表的顺序。
func init() {
	Add(Entry{ID: "overview", Family: "族谱总览", Name: "族谱总览：每一族的静止样子并排", Blurb: "按凹下去、平的、浮起来三档和状态、头像、消息排开，对着《设计自查》第六节一眼看出谁不统一。顶栏切亮色夜间、圆角疏密一起看。", Demo: overviewDemo})
	Add(Entry{ID: "button", Family: "按钮", Name: "按钮", Blurb: "文字按钮的主、次、禁用，图标钮的平时和禁用。点发送、撤回、收藏看反馈。", Demo: buttonDemo})
	Add(Entry{ID: "choice", Family: "选择类", Name: "分段、筛选片、开关、勾选框", Blurb: "几选一、筛选、开关、勾选，各自的选中和禁用。点一下，下面的句子跟着变。", Demo: choiceDemo})
	Add(Entry{ID: "motion", Family: "选中和切换", Name: "选中块滑动、图标页签", Blurb: "分段、筛选片、图标页签、分类栏、导航栏：点一下，选中的那块从旧位置滑到新位置。顶栏能慢放、重放（R）看细节。", Demo: motionDemo})
	Add(Entry{ID: "spring", Family: "弹簧反馈", Name: "按钮回弹、开关挤压、提示条", Blurb: "按钮按下缩小再弹回、开关滑钮按速度拉长、三颗按钮弹出提示条。顶栏能慢放、重放（R）看细节。", Demo: springDemo})
	Add(Entry{ID: "numbers", Family: "数字和图标", Name: "步进器、倒计时、图标变形", Blurb: "步进器加减时只有变了的位滚、审批倒计时每秒落一位、复制 ↔ 勾等四对图标点一下轮廓流到另一个。顶栏能慢放、重放（R）看细节。", Demo: numbersDemo})
	Add(Entry{ID: "menus", Family: "菜单", Name: "按钮菜单、选择器、右键菜单、对话框", Blurb: "菜单点开时从按钮那条边长出来，关掉时缩回去；窗口下面放不下就朝上长。对话框从点的那颗按钮长到正中。顶栏能慢放看细节。", Demo: menusDemo})
	Add(Entry{ID: "input", Family: "输入类", Name: "输入框、搜索框、下拉、步进器、滑块", Blurb: "打字、搜索、选模型、加减、拖滑块，旁边的结果实时变。", Demo: inputDemo})
	Add(Entry{ID: "bubble", Family: "聊天气泡", Name: "聊天气泡", Blurb: "对方的、自己的、连续两条；试一试里能发消息，也能发一条失败的再重发。", Demo: bubbleDemo})
	Add(Entry{ID: "table", Family: "表格", Name: "Markdown 表格", Blurb: "短表按内容宽、长文字列排满并换行、列很多的在表自己里横向滚动；数字列右对齐。", Demo: tableDemo})
	Add(Entry{ID: "message", Family: "消息和审批", Name: "审批收起、过程展开、整块出现", Blurb: "审批卡决定后自己收成一行、过程那一条点一下展开收起、表格和代码块整块淡入。顶栏能慢放、重放（R）看细节。", Demo: messageDemo})
	Add(Entry{ID: "approval", Family: "审批卡", Name: "审批卡", Blurb: "等批准的命令和改文件，提交中，四种结果；试一试里能拒绝、允许一次、选“⋯”。", Demo: approvalDemo})
	Add(Entry{ID: "chat-glass", Family: "材质", Name: "玻璃材质：消息的实色和玻璃", Blurb: "聊天里的消息做成半透明加模糊的玻璃，可以调浓度；长对话里滚着看，玻璃浮条压在上面。", Demo: bubbleGlassDemo})
	Add(Entry{ID: "settings", Family: "设置页", Name: "设置页：分栏、分组、一行一项", Blurb: "左边分类，右边按事情分组的面；开关、加减、下拉、筛选片就在名字旁边，改过的项有小圆点；点左边换成插件页，点“另有 17 个内置插件”展开。", Demo: settingsDemo})
}
