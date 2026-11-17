# 颜色来源

生成命令：`go run ./internal/cmd/themegen`。版本见 README.md。

覆盖 lib_ui 的 colors.palette 全部声明，含登录、设置、通话、钱包等扩展角色；不复制素材。别名展开为 RGBA，`| fallback` 的左侧是浅色声明值。night 使用同名覆盖，缺项继承浅色声明，再在合并表内解析别名；不含背景图片。

| MewTypeUI 键 | Telegram 常量 | 浅色来源 | 展开值 | night 来源 | night 展开值 |
|---|---|---|---|---|---|
| `avatar.archiveUserpicBackground` | `historyPeerArchiveUserpicBg` | ui/colors.palette:318 | `#bbbbbbff` | 继承浅色 / 合并别名 | `#3e546aff` |
| `avatar.peer1NameForeground` | `historyPeer1NameFg` | ui/colors.palette:292 | `#c03d33ff` | night.tdesktop-theme/colors.tdesktop-theme:218 | `#fb6169ff` |
| `avatar.peer1NameForegroundSelected` | `historyPeer1NameFgSelected` | ui/colors.palette:293 | `#c03d33ff` | night.tdesktop-theme/colors.tdesktop-theme:219 | `#ffffffff` |
| `avatar.peer1UserpicBackground` | `historyPeer1UserpicBg` | ui/colors.palette:294 | `#ff845eff` | night.tdesktop-theme/colors.tdesktop-theme:220 | `#ff845eff` |
| `avatar.peer1UserpicBackground2` | `historyPeer1UserpicBg2` | ui/colors.palette:320 | `#d45246ff` | night.tdesktop-theme/colors.tdesktop-theme:243 | `#d45246ff` |
| `avatar.peer2NameForeground` | `historyPeer2NameFg` | ui/colors.palette:295 | `#4fad2dff` | night.tdesktop-theme/colors.tdesktop-theme:221 | `#85de85ff` |
| `avatar.peer2NameForegroundSelected` | `historyPeer2NameFgSelected` | ui/colors.palette:296 | `#4fad2dff` | night.tdesktop-theme/colors.tdesktop-theme:222 | `#ffffffff` |
| `avatar.peer2UserpicBackground` | `historyPeer2UserpicBg` | ui/colors.palette:297 | `#9ad164ff` | night.tdesktop-theme/colors.tdesktop-theme:223 | `#9ad164ff` |
| `avatar.peer2UserpicBackground2` | `historyPeer2UserpicBg2` | ui/colors.palette:321 | `#46ba43ff` | night.tdesktop-theme/colors.tdesktop-theme:244 | `#46ba43ff` |
| `avatar.peer3NameForeground` | `historyPeer3NameFg` | ui/colors.palette:298 | `#d09306ff` | night.tdesktop-theme/colors.tdesktop-theme:224 | `#f3bc5cff` |
| `avatar.peer3NameForegroundSelected` | `historyPeer3NameFgSelected` | ui/colors.palette:299 | `#d09306ff` | night.tdesktop-theme/colors.tdesktop-theme:225 | `#ffffffff` |
| `avatar.peer3UserpicBackground` | `historyPeer3UserpicBg` | ui/colors.palette:300 | `#e5ca77ff` | night.tdesktop-theme/colors.tdesktop-theme:226 | `#e5ca77ff` |
| `avatar.peer3UserpicBackground2` | `historyPeer3UserpicBg2` | ui/colors.palette:322 | `#e5ca77ff` | night.tdesktop-theme/colors.tdesktop-theme:245 | `#e5ca77ff` |
| `avatar.peer4NameForeground` | `historyPeer4NameFg` | ui/colors.palette:301 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:227 | `#65bdf3ff` |
| `avatar.peer4NameForegroundSelected` | `historyPeer4NameFgSelected` | ui/colors.palette:302 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:228 | `#ffffffff` |
| `avatar.peer4UserpicBackground` | `historyPeer4UserpicBg` | ui/colors.palette:303 | `#5caffaff` | night.tdesktop-theme/colors.tdesktop-theme:229 | `#5caffaff` |
| `avatar.peer4UserpicBackground2` | `historyPeer4UserpicBg2` | ui/colors.palette:323 | `#408acfff` | night.tdesktop-theme/colors.tdesktop-theme:246 | `#408acfff` |
| `avatar.peer5NameForeground` | `historyPeer5NameFg` | ui/colors.palette:304 | `#8544d6ff` | night.tdesktop-theme/colors.tdesktop-theme:230 | `#b48bf2ff` |
| `avatar.peer5NameForegroundSelected` | `historyPeer5NameFgSelected` | ui/colors.palette:305 | `#8544d6ff` | night.tdesktop-theme/colors.tdesktop-theme:231 | `#ffffffff` |
| `avatar.peer5UserpicBackground` | `historyPeer5UserpicBg` | ui/colors.palette:306 | `#b694f9ff` | night.tdesktop-theme/colors.tdesktop-theme:232 | `#b694f9ff` |
| `avatar.peer5UserpicBackground2` | `historyPeer5UserpicBg2` | ui/colors.palette:324 | `#6c61dfff` | night.tdesktop-theme/colors.tdesktop-theme:247 | `#6c61dfff` |
| `avatar.peer6NameForeground` | `historyPeer6NameFg` | ui/colors.palette:307 | `#cd4073ff` | night.tdesktop-theme/colors.tdesktop-theme:233 | `#ff5694ff` |
| `avatar.peer6NameForegroundSelected` | `historyPeer6NameFgSelected` | ui/colors.palette:308 | `#cd4073ff` | night.tdesktop-theme/colors.tdesktop-theme:234 | `#ffffffff` |
| `avatar.peer6UserpicBackground` | `historyPeer6UserpicBg` | ui/colors.palette:309 | `#ff8aacff` | night.tdesktop-theme/colors.tdesktop-theme:235 | `#ff8aacff` |
| `avatar.peer6UserpicBackground2` | `historyPeer6UserpicBg2` | ui/colors.palette:325 | `#d95574ff` | night.tdesktop-theme/colors.tdesktop-theme:248 | `#d95574ff` |
| `avatar.peer7NameForeground` | `historyPeer7NameFg` | ui/colors.palette:310 | `#2996adff` | night.tdesktop-theme/colors.tdesktop-theme:236 | `#62d4e3ff` |
| `avatar.peer7NameForegroundSelected` | `historyPeer7NameFgSelected` | ui/colors.palette:311 | `#2996adff` | night.tdesktop-theme/colors.tdesktop-theme:237 | `#ffffffff` |
| `avatar.peer7UserpicBackground` | `historyPeer7UserpicBg` | ui/colors.palette:312 | `#5bcbe3ff` | night.tdesktop-theme/colors.tdesktop-theme:238 | `#5bcbe3ff` |
| `avatar.peer7UserpicBackground2` | `historyPeer7UserpicBg2` | ui/colors.palette:326 | `#359ad4ff` | night.tdesktop-theme/colors.tdesktop-theme:249 | `#359ad4ff` |
| `avatar.peer8NameForeground` | `historyPeer8NameFg` | ui/colors.palette:313 | `#ce671bff` | night.tdesktop-theme/colors.tdesktop-theme:239 | `#faa357ff` |
| `avatar.peer8NameForegroundSelected` | `historyPeer8NameFgSelected` | ui/colors.palette:314 | `#ce671bff` | night.tdesktop-theme/colors.tdesktop-theme:240 | `#ffffffff` |
| `avatar.peer8UserpicBackground` | `historyPeer8UserpicBg` | ui/colors.palette:315 | `#febb5bff` | night.tdesktop-theme/colors.tdesktop-theme:241 | `#febb5bff` |
| `avatar.peer8UserpicBackground2` | `historyPeer8UserpicBg2` | ui/colors.palette:327 | `#f68136ff` | night.tdesktop-theme/colors.tdesktop-theme:250 | `#f68136ff` |
| `avatar.savedMessagesBackground` | `historyPeerSavedMessagesBg` | ui/colors.palette:317 | `#5caffaff` | 继承浅色 / 合并别名 | `#5caffaff` |
| `avatar.savedMessagesBackground2` | `historyPeerSavedMessagesBg2` | ui/colors.palette:328 | `#408acfff` | night.tdesktop-theme/colors.tdesktop-theme:251 | `#408acfff` |
| `avatar.userpicForeground` | `historyPeerUserpicFg` | ui/colors.palette:316 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:242 | `#ffffffff` |
| `call.answerBackground` | `callAnswerBg` | ui/colors.palette:566 | `#66c95bff` | night.tdesktop-theme/colors.tdesktop-theme:434 | `#488fc9ff` |
| `call.answerBackgroundOuter` | `callAnswerBgOuter` | ui/colors.palette:568 | `#50eb4126` | night.tdesktop-theme/colors.tdesktop-theme:436 | `#3f95eb26` |
| `call.answerPressed` | `callAnswerRipple` | ui/colors.palette:567 | `#52b149ff` | night.tdesktop-theme/colors.tdesktop-theme:435 | `#4286c2ff` |
| `call.arrowForeground` | `callArrowFg` | ui/colors.palette:163 | `#2dad2dff` | night.tdesktop-theme/colors.tdesktop-theme:116 | `#49a2f0ff` |
| `call.arrowMissedForeground` | `callArrowMissedFg` | ui/colors.palette:164 | `#dd5b4aff` | night.tdesktop-theme/colors.tdesktop-theme:117 | `#ed5050ff` |
| `call.background` | `callBg` | ui/colors.palette:556 | `#26282cf2` | night.tdesktop-theme/colors.tdesktop-theme:429 | `#14191ff5` |
| `call.backgroundButton` | `callBgButton` | ui/colors.palette:558 | `#1b1f237f` | 继承浅色 / 合并别名 | `#1b1f237f` |
| `call.backgroundOpaque` | `callBgOpaque` | ui/colors.palette:557 | `#1b1f23ff` | 继承浅色 / 合并别名 | `#1b1f23ff` |
| `call.barBackground` | `callBarBg` | ui/colors.palette:604 | `#419fd9ff` | night.tdesktop-theme/colors.tdesktop-theme:443 | `#366693ff` |
| `call.barBackgroundMuted` | `callBarBgMuted` | ui/colors.palette:606 | `#8f8f8fff` | night.tdesktop-theme/colors.tdesktop-theme:445 | `#35495dff` |
| `call.barForeground` | `callBarFg` | ui/colors.palette:607 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:447 | `#ffffffff` |
| `call.barMutePressed` | `callBarMuteRipple` | ui/colors.palette:605 | `#2095d0ff` | night.tdesktop-theme/colors.tdesktop-theme:444 | `#4b7dabff` |
| `call.hangupBackground` | `callHangupBg` | ui/colors.palette:569 | `#d75a5aff` | night.tdesktop-theme/colors.tdesktop-theme:437 | `#cc4646ff` |
| `call.hangupPressed` | `callHangupRipple` | ui/colors.palette:570 | `#c04646ff` | night.tdesktop-theme/colors.tdesktop-theme:438 | `#ca4141ff` |
| `call.iconActivePressed` | `callIconActiveRipple` | ui/colors.palette:565 | `#f1f1f1ff` | 继承浅色 / 合并别名 | `#f1f1f1ff` |
| `call.iconBackground` | `callIconBg` | ui/colors.palette:561 | `#ffffff1f` | 继承浅色 / 合并别名 | `#ffffff1f` |
| `call.iconBackgroundActive` | `callIconBgActive` | ui/colors.palette:563 | `#ffffffe5` | 继承浅色 / 合并别名 | `#ffffffe5` |
| `call.iconForeground` | `callIconFg` | ui/colors.palette:562 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:433 | `#ffffffff` |
| `call.iconForegroundActive` | `callIconFgActive` | ui/colors.palette:564 | `#222222ff` | 继承浅色 / 合并别名 | `#222222ff` |
| `call.mutePressed` | `callMuteRipple` | ui/colors.palette:571 | `#ffffff12` | night.tdesktop-theme/colors.tdesktop-theme:442 | `#ffffff12` |
| `call.nameForeground` | `callNameFg` | ui/colors.palette:559 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:430 | `#ffffffff` |
| `call.statusForeground` | `callStatusFg` | ui/colors.palette:560 | `#aaabacff` | night.tdesktop-theme/colors.tdesktop-theme:432 | `#aaabacff` |
| `compose.areaForegroundService` | `historyComposeAreaFgService` | ui/colors.palette:460 | `#a0acb6ff` | night.tdesktop-theme/colors.tdesktop-theme:360 | `#6d7f8fff` |
| `compose.background` | `historyComposeAreaBg` | ui/colors.palette:458 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:358 | `#17212bff` |
| `compose.buttonBackground` | `historyComposeButtonBg` | ui/colors.palette:471 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:370 | `#17212bff` |
| `compose.buttonBackgroundHover` | `historyComposeButtonBgOver` | ui/colors.palette:472 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:371 | `#1c2835ff` |
| `compose.buttonBackgroundPressed` | `historyComposeButtonBgRipple` | ui/colors.palette:473 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:372 | `#222f3eff` |
| `compose.iconForeground` | `historyComposeIconFg` | ui/colors.palette:461 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:361 | `#6c7883ff` |
| `compose.iconForegroundHover` | `historyComposeIconFgOver` | ui/colors.palette:462 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:362 | `#dcdcdcff` |
| `compose.pinnedBackground` | `historyPinnedBg` | ui/colors.palette:465 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:365 | `#1b2734ff` |
| `compose.placeholder` | `placeholderFg` | ui/colors.palette:73 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:58 | `#6d7883ff` |
| `compose.placeholderFocused` | `placeholderFgActive` | ui/colors.palette:74 | `#aaaaaaff` | night.tdesktop-theme/colors.tdesktop-theme:59 | `#4f5b67ff` |
| `compose.replyBackground` | `historyReplyBg` | ui/colors.palette:466 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:366 | `#17212bff` |
| `compose.replyCancelForeground` | `historyReplyCancelFg` | ui/colors.palette:468 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:368 | `#737e87ff` |
| `compose.replyCancelForegroundHover` | `historyReplyCancelFgOver` | ui/colors.palette:469 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:369 | `#dcdcdcff` |
| `compose.replyIconForeground` | `historyReplyIconFg` | ui/colors.palette:467 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:367 | `#5288c1ff` |
| `compose.sendIconForeground` | `historySendIconFg` | ui/colors.palette:463 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:363 | `#5288c1ff` |
| `compose.sendIconForegroundHover` | `historySendIconFgOver` | ui/colors.palette:464 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:364 | `#5288c1ff` |
| `compose.text` | `historyComposeAreaFg` | ui/colors.palette:459 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:359 | `#f5f5f5ff` |
| `contact.background` | `contactsBg` | ui/colors.palette:153 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:108 | `#17212bff` |
| `contact.backgroundHover` | `contactsBgOver` | ui/colors.palette:154 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:109 | `#1f2c39ff` |
| `contact.nameForeground` | `contactsNameFg` | ui/colors.palette:155 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:110 | `#f5f5f5ff` |
| `contact.statusForeground` | `contactsStatusFg` | ui/colors.palette:156 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:111 | `#677a8bff` |
| `contact.statusForegroundHover` | `contactsStatusFgOver` | ui/colors.palette:157 | `#919191ff` | night.tdesktop-theme/colors.tdesktop-theme:112 | `#677a8bff` |
| `contact.statusForegroundOnline` | `contactsStatusFgOnline` | ui/colors.palette:158 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:113 | `#559be0ff` |
| `control.activeLineForegroundError` | `activeLineFgError` | ui/colors.palette:40 | `#e48383ff` | night.tdesktop-theme/colors.tdesktop-theme:28 | `#ef5959ff` |
| `control.border` | `inputBorderFg` | ui/colors.palette:75 | `#e0e0e0ff` | night.tdesktop-theme/colors.tdesktop-theme:60 | `#313c49ff` |
| `control.cancelIconForeground` | `cancelIconFg` | ui/colors.palette:131 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:96 | `#737e87ff` |
| `control.cancelIconForegroundHover` | `cancelIconFgOver` | ui/colors.palette:132 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:97 | `#dcdcdcff` |
| `control.checkboxForeground` | `checkboxFg` | ui/colors.palette:79 | `#b3b3b3ff` | night.tdesktop-theme/colors.tdesktop-theme:63 | `#4f6276ff` |
| `control.currencyForeground` | `currencyFg` | ui/colors.palette:690 | `#168acdff` | 继承浅色 / 合并别名 | `#168acdff` |
| `control.dangerBackgroundHover` | `attentionButtonBgOver` | ui/colors.palette:50 | `#fcdfdeff` | night.tdesktop-theme/colors.tdesktop-theme:36 | `#592a2a64` |
| `control.dangerBackgroundPressed` | `attentionButtonBgRipple` | ui/colors.palette:51 | `#f4c3c2ff` | night.tdesktop-theme/colors.tdesktop-theme:37 | `#68323264` |
| `control.dangerForeground` | `attentionButtonFg` | ui/colors.palette:48 | `#d14e4eff` | night.tdesktop-theme/colors.tdesktop-theme:34 | `#ec3942ff` |
| `control.dangerForegroundHover` | `attentionButtonFgOver` | ui/colors.palette:49 | `#d14e4eff` | night.tdesktop-theme/colors.tdesktop-theme:35 | `#ec3942ff` |
| `control.filterInputActiveBackground` | `filterInputActiveBg` | ui/colors.palette:77 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:451 | `#242f3dff` |
| `control.filterInputBorderForeground` | `filterInputBorderFg` | ui/colors.palette:76 | `#54c3f3ff` | night.tdesktop-theme/colors.tdesktop-theme:61 | `#242f3dff` |
| `control.filterInputInactiveBackground` | `filterInputInactiveBg` | ui/colors.palette:78 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:62 | `#242f3dff` |
| `control.focus` | `activeLineFg` | ui/colors.palette:39 | `#37a1deff` | night.tdesktop-theme/colors.tdesktop-theme:27 | `#6396cbff` |
| `control.importantTooltipBackground` | `importantTooltipBg` | ui/colors.palette:609 | `#2c3033e5` | night.tdesktop-theme/colors.tdesktop-theme:448 | `#000000b2` |
| `control.importantTooltipForeground` | `importantTooltipFg` | ui/colors.palette:610 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:449 | `#ffffffff` |
| `control.importantTooltipForegroundLink` | `importantTooltipFgLink` | ui/colors.palette:611 | `#4db8ffff` | night.tdesktop-theme/colors.tdesktop-theme:450 | `#65b6fcff` |
| `control.layerBackground` | `layerBg` | ui/colors.palette:129 | `#0000007f` | night.tdesktop-theme/colors.tdesktop-theme:95 | `#0000007f` |
| `control.mapPointDot` | `mapPointDot` | ui/colors.palette:476 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `control.mapPointDrop` | `mapPointDrop` | ui/colors.palette:475 | `#fd4444ff` | 继承浅色 / 合并别名 | `#fd4444ff` |
| `control.membersAboutLimitForeground` | `membersAboutLimitFg` | ui/colors.palette:151 | `#919191ff` | night.tdesktop-theme/colors.tdesktop-theme:107 | `#677a8bff` |
| `control.outdateSoonBackground` | `outdateSoonBg` | ui/colors.palette:614 | `#e08543ff` | 继承浅色 / 合并别名 | `#e08543ff` |
| `control.outdatedBackground` | `outdatedBg` | ui/colors.palette:615 | `#e05745ff` | 继承浅色 / 合并别名 | `#e05745ff` |
| `control.outdatedForeground` | `outdatedFg` | ui/colors.palette:613 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `control.overviewCheckBackground` | `overviewCheckBg` | ui/colors.palette:479 | `#00000040` | night.tdesktop-theme/colors.tdesktop-theme:373 | `#00000040` |
| `control.overviewCheckBackgroundActive` | `overviewCheckBgActive` | ui/colors.palette:480 | `#40a7e3ff` | 继承浅色 / 合并别名 | `#5288c1ff` |
| `control.overviewCheckBorder` | `overviewCheckBorder` | ui/colors.palette:481 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:456 | `#e4eaefff` |
| `control.overviewCheckForegroundActive` | `overviewCheckFgActive` | ui/colors.palette:482 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:375 | `#ffffffff` |
| `control.overviewPhotoSelectOverlay` | `overviewPhotoSelectOverlay` | ui/colors.palette:483 | `#40ace333` | night.tdesktop-theme/colors.tdesktop-theme:376 | `#3fa2e333` |
| `control.paymentsTipActive` | `paymentsTipActive` | ui/colors.palette:149 | `#01ad0fff` | 继承浅色 / 合并别名 | `#01ad0fff` |
| `control.photoCropFadeBackground` | `photoCropFadeBg` | ui/colors.palette:160 | `#0000007f` | night.tdesktop-theme/colors.tdesktop-theme:114 | `#0000007f` |
| `control.photoCropPointForeground` | `photoCropPointFg` | ui/colors.palette:161 | `#ffffff7f` | night.tdesktop-theme/colors.tdesktop-theme:115 | `#ffffff7f` |
| `control.photoEditorItemBaseHandleForeground` | `photoEditorItemBaseHandleFg` | ui/colors.palette:660 | `#3ccaefff` | 继承浅色 / 合并别名 | `#3ccaefff` |
| `control.primaryBackground` | `activeButtonBg` | ui/colors.palette:32 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:20 | `#2f6ea5ff` |
| `control.primaryBackgroundHover` | `activeButtonBgOver` | ui/colors.palette:33 | `#39a5dbff` | night.tdesktop-theme/colors.tdesktop-theme:21 | `#3476abff` |
| `control.primaryBackgroundPressed` | `activeButtonBgRipple` | ui/colors.palette:34 | `#2095d0ff` | night.tdesktop-theme/colors.tdesktop-theme:22 | `#3b7cb1ff` |
| `control.primaryForeground` | `activeButtonFg` | ui/colors.palette:35 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:23 | `#ffffffff` |
| `control.primaryForegroundHover` | `activeButtonFgOver` | ui/colors.palette:36 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:24 | `#ffffffff` |
| `control.primarySecondaryForeground` | `activeButtonSecondaryFg` | ui/colors.palette:37 | `#cceeffff` | night.tdesktop-theme/colors.tdesktop-theme:25 | `#9abfe7ff` |
| `control.primarySecondaryForegroundHover` | `activeButtonSecondaryFgOver` | ui/colors.palette:38 | `#cceeffff` | night.tdesktop-theme/colors.tdesktop-theme:26 | `#9abfe7ff` |
| `control.profileAdminStartForeground` | `profileAdminStartFg` | ui/colors.palette:489 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:380 | `#62a9e6ff` |
| `control.profileStatusForegroundHover` | `profileStatusFgOver` | ui/colors.palette:486 | `#7c99b2ff` | night.tdesktop-theme/colors.tdesktop-theme:377 | `#677a8bff` |
| `control.profileVerifiedCheckBackground` | `profileVerifiedCheckBg` | ui/colors.palette:487 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:378 | `#5288c1ff` |
| `control.profileVerifiedCheckForeground` | `profileVerifiedCheckFg` | ui/colors.palette:488 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:379 | `#ffffffff` |
| `control.radialBackground` | `radialBg` | ui/colors.palette:71 | `#00000056` | night.tdesktop-theme/colors.tdesktop-theme:57 | `#00000056` |
| `control.radialForeground` | `radialFg` | ui/colors.palette:70 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:56 | `#ffffffff` |
| `control.shadowForeground` | `shadowFg` | ui/colors.palette:24 | `#00000018` | night.tdesktop-theme/colors.tdesktop-theme:15 | `#04080e56` |
| `control.slideFadeOutBackground` | `slideFadeOutBg` | ui/colors.palette:25 | `#0000003c` | night.tdesktop-theme/colors.tdesktop-theme:16 | `#0000003c` |
| `control.slideFadeOutShadowForeground` | `slideFadeOutShadowFg` | ui/colors.palette:26 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:17 | `#000000ff` |
| `control.sliderBackgroundActive` | `sliderBgActive` | ui/colors.palette:92 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:65 | `#4a81bcff` |
| `control.sliderBackgroundInactive` | `sliderBgInactive` | ui/colors.palette:91 | `#e1eaefff` | night.tdesktop-theme/colors.tdesktop-theme:64 | `#364554ff` |
| `control.smallCloseIconForeground` | `smallCloseIconFg` | ui/colors.palette:67 | `#c7c7c7ff` | night.tdesktop-theme/colors.tdesktop-theme:54 | `#55616cff` |
| `control.smallCloseIconForegroundHover` | `smallCloseIconFgOver` | ui/colors.palette:68 | `#a3a3a3ff` | night.tdesktop-theme/colors.tdesktop-theme:55 | `#a3a3a3ff` |
| `control.songCoverOverlayForeground` | `songCoverOverlayFg` | ui/colors.palette:658 | `#00000066` | 继承浅色 / 合并别名 | `#00000066` |
| `control.spellUnderline` | `spellUnderline` | ui/colors.palette:618 | `#ff000088` | 继承浅色 / 合并别名 | `#ff000088` |
| `control.surfaceBackground` | `lightButtonBg` | ui/colors.palette:42 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:29 | `#17212bff` |
| `control.surfaceBackgroundHover` | `lightButtonBgOver` | ui/colors.palette:43 | `#e3f1faff` | night.tdesktop-theme/colors.tdesktop-theme:30 | `#1d2a39ff` |
| `control.surfaceBackgroundPressed` | `lightButtonBgRipple` | ui/colors.palette:44 | `#c9e4f6ff` | night.tdesktop-theme/colors.tdesktop-theme:31 | `#223143ff` |
| `control.surfaceForeground` | `lightButtonFg` | ui/colors.palette:45 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:32 | `#6ab2f2ff` |
| `control.surfaceForegroundHover` | `lightButtonFgOver` | ui/colors.palette:46 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:33 | `#6ab2f2ff` |
| `control.trayCounterBackground` | `trayCounterBg` | ui/colors.palette:122 | `#f23c34ff` | night.tdesktop-theme/colors.tdesktop-theme:90 | `#f23c34ff` |
| `control.trayCounterBackgroundMacInvert` | `trayCounterBgMacInvert` | ui/colors.palette:125 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:93 | `#ffffffff` |
| `control.trayCounterBackgroundMute` | `trayCounterBgMute` | ui/colors.palette:123 | `#888888ff` | night.tdesktop-theme/colors.tdesktop-theme:91 | `#888888ff` |
| `control.trayCounterForeground` | `trayCounterFg` | ui/colors.palette:124 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:92 | `#ffffffff` |
| `control.trayCounterForegroundMacInvert` | `trayCounterFgMacInvert` | ui/colors.palette:126 | `#ffffff01` | night.tdesktop-theme/colors.tdesktop-theme:94 | `#ffffff01` |
| `control.videoPlayIconBackground` | `videoPlayIconBg` | ui/colors.palette:446 | `#0000007f` | night.tdesktop-theme/colors.tdesktop-theme:346 | `#0000007f` |
| `control.videoPlayIconForeground` | `videoPlayIconFg` | ui/colors.palette:447 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:347 | `#ffffffff` |
| `control.youtubePlayIconBackground` | `youtubePlayIconBg` | ui/colors.palette:444 | `#e83131c8` | night.tdesktop-theme/colors.tdesktop-theme:344 | `#e83131c8` |
| `control.youtubePlayIconForeground` | `youtubePlayIconFg` | ui/colors.palette:445 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:345 | `#ffffffff` |
| `conversation.archiveForeground` | `dialogsArchiveFg` | ui/colors.palette:197 | `#525252ff` | 继承浅色 / 合并别名 | `#525252ff` |
| `conversation.archiveForegroundHover` | `dialogsArchiveFgOver` | ui/colors.palette:215 | `#525252ff` | 继承浅色 / 合并别名 | `#525252ff` |
| `conversation.background` | `dialogsBg` | ui/colors.palette:183 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:131 | `#17212bff` |
| `conversation.backgroundActive` | `dialogsBgActive` | ui/colors.palette:218 | `#419fd9ff` | night.tdesktop-theme/colors.tdesktop-theme:160 | `#2b5278ff` |
| `conversation.backgroundHover` | `dialogsBgOver` | ui/colors.palette:201 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:145 | `#202b36ff` |
| `conversation.chatIconForeground` | `dialogsChatIconFg` | ui/colors.palette:185 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:133 | `#f5f5f5ff` |
| `conversation.chatIconForegroundActive` | `dialogsChatIconFgActive` | ui/colors.palette:220 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:162 | `#ffffffff` |
| `conversation.chatIconForegroundHover` | `dialogsChatIconFgOver` | ui/colors.palette:203 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:148 | `#e9e9e9ff` |
| `conversation.draftForeground` | `dialogsDraftFg` | ui/colors.palette:189 | `#dd4b39ff` | night.tdesktop-theme/colors.tdesktop-theme:137 | `#ff525dff` |
| `conversation.draftForegroundActive` | `dialogsDraftFgActive` | ui/colors.palette:224 | `#c6e1f7ff` | night.tdesktop-theme/colors.tdesktop-theme:166 | `#a9caf0ff` |
| `conversation.draftForegroundHover` | `dialogsDraftFgOver` | ui/colors.palette:207 | `#dd4b39ff` | night.tdesktop-theme/colors.tdesktop-theme:152 | `#ff525dff` |
| `conversation.mentionIconForeground` | `dialogsMentionIconFg` | ui/colors.palette:696 | `#40a7e3ff` | 继承浅色 / 合并别名 | `#40a7e3ff` |
| `conversation.menuIconForeground` | `dialogsMenuIconFg` | ui/colors.palette:180 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:129 | `#6c7883ff` |
| `conversation.menuIconForegroundHover` | `dialogsMenuIconFgOver` | ui/colors.palette:181 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:130 | `#dcdcdcff` |
| `conversation.nameForeground` | `dialogsNameFg` | ui/colors.palette:184 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:132 | `#f5f5f5ff` |
| `conversation.nameForegroundActive` | `dialogsNameFgActive` | ui/colors.palette:219 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:161 | `#ffffffff` |
| `conversation.nameForegroundHover` | `dialogsNameFgOver` | ui/colors.palette:202 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:147 | `#e9e9e9ff` |
| `conversation.onlineBadgeForeground` | `dialogsOnlineBadgeFg` | ui/colors.palette:198 | `#4dc920ff` | night.tdesktop-theme/colors.tdesktop-theme:146 | `#63b6ffff` |
| `conversation.onlineBadgeForegroundActive` | `dialogsOnlineBadgeFgActive` | ui/colors.palette:232 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:174 | `#ffffffff` |
| `conversation.pollIconForeground` | `dialogsPollIconFg` | ui/colors.palette:698 | `#997be1ff` | 继承浅色 / 合并别名 | `#997be1ff` |
| `conversation.pressedBackground` | `dialogsRippleBg` | ui/colors.palette:235 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:175 | `#25313dff` |
| `conversation.pressedBackgroundActive` | `dialogsRippleBgActive` | ui/colors.palette:236 | `#2095d0ff` | night.tdesktop-theme/colors.tdesktop-theme:176 | `#315a80ff` |
| `conversation.reactionIconForeground` | `dialogsReactionIconFg` | ui/colors.palette:697 | `#e05356ff` | 继承浅色 / 合并别名 | `#e05356ff` |
| `conversation.scamForeground` | `dialogsScamFg` | ui/colors.palette:199 | `#dd4b39ff` | 继承浅色 / 合并别名 | `#ff525dff` |
| `conversation.scamForegroundActive` | `dialogsScamFgActive` | ui/colors.palette:233 | `#c6e1f7ff` | 继承浅色 / 合并别名 | `#a9caf0ff` |
| `conversation.scamForegroundHover` | `dialogsScamFgOver` | ui/colors.palette:216 | `#dd4b39ff` | 继承浅色 / 合并别名 | `#ff525dff` |
| `conversation.sendingIconForeground` | `dialogsSendingIconFg` | ui/colors.palette:192 | `#c1c1c1ff` | night.tdesktop-theme/colors.tdesktop-theme:140 | `#647d96ff` |
| `conversation.sendingIconForegroundActive` | `dialogsSendingIconFgActive` | ui/colors.palette:227 | `#ffffff99` | night.tdesktop-theme/colors.tdesktop-theme:169 | `#ffffff99` |
| `conversation.sendingIconForegroundHover` | `dialogsSendingIconFgOver` | ui/colors.palette:210 | `#c1c1c1ff` | night.tdesktop-theme/colors.tdesktop-theme:155 | `#647d96ff` |
| `conversation.sentIconForeground` | `dialogsSentIconFg` | ui/colors.palette:193 | `#5dc452ff` | night.tdesktop-theme/colors.tdesktop-theme:141 | `#72bcfdff` |
| `conversation.sentIconForegroundActive` | `dialogsSentIconFgActive` | ui/colors.palette:228 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:170 | `#ffffffff` |
| `conversation.sentIconForegroundHover` | `dialogsSentIconFgOver` | ui/colors.palette:211 | `#58b84dff` | night.tdesktop-theme/colors.tdesktop-theme:156 | `#72bcfdff` |
| `conversation.textForeground` | `dialogsTextFg` | ui/colors.palette:187 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:135 | `#7f91a4ff` |
| `conversation.textForegroundActive` | `dialogsTextFgActive` | ui/colors.palette:222 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:164 | `#ffffffff` |
| `conversation.textForegroundHover` | `dialogsTextFgOver` | ui/colors.palette:205 | `#919191ff` | night.tdesktop-theme/colors.tdesktop-theme:150 | `#91a3b5ff` |
| `conversation.textForegroundService` | `dialogsTextFgService` | ui/colors.palette:188 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:136 | `#73b9f5ff` |
| `conversation.textForegroundServiceActive` | `dialogsTextFgServiceActive` | ui/colors.palette:223 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:165 | `#ffffffff` |
| `conversation.textForegroundServiceHover` | `dialogsTextFgServiceOver` | ui/colors.palette:206 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:151 | `#73b9f5ff` |
| `conversation.timestampForeground` | `dialogsDateFg` | ui/colors.palette:186 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:134 | `#8696a8ff` |
| `conversation.timestampForegroundActive` | `dialogsDateFgActive` | ui/colors.palette:221 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:163 | `#ffffffff` |
| `conversation.timestampForegroundHover` | `dialogsDateFgOver` | ui/colors.palette:204 | `#919191ff` | night.tdesktop-theme/colors.tdesktop-theme:149 | `#8495a9ff` |
| `conversation.unreadBackground` | `dialogsUnreadBg` | ui/colors.palette:194 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:142 | `#4082bcff` |
| `conversation.unreadBackgroundActive` | `dialogsUnreadBgActive` | ui/colors.palette:229 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:171 | `#ffffffff` |
| `conversation.unreadBackgroundHover` | `dialogsUnreadBgOver` | ui/colors.palette:212 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:157 | `#4082bcff` |
| `conversation.unreadBackgroundMuted` | `dialogsUnreadBgMuted` | ui/colors.palette:195 | `#bbbbbbff` | night.tdesktop-theme/colors.tdesktop-theme:143 | `#3e546aff` |
| `conversation.unreadBackgroundMutedActive` | `dialogsUnreadBgMutedActive` | ui/colors.palette:230 | `#c6e1f7ff` | night.tdesktop-theme/colors.tdesktop-theme:172 | `#7aa3caff` |
| `conversation.unreadBackgroundMutedHover` | `dialogsUnreadBgMutedOver` | ui/colors.palette:213 | `#bbbbbbff` | night.tdesktop-theme/colors.tdesktop-theme:158 | `#4d5762ff` |
| `conversation.unreadForeground` | `dialogsUnreadFg` | ui/colors.palette:196 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:144 | `#ffffffff` |
| `conversation.unreadForegroundActive` | `dialogsUnreadFgActive` | ui/colors.palette:231 | `#419fd9ff` | night.tdesktop-theme/colors.tdesktop-theme:173 | `#2a5378ff` |
| `conversation.unreadForegroundHover` | `dialogsUnreadFgOver` | ui/colors.palette:214 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:159 | `#ffffffff` |
| `conversation.verifiedIconBackground` | `dialogsVerifiedIconBg` | ui/colors.palette:190 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:138 | `#6ab3f3ff` |
| `conversation.verifiedIconBackgroundActive` | `dialogsVerifiedIconBgActive` | ui/colors.palette:225 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:167 | `#ffffffff` |
| `conversation.verifiedIconBackgroundHover` | `dialogsVerifiedIconBgOver` | ui/colors.palette:208 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:153 | `#6ab3f3ff` |
| `conversation.verifiedIconForeground` | `dialogsVerifiedIconFg` | ui/colors.palette:191 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:139 | `#17212bff` |
| `conversation.verifiedIconForegroundActive` | `dialogsVerifiedIconFgActive` | ui/colors.palette:226 | `#419fd9ff` | night.tdesktop-theme/colors.tdesktop-theme:168 | `#2b5278ff` |
| `conversation.verifiedIconForegroundHover` | `dialogsVerifiedIconFgOver` | ui/colors.palette:209 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:154 | `#17212bff` |
| `credits.background1` | `creditsBg1` | ui/colors.palette:685 | `#ffb222ff` | 继承浅色 / 合并别名 | `#ffb222ff` |
| `credits.background2` | `creditsBg2` | ui/colors.palette:686 | `#ffd951ff` | 继承浅色 / 合并别名 | `#ffd951ff` |
| `credits.background3` | `creditsBg3` | ui/colors.palette:687 | `#f0b400ff` | 继承浅色 / 合并别名 | `#f0b400ff` |
| `credits.foreground` | `creditsFg` | ui/colors.palette:688 | `#ba7000ff` | 继承浅色 / 合并别名 | `#ba7000ff` |
| `credits.stroke` | `creditsStroke` | ui/colors.palette:689 | `#da8735ff` | 继承浅色 / 合并别名 | `#da8735ff` |
| `dialog.background` | `boxBg` | ui/colors.palette:135 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:98 | `#17212bff` |
| `dialog.dividerBackground` | `boxDividerBg` | ui/colors.palette:146 | `#f1f1f1ff` | 继承浅色 / 合并别名 | `#232e3cff` |
| `dialog.dividerForeground` | `boxDividerFg` | ui/colors.palette:147 | `#000000ff` | 继承浅色 / 合并别名 | `#000000ff` |
| `dialog.searchBackground` | `boxSearchBg` | ui/colors.palette:140 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:103 | `#17212bff` |
| `dialog.textForeground` | `boxTextFg` | ui/colors.palette:136 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:99 | `#f5f5f5ff` |
| `dialog.textForegroundError` | `boxTextFgError` | ui/colors.palette:138 | `#d84d4dff` | night.tdesktop-theme/colors.tdesktop-theme:101 | `#dc3d3dff` |
| `dialog.textForegroundGood` | `boxTextFgGood` | ui/colors.palette:137 | `#4ab44aff` | night.tdesktop-theme/colors.tdesktop-theme:100 | `#5598dbff` |
| `dialog.titleAdditionalForeground` | `boxTitleAdditionalFg` | ui/colors.palette:142 | `#808080ff` | night.tdesktop-theme/colors.tdesktop-theme:104 | `#6d7f8fff` |
| `dialog.titleCloseForeground` | `boxTitleCloseFg` | ui/colors.palette:143 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:105 | `#737e87ff` |
| `dialog.titleCloseForegroundHover` | `boxTitleCloseFgOver` | ui/colors.palette:144 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:106 | `#dcdcdcff` |
| `dialog.titleForeground` | `boxTitleFg` | ui/colors.palette:139 | `#404040ff` | night.tdesktop-theme/colors.tdesktop-theme:102 | `#ebebebff` |
| `emoji.iconForeground` | `emojiIconFg` | ui/colors.palette:252 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:454 | `#6c7883ff` |
| `emoji.panBackground` | `emojiPanBg` | ui/colors.palette:248 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:182 | `#17212bff` |
| `emoji.panCategories` | `emojiPanCategories` | ui/colors.palette:249 | `#f7f7f7ff` | night.tdesktop-theme/colors.tdesktop-theme:183 | `#17212bff` |
| `emoji.panHeaderBackground` | `emojiPanHeaderBg` | ui/colors.palette:251 | `#fffffff2` | night.tdesktop-theme/colors.tdesktop-theme:185 | `#fffffff2` |
| `emoji.panHeaderForeground` | `emojiPanHeaderFg` | ui/colors.palette:250 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:184 | `#7c8d9eff` |
| `emoji.subIconForegroundActive` | `emojiSubIconFgActive` | ui/colors.palette:253 | `#666666ff` | 继承浅色 / 合并别名 | `#666666ff` |
| `groupCall.activeForeground` | `groupCallActiveFg` | ui/colors.palette:574 | `#4db8ffff` | 继承浅色 / 合并别名 | `#4db8ffff` |
| `groupCall.background` | `groupCallBg` | ui/colors.palette:573 | `#1a2026ff` | 继承浅色 / 合并别名 | `#1a2026ff` |
| `groupCall.forceMuted1` | `groupCallForceMuted1` | ui/colors.palette:593 | `#4f9cffff` | 继承浅色 / 合并别名 | `#4f9cffff` |
| `groupCall.forceMuted2` | `groupCallForceMuted2` | ui/colors.palette:594 | `#9b52e9ff` | 继承浅色 / 合并别名 | `#9b52e9ff` |
| `groupCall.forceMuted3` | `groupCallForceMuted3` | ui/colors.palette:595 | `#eb5353ff` | 继承浅色 / 合并别名 | `#eb5353ff` |
| `groupCall.forceMutedBar1` | `groupCallForceMutedBar1` | ui/colors.palette:590 | `#c65493ff` | 继承浅色 / 合并别名 | `#c65493ff` |
| `groupCall.forceMutedBar2` | `groupCallForceMutedBar2` | ui/colors.palette:591 | `#7a6af1ff` | 继承浅色 / 合并别名 | `#7a6af1ff` |
| `groupCall.forceMutedBar3` | `groupCallForceMutedBar3` | ui/colors.palette:592 | `#5f95e8ff` | 继承浅色 / 合并别名 | `#5f95e8ff` |
| `groupCall.iconForeground` | `groupCallIconFg` | ui/colors.palette:585 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `groupCall.leaveBackground` | `groupCallLeaveBg` | ui/colors.palette:599 | `#f75c5c7f` | 继承浅色 / 合并别名 | `#f75c5c7f` |
| `groupCall.leaveBackgroundPressed` | `groupCallLeaveBgRipple` | ui/colors.palette:600 | `#f75c5c9e` | 继承浅色 / 合并别名 | `#f75c5c9e` |
| `groupCall.live1` | `groupCallLive1` | ui/colors.palette:586 | `#0dcc39ff` | 继承浅色 / 合并别名 | `#0dcc39ff` |
| `groupCall.live2` | `groupCallLive2` | ui/colors.palette:587 | `#0bb6bdff` | 继承浅色 / 合并别名 | `#0bb6bdff` |
| `groupCall.memberActiveIcon` | `groupCallMemberActiveIcon` | ui/colors.palette:579 | `#8deb90ff` | 继承浅色 / 合并别名 | `#8deb90ff` |
| `groupCall.memberActiveStatus` | `groupCallMemberActiveStatus` | ui/colors.palette:580 | `#8deb90ff` | 继承浅色 / 合并别名 | `#8deb90ff` |
| `groupCall.memberInactiveIcon` | `groupCallMemberInactiveIcon` | ui/colors.palette:581 | `#84888fff` | 继承浅色 / 合并别名 | `#84888fff` |
| `groupCall.memberInactiveStatus` | `groupCallMemberInactiveStatus` | ui/colors.palette:582 | `#61c0ffff` | 继承浅色 / 合并别名 | `#61c0ffff` |
| `groupCall.memberMutedIcon` | `groupCallMemberMutedIcon` | ui/colors.palette:583 | `#ed7372ff` | 继承浅色 / 合并别名 | `#ed7372ff` |
| `groupCall.memberNotJoinedStatus` | `groupCallMemberNotJoinedStatus` | ui/colors.palette:584 | `#91979eff` | 继承浅色 / 合并别名 | `#91979eff` |
| `groupCall.membersBackground` | `groupCallMembersBg` | ui/colors.palette:575 | `#2c333dff` | 继承浅色 / 合并别名 | `#2c333dff` |
| `groupCall.membersBackgroundHover` | `groupCallMembersBgOver` | ui/colors.palette:576 | `#323a45ff` | 继承浅色 / 合并别名 | `#323a45ff` |
| `groupCall.membersBackgroundPressed` | `groupCallMembersBgRipple` | ui/colors.palette:577 | `#39424fff` | 继承浅色 / 合并别名 | `#39424fff` |
| `groupCall.membersForeground` | `groupCallMembersFg` | ui/colors.palette:578 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `groupCall.menuBackground` | `groupCallMenuBg` | ui/colors.palette:596 | `#292d33ff` | 继承浅色 / 合并别名 | `#292d33ff` |
| `groupCall.menuBackgroundHover` | `groupCallMenuBgOver` | ui/colors.palette:597 | `#343940ff` | 继承浅色 / 合并别名 | `#343940ff` |
| `groupCall.menuBackgroundPressed` | `groupCallMenuBgRipple` | ui/colors.palette:598 | `#3a4047ff` | 继承浅色 / 合并别名 | `#3a4047ff` |
| `groupCall.muted1` | `groupCallMuted1` | ui/colors.palette:588 | `#0992efff` | 继承浅色 / 合并别名 | `#0992efff` |
| `groupCall.muted2` | `groupCallMuted2` | ui/colors.palette:589 | `#16ccfbff` | 继承浅色 / 合并别名 | `#16ccfbff` |
| `groupCall.videoSubTextForeground` | `groupCallVideoSubTextFg` | ui/colors.palette:602 | `#ffffffc0` | 继承浅色 / 合并别名 | `#ffffffc0` |
| `groupCall.videoTextForeground` | `groupCallVideoTextFg` | ui/colors.palette:601 | `#ffffffe0` | 继承浅色 / 合并别名 | `#ffffffe0` |
| `header.background` | `topBarBg` | ui/colors.palette:246 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:181 | `#17212bff` |
| `intro.background` | `introBg` | ui/colors.palette:167 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:118 | `#17212bff` |
| `intro.coverBottomBackground` | `introCoverBottomBg` | ui/colors.palette:172 | `#39b0f0ff` | night.tdesktop-theme/colors.tdesktop-theme:123 | `#23659fff` |
| `intro.coverIconsForeground` | `introCoverIconsFg` | ui/colors.palette:173 | `#5ec6ffff` | night.tdesktop-theme/colors.tdesktop-theme:124 | `#3b7cbdff` |
| `intro.coverPlaneInner` | `introCoverPlaneInner` | ui/colors.palette:175 | `#c6d8e8ff` | night.tdesktop-theme/colors.tdesktop-theme:126 | `#ced9e2ff` |
| `intro.coverPlaneOuter` | `introCoverPlaneOuter` | ui/colors.palette:176 | `#a1bed4ff` | night.tdesktop-theme/colors.tdesktop-theme:127 | `#97a9b5ff` |
| `intro.coverPlaneTop` | `introCoverPlaneTop` | ui/colors.palette:177 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:128 | `#ffffffff` |
| `intro.coverPlaneTrace` | `introCoverPlaneTrace` | ui/colors.palette:174 | `#5ec6ff69` | night.tdesktop-theme/colors.tdesktop-theme:125 | `#3870a8ff` |
| `intro.coverTopBackground` | `introCoverTopBg` | ui/colors.palette:171 | `#0f89d0ff` | night.tdesktop-theme/colors.tdesktop-theme:122 | `#124a82ff` |
| `intro.descriptionForeground` | `introDescriptionFg` | ui/colors.palette:169 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:120 | `#72879bff` |
| `intro.titleForeground` | `introTitleFg` | ui/colors.palette:168 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:119 | `#eeeeeeff` |
| `keyboard.background` | `botKbBg` | ui/colors.palette:81 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:452 | `#242f3dff` |
| `keyboard.color` | `botKbColor` | ui/colors.palette:83 | `#222222ff` | 继承浅色 / 合并别名 | `#e9e9e9ff` |
| `keyboard.dangerBackground` | `botKbDangerBg` | ui/colors.palette:85 | `#e05356cc` | 继承浅色 / 合并别名 | `#e05356cc` |
| `keyboard.downBackground` | `botKbDownBg` | ui/colors.palette:82 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:453 | `#2d3a49ff` |
| `keyboard.inlineDangerBackground` | `botKbInlineDangerBg` | ui/colors.palette:88 | `#c9543eb3` | 继承浅色 / 合并别名 | `#c9543eb3` |
| `keyboard.inlinePrimaryBackground` | `botKbInlinePrimaryBg` | ui/colors.palette:87 | `#378eaeb3` | 继承浅色 / 合并别名 | `#378eaeb3` |
| `keyboard.inlineSuccessBackground` | `botKbInlineSuccessBg` | ui/colors.palette:89 | `#489d38b3` | 继承浅色 / 合并别名 | `#489d38b3` |
| `keyboard.primaryBackground` | `botKbPrimaryBg` | ui/colors.palette:84 | `#298acfcc` | 继承浅色 / 合并别名 | `#298acfcc` |
| `keyboard.successBackground` | `botKbSuccessBg` | ui/colors.palette:86 | `#61c752cc` | 继承浅色 / 合并别名 | `#61c752cc` |
| `link.text` | `windowActiveTextFg` | ui/colors.palette:20 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:12 | `#6ab3f3ff` |
| `media.imageBackground` | `imageBg` | ui/colors.palette:28 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:18 | `#000000ff` |
| `media.imageBackgroundTransparent` | `imageBgTransparent` | ui/colors.palette:29 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:19 | `#ffffffff` |
| `media.inForeground` | `mediaInFg` | ui/colors.palette:439 | `#a0acb6ff` | night.tdesktop-theme/colors.tdesktop-theme:340 | `#6d7f8fff` |
| `media.inForegroundSelected` | `mediaInFgSelected` | ui/colors.palette:440 | `#6a9cc5ff` | night.tdesktop-theme/colors.tdesktop-theme:341 | `#ffffffff` |
| `media.outForeground` | `mediaOutFg` | ui/colors.palette:441 | `#6db566ff` | night.tdesktop-theme/colors.tdesktop-theme:342 | `#7da8d3ff` |
| `media.outForegroundSelected` | `mediaOutFgSelected` | ui/colors.palette:442 | `#56b2a6ff` | night.tdesktop-theme/colors.tdesktop-theme:343 | `#ffffffff` |
| `menu.background` | `menuBg` | ui/colors.palette:53 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:42 | `#17212bff` |
| `menu.backgroundHover` | `menuBgOver` | ui/colors.palette:54 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:43 | `#ffffffff` |
| `menu.backgroundPressed` | `menuBgRipple` | ui/colors.palette:55 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:44 | `#1f2934ff` |
| `menu.foregroundDisabled` | `menuFgDisabled` | ui/colors.palette:59 | `#ccccccff` | night.tdesktop-theme/colors.tdesktop-theme:48 | `#3d4e5cff` |
| `menu.iconForeground` | `menuIconFg` | ui/colors.palette:56 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:45 | `#6c7883ff` |
| `menu.iconForegroundHover` | `menuIconFgOver` | ui/colors.palette:57 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:46 | `#dcdcdcff` |
| `menu.separatorForeground` | `menuSeparatorFg` | ui/colors.palette:60 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:49 | `#232f39ff` |
| `menu.submenuArrowForeground` | `menuSubmenuArrowFg` | ui/colors.palette:58 | `#373737ff` | night.tdesktop-theme/colors.tdesktop-theme:47 | `#637c95ff` |
| `message.botKbHoverBackgroundAdd` | `msgBotKbOverBgAdd` | ui/colors.palette:435 | `#ffffff20` | night.tdesktop-theme/colors.tdesktop-theme:337 | `#80b1db0f` |
| `message.botKbIconForeground` | `msgBotKbIconFg` | ui/colors.palette:436 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:338 | `#ffffffff` |
| `message.botKbPressedBackground` | `msgBotKbRippleBg` | ui/colors.palette:437 | `#00000020` | night.tdesktop-theme/colors.tdesktop-theme:339 | `#92c0e50b` |
| `message.callArrowInForeground` | `historyCallArrowInFg` | ui/colors.palette:278 | `#32b032ff` | night.tdesktop-theme/colors.tdesktop-theme:207 | `#5093d6ff` |
| `message.callArrowInForegroundSelected` | `historyCallArrowInFgSelected` | ui/colors.palette:279 | `#2592a8ff` | night.tdesktop-theme/colors.tdesktop-theme:208 | `#ffffffff` |
| `message.callArrowMissedInForeground` | `historyCallArrowMissedInFg` | ui/colors.palette:280 | `#dd5b4aff` | night.tdesktop-theme/colors.tdesktop-theme:209 | `#ed5050ff` |
| `message.callArrowMissedInForegroundSelected` | `historyCallArrowMissedInFgSelected` | ui/colors.palette:281 | `#dd5b4aff` | night.tdesktop-theme/colors.tdesktop-theme:210 | `#ffffffff` |
| `message.callArrowOutForeground` | `historyCallArrowOutFg` | ui/colors.palette:282 | `#32b032ff` | night.tdesktop-theme/colors.tdesktop-theme:211 | `#ffffffff` |
| `message.callArrowOutForegroundSelected` | `historyCallArrowOutFgSelected` | ui/colors.palette:283 | `#2592a8ff` | night.tdesktop-theme/colors.tdesktop-theme:212 | `#ffffffff` |
| `message.file1Background` | `msgFile1Bg` | ui/colors.palette:394 | `#72b1dfff` | night.tdesktop-theme/colors.tdesktop-theme:300 | `#3e7ebaff` |
| `message.file1BackgroundDark` | `msgFile1BgDark` | ui/colors.palette:395 | `#5c9eceff` | night.tdesktop-theme/colors.tdesktop-theme:301 | `#24679eff` |
| `message.file1BackgroundHover` | `msgFile1BgOver` | ui/colors.palette:396 | `#5294c4ff` | night.tdesktop-theme/colors.tdesktop-theme:302 | `#1d5e93ff` |
| `message.file1BackgroundSelected` | `msgFile1BgSelected` | ui/colors.palette:397 | `#5099d0ff` | night.tdesktop-theme/colors.tdesktop-theme:303 | `#ffffffff` |
| `message.file2Background` | `msgFile2Bg` | ui/colors.palette:398 | `#5fbe67ff` | night.tdesktop-theme/colors.tdesktop-theme:304 | `#3ea34aff` |
| `message.file2BackgroundDark` | `msgFile2BgDark` | ui/colors.palette:399 | `#4da859ff` | night.tdesktop-theme/colors.tdesktop-theme:305 | `#298835ff` |
| `message.file2BackgroundHover` | `msgFile2BgOver` | ui/colors.palette:400 | `#44a050ff` | night.tdesktop-theme/colors.tdesktop-theme:306 | `#1b7725ff` |
| `message.file2BackgroundSelected` | `msgFile2BgSelected` | ui/colors.palette:401 | `#50ac9bff` | night.tdesktop-theme/colors.tdesktop-theme:307 | `#1b7725ff` |
| `message.file3Background` | `msgFile3Bg` | ui/colors.palette:402 | `#e47272ff` | night.tdesktop-theme/colors.tdesktop-theme:308 | `#d6454cff` |
| `message.file3BackgroundDark` | `msgFile3BgDark` | ui/colors.palette:403 | `#cd5b5eff` | night.tdesktop-theme/colors.tdesktop-theme:309 | `#bf333aff` |
| `message.file3BackgroundHover` | `msgFile3BgOver` | ui/colors.palette:404 | `#c35154ff` | night.tdesktop-theme/colors.tdesktop-theme:310 | `#b2282fff` |
| `message.file3BackgroundSelected` | `msgFile3BgSelected` | ui/colors.palette:405 | `#9f6a82ff` | night.tdesktop-theme/colors.tdesktop-theme:311 | `#b2282fff` |
| `message.file4Background` | `msgFile4Bg` | ui/colors.palette:406 | `#efc274ff` | night.tdesktop-theme/colors.tdesktop-theme:312 | `#d99546ff` |
| `message.file4BackgroundDark` | `msgFile4BgDark` | ui/colors.palette:407 | `#e6a561ff` | night.tdesktop-theme/colors.tdesktop-theme:313 | `#c17d39ff` |
| `message.file4BackgroundHover` | `msgFile4BgOver` | ui/colors.palette:408 | `#dc9c5aff` | night.tdesktop-theme/colors.tdesktop-theme:314 | `#ac6b29ff` |
| `message.file4BackgroundSelected` | `msgFile4BgSelected` | ui/colors.palette:409 | `#b19d84ff` | night.tdesktop-theme/colors.tdesktop-theme:315 | `#ac6b29ff` |
| `message.fileInBackground` | `msgFileInBg` | ui/colors.palette:388 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:294 | `#3f96d0ff` |
| `message.fileInBackgroundHover` | `msgFileInBgOver` | ui/colors.palette:389 | `#4eade3ff` | night.tdesktop-theme/colors.tdesktop-theme:295 | `#489ed7ff` |
| `message.fileInBackgroundSelected` | `msgFileInBgSelected` | ui/colors.palette:390 | `#51a3d3ff` | night.tdesktop-theme/colors.tdesktop-theme:296 | `#6ab4f4ff` |
| `message.fileInIconForeground` | `historyFileInIconFg` | ui/colors.palette:411 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:316 | `#ffffffff` |
| `message.fileInIconForegroundSelected` | `historyFileInIconFgSelected` | ui/colors.palette:412 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:317 | `#ffffffff` |
| `message.fileInRadialForeground` | `historyFileInRadialFg` | ui/colors.palette:413 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:318 | `#ffffffff` |
| `message.fileInRadialForegroundSelected` | `historyFileInRadialFgSelected` | ui/colors.palette:414 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:319 | `#ffffffff` |
| `message.fileNameInForeground` | `historyFileNameInFg` | ui/colors.palette:268 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:197 | `#f5f5f5ff` |
| `message.fileNameInForegroundSelected` | `historyFileNameInFgSelected` | ui/colors.palette:269 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:198 | `#ffffffff` |
| `message.fileNameOutForeground` | `historyFileNameOutFg` | ui/colors.palette:270 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:199 | `#e4ecf2ff` |
| `message.fileNameOutForegroundSelected` | `historyFileNameOutFgSelected` | ui/colors.palette:271 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:200 | `#ffffffff` |
| `message.fileOutBackground` | `msgFileOutBg` | ui/colors.palette:391 | `#5fbe67ff` | night.tdesktop-theme/colors.tdesktop-theme:297 | `#4c9ce2ff` |
| `message.fileOutBackgroundSelected` | `msgFileOutBgSelected` | ui/colors.palette:392 | `#50ac9bff` | night.tdesktop-theme/colors.tdesktop-theme:299 | `#58abf3ff` |
| `message.fileOutIconForeground` | `historyFileOutIconFg` | ui/colors.palette:415 | `#effddeff` | night.tdesktop-theme/colors.tdesktop-theme:320 | `#ffffffff` |
| `message.fileOutIconForegroundSelected` | `historyFileOutIconFgSelected` | ui/colors.palette:416 | `#b7dbdbff` | night.tdesktop-theme/colors.tdesktop-theme:321 | `#ffffffff` |
| `message.fileOutRadialForeground` | `historyFileOutRadialFg` | ui/colors.palette:417 | `#effddeff` | night.tdesktop-theme/colors.tdesktop-theme:322 | `#ffffffff` |
| `message.fileOutRadialForegroundSelected` | `historyFileOutRadialFgSelected` | ui/colors.palette:418 | `#b7dbdbff` | night.tdesktop-theme/colors.tdesktop-theme:323 | `#ffffffff` |
| `message.fileThumbIconForeground` | `historyFileThumbIconFg` | ui/colors.palette:419 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:324 | `#efefefff` |
| `message.fileThumbIconForegroundSelected` | `historyFileThumbIconFgSelected` | ui/colors.palette:420 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:325 | `#ffffffff` |
| `message.fileThumbLinkInForeground` | `msgFileThumbLinkInFg` | ui/colors.palette:384 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:290 | `#6ab2f2ff` |
| `message.fileThumbLinkInForegroundSelected` | `msgFileThumbLinkInFgSelected` | ui/colors.palette:385 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:291 | `#ffffffff` |
| `message.fileThumbLinkOutForeground` | `msgFileThumbLinkOutFg` | ui/colors.palette:386 | `#4ba831ff` | night.tdesktop-theme/colors.tdesktop-theme:292 | `#6c9ecfff` |
| `message.fileThumbLinkOutForegroundSelected` | `msgFileThumbLinkOutFgSelected` | ui/colors.palette:387 | `#31a298ff` | night.tdesktop-theme/colors.tdesktop-theme:293 | `#ffffffff` |
| `message.fileThumbRadialForeground` | `historyFileThumbRadialFg` | ui/colors.palette:421 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:326 | `#efefefff` |
| `message.fileThumbRadialForegroundSelected` | `historyFileThumbRadialFgSelected` | ui/colors.palette:422 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:327 | `#ffffffff` |
| `message.forwardChooseBackground` | `historyForwardChooseBg` | ui/colors.palette:289 | `#0000004c` | night.tdesktop-theme/colors.tdesktop-theme:216 | `#0000004c` |
| `message.forwardChooseForeground` | `historyForwardChooseFg` | ui/colors.palette:290 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:217 | `#ffffffff` |
| `message.iconForegroundInverted` | `historyIconFgInverted` | ui/colors.palette:274 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:203 | `#ffffffe5` |
| `message.in.background` | `msgInBg` | ui/colors.palette:349 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:256 | `#182533ff` |
| `message.in.backgroundSelected` | `msgInBgSelected` | ui/colors.palette:350 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:257 | `#2e70a5ff` |
| `message.in.codeForeground` | `msgInMonoFg` | ui/colors.palette:375 | `#4e7391ff` | night.tdesktop-theme/colors.tdesktop-theme:282 | `#5a8cb7ff` |
| `message.in.codeForegroundSelected` | `msgInMonoFgSelected` | ui/colors.palette:377 | `#4e7391ff` | night.tdesktop-theme/colors.tdesktop-theme:284 | `#a3cdf7ff` |
| `message.in.link` | `historyLinkInFg` | ui/colors.palette:264 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:193 | `#70baf5ff` |
| `message.in.linkSelected` | `historyLinkInFgSelected` | ui/colors.palette:265 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:194 | `#aadcffff` |
| `message.in.serviceForeground` | `msgInServiceFg` | ui/colors.palette:355 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:262 | `#71bafaff` |
| `message.in.serviceForegroundSelected` | `msgInServiceFgSelected` | ui/colors.palette:356 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:263 | `#ffffffff` |
| `message.in.shadow` | `msgInShadow` | ui/colors.palette:359 | `#748ea229` | night.tdesktop-theme/colors.tdesktop-theme:266 | `#748ea200` |
| `message.in.shadowSelected` | `msgInShadowSelected` | ui/colors.palette:360 | `#548dbb29` | night.tdesktop-theme/colors.tdesktop-theme:267 | `#538ebb00` |
| `message.in.text` | `historyTextInFg` | ui/colors.palette:260 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:189 | `#f5f5f5ff` |
| `message.in.textSelected` | `historyTextInFgSelected` | ui/colors.palette:261 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:190 | `#ffffffff` |
| `message.in.timestampForeground` | `msgInDateFg` | ui/colors.palette:363 | `#a0acb6ff` | night.tdesktop-theme/colors.tdesktop-theme:270 | `#6d7f8fff` |
| `message.in.timestampForegroundSelected` | `msgInDateFgSelected` | ui/colors.palette:364 | `#6a9cc5ff` | night.tdesktop-theme/colors.tdesktop-theme:271 | `#ffffffff` |
| `message.out.background` | `msgOutBg` | ui/colors.palette:351 | `#effddeff` | night.tdesktop-theme/colors.tdesktop-theme:258 | `#2b5278ff` |
| `message.out.backgroundSelected` | `msgOutBgSelected` | ui/colors.palette:352 | `#b7dbdbff` | night.tdesktop-theme/colors.tdesktop-theme:259 | `#2e70a5ff` |
| `message.out.codeForeground` | `msgOutMonoFg` | ui/colors.palette:376 | `#459866ff` | night.tdesktop-theme/colors.tdesktop-theme:283 | `#aed1f3ff` |
| `message.out.codeForegroundSelected` | `msgOutMonoFgSelected` | ui/colors.palette:378 | `#459866ff` | night.tdesktop-theme/colors.tdesktop-theme:285 | `#a3cdf7ff` |
| `message.out.link` | `historyLinkOutFg` | ui/colors.palette:266 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:195 | `#83caffff` |
| `message.out.linkSelected` | `historyLinkOutFgSelected` | ui/colors.palette:267 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:196 | `#aadcffff` |
| `message.out.serviceForeground` | `msgOutServiceFg` | ui/colors.palette:357 | `#45a32dff` | night.tdesktop-theme/colors.tdesktop-theme:264 | `#90caffff` |
| `message.out.serviceForegroundSelected` | `msgOutServiceFgSelected` | ui/colors.palette:358 | `#469992ff` | night.tdesktop-theme/colors.tdesktop-theme:265 | `#ffffffff` |
| `message.out.shadow` | `msgOutShadow` | ui/colors.palette:361 | `#3ac3461d` | night.tdesktop-theme/colors.tdesktop-theme:268 | `#00000000` |
| `message.out.shadowSelected` | `msgOutShadowSelected` | ui/colors.palette:362 | `#37a78d22` | night.tdesktop-theme/colors.tdesktop-theme:269 | `#366ea600` |
| `message.out.text` | `historyTextOutFg` | ui/colors.palette:262 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:191 | `#e4ecf2ff` |
| `message.out.textSelected` | `historyTextOutFgSelected` | ui/colors.palette:263 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:192 | `#ffffffff` |
| `message.out.timestampForeground` | `msgOutDateFg` | ui/colors.palette:365 | `#6db566ff` | night.tdesktop-theme/colors.tdesktop-theme:272 | `#7da8d3ff` |
| `message.out.timestampForegroundSelected` | `msgOutDateFgSelected` | ui/colors.palette:366 | `#56b2a6ff` | night.tdesktop-theme/colors.tdesktop-theme:273 | `#ffffffff` |
| `message.outIconForeground` | `historyOutIconFg` | ui/colors.palette:272 | `#57b84cff` | night.tdesktop-theme/colors.tdesktop-theme:201 | `#6bbfffff` |
| `message.outIconForegroundSelected` | `historyOutIconFgSelected` | ui/colors.palette:273 | `#45a3aaff` | night.tdesktop-theme/colors.tdesktop-theme:202 | `#ffffffff` |
| `message.selectOverlay` | `msgSelectOverlay` | ui/colors.palette:353 | `#358cd44c` | night.tdesktop-theme/colors.tdesktop-theme:260 | `#3585d44c` |
| `message.sendingInIconForeground` | `historySendingInIconFg` | ui/colors.palette:276 | `#a0adb5ff` | night.tdesktop-theme/colors.tdesktop-theme:205 | `#76838bff` |
| `message.sendingInvertedIconForeground` | `historySendingInvertedIconFg` | ui/colors.palette:277 | `#ffffffc8` | night.tdesktop-theme/colors.tdesktop-theme:206 | `#ffffffc8` |
| `message.sendingOutIconForeground` | `historySendingOutIconFg` | ui/colors.palette:275 | `#98d292ff` | night.tdesktop-theme/colors.tdesktop-theme:204 | `#70a4d2ff` |
| `message.stickerOverlay` | `msgStickerOverlay` | ui/colors.palette:354 | `#358cd47f` | night.tdesktop-theme/colors.tdesktop-theme:261 | `#3585d47f` |
| `message.timestampImgBackground` | `msgDateImgBg` | ui/colors.palette:380 | `#00000054` | night.tdesktop-theme/colors.tdesktop-theme:287 | `#00000054` |
| `message.timestampImgBackgroundHover` | `msgDateImgBgOver` | ui/colors.palette:381 | `#00000074` | night.tdesktop-theme/colors.tdesktop-theme:288 | `#00000074` |
| `message.timestampImgBackgroundSelected` | `msgDateImgBgSelected` | ui/colors.palette:382 | `#1c4a7187` | night.tdesktop-theme/colors.tdesktop-theme:289 | `#204f7887` |
| `message.timestampImgForeground` | `msgDateImgFg` | ui/colors.palette:379 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:286 | `#fffffff2` |
| `message.videoMessageProgressForeground` | `historyVideoMessageProgressFg` | ui/colors.palette:424 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:328 | `#efefefff` |
| `message.waveformInActive` | `msgWaveformInActive` | ui/colors.palette:426 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:329 | `#549cd7ff` |
| `message.waveformInActiveSelected` | `msgWaveformInActiveSelected` | ui/colors.palette:427 | `#51a3d3ff` | night.tdesktop-theme/colors.tdesktop-theme:330 | `#ffffffff` |
| `message.waveformInInactive` | `msgWaveformInInactive` | ui/colors.palette:428 | `#d4dee6ff` | night.tdesktop-theme/colors.tdesktop-theme:331 | `#3a4d61ff` |
| `message.waveformInInactiveSelected` | `msgWaveformInInactiveSelected` | ui/colors.palette:429 | `#9cc1e1ff` | night.tdesktop-theme/colors.tdesktop-theme:332 | `#6fa5d4ff` |
| `message.waveformOutActive` | `msgWaveformOutActive` | ui/colors.palette:430 | `#5ebd66ff` | night.tdesktop-theme/colors.tdesktop-theme:333 | `#62b2fdff` |
| `message.waveformOutActiveSelected` | `msgWaveformOutActiveSelected` | ui/colors.palette:431 | `#6badadff` | night.tdesktop-theme/colors.tdesktop-theme:334 | `#ffffffff` |
| `message.waveformOutInactive` | `msgWaveformOutInactive` | ui/colors.palette:432 | `#b3e2b4ff` | night.tdesktop-theme/colors.tdesktop-theme:335 | `#4b7fb3ff` |
| `message.waveformOutInactiveSelected` | `msgWaveformOutInactiveSelected` | ui/colors.palette:433 | `#91c3c3ff` | night.tdesktop-theme/colors.tdesktop-theme:336 | `#6fa5d4ff` |
| `navigation.mainMenuBackground` | `mainMenuBg` | ui/colors.palette:500 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:389 | `#17212bff` |
| `navigation.mainMenuCloudBackground` | `mainMenuCloudBg` | ui/colors.palette:503 | `#2785bfff` | night.tdesktop-theme/colors.tdesktop-theme:393 | `#185680ff` |
| `navigation.mainMenuCloudForeground` | `mainMenuCloudFg` | ui/colors.palette:502 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:392 | `#ffffffff` |
| `navigation.mainMenuCoverBackground` | `mainMenuCoverBg` | ui/colors.palette:501 | `#419fd9ff` | night.tdesktop-theme/colors.tdesktop-theme:390 | `#276899ff` |
| `navigation.toLatestBackground` | `historyToDownBg` | ui/colors.palette:451 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:352 | `#1d2b3aff` |
| `navigation.toLatestBackgroundHover` | `historyToDownBgOver` | ui/colors.palette:452 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:353 | `#243446ff` |
| `navigation.toLatestBackgroundPressed` | `historyToDownBgRipple` | ui/colors.palette:453 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:354 | `#2b3c4fff` |
| `navigation.toLatestForeground` | `historyToDownFg` | ui/colors.palette:454 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:355 | `#adb4baff` |
| `navigation.toLatestForegroundHover` | `historyToDownFgOver` | ui/colors.palette:455 | `#8a8a8aff` | night.tdesktop-theme/colors.tdesktop-theme:356 | `#dcdcdcff` |
| `navigation.toLatestShadow` | `historyToDownShadow` | ui/colors.palette:456 | `#00000040` | night.tdesktop-theme/colors.tdesktop-theme:357 | `#00000040` |
| `notification.background` | `notificationBg` | ui/colors.palette:553 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:428 | `#17212bff` |
| `notification.sampleCloseForeground` | `notificationSampleCloseFg` | ui/colors.palette:496 | `#d7d7d7ff` | night.tdesktop-theme/colors.tdesktop-theme:384 | `#d7d7d7ff` |
| `notification.sampleNameForeground` | `notificationSampleNameFg` | ui/colors.palette:498 | `#939393ff` | night.tdesktop-theme/colors.tdesktop-theme:386 | `#939393ff` |
| `notification.sampleTextForeground` | `notificationSampleTextFg` | ui/colors.palette:497 | `#d7d7d7ff` | night.tdesktop-theme/colors.tdesktop-theme:385 | `#d7d7d7ff` |
| `notification.sampleUserpicForeground` | `notificationSampleUserpicFg` | ui/colors.palette:495 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:383 | `#5288c1ff` |
| `notification.settingsMonitorForeground` | `notificationsBoxMonitorFg` | ui/colors.palette:492 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:381 | `#f5f5f5ff` |
| `notification.settingsScreenBackground` | `notificationsBoxScreenBg` | ui/colors.palette:493 | `#419fd9ff` | night.tdesktop-theme/colors.tdesktop-theme:382 | `#2b5278ff` |
| `player.activeForeground` | `mediaPlayerActiveFg` | ui/colors.palette:506 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:395 | `#5288c1ff` |
| `player.background` | `mediaPlayerBg` | ui/colors.palette:505 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:394 | `#17212bff` |
| `player.disabledForeground` | `mediaPlayerDisabledFg` | ui/colors.palette:508 | `#9dd1efff` | night.tdesktop-theme/colors.tdesktop-theme:397 | `#9dd1efff` |
| `player.inactiveForeground` | `mediaPlayerInactiveFg` | ui/colors.palette:507 | `#e1eaefff` | night.tdesktop-theme/colors.tdesktop-theme:396 | `#364554ff` |
| `premium.buttonBackground1` | `premiumButtonBg1` | ui/colors.palette:662 | `#55a5ffff` | 继承浅色 / 合并别名 | `#55a5ffff` |
| `premium.buttonBackground2` | `premiumButtonBg2` | ui/colors.palette:663 | `#a767ffff` | 继承浅色 / 合并别名 | `#a767ffff` |
| `premium.buttonBackground3` | `premiumButtonBg3` | ui/colors.palette:664 | `#db5c9dff` | 继承浅色 / 合并别名 | `#db5c9dff` |
| `premium.buttonForeground` | `premiumButtonFg` | ui/colors.palette:665 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `premium.iconBackground1` | `premiumIconBg1` | ui/colors.palette:667 | `#f38926ff` | 继承浅色 / 合并别名 | `#f38926ff` |
| `premium.iconBackground2` | `premiumIconBg2` | ui/colors.palette:668 | `#e44456ff` | 继承浅色 / 合并别名 | `#e44456ff` |
| `premium.iconBackground3` | `premiumIconBg3` | ui/colors.palette:669 | `#4acd43ff` | 继承浅色 / 合并别名 | `#4acd43ff` |
| `quote.codeDarkBackground` | `imageBg` | ui/colors.palette:28 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:18 | `#000000ff` |
| `quote.in.background` | `msgInReplyBarColor` | ui/colors.palette:370 | `#37a1deff` | night.tdesktop-theme/colors.tdesktop-theme:277 | `#429bdbff` |
| `quote.in.bar` | `msgInReplyBarColor` | ui/colors.palette:370 | `#37a1deff` | night.tdesktop-theme/colors.tdesktop-theme:277 | `#429bdbff` |
| `quote.in.barSelected` | `msgInReplyBarSelColor` | ui/colors.palette:371 | `#37a1deff` | night.tdesktop-theme/colors.tdesktop-theme:278 | `#ffffffff` |
| `quote.in.name` | `msgInServiceFg` | ui/colors.palette:355 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:262 | `#71bafaff` |
| `quote.in.summary` | `historyTextInFg` | ui/colors.palette:260 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:189 | `#f5f5f5ff` |
| `quote.media.bar` | `msgImgReplyBarColor` | ui/colors.palette:374 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:281 | `#ffffffff` |
| `quote.out.background` | `msgOutReplyBarColor` | ui/colors.palette:372 | `#5eb854ff` | night.tdesktop-theme/colors.tdesktop-theme:279 | `#65b9f4ff` |
| `quote.out.bar` | `msgOutReplyBarColor` | ui/colors.palette:372 | `#5eb854ff` | night.tdesktop-theme/colors.tdesktop-theme:279 | `#65b9f4ff` |
| `quote.out.barSelected` | `msgOutReplyBarSelColor` | ui/colors.palette:373 | `#45a3aaff` | night.tdesktop-theme/colors.tdesktop-theme:280 | `#ffffffff` |
| `quote.out.name` | `msgOutServiceFg` | ui/colors.palette:357 | `#45a32dff` | night.tdesktop-theme/colors.tdesktop-theme:264 | `#90caffff` |
| `quote.out.summary` | `historyTextOutFg` | ui/colors.palette:262 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:191 | `#e4ecf2ff` |
| `rank.adminForeground` | `rankAdminFg` | ui/colors.palette:692 | `#49a355ff` | 继承浅色 / 合并别名 | `#49a355ff` |
| `rank.ownerForeground` | `rankOwnerFg` | ui/colors.palette:693 | `#956ac8ff` | 继承浅色 / 合并别名 | `#956ac8ff` |
| `rank.userForeground` | `rankUserFg` | ui/colors.palette:694 | `#999999ff` | 继承浅色 / 合并别名 | `#708499ff` |
| `reaction.in.background` | `msgFileInBg` | ui/colors.palette:388 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:294 | `#3f96d0ff` |
| `reaction.in.ownBackground` | `msgFileInBg` | ui/colors.palette:388 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:294 | `#3f96d0ff` |
| `reaction.in.ownText` | `historyFileInIconFg` | ui/colors.palette:411 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:316 | `#ffffffff` |
| `reaction.in.selectedBackground` | `msgFileInBgSelected` | ui/colors.palette:390 | `#51a3d3ff` | night.tdesktop-theme/colors.tdesktop-theme:296 | `#6ab4f4ff` |
| `reaction.in.selectedOwnText` | `historyFileInIconFgSelected` | ui/colors.palette:412 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:317 | `#ffffffff` |
| `reaction.in.selectedText` | `msgInServiceFgSelected` | ui/colors.palette:356 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:263 | `#ffffffff` |
| `reaction.in.text` | `msgInServiceFg` | ui/colors.palette:355 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:262 | `#71bafaff` |
| `reaction.out.background` | `msgFileOutBg` | ui/colors.palette:391 | `#5fbe67ff` | night.tdesktop-theme/colors.tdesktop-theme:297 | `#4c9ce2ff` |
| `reaction.out.ownBackground` | `msgFileOutBg` | ui/colors.palette:391 | `#5fbe67ff` | night.tdesktop-theme/colors.tdesktop-theme:297 | `#4c9ce2ff` |
| `reaction.out.ownText` | `historyFileOutIconFg` | ui/colors.palette:415 | `#effddeff` | night.tdesktop-theme/colors.tdesktop-theme:320 | `#ffffffff` |
| `reaction.out.selectedBackground` | `msgFileOutBgSelected` | ui/colors.palette:392 | `#50ac9bff` | night.tdesktop-theme/colors.tdesktop-theme:299 | `#58abf3ff` |
| `reaction.out.selectedOwnText` | `historyFileOutIconFgSelected` | ui/colors.palette:416 | `#b7dbdbff` | night.tdesktop-theme/colors.tdesktop-theme:321 | `#ffffffff` |
| `reaction.out.selectedText` | `msgOutServiceFgSelected` | ui/colors.palette:358 | `#469992ff` | night.tdesktop-theme/colors.tdesktop-theme:265 | `#ffffffff` |
| `reaction.out.text` | `msgOutServiceFg` | ui/colors.palette:357 | `#45a32dff` | night.tdesktop-theme/colors.tdesktop-theme:264 | `#90caffff` |
| `scrollbar.chatThumbBackground` | `historyScrollBarBg` | ui/colors.palette:344 | `#517c417a` | night.tdesktop-theme/colors.tdesktop-theme:252 | `#7f84897a` |
| `scrollbar.chatThumbBackgroundHover` | `historyScrollBarBgOver` | ui/colors.palette:345 | `#517c41bc` | night.tdesktop-theme/colors.tdesktop-theme:253 | `#64686cbc` |
| `scrollbar.chatTrackBackground` | `historyScrollBg` | ui/colors.palette:346 | `#517c414c` | night.tdesktop-theme/colors.tdesktop-theme:254 | `#565a5e4c` |
| `scrollbar.chatTrackBackgroundHover` | `historyScrollBgOver` | ui/colors.palette:347 | `#517c416b` | night.tdesktop-theme/colors.tdesktop-theme:255 | `#5a5d616b` |
| `scrollbar.thumbBackground` | `scrollBarBg` | ui/colors.palette:62 | `#00000053` | night.tdesktop-theme/colors.tdesktop-theme:50 | `#ffffff53` |
| `scrollbar.thumbBackgroundHover` | `scrollBarBgOver` | ui/colors.palette:63 | `#0000007a` | night.tdesktop-theme/colors.tdesktop-theme:51 | `#ffffff7a` |
| `scrollbar.trackBackground` | `scrollBg` | ui/colors.palette:64 | `#0000001a` | night.tdesktop-theme/colors.tdesktop-theme:52 | `#ffffff1a` |
| `scrollbar.trackBackgroundHover` | `scrollBgOver` | ui/colors.palette:65 | `#0000002c` | night.tdesktop-theme/colors.tdesktop-theme:53 | `#ffffff2c` |
| `search.barBackground` | `searchedBarBg` | ui/colors.palette:238 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:179 | `#202b38ff` |
| `search.barForeground` | `searchedBarFg` | ui/colors.palette:239 | `#919191ff` | night.tdesktop-theme/colors.tdesktop-theme:180 | `#8c98a4ff` |
| `search.textCurrentMatchBackground` | `searchedTextCurrentMatchBg` | ui/colors.palette:242 | `#f5ab5cff` | 继承浅色 / 合并别名 | `#f5ab5cff` |
| `search.textCurrentMatchForeground` | `searchedTextCurrentMatchFg` | ui/colors.palette:243 | `#000000ff` | 继承浅色 / 合并别名 | `#000000ff` |
| `search.textMatchBackground` | `searchedTextMatchBg` | ui/colors.palette:240 | `#fffc67ff` | 继承浅色 / 合并别名 | `#fffc67ff` |
| `search.textMatchForeground` | `searchedTextMatchFg` | ui/colors.palette:241 | `#000000ff` | 继承浅色 / 合并别名 | `#000000ff` |
| `selection.background` | `msgInBgSelected` | ui/colors.palette:350 | `#c2dcf2ff` | night.tdesktop-theme/colors.tdesktop-theme:257 | `#2e70a5ff` |
| `semantic.danger` | `attentionButtonFg` | ui/colors.palette:48 | `#d14e4eff` | night.tdesktop-theme/colors.tdesktop-theme:34 | `#ec3942ff` |
| `semantic.success` | `boxTextFgGood` | ui/colors.palette:137 | `#4ab44aff` | night.tdesktop-theme/colors.tdesktop-theme:100 | `#5598dbff` |
| `semantic.warning` | `statisticsChartLineGolden` | ui/colors.palette:677 | `#eba52dff` | 继承浅色 / 合并别名 | `#eba52dff` |
| `service.background` | `msgServiceBg` | ui/colors.palette:368 | `#517c417f` | night.tdesktop-theme/colors.tdesktop-theme:275 | `#213040d5` |
| `service.backgroundSelected` | `msgServiceBgSelected` | ui/colors.palette:369 | `#96b38ba2` | night.tdesktop-theme/colors.tdesktop-theme:276 | `#2e7ab4ff` |
| `service.text` | `msgServiceFg` | ui/colors.palette:367 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:274 | `#ffffffff` |
| `service.unreadBackground` | `historyUnreadBarBg` | ui/colors.palette:285 | `#fcfbfaff` | night.tdesktop-theme/colors.tdesktop-theme:213 | `#182433ff` |
| `service.unreadBorder` | `historyUnreadBarBorder` | ui/colors.palette:286 | `#00000018` | night.tdesktop-theme/colors.tdesktop-theme:214 | `#03080d00` |
| `service.unreadText` | `historyUnreadBarFg` | ui/colors.palette:287 | `#538bb4ff` | night.tdesktop-theme/colors.tdesktop-theme:215 | `#ffffffff` |
| `settings.iconBackground1` | `settingsIconBg1` | ui/colors.palette:330 | `#f06964ff` | 继承浅色 / 合并别名 | `#f06964ff` |
| `settings.iconBackground2` | `settingsIconBg2` | ui/colors.palette:331 | `#6dc534ff` | 继承浅色 / 合并别名 | `#6dc534ff` |
| `settings.iconBackground3` | `settingsIconBg3` | ui/colors.palette:332 | `#ed9f20ff` | 继承浅色 / 合并别名 | `#ed9f20ff` |
| `settings.iconBackground4` | `settingsIconBg4` | ui/colors.palette:333 | `#56b3f5ff` | 继承浅色 / 合并别名 | `#56b3f5ff` |
| `settings.iconBackground5` | `settingsIconBg5` | ui/colors.palette:334 | `#7595ffff` | 继承浅色 / 合并别名 | `#7595ffff` |
| `settings.iconBackground6` | `settingsIconBg6` | ui/colors.palette:335 | `#b580e2ff` | 继承浅色 / 合并别名 | `#b580e2ff` |
| `settings.iconBackground8` | `settingsIconBg8` | ui/colors.palette:336 | `#f2925bff` | 继承浅色 / 合并别名 | `#f2925bff` |
| `settings.iconBackgroundArchive` | `settingsIconBgArchive` | ui/colors.palette:337 | `#9da2b0ff` | 继承浅色 / 合并别名 | `#9da2b0ff` |
| `settings.iconForeground` | `settingsIconFg` | ui/colors.palette:338 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `sidebar.background` | `sideBarBg` | ui/colors.palette:645 | `#293a4cff` | night.tdesktop-theme/colors.tdesktop-theme:457 | `#0e1621ff` |
| `sidebar.backgroundActive` | `sideBarBgActive` | ui/colors.palette:646 | `#17212bff` | night.tdesktop-theme/colors.tdesktop-theme:458 | `#25303eff` |
| `sidebar.backgroundPressed` | `sideBarBgRipple` | ui/colors.palette:647 | `#1e2b38ff` | night.tdesktop-theme/colors.tdesktop-theme:459 | `#1e2733ff` |
| `sidebar.badgeBackground` | `sideBarBadgeBg` | ui/colors.palette:652 | `#5eb5f7ff` | night.tdesktop-theme/colors.tdesktop-theme:464 | `#5eb5f7ff` |
| `sidebar.badgeBackgroundActive` | `sideBarBadgeBgActive` | ui/colors.palette:653 | `#5eb5f7ff` | 继承浅色 / 合并别名 | `#5eb5f7ff` |
| `sidebar.badgeBackgroundMuted` | `sideBarBadgeBgMuted` | ui/colors.palette:654 | `#8393a3ff` | night.tdesktop-theme/colors.tdesktop-theme:465 | `#768c9eff` |
| `sidebar.badgeBackgroundMutedActive` | `sideBarBadgeBgMutedActive` | ui/colors.palette:655 | `#5eb5f7ff` | 继承浅色 / 合并别名 | `#5eb5f7ff` |
| `sidebar.badgeForeground` | `sideBarBadgeFg` | ui/colors.palette:656 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `sidebar.iconForeground` | `sideBarIconFg` | ui/colors.palette:650 | `#8393a3ff` | night.tdesktop-theme/colors.tdesktop-theme:462 | `#768c9eff` |
| `sidebar.iconForegroundActive` | `sideBarIconFgActive` | ui/colors.palette:651 | `#5eb5f7ff` | night.tdesktop-theme/colors.tdesktop-theme:463 | `#5eb5f7ff` |
| `sidebar.textForeground` | `sideBarTextFg` | ui/colors.palette:648 | `#8897a6ff` | night.tdesktop-theme/colors.tdesktop-theme:460 | `#768c9eff` |
| `sidebar.textForegroundActive` | `sideBarTextFgActive` | ui/colors.palette:649 | `#64b9faff` | night.tdesktop-theme/colors.tdesktop-theme:461 | `#64b9faff` |
| `statistics.chartActive` | `statisticsChartActive` | ui/colors.palette:672 | `#baccd9d8` | night.tdesktop-theme/colors.tdesktop-theme:467 | `#526679d8` |
| `statistics.chartInactive` | `statisticsChartInactive` | ui/colors.palette:671 | `#e2eef999` | night.tdesktop-theme/colors.tdesktop-theme:466 | `#313d4cc8` |
| `statistics.chartLineBlue` | `statisticsChartLineBlue` | ui/colors.palette:674 | `#327fe5ff` | 继承浅色 / 合并别名 | `#327fe5ff` |
| `statistics.chartLineCyan` | `statisticsChartLineCyan` | ui/colors.palette:683 | `#40d0caff` | 继承浅色 / 合并别名 | `#40d0caff` |
| `statistics.chartLineGolden` | `statisticsChartLineGolden` | ui/colors.palette:677 | `#eba52dff` | 继承浅色 / 合并别名 | `#eba52dff` |
| `statistics.chartLineGreen` | `statisticsChartLineGreen` | ui/colors.palette:675 | `#61c752ff` | 继承浅色 / 合并别名 | `#61c752ff` |
| `statistics.chartLineIndigo` | `statisticsChartLineIndigo` | ui/colors.palette:681 | `#7f79f3ff` | 继承浅色 / 合并别名 | `#7f79f3ff` |
| `statistics.chartLineLightblue` | `statisticsChartLineLightblue` | ui/colors.palette:678 | `#58a8edff` | 继承浅色 / 合并别名 | `#58a8edff` |
| `statistics.chartLineLightgreen` | `statisticsChartLineLightgreen` | ui/colors.palette:679 | `#8fcf39ff` | 继承浅色 / 合并别名 | `#8fcf39ff` |
| `statistics.chartLineOrange` | `statisticsChartLineOrange` | ui/colors.palette:680 | `#f28c39ff` | 继承浅色 / 合并别名 | `#f28c39ff` |
| `statistics.chartLinePurple` | `statisticsChartLinePurple` | ui/colors.palette:682 | `#9f79e8ff` | 继承浅色 / 合并别名 | `#9f79e8ff` |
| `statistics.chartLineRed` | `statisticsChartLineRed` | ui/colors.palette:676 | `#e05356ff` | 继承浅色 / 合并别名 | `#e05356ff` |
| `sticker.panDeleteBackground` | `stickerPanDeleteBg` | ui/colors.palette:254 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:186 | `#000000cc` |
| `sticker.panDeleteForeground` | `stickerPanDeleteFg` | ui/colors.palette:255 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:187 | `#ffffffff` |
| `sticker.panPremium1` | `stickerPanPremium1` | ui/colors.palette:257 | `#5a99ffff` | 继承浅色 / 合并别名 | `#5a99ffff` |
| `sticker.panPremium2` | `stickerPanPremium2` | ui/colors.palette:258 | `#45b9f3ff` | 继承浅色 / 合并别名 | `#45b9f3ff` |
| `sticker.previewBackground` | `stickerPreviewBg` | ui/colors.palette:256 | `#ffffffb0` | night.tdesktop-theme/colors.tdesktop-theme:188 | `#000000b0` |
| `titlebar.background` | `titleBg` | ui/colors.palette:100 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:70 | `#1f2936ff` |
| `titlebar.backgroundActive` | `titleBgActive` | ui/colors.palette:101 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:71 | `#242f3dff` |
| `titlebar.buttonBackground` | `titleButtonBg` | ui/colors.palette:102 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:72 | `#1f2936ff` |
| `titlebar.buttonBackgroundActive` | `titleButtonBgActive` | ui/colors.palette:106 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:76 | `#242f3dff` |
| `titlebar.buttonBackgroundActiveHover` | `titleButtonBgActiveOver` | ui/colors.palette:108 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:78 | `#2c3847ff` |
| `titlebar.buttonBackgroundHover` | `titleButtonBgOver` | ui/colors.palette:104 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:74 | `#25303eff` |
| `titlebar.buttonCloseBackground` | `titleButtonCloseBg` | ui/colors.palette:110 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:80 | `#1f2936ff` |
| `titlebar.buttonCloseBackgroundActive` | `titleButtonCloseBgActive` | ui/colors.palette:114 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:84 | `#242f3dff` |
| `titlebar.buttonCloseBackgroundActiveHover` | `titleButtonCloseBgActiveOver` | ui/colors.palette:116 | `#e81123ff` | night.tdesktop-theme/colors.tdesktop-theme:86 | `#e92539ff` |
| `titlebar.buttonCloseBackgroundHover` | `titleButtonCloseBgOver` | ui/colors.palette:112 | `#e81123ff` | night.tdesktop-theme/colors.tdesktop-theme:82 | `#e92539ff` |
| `titlebar.buttonCloseForeground` | `titleButtonCloseFg` | ui/colors.palette:111 | `#abababff` | night.tdesktop-theme/colors.tdesktop-theme:81 | `#576673ff` |
| `titlebar.buttonCloseForegroundActive` | `titleButtonCloseFgActive` | ui/colors.palette:115 | `#abababff` | night.tdesktop-theme/colors.tdesktop-theme:85 | `#576673ff` |
| `titlebar.buttonCloseForegroundActiveHover` | `titleButtonCloseFgActiveOver` | ui/colors.palette:117 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:87 | `#ffffffff` |
| `titlebar.buttonCloseForegroundHover` | `titleButtonCloseFgOver` | ui/colors.palette:113 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:83 | `#ffffffff` |
| `titlebar.buttonForeground` | `titleButtonFg` | ui/colors.palette:103 | `#abababff` | night.tdesktop-theme/colors.tdesktop-theme:73 | `#576673ff` |
| `titlebar.buttonForegroundActive` | `titleButtonFgActive` | ui/colors.palette:107 | `#abababff` | night.tdesktop-theme/colors.tdesktop-theme:77 | `#576673ff` |
| `titlebar.buttonForegroundActiveHover` | `titleButtonFgActiveOver` | ui/colors.palette:109 | `#9a9a9aff` | night.tdesktop-theme/colors.tdesktop-theme:79 | `#e0e0e0ff` |
| `titlebar.buttonForegroundHover` | `titleButtonFgOver` | ui/colors.palette:105 | `#9a9a9aff` | night.tdesktop-theme/colors.tdesktop-theme:75 | `#e0e0e0ff` |
| `titlebar.foreground` | `titleFg` | ui/colors.palette:118 | `#acacacff` | night.tdesktop-theme/colors.tdesktop-theme:88 | `#6a7680ff` |
| `titlebar.foregroundActive` | `titleFgActive` | ui/colors.palette:119 | `#3e3c3eff` | night.tdesktop-theme/colors.tdesktop-theme:89 | `#91a3b3ff` |
| `titlebar.shadow` | `titleShadow` | ui/colors.palette:99 | `#00000003` | night.tdesktop-theme/colors.tdesktop-theme:69 | `#00000000` |
| `toast.background` | `toastBg` | ui/colors.palette:448 | `#2c3033e5` | night.tdesktop-theme/colors.tdesktop-theme:348 | `#000000b2` |
| `toast.foreground` | `toastFg` | ui/colors.palette:449 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:349 | `#ffffffff` |
| `tooltip.background` | `tooltipBg` | ui/colors.palette:94 | `#eef2f5ff` | night.tdesktop-theme/colors.tdesktop-theme:66 | `#16222dff` |
| `tooltip.borderForeground` | `tooltipBorderFg` | ui/colors.palette:96 | `#c9d1dbff` | night.tdesktop-theme/colors.tdesktop-theme:68 | `#16222dff` |
| `tooltip.foreground` | `tooltipFg` | ui/colors.palette:95 | `#5d6c80ff` | night.tdesktop-theme/colors.tdesktop-theme:67 | `#d4e0eaff` |
| `viewer.background` | `mediaviewBg` | ui/colors.palette:525 | `#222222eb` | night.tdesktop-theme/colors.tdesktop-theme:410 | `#222222eb` |
| `viewer.captionBackground` | `mediaviewCaptionBg` | ui/colors.palette:529 | `#11111180` | night.tdesktop-theme/colors.tdesktop-theme:414 | `#11111180` |
| `viewer.captionForeground` | `mediaviewCaptionFg` | ui/colors.palette:530 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:415 | `#ffffffff` |
| `viewer.controlBackground` | `mediaviewControlBg` | ui/colors.palette:527 | `#0000003c` | night.tdesktop-theme/colors.tdesktop-theme:412 | `#0000003c` |
| `viewer.controlForeground` | `mediaviewControlFg` | ui/colors.palette:528 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:413 | `#ffffffff` |
| `viewer.fileBackground` | `mediaviewFileBg` | ui/colors.palette:511 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:398 | `#17212bff` |
| `viewer.fileBlueCornerForeground` | `mediaviewFileBlueCornerFg` | ui/colors.palette:517 | `#599dcfff` | night.tdesktop-theme/colors.tdesktop-theme:404 | `#599dcfff` |
| `viewer.fileExtForeground` | `mediaviewFileExtFg` | ui/colors.palette:518 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:405 | `#ffffffff` |
| `viewer.fileGreenCornerForeground` | `mediaviewFileGreenCornerFg` | ui/colors.palette:516 | `#49a957ff` | night.tdesktop-theme/colors.tdesktop-theme:403 | `#49a957ff` |
| `viewer.fileNameForeground` | `mediaviewFileNameFg` | ui/colors.palette:512 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:399 | `#f5f5f5ff` |
| `viewer.fileRedCornerForeground` | `mediaviewFileRedCornerFg` | ui/colors.palette:514 | `#d55959ff` | night.tdesktop-theme/colors.tdesktop-theme:401 | `#d55959ff` |
| `viewer.fileSizeForeground` | `mediaviewFileSizeFg` | ui/colors.palette:513 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:400 | `#708499ff` |
| `viewer.fileYellowCornerForeground` | `mediaviewFileYellowCornerFg` | ui/colors.palette:515 | `#e8a659ff` | night.tdesktop-theme/colors.tdesktop-theme:402 | `#e8a659ff` |
| `viewer.menuBackground` | `mediaviewMenuBg` | ui/colors.palette:520 | `#383838ff` | night.tdesktop-theme/colors.tdesktop-theme:406 | `#383838ff` |
| `viewer.menuBackgroundHover` | `mediaviewMenuBgOver` | ui/colors.palette:521 | `#505050ff` | night.tdesktop-theme/colors.tdesktop-theme:407 | `#505050ff` |
| `viewer.menuBackgroundPressed` | `mediaviewMenuBgRipple` | ui/colors.palette:522 | `#676767ff` | night.tdesktop-theme/colors.tdesktop-theme:408 | `#676767ff` |
| `viewer.menuForeground` | `mediaviewMenuFg` | ui/colors.palette:523 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:409 | `#ffffffff` |
| `viewer.pipControlsForeground` | `mediaviewPipControlsFg` | ui/colors.palette:544 | `#ffffffd9` | 继承浅色 / 合并别名 | `#ffffffd9` |
| `viewer.pipControlsForegroundHover` | `mediaviewPipControlsFgOver` | ui/colors.palette:545 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `viewer.pipPlaybackActive` | `mediaviewPipPlaybackActive` | ui/colors.palette:546 | `#ffffffda` | 继承浅色 / 合并别名 | `#ffffffda` |
| `viewer.pipPlaybackInactive` | `mediaviewPipPlaybackInactive` | ui/colors.palette:547 | `#ffffff26` | 继承浅色 / 合并别名 | `#ffffff26` |
| `viewer.playbackActive` | `mediaviewPlaybackActive` | ui/colors.palette:535 | `#c7c7c7ff` | night.tdesktop-theme/colors.tdesktop-theme:419 | `#c7c7c7ff` |
| `viewer.playbackActiveHover` | `mediaviewPlaybackActiveOver` | ui/colors.palette:537 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:421 | `#ffffffff` |
| `viewer.playbackIconForeground` | `mediaviewPlaybackIconFg` | ui/colors.palette:540 | `#c7c7c7ff` | night.tdesktop-theme/colors.tdesktop-theme:424 | `#c7c7c7ff` |
| `viewer.playbackIconForegroundHover` | `mediaviewPlaybackIconFgOver` | ui/colors.palette:541 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:425 | `#ffffffff` |
| `viewer.playbackIconPressed` | `mediaviewPlaybackIconRipple` | ui/colors.palette:542 | `#ffffff14` | 继承浅色 / 合并别名 | `#ffffff14` |
| `viewer.playbackInactive` | `mediaviewPlaybackInactive` | ui/colors.palette:536 | `#252525ff` | night.tdesktop-theme/colors.tdesktop-theme:420 | `#252525ff` |
| `viewer.playbackInactiveHover` | `mediaviewPlaybackInactiveOver` | ui/colors.palette:538 | `#474747ff` | night.tdesktop-theme/colors.tdesktop-theme:422 | `#474747ff` |
| `viewer.playbackProgressForeground` | `mediaviewPlaybackProgressFg` | ui/colors.palette:539 | `#ffffffc7` | night.tdesktop-theme/colors.tdesktop-theme:423 | `#ffffffc7` |
| `viewer.saveMsgBackground` | `mediaviewSaveMsgBg` | ui/colors.palette:532 | `#2c3033e5` | night.tdesktop-theme/colors.tdesktop-theme:417 | `#000000b2` |
| `viewer.saveMsgForeground` | `mediaviewSaveMsgFg` | ui/colors.palette:533 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:418 | `#ffffffff` |
| `viewer.textLinkForeground` | `mediaviewTextLinkFg` | ui/colors.palette:531 | `#4db8ffff` | night.tdesktop-theme/colors.tdesktop-theme:416 | `#70baf5ff` |
| `viewer.transparentBackground` | `mediaviewTransparentBg` | ui/colors.palette:549 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:426 | `#ffffffff` |
| `viewer.transparentForeground` | `mediaviewTransparentFg` | ui/colors.palette:550 | `#ccccccff` | night.tdesktop-theme/colors.tdesktop-theme:427 | `#ccccccff` |
| `viewer.videoBackground` | `mediaviewVideoBg` | ui/colors.palette:526 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:411 | `#000000ff` |
| `wallet.balanceForeground` | `walletBalanceFg` | ui/colors.palette:639 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `wallet.subBalanceForeground` | `walletSubBalanceFg` | ui/colors.palette:640 | `#f9f9f9ff` | 继承浅色 / 合并别名 | `#f9f9f9ff` |
| `wallet.titleBackground` | `walletTitleBg` | ui/colors.palette:620 | `#121213ff` | 继承浅色 / 合并别名 | `#121213ff` |
| `wallet.titleBackgroundActive` | `walletTitleBgActive` | ui/colors.palette:621 | `#121213ff` | 继承浅色 / 合并别名 | `#121213ff` |
| `wallet.titleButtonBackground` | `walletTitleButtonBg` | ui/colors.palette:622 | `#121213ff` | 继承浅色 / 合并别名 | `#121213ff` |
| `wallet.titleButtonBackgroundActive` | `walletTitleButtonBgActive` | ui/colors.palette:626 | `#121213ff` | 继承浅色 / 合并别名 | `#121213ff` |
| `wallet.titleButtonBackgroundActiveHover` | `walletTitleButtonBgActiveOver` | ui/colors.palette:628 | `#373738ff` | 继承浅色 / 合并别名 | `#373738ff` |
| `wallet.titleButtonBackgroundHover` | `walletTitleButtonBgOver` | ui/colors.palette:624 | `#373738ff` | 继承浅色 / 合并别名 | `#373738ff` |
| `wallet.titleButtonCloseBackground` | `walletTitleButtonCloseBg` | ui/colors.palette:630 | `#121213ff` | 继承浅色 / 合并别名 | `#121213ff` |
| `wallet.titleButtonCloseBackgroundActive` | `walletTitleButtonCloseBgActive` | ui/colors.palette:634 | `#121213ff` | 继承浅色 / 合并别名 | `#121213ff` |
| `wallet.titleButtonCloseBackgroundActiveHover` | `walletTitleButtonCloseBgActiveOver` | ui/colors.palette:636 | `#e81123ff` | 继承浅色 / 合并别名 | `#e92539ff` |
| `wallet.titleButtonCloseBackgroundHover` | `walletTitleButtonCloseBgOver` | ui/colors.palette:632 | `#e81123ff` | 继承浅色 / 合并别名 | `#e92539ff` |
| `wallet.titleButtonCloseForeground` | `walletTitleButtonCloseFg` | ui/colors.palette:631 | `#5a5a5bff` | 继承浅色 / 合并别名 | `#5a5a5bff` |
| `wallet.titleButtonCloseForegroundActive` | `walletTitleButtonCloseFgActive` | ui/colors.palette:635 | `#5a5a5bff` | 继承浅色 / 合并别名 | `#5a5a5bff` |
| `wallet.titleButtonCloseForegroundActiveHover` | `walletTitleButtonCloseFgActiveOver` | ui/colors.palette:637 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `wallet.titleButtonCloseForegroundHover` | `walletTitleButtonCloseFgOver` | ui/colors.palette:633 | `#ffffffff` | 继承浅色 / 合并别名 | `#ffffffff` |
| `wallet.titleButtonForeground` | `walletTitleButtonFg` | ui/colors.palette:623 | `#5a5a5bff` | 继承浅色 / 合并别名 | `#5a5a5bff` |
| `wallet.titleButtonForegroundActive` | `walletTitleButtonFgActive` | ui/colors.palette:627 | `#5a5a5bff` | 继承浅色 / 合并别名 | `#5a5a5bff` |
| `wallet.titleButtonForegroundActiveHover` | `walletTitleButtonFgActiveOver` | ui/colors.palette:629 | `#747475ff` | 继承浅色 / 合并别名 | `#747475ff` |
| `wallet.titleButtonForegroundHover` | `walletTitleButtonFgOver` | ui/colors.palette:625 | `#747475ff` | 继承浅色 / 合并别名 | `#747475ff` |
| `wallet.topBackground` | `walletTopBg` | ui/colors.palette:638 | `#1e1f21ff` | 继承浅色 / 合并别名 | `#1e1f21ff` |
| `wallet.topIconForeground` | `walletTopIconFg` | ui/colors.palette:642 | `#999999ff` | 继承浅色 / 合并别名 | `#999999ff` |
| `wallet.topIconPressed` | `walletTopIconRipple` | ui/colors.palette:643 | `#ffffff12` | 继承浅色 / 合并别名 | `#ffffff12` |
| `wallet.topLabelForeground` | `walletTopLabelFg` | ui/colors.palette:641 | `#999999ff` | 继承浅色 / 合并别名 | `#999999ff` |
| `window.activeTextForeground` | `windowActiveTextFg` | ui/colors.palette:20 | `#168acdff` | night.tdesktop-theme/colors.tdesktop-theme:12 | `#6ab3f3ff` |
| `window.background` | `windowBg` | ui/colors.palette:9 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:1 | `#17212bff` |
| `window.backgroundActive` | `windowBgActive` | ui/colors.palette:18 | `#40a7e3ff` | night.tdesktop-theme/colors.tdesktop-theme:10 | `#5288c1ff` |
| `window.backgroundHover` | `windowBgOver` | ui/colors.palette:11 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:3 | `#232e3cff` |
| `window.backgroundPressed` | `windowBgRipple` | ui/colors.palette:12 | `#e5e5e5ff` | night.tdesktop-theme/colors.tdesktop-theme:4 | `#24303dff` |
| `window.boldForeground` | `windowBoldFg` | ui/colors.palette:16 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:8 | `#e9e8e8ff` |
| `window.boldForegroundHover` | `windowBoldFgOver` | ui/colors.palette:17 | `#222222ff` | night.tdesktop-theme/colors.tdesktop-theme:9 | `#e9e9e9ff` |
| `window.foregroundActive` | `windowFgActive` | ui/colors.palette:19 | `#ffffffff` | night.tdesktop-theme/colors.tdesktop-theme:11 | `#ffffffff` |
| `window.foregroundHover` | `windowFgOver` | ui/colors.palette:13 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:5 | `#e9ecf0ff` |
| `window.mutedText` | `windowSubTextFg` | ui/colors.palette:14 | `#999999ff` | night.tdesktop-theme/colors.tdesktop-theme:6 | `#708499ff` |
| `window.shadowForeground` | `windowShadowFg` | ui/colors.palette:21 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:13 | `#000000ff` |
| `window.shadowForegroundFallback` | `windowShadowFgFallback` | ui/colors.palette:22 | `#f1f1f1ff` | night.tdesktop-theme/colors.tdesktop-theme:14 | `#17212bff` |
| `window.subTextForegroundHover` | `windowSubTextFgOver` | ui/colors.palette:15 | `#919191ff` | night.tdesktop-theme/colors.tdesktop-theme:7 | `#7c90a4ff` |
| `window.text` | `windowFg` | ui/colors.palette:10 | `#000000ff` | night.tdesktop-theme/colors.tdesktop-theme:2 | `#f5f5f5ff` |
| `profileTabs.outlineLight` | outline RGB | paint/tabs.md; info_profile_tabs_strip.cpp:34-42 | `#000000ff` | 同源；alpha由主题指标控制 | `#000000ff` |
| `profileTabs.outlineDark` | outline RGB | paint/tabs.md; info_profile_tabs_strip.cpp:34-42 | `#ffffffff` | 同源；alpha由主题指标控制 | `#ffffffff` |

共 586 个 palette 角色，加 28 个用途映射。night 中 447 个同名声明覆盖当前 palette，139 个当前角色继承合并表，20 个旧声明不公开。映射选择见 exceptions.md。
