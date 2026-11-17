# 字阶、动效与材质来源

下列行号对应 reference/README.md 的固定源码版本。normal / semibold / bold 的数值映射见 exceptions.md。

| typography 键 | 字号 / 字重 | Telegram 常量 | 文件:行号 |
|---|---|---|---|
| body | 13 / 400 | fsize, normalFont | lib_ui/ui/basic.style:53-54 |
| conversationTitle | 13 / 600 | semiboldFont | lib_ui/ui/basic.style:55 |
| conversationPreview | 13 / 400 | dialogsTextFont → normalFont | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:86 |
| conversationTime | 13 / 400 | dialogsDateFont | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:88 |
| headerTitle | 13 / 600 | infoTopBar.titleWithSubtitle.style → semiboldTextStyle | tdesktop/Telegram/SourceFiles/info/info.style:239 |
| headerStatus | 13 / 400 | normalFont | lib_ui/ui/basic.style:54 |
| message | 13 / 400 | msgFont → fsize | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:15 |
| messageTime | 13 / 400 | msgDateFont | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:21 |
| quote | 13 / 400 | historyTextStyle → defaultTextStyle.font | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1289; lib_ui/ui/basic.style:85 |
| reaction | 12 / 400 | reactionInlineTagFont | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:684 |
| compose | 13 / 400 | historyComposeField.placeholderFont → normalFont | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1321 |
| sidebar | 11 / 600 | windowFiltersButton.style.font | tdesktop/Telegram/SourceFiles/window/window.style:236 |
| menu | 13 / 400 | defaultMenu.itemStyle → defaultTextStyle | lib_ui/ui/widgets/widgets.style:971 |
| badge | 12 / 700 | dialogsUnreadFont | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:78 |
| conversationTag | 10 / 400 | dialogRowFilterTagStyle.font | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:160 |
| sidebarBadge | 12 / 600 | windowFiltersButton.badgeStyle.font | tdesktop/Telegram/SourceFiles/window/window.style:239 |
| avatar | 20 / 600 | 我定的：使用 dialogsPinnedIcon 的 20 DIP 画布作为默认 initials 字号；宿主可传预制头像 | exceptions.md |
| messageName | 13 / 600 | msgNameFont → semiboldFont | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:16 |
| service | 13 / 600 | msgServiceFont → semiboldFont | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:18 |
| latestBadge | 13 / 600 | historyToDownBadgeFont → semiboldFont | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1223 |
| contentTitle | 13 / 600 | msgNameFont, fwdTextStyle, webPageTitleFont, reaction count semiboldFont | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:16,151,530; history/view/reactions/history_view_reactions.cpp:469 |
| contentStrong | 13 / 700 | boldFont | lib_ui/ui/basic.style:56 |
| contentCode | 13 / 400 | msgFont, monospace family chosen by platform | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:15 |
| dialogTitle | 16 / 600 | boxTitleFont | lib_ui/ui/layers/layers.style:73 |
| dialogBody | 14 / 400 | boxTextFont | lib_ui/ui/basic.style:56-57 |
| dialogButton | 14 / 600 | defaultBoxButtonTextStyle.font | lib_ui/ui/layers/layers.style:42 |
| composeTitle | 13 / 600 | defaultMessageBar.title → semiboldTextStyle | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1036 |
| pickerTitle | 13 / 600 | emojiPanHeaderFont → semiboldFont | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:586 |
| conversationTTL | 9 / 600 | dialogsScamFont used by PaintTimerIcon | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:492；ui/effects/ttl_icon.cpp:21 |
| settingsSectionTitle | 14 / 600 | defaultSubsectionTitle.font | lib_ui/ui/layers/layers.style:148-150 |
| inputPlaceholder | 14 / 600 | defaultInputField.placeholderFont | lib_ui/ui/widgets/widgets.style:1043 |
| messageAvatar | 13 / 600 | historyPeerUserpicFont，EmptyUserpic字号为头像33×13/33 | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:332；ui/empty_userpic.cpp:300–302；[画法](paint/chat-identity.md#圆与首字) |
| pageTitle | 14 / 600 | infoTopBarTitle.font | paint/page-title.md; info/info.style:154-156 |
| statusBadge | 10 / 600 | settingsPremiumNewBadge.font | paint/badge.md; settings/settings.style:156 |

| motion 键 | 时长 ms | 缓动（未核实项见 exceptions.md） | Telegram 常量 | 文件:行号 |
|---|---|---|---|---|
| control | 120 | ease-out | universalDuration | lib_ui/ui/basic.style:135 |
| page | 240 | ease-out-circ，已核实 window_slide_animation.h:43-44, cpp:196-211 | slideDuration | lib_ui/ui/basic.style:96 |
| slide | 150 | ease-out | slideWrapDuration | lib_ui/ui/basic.style:100 |
| fade | 200 | linear | fadeWrapDuration | lib_ui/ui/basic.style:101 |
| dateFade | 200 | linear | historyDateFadeDuration | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:313 |
| messageReveal | 150 | ease-out-circ（已核实 history_widget.cpp:8958） | itemRevealDuration | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:58 |
| menu | 150 | linear，已核对 popup_menu.cpp:847-851 | defaultPopupMenu.duration | lib_ui/ui/widgets/widgets.style:996 |
| menuShow | 200 | ease-out-circ，已核对 panel_animation.cpp:325-350 | defaultPopupMenu.showDuration | lib_ui/ui/widgets/widgets.style:997 |
| rippleShow | 650 | ease-out-quint（已核实 lib_ui/ui/effects/ripple_animation.cpp:86） | defaultRippleAnimation.showDuration | lib_ui/ui/widgets/widgets.style:705 |
| rippleHide | 200 | linear | defaultRippleAnimation.hideDuration | lib_ui/ui/widgets/widgets.style:706 |
| toLatest | 150 | linear（已核实 history_view_corner_buttons.cpp:451-455） | historyToDownDuration | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1227 |
| scrollbar | 150 | linear | defaultScrollArea.duration | lib_ui/ui/widgets/widgets.style:825 |
| listCollapse | 120 | ease-out-circ | dialogsWidthDuration → universalDuration | dialogs/dialogs.style:217; mainwidget.cpp:2968-2973 |
| thirdPanel | 240 | ease-out-circ | slideDuration；主槽缓存页面切换，并排开合直接重排 | lib_ui/ui/basic.style:96; window/window_slide_animation.cpp:196-211 |
| online | 150 | linear（默认动画调用） | dialogsOnlineBadgeDuration | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:188; dialogs/dialogs_row.cpp:286-305 |
| typing | 800 | ease-out-circ（两段半周期，已对照） | historySendActionTypingDuration | lib_ui/ui/widgets/widgets.style:1337; tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:122-160 |
| typingHalfPeriod | 320 | linear | historySendActionTypingHalfPeriod | lib_ui/ui/widgets/widgets.style:1338 |
| typingStagger | 150 | linear | historySendActionTypingDeltaTime | lib_ui/ui/widgets/widgets.style:1339 |
| messageRevealList | 150 | ease-out-quint（已核实 history_view_list_widget.cpp:3093） | itemRevealDuration | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:58 |
| dateHold | 1000 | linear（等待时间，不插值） | historyScrollDateHideTimeout | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:312 |
| chatScroll | 240 | sine-in-out（已核实 history_widget.cpp:1907） | slideDuration | lib_ui/ui/basic.style:96 |
| dateCrossingHold | 3000 | linear（等待时间，不插值） | kScrollDateHideOnDayCrossingTimeout | tdesktop/Telegram/SourceFiles/history/history_inner_widget.cpp:143 |
| fileHover | 200 | linear | msgFileOverDuration | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:426 |
| mediaTransferRotate | 3000 | linear（已核对） | radialPeriod | lib_ui/ui/basic.style:125；lib_ui/ui/effects/radial_animation.cpp:68-69 |
| dialog | 200 | linear，layer_widget.cpp:205-209 | boxDuration | lib_ui/ui/layers/layers.style:37 |
| dialogScrim | 200 | ease-out-circ，layer_widget.cpp:215-221 | boxDuration | lib_ui/ui/layers/layers.style:37 |
| tooltipDelay | 1000 | 定时器，无入场动画 | Tooltip::Show(1000) | tdesktop/Telegram/SourceFiles/history/history_inner_widget.cpp:5698 |
| toastHold | 1500 | 定时器，从出现开始计时 | kDefaultDuration | lib_ui/ui/toast/toast.h:46; toast.cpp:32-34 |
| toastIn | 200 | linear，toast.cpp:40-44 | defaultToast.durationFadeIn | lib_ui/ui/widgets/widgets.style:1557 |
| toastOut | 1000 | linear，toast.cpp:109-113 | defaultToast.durationFadeOut | lib_ui/ui/widgets/widgets.style:1558 |
| toastSlide | 160 | linear，同上 | defaultToast.durationSlide | lib_ui/ui/widgets/widgets.style:1559 |
| viewerWait | 1100 | 定时器 | mediaviewWaitHide | tdesktop/Telegram/SourceFiles/media/view/media_view.style:279 |
| viewerShow | 200 | linear，media_view_overlay_widget.cpp:2587-2605 | mediaviewShowDuration | tdesktop/Telegram/SourceFiles/media/view/media_view.style:281 |
| viewerHide | 600 | linear，同上 | mediaviewHideDuration | tdesktop/Telegram/SourceFiles/media/view/media_view.style:280 |
| viewerHover | 150 | linear，media_view_overlay_widget.cpp:2580 | mediaviewOverDuration | tdesktop/Telegram/SourceFiles/media/view/media_view.style:18 |
| submenuAim | 300 | 定时器，方向三角区内延迟换项 | kSubmenuAimDelay | lib_ui/ui/widgets/popup_menu.cpp:39,494 |
| viewerLoading | 350 | linear 透明度；进度按 `1-duration/(duration+elapsed)` 追踪宿主目标 | radialDuration | lib_ui/ui/basic.style:124; effects/radial_animation.cpp:49-69 |
| viewerLoadingRotation | 3000 | linear，一圈 | radialPeriod | lib_ui/ui/basic.style:125; effects/radial_animation.cpp:68-69 |
| composeButton | 120 | linear | universalDuration；send_button.cpp:139-143 默认线性调用 | lib_ui/ui/basic.style:135 |
| composePlaceholder | 100 | linear | historyComposeField.duration | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1325 |
| picker | 200 | linear 时间轴，显现分阶段 easeOutCirc | emojiPanShowDuration / emojiPanDuration；tabbed_panel.cpp:369-394 | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:518-519 |
| pickerSlide | 200 | linear 时间轴，进出分别 easeOutCirc/easeInCirc | emojiPanSlideDuration；tabbed_selector.cpp:148-149,1409-1411 | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:521 |
| sendingFlight | 280 | linear时间轴；纵向Bezier(0.25,0.1,0.25,1) | kDuration/RiseProgress | tdesktop/Telegram/SourceFiles/ui/effects/message_sending_text_flight.cpp:39,159-160 |
| sendingSlide | 160 | ease-out-quint | kSlideDuration | 同文件:40,735-741 |
| sendingMix | 110 | linear | kFadeDuration | 同文件:41,803-830 |
| sendingTimeout | 1000 | 等待时间 | kGiveUpTimeout | 同文件:42,151-156 |
| mediaTransferCatchup | 350 | elapsed/(duration+elapsed)，目标变化时从已显示值接续 | radialDuration | lib_ui/ui/basic.style:124; effects/radial_animation.cpp:40-59 |
| spoilerFadeIn / spoilerHold / spoilerFadeOut | 200 / 200 / 200 | linear粒子alpha；hold不插值 | DefaultDescriptorText | lib_ui/ui/effects/spoiler_mess.cpp:40-42 |
| spoilerFrame | 33 | 周期60帧 | kDefaultFrameDuration | 同文件:32-33,50-51 |
| spoilerImageFadeIn / spoilerImageFadeOut | 300 / 300 | linear，中间无停留 | DefaultDescriptorImage | 同文件:59-61 |
| recordActivity / uploadActivity | 500 / 500 | linear，弧/箭头位移及首尾透明度 | historySendActionRecordDuration / UploadDuration | lib_ui/ui/widgets/widgets.style:1346,1352；send_action_animations.cpp:197-229,330-374 |
| stickerActivity | 2000 | linear 时间轴，双眼阶段使用 Circ/Quint | historySendActionChooseStickerDuration | lib_ui/ui/widgets/widgets.style:1359；send_action_animations.cpp:635-697 |
| speaking / speakingFade | 3200 / 400 | linear，八阶段三柱频谱及启停幅度 | kSpeakingDuration / kSpeakingFadeDuration | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:24-25,491-565 |
| pickerLeave | 300 | 离开面板/锚点后的定时器 | kHideTimeoutMs | tdesktop/Telegram/SourceFiles/chat_helpers/tabbed_panel.cpp:25,274-283 |
| settingsToggle | 120 | linear | defaultToggle.duration → universalDuration | lib_ui/ui/widgets/widgets.style:867; basic.style:135; widgets/checkbox.cpp:36-40 |
| choice | 120 | linear | defaultCheck/Radio.duration | lib_ui/ui/widgets/widgets.style:848,859; widgets/checkbox.cpp:36-40 |
| chooseSearch / chooseClear | 150 / 150 | linear | field.duration / cancel.duration | lib_ui/ui/widgets/widgets.style:1044,1123; widgets/buttons.cpp:748-752 |
| inputField | 150 | linear | defaultInputField.duration | lib_ui/ui/widgets/widgets.style:1044; widgets/fields/masked_input_field.cpp:237-251 |
| sliderHover | 150 | linear | defaultContinuousSlider.duration | lib_ui/ui/widgets/widgets.style:1220; tdesktop/Telegram/SourceFiles/ui/widgets/continuous_sliders.cpp:212-217 |
| buttonNumbers | 150 | ease-out-circ | numbers animation | paint/round-button.md; lib_ui/ui/effects/numbers_animation.cpp:20,108-112,134 |
| segments / tabs | 150 / 150 | linear | defaultSettingsSlider/defaultTabsSlider.duration | paint/settings-slider.md; paint/tabs.md; lib_ui/ui/widgets/widgets.style:1189,1207 |
| profileTabs | 200 | ease-out-quint | infoProfileTabsStrip.duration | paint/tabs.md; info/info.style:719-723 |
| profileTabsScroll | 150 | ease-out-circ | scrollTo animation | paint/tabs.md; info/profile/tabs/info_profile_tabs_strip.cpp:240-277 |
| collapse | 150 | linear | slideWrapDuration | paint/collapse.md; lib_ui/ui/basic.style:100; wrap/slide_wrap.cpp:55-169 |
| pageTitle | 150 | linear | infoTopBarTitleDuration | paint/page-title.md; info/info.style:264-265 |
| profileTabsWheelGrace / profileTabsWheelRest | 250 / 1000 | 定时器，不插值 | vertical-wheel protection | paint/tabs.md; info/profile/tabs/info_profile_tabs_strip.cpp:25-27 |

ease-out-circ / ease-out-quint / sine-in-out 的单位进度方程依据 `lib_ui/ui/effects/animation_value.cpp:31-38,75-84,118-127` 独立实现。dateFade 的 linear 调用见 `history/history_inner_widget.cpp:4767`；rippleHide 的 linear 调用见 `lib_ui/ui/effects/ripple_animation.cpp:151`。未注明已核实的其他角色仍属于 exceptions.md 的缓动选择。

menuShow 的宽、高、透明度完成比例分别是 0.6 / 0.9 / 0.3，默认宽从 0.5、高从 0.3 开始；内容保持最终字号与布局，仅裁剪露出。遮盖渐变带高为最终面板高的 0.2。来源 widgets.style:931-942、panel_animation.cpp:325-350,377-412。其余通用角色 control/slide 仍是主题层的映射选择，见 exceptions.md。

原生 tooltip 按 native-overlay 附件使用已有 motion.fade（200 ms linear，basic.style:101）淡入；普通 Tooltip::popup 本身没有该动画，这是附件要求的差异。菜单仍使用 menuShow/menu 的200/150 ms与上述分阶段参数，Native窗口退出后才销毁；系统强制关闭/宿主销毁不保留离场动画。

materials 的 window/control/conversation/header/messageIn/messageOut/compose/sidebar/menu/tooltip/service/overlay 共 12 个表面分别引用同名颜色组（control 为 control.surfaceBackground、overlay 为 control.layerBackground）。每个表面的 background、alpha、backdropBlur、glass、clarity 均可覆盖；kind 仅 window 可设 mica/acrylic。默认数值是本库决定，见 exceptions.md；所有颜色别名的 Telegram 来源见 theme-colors.md。
