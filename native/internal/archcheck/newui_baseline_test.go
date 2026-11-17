package archcheck

// uiKitBaseline 是“页面直接 import 组件库”的存量：规则见 newui_test.go 开头，这里只放数据。
// 每一项是“文件 → 它 import 的库”，路径相对 native/，用正斜杠。只许变少、不许新增：
//   - 新文件、新的“文件 × 库”组合一出现，TestUIKitImportsOnlyThroughFacade 就红；
//   - 某一项已经不成立（文件没了、不再 import 那个库）也红，要从这里删掉，不留死条目。
//
// 临时：这些存量随各页改成经 internal/winshell 取组件、旧界面退役时逐个清掉（后续路线 P3-4、P3-9）；
// 清空以后把这张表和 uiKitBaselineReason 删了。每个库的原因和什么时候去掉写在 uiKitBaselineReason 里。
// 往里加条目等于放宽规则，只有“旧代码原样搬了位置”才说得过去，并在提交说明里写原因。
var uiKitBaseline = map[string][]string{
	"_spike/c_test.go":                    {"CrychicUI"},
	"appearance.go":                       {"CrychicUI", "MewTypeUI"},
	"appearance_test.go":                  {"MewTypeUI"},
	"arrive.go":                           {"MewTypeUI"},
	"body.go":                             {"MewTypeUI"},
	"bubble.go":                           {"MewTypeUI"},
	"card.go":                             {"MewTypeUI"},
	"chatmedia.go":                        {"MewTypeUI"},
	"convlist.go":                         {"MewTypeUI"},
	"convlist_test.go":                    {"MewTypeUI"},
	"datalocation.go":                     {"CrychicUI"},
	"feedback.go":                         {"MewTypeUI"},
	"file.go":                             {"MewTypeUI"},
	"fontfamily.go":                       {"CrychicUI", "MewTypeUI"},
	"fontfamily_test.go":                  {"MewTypeUI"},
	"fontshot_test.go":                    {"MewTypeUI"},
	"forward.go":                          {"MewTypeUI"},
	"freeapisettings.go":                  {"MewTypeUI"},
	"inline.go":                           {"MewTypeUI"},
	"internal/desksettings/appearance.go": {"MewTypeUI"},
	"internal/desksettings/appearance_test.go": {"MewTypeUI"},
	"internal/linkcard/card.go":                {"MewTypeUI"},
	"internal/lookpreset/derive.go":            {"MewTypeUI"},
	"internal/lookpreset/lookpreset.go":        {"MewTypeUI"},
	"internal/lookpreset/lookpreset_test.go":   {"MewTypeUI"},
	"legacy_choice.go":                         {"CrychicUI", "MewTypeUI"},
	"lifesettings.go":                          {"MewTypeUI"},
	"linkforward.go":                           {"MewTypeUI"},
	"linksettings.go":                          {"MewTypeUI"},
	"linksites.go":                             {"MewTypeUI"},
	"localchat.go":                             {"MewTypeUI"},
	"localgroup.go":                            {"CrychicUI"},
	"localinput_test.go":                       {"MewTypeUI"},
	"main.go":                                  {"MewTypeUI"},
	"media_download.go":                        {"MewTypeUI"},
	"memoryview.go":                            {"CrychicUI"},
	"modelsettings.go":                         {"MewTypeUI"},
	"modelsettings_limits.go":                  {"MewTypeUI"},
	"panes.go":                                 {"MewTypeUI"},
	"panes_test.go":                            {"MewTypeUI"},
	"picture.go":                               {"MewTypeUI"},
	"proto_palette_shot_test.go":               {"MewTypeUI"},
	"proto_palette_test.go":                    {"MewTypeUI"},
	"quote.go":                                 {"MewTypeUI"},
	"quote_test.go":                            {"MewTypeUI"},
	"reaction.go":                              {"MewTypeUI"},
	"row.go":                                   {"MewTypeUI"},
	"runpage_quality.go":                       {"CrychicUI"},
	"runpage_resources.go":                     {"CrychicUI"},
	"session.go":                               {"MewTypeUI"},
	"settings.go":                              {"CrychicUI", "MewTypeUI"},
	"status.go":                                {"CrychicUI", "MewTypeUI"},
	"textsize.go":                              {"CrychicUI", "MewTypeUI"},
	"textsize_test.go":                         {"MewTypeUI"},
	"theme.go":                                 {"MewTypeUI"},
	"themefile.go":                             {"MewTypeUI"},
	"titlebar.go":                              {"MewTypeUI"},
	"ui.go":                                    {"MewTypeUI"},
	"video.go":                                 {"MewTypeUI"},
	"video_test.go":                            {"MewTypeUI"},
	"viewer.go":                                {"CrychicUI"},
	"viewer_test.go":                           {"CrychicUI"},
}

// uiKitBaselineMax 是 uiKitBaseline 里“文件 × 库”的对数，写死。基线只许变少：删条目时把它同步改小（测试会提醒）；
// 往基线加条目就得改大它——**只许改小；要改大先问主人**。
const uiKitBaselineMax = 68

// uiKitBaselineReason 说明每个库的存量为什么还在、什么时候去掉。
var uiKitBaselineReason = map[string]string{
	"MewTypeUI": "旧界面（mainView=legacy）和旧页面还在用，随旧界面退役整体删；新写的页面不许再用（ADR-374 第 4 条“仍按原规则不许进”）",
	"CrychicUI": "ADR-364 时期页面直接用 CrychicUI 留下的；按 ADR-374 改成经 internal/winshell 取组件（P3-4、P3-9），试验目录 _spike 用完即删",
}
