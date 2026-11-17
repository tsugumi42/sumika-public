# 尺寸来源

尺寸单位 DIP，等于 Telegram 100% 缩放逻辑像素；不透明度、计数、比例和缩放档位无单位，viewer.loadingMinArc 使用度。这里记录声明，运行规则见各组件对照页。生成命令：`go run ./internal/cmd/themegen`。

| MewTypeUI 键 | Telegram 常量 | 文件:行号 | 值 |
|---|---|---|---|
| `control.radiusLarge` | `roundRadiusLarge` | lib_ui/ui/basic.style:107 | 6 |
| `control.radiusSmall` | `roundRadiusSmall` | lib_ui/ui/basic.style:108 | 3 |
| `control.spacing` | `historyGroupSkip (mapping choice)` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:510 | 4 |
| `control.borderWidth` | `lineWidth` | lib_ui/ui/basic.style:62 | 1 |
| `conversation.rowHeight` | `dialogsRowHeight` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:91 | 62 |
| `conversation.avatarSize` | `defaultDialogRow.photoSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:98 | 46 |
| `conversation.paddingLeft` | `defaultDialogRow.padding.left` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:97 | 10 |
| `conversation.paddingTop` | `defaultDialogRow.padding.top` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:97 | 8 |
| `conversation.paddingRight` | `defaultDialogRow.padding.right` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:97 | 10 |
| `conversation.paddingBottom` | `defaultDialogRow.padding.bottom` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:97 | 8 |
| `conversation.titleLeft` | `defaultDialogRow.nameLeft` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:99 | 68 |
| `conversation.titleTop` | `defaultDialogRow.nameTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:100 | 10 |
| `conversation.previewLeft` | `defaultDialogRow.textLeft` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:101 | 68 |
| `conversation.previewTop` | `defaultDialogRow.textTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:102 | 34 |
| `conversation.badgeHeight` | `dialogsUnreadHeight` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:79 | 19 |
| `conversation.badgePadding` | `dialogsUnreadPadding` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:80 | 5 |
| `header.height` | `topBarHeight` | tdesktop/Telegram/SourceFiles/info/info.style:919 | 54 |
| `header.actionWidth` | `topBarSearch.width` | tdesktop/Telegram/SourceFiles/info/info.style:929 | 40 |
| `header.actionGap` | `topBarActionSkip` | tdesktop/Telegram/SourceFiles/info/info.style:972 | 10 |
| `header.backWidth` | `historyTopBarBack.width` | tdesktop/Telegram/SourceFiles/info/info.style:917 | 52 |
| `message.maxWidth` | `msgMaxWidth` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:14 | 430 |
| `message.minWidth` | `msgMinWidth` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:22 | 160 |
| `message.avatarSize` | `msgPhotoSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:23 | 33 |
| `message.avatarSkip` | `msgPhotoSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:24 | 40 |
| `message.paddingLeft` | `msgPadding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:25 | 11 |
| `message.paddingTop` | `msgPadding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:25 | 8 |
| `message.paddingRight` | `msgPadding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:25 | 11 |
| `message.paddingBottom` | `msgPadding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:25 | 8 |
| `message.marginLeft` | `msgMargin.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:52 | 16 |
| `message.marginTop` | `msgMargin.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:52 | 6 |
| `message.marginRight` | `msgMargin.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:52 | 56 |
| `message.marginBottom` | `msgMargin.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:52 | 2 |
| `message.attachedTop` | `msgMarginTopAttached` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:53 | 0 |
| `message.radiusLarge` | `bubbleRadiusLarge` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:327 | 16 |
| `message.radiusSmall` | `bubbleRadiusSmall → roundRadiusLarge` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:326 | 6 |
| `message.shadowHeight` | `msgShadow` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:54 | 2 |
| `message.timestampSpace` | `msgDateSpace` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:99 | 12 |
| `quote.paddingLeft` | `historyQuoteStyle.padding.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1283 | 10 |
| `quote.paddingTop` | `historyQuoteStyle.padding.top` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1283 | 2 |
| `quote.paddingRight` | `historyQuoteStyle.padding.right` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1283 | 4 |
| `quote.paddingBottom` | `historyQuoteStyle.padding.bottom` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1283 | 2 |
| `quote.verticalGap` | `historyQuoteStyle.verticalSkip` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1284 | 4 |
| `quote.outlineWidth` | `historyQuoteStyle.outline` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1285 | 3 |
| `quote.outlineShift` | `historyQuoteStyle.outlineShift` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1286 | 2 |
| `quote.radius` | `historyQuoteStyle.radius` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1287 | 5 |
| `quote.replyBarWidth` | `msgReplyBarSize.width` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:93 | 2 |
| `quote.replyBarHeight` | `msgReplyBarSize.height` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:93 | 36 |
| `quote.replyBarGap` | `msgReplyBarSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:94 | 10 |
| `reaction.paddingLeft` | `reactionInlinePadding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:673 | 5 |
| `reaction.paddingTop` | `reactionInlinePadding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:673 | 2 |
| `reaction.paddingRight` | `reactionInlinePadding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:673 | 7 |
| `reaction.paddingBottom` | `reactionInlinePadding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:673 | 2 |
| `reaction.iconSize` | `reactionInlineSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:674 | 18 |
| `reaction.imageSize` | `reactionInlineImage` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:675 | 32 |
| `reaction.labelGap` | `reactionInlineSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:676 | 3 |
| `reaction.gap` | `reactionInlineBetween` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:687 | 4 |
| `reaction.tagRadiusLeft` | `reactionInlineTagLeftRadius` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:678 | 6 |
| `reaction.tagRadiusRight` | `reactionInlineTagRightRadius` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:679 | 3 |
| `media.minPhotoSize` | `minPhotoSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:168 | 100 |
| `media.maxSize` | `maxMediaSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:169 | 430 |
| `media.maxStickerSize` | `maxStickerSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:170 | 224 |
| `media.maxEmojiSize` | `maxAnimatedEmojiSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:171 | 112 |
| `media.maxGifSize` | `maxGifSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:172 | 320 |
| `media.maxVideoMessageSize` | `maxVideoMessageSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:173 | 240 |
| `media.albumGap` | `historyGroupSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:510 | 4 |
| `compose.fieldMinHeight` | `historyComposeField.heightMin` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1313 | 36 |
| `compose.fieldMaxHeight` | `historyComposeField.heightMax` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1314 | 72 |
| `compose.expandedMaxHeight` | `historyComposeFieldMaxHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1327 | 224 |
| `compose.fadeHeight` | `historyComposeFieldFadeHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1328 | 6 |
| `compose.sendRight` | `historySendRight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1329 | 2 |
| `compose.sendPadding` | `historySendPadding` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1330 | 9 |
| `compose.attachWidth` | `historyAttach.width` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1334 | 44 |
| `compose.attachHeight` | `historyAttach.height` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1335 | 46 |
| `compose.fieldRadius` | `defaultInputField.borderRadius` | lib_ui/ui/widgets/widgets.style:1052 | 0 |
| `compose.textMargin` | `historyComposeField.textMargins` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1309 | 0 |
| `compose.placeholderInset` | `historyComposeField.placeholderMargins.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1318 | 2 |
| `compose.sendWidth` | `historySendSize.width` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1431 | 44 |
| `compose.sendHeight` | `historySendSize.height` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1431 | 46 |
| `compose.iconSize` | `historySendSize.width − 2 × historySend.sendIconPosition.x` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1524 | 24 |
| `compose.voiceIconSize` | `historySend.recordSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1535 | 26 |
| `compose.rippleSize` | `historyAttach.rippleAreaSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1341 | 40 |
| `compose.replyHeight` | `historyReplyHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1146 | 49 |
| `compose.replyTextLeft` | `historyReplySkip` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1144 | 53 |
| `compose.replyPreviewSize` | `historyReplyPreview` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:65 | 32 |
| `compose.replyTextTop` | `msgReplyPadding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:91 | 6 |
| `compose.replyTextRight` | `msgReplyPadding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:91 | 11 |
| `compose.replyTextBottom` | `msgReplyPadding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:91 | 6 |
| `compose.swapScale` | `kWideScale` | tdesktop/Telegram/SourceFiles/ui/controls/send_button.cpp:24 | 5 |
| `picker.width` | `emojiPanWidth` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:514 | 345 |
| `picker.minHeight` | `emojiPanMinHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:515 | 278 |
| `picker.maxHeight` | `emojiPanMaxHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:516 | 640 |
| `picker.heightRatio` | `emojiPanHeightRatio` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:517 | 0.75 |
| `picker.radius` | `emojiPanRadius` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:523 | 8 |
| `picker.cellWidth` | `defaultEmojiPan.desiredSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:799 | 37 |
| `picker.cellHeight` | `defaultEmojiPan.desiredSize − 2 × verticalSizeSub; EmojiListWidget::countDesiredHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:799-800 | 35 |
| `picker.imageSize` | `emojiPanArea.height` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:522 | 32 |
| `picker.paddingLeft` | `defaultEmojiPan.padding.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:797 | 7 |
| `picker.paddingRight` | `defaultEmojiPan.padding.right` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:797 | 4 |
| `picker.paddingBottom` | `defaultEmojiPan.padding.bottom` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:797 | 7 |
| `picker.headerHeight` | `defaultEmojiPan.header` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:801 | 33 |
| `picker.headerLeft` | `defaultEmojiPan.headerLeft` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:802 | 14 |
| `picker.headerTop` | `defaultEmojiPan.headerTop` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:805 | 10 |
| `picker.marginLeft` | `defaultEmojiPan.margin.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:796 | 7 |
| `picker.marginRight` | `defaultEmojiPan.margin.right` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:796 | 7 |
| `picker.scrollbarWidth` | `emojiScroll.width` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:791 | 7 |
| `picker.footerHeight` | `defaultEmojiPan.footer` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:806 | 36 |
| `picker.categoryWidth` | `defaultEmojiPan.iconWidth` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:808 | 30 |
| `picker.categoryGap` | `defaultEmojiPan.iconSkip` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:807 | 3 |
| `picker.categoryImageSize` | `defaultEmojiPan.iconArea` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:809 | 28 |
| `picker.gifPaddingLeft` | `gifsPadding.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:879 | 9 |
| `picker.gifPaddingTop` | `gifsPadding.top` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:879 | 5 |
| `picker.gifPaddingRight` | `gifsPadding.right` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:879 | 3 |
| `picker.gifPaddingBottom` | `gifsPadding.bottom` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:879 | 9 |
| `picker.tabHeight` | `defaultTabsSlider.height` | lib_ui/ui/widgets/widgets.style:1193 | 43 |
| `picker.tabLine` | `defaultTabsSlider.height − barTop` | lib_ui/ui/widgets/widgets.style:1193-1194 | 3 |
| `picker.searchHeight` | `defaultTabbedSearch.height` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:580 | 33 |
| `picker.searchLeft` | `defaultEmojiPan.searchMargin.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:833 | 1 |
| `picker.searchTop` | `defaultEmojiPan.searchMargin.top` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:833 | 11 |
| `picker.searchRight` | `defaultEmojiPan.searchMargin.right` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:833 | 2 |
| `picker.searchBottom` | `defaultEmojiPan.searchMargin.bottom` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:833 | 5 |
| `picker.searchTextLeft` | `defaultTabbedSearchField.textMargins.left` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:547 | 2 |
| `picker.stickerSize` | `stickerPanWidthMin` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:611 | 64 |
| `picker.stickerPadding` | `stickerPanPadding` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:614 | 11 |
| `picker.gifHeight` | `inlineMediaHeight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:934 | 96 |
| `picker.gifGap` | `inlineResultsSkip` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:933 | 3 |
| `picker.gifMinWidth` | `inlineResultsMinWidth` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:944 | 48 |
| `picker.shadowBlur` | `defaultBoxShadow.blurRadius` | lib_ui/ui/widgets/widgets.style:914 | 5 |
| `picker.shadowOffsetY` | `defaultBoxShadow.offset.y` | lib_ui/ui/widgets/widgets.style:915 | 1 |
| `picker.shadowOpacity` | `defaultBoxShadow.opacity` | lib_ui/ui/widgets/widgets.style:916 | 0.25 |
| `picker.showStartWidth` | `defaultPanelAnimation.startWidth` | lib_ui/ui/widgets/widgets.style:932 | 0.5 |
| `picker.showWidthFraction` | `defaultPanelAnimation.widthDuration` | lib_ui/ui/widgets/widgets.style:933 | 0.6 |
| `picker.showStartHeight` | `defaultPanelAnimation.startHeight` | lib_ui/ui/widgets/widgets.style:934 | 0.3 |
| `picker.showHeightFraction` | `defaultPanelAnimation.heightDuration` | lib_ui/ui/widgets/widgets.style:935 | 0.9 |
| `picker.showFadeStart` | `defaultPanelAnimation.startFadeTop` | lib_ui/ui/widgets/widgets.style:938 | 0 |
| `picker.showFadeHeight` | `defaultPanelAnimation.fadeHeight` | lib_ui/ui/widgets/widgets.style:939 | 0.2 |
| `picker.showFadeOpacity` | `defaultPanelAnimation.fadeOpacity` | lib_ui/ui/widgets/widgets.style:940 | 1 |
| `picker.showOpacityFraction` | `defaultPanelAnimation.opacityDuration` | lib_ui/ui/widgets/widgets.style:937 | 0.3 |
| `attachment.width` | `sendMediaPreviewSize` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:188 | 308 |
| `attachment.gap` | `sendMediaRowSkip` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:190 | 10 |
| `attachment.thumbSize` | `attachPreviewThumbLayout.thumbSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:417 | 64 |
| `attachment.fileSize` | `attachPreviewLayout.thumbSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:410 | 44 |
| `attachment.thumbGap` | `attachPreviewThumbLayout.thumbSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:418 | 10 |
| `attachment.fileGap` | `attachPreviewLayout.thumbSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:411 | 11 |
| `attachment.actionSize` | `editMediaButtonSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:679 | 32 |
| `attachment.nameTop` | `attachPreviewThumbLayout.nameTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:415 | 7 |
| `attachment.statusTop` | `attachPreviewThumbLayout.statusTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:416 | 37 |
| `attachment.fileNameTop` | `attachPreviewLayout.nameTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:408 | 6 |
| `attachment.fileStatusTop` | `attachPreviewLayout.statusTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:409 | 27 |
| `sidebar.width` | `windowFiltersWidth` | tdesktop/Telegram/SourceFiles/window/window.style:229 | 72 |
| `sidebar.itemMinHeight` | `windowFiltersButton.minHeight` | tdesktop/Telegram/SourceFiles/window/window.style:233 | 62 |
| `sidebar.textTop` | `windowFiltersButton.textTop` | tdesktop/Telegram/SourceFiles/window/window.style:231 | 40 |
| `sidebar.textGap` | `windowFiltersButton.textSkip` | tdesktop/Telegram/SourceFiles/window/window.style:232 | 6 |
| `sidebar.badgeHeight` | `windowFiltersButton.badgeHeight` | tdesktop/Telegram/SourceFiles/window/window.style:242 | 17 |
| `sidebar.badgeStroke` | `windowFiltersButton.badgeStroke` | tdesktop/Telegram/SourceFiles/window/window.style:243 | 2 |
| `menu.paddingLeft` | `defaultMenu.itemPadding.left` | lib_ui/ui/widgets/widgets.style:969 | 17 |
| `menu.paddingTop` | `defaultMenu.itemPadding.top` | lib_ui/ui/widgets/widgets.style:969 | 8 |
| `menu.paddingRight` | `defaultMenu.itemPadding.right` | lib_ui/ui/widgets/widgets.style:969 | 17 |
| `menu.paddingBottom` | `defaultMenu.itemPadding.bottom` | lib_ui/ui/widgets/widgets.style:969 | 7 |
| `menu.minWidth` | `defaultMenu.widthMin` | lib_ui/ui/widgets/widgets.style:980 | 156 |
| `menu.maxWidth` | `defaultMenu.widthMax` | lib_ui/ui/widgets/widgets.style:981 | 300 |
| `menu.radius` | `defaultPopupMenu.radius` | lib_ui/ui/widgets/widgets.style:995 | 8 |
| `scrollbar.width` | `defaultScrollArea.width − 2 × deltax` | lib_ui/ui/widgets/widgets.style:815 | 4 |
| `scrollbar.trackWidth` | `defaultScrollArea.width` | lib_ui/ui/widgets/widgets.style:815 | 10 |
| `scrollbar.minHeight` | `defaultScrollArea.minHeight` | lib_ui/ui/widgets/widgets.style:816 | 20 |
| `scrollbar.radius` | `defaultScrollArea.round` | lib_ui/ui/widgets/widgets.style:813 | 2 |
| `navigation.toLatestWidth` | `historyToDown.width` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1208 | 52 |
| `navigation.toLatestHeight` | `historyToDown.height` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1209 | 62 |
| `navigation.toLatestRippleSize` | `historyToDown.rippleAreaSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1218 | 42 |
| `service.unreadHeight` | `historyUnreadBarHeight` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:353 | 32 |
| `service.unreadMargin` | `historyUnreadBarMargin` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:354 | 8 |
| `layout.minWidth` | `windowMinWidth` | tdesktop/Telegram/SourceFiles/window/window.style:13 | 380 |
| `layout.minHeight` | `windowMinHeight` | tdesktop/Telegram/SourceFiles/window/window.style:14 | 480 |
| `layout.defaultWidth` | `windowDefaultWidth` | tdesktop/Telegram/SourceFiles/window/window.style:15 | 800 |
| `layout.defaultHeight` | `windowDefaultHeight` | tdesktop/Telegram/SourceFiles/window/window.style:16 | 600 |
| `layout.bigWidth` | `windowBigDefaultWidth` | tdesktop/Telegram/SourceFiles/window/window.style:17 | 1024 |
| `layout.bigHeight` | `windowBigDefaultHeight` | tdesktop/Telegram/SourceFiles/window/window.style:18 | 768 |
| `layout.listMinWidth` | `columnMinimalWidthLeft` | tdesktop/Telegram/SourceFiles/window/window.style:34 | 260 |
| `layout.listMaxWidth` | `columnMaximalWidthLeft (library cap; upstream clamp disabled)` | tdesktop/Telegram/SourceFiles/window/window.style:35; window_session_controller.cpp:2825 | 540 |
| `layout.chatMinWidth` | `columnMinimalWidthMain` | tdesktop/Telegram/SourceFiles/window/window.style:36 | 380 |
| `layout.thirdMinWidth` | `columnMinimalWidthThird` | tdesktop/Telegram/SourceFiles/window/window.style:37 | 292 |
| `layout.thirdMaxWidth` | `columnMaximalWidthThird` | tdesktop/Telegram/SourceFiles/window/window.style:38 | 392 |
| `layout.resizeHitWidth` | `historyResizeWidth` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:203 | 6 |
| `layout.listRatio` | `kDefaultDialogsWidthRatio (Windows)` | tdesktop/Telegram/SourceFiles/core/core_settings.h:1073 | 0.35714285714285715 |
| `layout.wideChatWidth` | `adaptiveChatWideWidth` | tdesktop/Telegram/SourceFiles/window/window.style:40 | 880 |
| `layout.folderTabsHeight` | `chatsFiltersTabs.height inherited from dialogsSearchTabs` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:764-785 | 33 |
| `conversation.dateSkip` | `dialogsDateSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:89 | 5 |
| `conversation.sendStateWidth` | `dialogsSendStateSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:404 | 20 |
| `conversation.iconSize` | `dialogsPinnedIcon canvas (Lucide replacement)` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_pinned.png | 20 |
| `conversation.typeSkip` | `dialogsChatTypeSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:387 | 3 |
| `conversation.onlineSize` | `dialogsOnlineBadgeSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:186 | 10 |
| `conversation.onlineStroke` | `dialogsOnlineBadgeStroke` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:185 | 2 |
| `conversation.onlineSkipY` | `dialogsOnlineBadgeSkip.y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:187 | 2 |
| `conversation.taggedHeight` | `taggedDialogRow.height` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:115 | 72 |
| `conversation.taggedPreviewTop` | `taggedDialogRow.textTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:116 | 30 |
| `conversation.tagTop` | `taggedDialogRow.tagTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:117 | 52 |
| `conversation.tagGap` | `dialogRowFilterTagSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:158 | 4 |
| `conversation.forumHeight` | `forumDialogRow.height` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:120 | 80 |
| `conversation.forumPreviewTop` | `forumDialogRow.textTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:121 | 32 |
| `conversation.topicHeight` | `forumDialogRow.topicsHeight` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:124 | 21 |
| `conversation.taggedForumHeight` | `taggedForumDialogRow.height` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:127 | 96 |
| `conversation.forumTagTop` | `taggedForumDialogRow.tagTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:128 | 77 |
| `search.height` | `dialogsFilter.heightMin` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:338 | 35 |
| `search.radius` | `dialogsFilter.borderRadius` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:333 | 18 |
| `search.paddingLeft` | `dialogsFilter.textMargins.left` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:317 | 12 |
| `search.paddingTop` | `dialogsFilter.textMargins.top` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:317 | 8 |
| `search.paddingRight` | `dialogsFilter.textMargins.right` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:317 | 30 |
| `search.paddingBottom` | `dialogsFilter.textMargins.bottom` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:317 | 5 |
| `search.border` | `dialogsFilter.border / borderDenominator` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:331-334 | 1.5 |
| `search.borderActive` | `dialogsFilter.borderActive / borderDenominator` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:332-334 | 1 |
| `search.clearSize` | `dialogsCancelSearch.width/height` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:348-349 | 35 |
| `search.clearInset` | `dialogsCancelSearch.cross.skip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:353 | 12 |
| `search.outerPadding` | `dialogsFilterPadding` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:92 | 7 |
| `search.outerSkip` | `dialogsFilterSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:93 | 4 |
| `search.groupHeight` | `searchedBarHeight` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:808 | 28 |
| `search.groupLeft` | `searchedBarPosition.x` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:810 | 14 |
| `search.groupTop` | `searchedBarPosition.y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:810 | 5 |
| `sidebar.iconSize` | `folders_work canvas (Lucide replacement)` | tdesktop/Telegram/Resources/icons/folders/folders_work.png | 36 |
| `sidebar.iconTop` | `windowFiltersButton.iconPosition.y` | tdesktop/Telegram/SourceFiles/window/window.style:245 | 6 |
| `sidebar.badgeOffsetX` | `windowFiltersButton.badgePosition.x` | tdesktop/Telegram/SourceFiles/window/window.style:244 | 3 |
| `sidebar.badgeTop` | `windowFiltersButton.badgePosition.y` | tdesktop/Telegram/SourceFiles/window/window.style:244 | 7 |
| `sidebar.badgePadding` | `windowFiltersButton.badgeSkip` | tdesktop/Telegram/SourceFiles/window/window.style:241 | 4 |
| `sidebar.textOnlyTop` | `windowFiltersButtonTextOnly.textTop` | tdesktop/Telegram/SourceFiles/window/window.style:248 | 15 |
| `sidebar.textOnlyHeight` | `windowFiltersButtonTextOnly.minHeight` | tdesktop/Telegram/SourceFiles/window/window.style:249 | 44 |
| `sidebar.iconsOnlyHeight` | `windowFiltersButtonIconsOnly.minHeight` | tdesktop/Telegram/SourceFiles/window/window.style:254 | 54 |
| `activity.dotGap` | `historySendActionTypingDelta` | lib_ui/ui/widgets/widgets.style:1341 | 6 |
| `activity.dotLargeRadius` | `historySendActionTypingLargeNumerator / Denominator` | lib_ui/ui/widgets/widgets.style:1342-1344 | 2.3333333 |
| `activity.dotSmallRadius` | `historySendActionTypingSmallNumerator / Denominator` | lib_ui/ui/widgets/widgets.style:1343-1344 | 1.3333333 |
| `conversation.tagAlpha` | `ChatsFilterTag background alpha` | tdesktop/Telegram/SourceFiles/ui/chat/chats_filter_tag.cpp:261 | 0.15 |
| `sidebar.textOnlyBadgeOffsetX` | `windowFiltersButtonTextOnly.badgePosition.x` | tdesktop/Telegram/SourceFiles/window/window.style:250 | 15 |
| `sidebar.textOnlyBadgeTop` | `windowFiltersButtonTextOnly.badgePosition.y` | tdesktop/Telegram/SourceFiles/window/window.style:250 | 1 |
| `activity.dotLeft` | `historySendActionTypingPosition.x` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:102-105; lib_ui/ui/widgets/widgets.style:1340 | 4 |
| `conversation.groupIcon.width` | `dialogsChatIcon bitmap width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_chat.png | 16 |
| `conversation.groupIcon.height` | `dialogsChatIcon bitmap height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_chat.png | 10 |
| `conversation.groupIcon.left` | `dialogsChatIcon.icon.position.x` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:389 | 1 |
| `conversation.groupIcon.top` | `dialogsChatIcon.icon.position.y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:389 | 4 |
| `conversation.channelIcon.width` | `dialogsChannelIcon bitmap width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_channel.png | 12 |
| `conversation.channelIcon.height` | `dialogsChannelIcon bitmap height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_channel.png | 11 |
| `conversation.channelIcon.left` | `dialogsChannelIcon.icon.position.x` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:394 | 3 |
| `conversation.channelIcon.top` | `dialogsChannelIcon.icon.position.y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:394 | 4 |
| `conversation.botIcon.width` | `dialogsBotIcon bitmap width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_bot.png | 16 |
| `conversation.botIcon.height` | `dialogsBotIcon bitmap height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_bot.png | 11 |
| `conversation.botIcon.left` | `dialogsBotIcon.icon.position.x` | tdesktop/Telegram/SourceFiles/dialogs/ui/dialogs_layout.style:41 | 1 |
| `conversation.botIcon.top` | `dialogsBotIcon.icon.position.y` | tdesktop/Telegram/SourceFiles/dialogs/ui/dialogs_layout.style:41 | 3 |
| `conversation.sendingIcon.width` | `dialogsSendingIcon bitmap width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_sending.png | 11 |
| `conversation.sendingIcon.height` | `dialogsSendingIcon bitmap height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_sending.png | 11 |
| `conversation.sendingIcon.left` | `dialogsSendingIcon.icon.position.x` | tdesktop/Telegram/SourceFiles/dialogs/ui/dialogs_layout.style:51 | 8 |
| `conversation.sendingIcon.top` | `dialogsSendingIcon.icon.position.y` | tdesktop/Telegram/SourceFiles/dialogs/ui/dialogs_layout.style:51 | 4 |
| `conversation.sentIcon.width` | `dialogsSentIcon bitmap width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_sent.png | 12 |
| `conversation.sentIcon.height` | `dialogsSentIcon bitmap height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_sent.png | 10 |
| `conversation.sentIcon.left` | `dialogsSentIcon.icon.position.x` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:406 | 10 |
| `conversation.sentIcon.top` | `dialogsSentIcon.icon.position.y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:406 | 4 |
| `conversation.readIcon.width` | `dialogsReceivedIcon bitmap width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_received.png | 17 |
| `conversation.readIcon.height` | `dialogsReceivedIcon bitmap height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_received.png | 10 |
| `conversation.readIcon.left` | `dialogsReceivedIcon.icon.position.x` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:411 | 5 |
| `conversation.readIcon.top` | `dialogsReceivedIcon.icon.position.y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:411 | 4 |
| `conversation.muteIconSize` | `dialogsMuteIcon bitmap width/height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_mute.png | 8 |
| `conversation.muteSkip` | `dialogsMuteIconSkip` | tdesktop/Telegram/SourceFiles/dialogs/ui/dialogs_layout.style:112 | 4 |
| `sidebar.lockedOpacity` | `kPremiumLockedOpacity` | lib_ui/ui/widgets/side_bar_button.cpp:20 | 0.6 |
| `header.titleLeft` | `topBarArrowPadding.right` | tdesktop/Telegram/SourceFiles/info/info.style:923 | 17 |
| `header.titleTop` | `topBarArrowPadding.top` | tdesktop/Telegram/SourceFiles/info/info.style:923 | 8 |
| `header.statusBottom` | `topBarArrowPadding.bottom` | tdesktop/Telegram/SourceFiles/info/info.style:923 | 8 |
| `header.menuWidth` | `topBarMenuToggle.width` | tdesktop/Telegram/SourceFiles/info/info.style:950 | 44 |
| `header.rippleSize` | `topBarSearch.rippleAreaSize` | tdesktop/Telegram/SourceFiles/info/info.style:937 | 40 |
| `header.iconSize` | `Lucide viewBox (asset substitution; exceptions.md)` | CrychicUI/icon/svg/search.svg:6 | 24 |
| `message.timestampDeltaX` | `msgDateDelta.x` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:100 | 2 |
| `message.timestampDeltaY` | `msgDateDelta.y` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:100 | 5 |
| `message.tailWidth` | `bubble_tail 100% canvas width (geometry only)` | tdesktop/Telegram/Resources/icons/bubble_tail.png | 6 |
| `message.tailHeight` | `bubble_tail 100% fill canvas height (shadow separate)` | tdesktop/Telegram/Resources/icons/bubble_tail.png | 10 |
| `message.avatarLeft` | `historyPhotoLeft` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:329 | 14 |
| `service.paddingLeft` | `msgServicePadding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:95 | 12 |
| `service.paddingTop` | `msgServicePadding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:95 | 3 |
| `service.paddingRight` | `msgServicePadding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:95 | 12 |
| `service.paddingBottom` | `msgServicePadding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:95 | 4 |
| `service.marginLeft` | `msgServiceMargin.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:96 | 10 |
| `service.marginTop` | `msgServiceMargin.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:96 | 10 |
| `service.marginRight` | `msgServiceMargin.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:96 | 10 |
| `service.marginBottom` | `msgServiceMargin.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:96 | 2 |
| `navigation.toLatestRight` | `historyToDownPosition.x` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1195 | 12 |
| `navigation.toLatestBottom` | `historyToDownPosition.y` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1195 | 10 |
| `navigation.toLatestRippleLeft` | `historyToDown.rippleAreaPosition.x` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1217 | 5 |
| `navigation.toLatestRippleTop` | `historyToDown.rippleAreaPosition.y` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1217 | 15 |
| `navigation.toLatestBadgeHeight` | `historyToDownBadgeSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1224 | 22 |
| `navigation.toLatestBadgePadding` | `PaintUnreadBadge padding` | tdesktop/Telegram/SourceFiles/ui/controls/jump_down_button.cpp:56 | 4 |
| `navigation.toLatestShownAfter` | `historyToDownShownAfter` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1226 | 480 |
| `quote.replyPaddingLeft` | `historyReplyPadding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:73 | 11 |
| `quote.replyPaddingTop` | `historyReplyPadding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:73 | 2 |
| `quote.replyPaddingRight` | `historyReplyPadding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:73 | 6 |
| `quote.replyPaddingBottom` | `historyReplyPadding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:73 | 2 |
| `quote.previewSize` | `historyReplyPreview` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:65 | 32 |
| `quote.previewGap` | `historyReplyPreviewMargin.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:66 | 4 |
| `quote.backgroundOpacity` | `kDefaultBgOpacity (unitless)` | tdesktop/Telegram/SourceFiles/ui/chat/chat_style.h:56 | 0.12 |
| `quote.outlineOpacity` | `kDefaultOutline1Opacity (unitless)` | tdesktop/Telegram/SourceFiles/ui/chat/chat_style.h:57 | 0.9 |
| `quote.collapsedLines` | `kNonExpandedLinesLimit (count)` | tdesktop/Telegram/SourceFiles/history/view/history_view_reply.cpp:46 | 5 |
| `quote.textPaddingRight` | `historyTextStyle.blockquote.padding.right` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1291 | 20 |
| `quote.codeHeaderHeight` | `historyTextStyle.pre.header` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1300 | 20 |
| `reaction.inOpacity` | `kInNonChosenOpacity (unitless)` | tdesktop/Telegram/SourceFiles/history/view/reactions/history_view_reactions.cpp:38 | 0.12 |
| `reaction.outOpacity` | `kOutNonChosenOpacity (unitless)` | tdesktop/Telegram/SourceFiles/history/view/reactions/history_view_reactions.cpp:39 | 0.18 |
| `reaction.rippleOpacity` | `rippleColor alpha=25/255 (unitless)` | tdesktop/Telegram/SourceFiles/history/view/reactions/history_view_reactions.cpp:556 | 0.098039216 |
| `reaction.userpicSize` | `reactionInlineUserpics.size` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:691 | 20 |
| `reaction.userpicOverlap` | `reactionInlineUserpics.shift` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:692 | 7 |
| `reaction.userpicStroke` | `reactionInlineUserpics.stroke` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:693 | 1 |
| `media.badgeInset` | `msgDateImgDelta` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:102 | 4 |
| `media.badgePaddingX` | `msgDateImgPadding.x` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:103 | 8 |
| `media.badgePaddingY` | `msgDateImgPadding.y` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:103 | 2 |
| `media.actionSize` | `msgFileLayout.thumbSize / historyGroupRadialSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:384,511 | 44 |
| `media.actionIconSize` | `Lucide native viewBox (exception)` | CrychicUI/icon/svg/play.svg:6 | 24 |
| `preview.paddingLeft` | `historyPagePreview.padding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:108 | 10 |
| `preview.paddingTop` | `historyPagePreview.padding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:108 | 5 |
| `preview.paddingRight` | `historyPagePreview.padding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:108 | 7 |
| `preview.paddingBottom` | `historyPagePreview.padding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:108 | 7 |
| `preview.imageGap` | `webPagePhotoDelta` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:535 | 8 |
| `preview.maxLines` | `linesMax (count)` | tdesktop/Telegram/SourceFiles/history/view/media/history_view_web_page.cpp:805 | 5 |
| `preview.mediaGap` | `mediaInBubbleSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:156 | 5 |
| `file.minWidth` | `msgFileMinWidth` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:423 | 268 |
| `file.paddingLeft` | `msgFileLayout.padding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:381 | 12 |
| `file.paddingTop` | `msgFileLayout.padding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:381 | 8 |
| `file.paddingRight` | `msgFileLayout.padding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:381 | 10 |
| `file.paddingBottom` | `msgFileLayout.padding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:381 | 8 |
| `file.nameTop` | `msgFileLayout.nameTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:382 | 12 |
| `file.statusTop` | `msgFileLayout.statusTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:383 | 34 |
| `file.iconSize` | `msgFileLayout.thumbSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:384 | 44 |
| `file.iconGap` | `msgFileLayout.thumbSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:385 | 11 |
| `file.thumbSize` | `msgFileThumbLayout.thumbSize` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:392 | 72 |
| `file.thumbGap` | `msgFileThumbLayout.thumbSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:393 | 14 |
| `file.thumbRadius` | `msgFileThumbRadiusSmall` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:378 | 4 |
| `voice.barWidth` | `msgWaveformBar` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:429 | 2 |
| `voice.barGap` | `msgWaveformSkip` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:430 | 1 |
| `voice.minHeight` | `msgWaveformMin` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:431 | 3 |
| `voice.maxHeight` | `msgWaveformMax` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:432 | 17 |
| `quote.textCollapsedLines` | `kQuoteCollapsedLines (count)` | lib_ui/ui/text/text_renderer.h:17 | 3 |
| `preview.titleLines` | `twoTitleLines (count)` | tdesktop/Telegram/SourceFiles/history/view/media/history_view_web_page.cpp:807 | 2 |
| `file.thumbPaddingLeft` | `msgFileThumbLayout.padding.left` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:388 | 6 |
| `file.thumbPaddingTop` | `msgFileThumbLayout.padding.top` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:388 | 6 |
| `file.thumbPaddingRight` | `msgFileThumbLayout.padding.right` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:388 | 10 |
| `file.thumbPaddingBottom` | `msgFileThumbLayout.padding.bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:388 | 6 |
| `file.thumbNameTop` | `msgFileThumbLayout.nameTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:389 | 11 |
| `file.thumbStatusTop` | `msgFileThumbLayout.statusTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:390 | 31 |
| `file.thumbLinkTop` | `msgFileThumbLayout.linkTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:391 | 55 |
| `media.progressWidth` | `msgFileRadialLine / historyGroupRadialLine` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:427,512 | 3 |
| `quote.codeDarkOpacity` | `preBgOverride alpha=192/255 (unitless)` | tdesktop/Telegram/SourceFiles/ui/chat/chat_style.cpp:785-787 | 0.7529412 |
| `quote.codeHeaderOpacity` | `kDefaultOutline2Opacity (unitless)` | tdesktop/Telegram/SourceFiles/ui/chat/chat_style.h:58 | 0.3 |
| `forward.radius` | `nameRippleRadius` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:147 | 4 |
| `menu.iconPaddingLeft` | `menuWithIcons.itemPadding.left` | lib_ui/ui/widgets/widgets.style:1697 | 54 |
| `menu.iconPaddingBottom` | `menuWithIcons.itemPadding.bottom` | lib_ui/ui/widgets/widgets.style:1697 | 8 |
| `menu.iconLeft` | `menuWithIcons.itemIconPosition.x` | lib_ui/ui/widgets/widgets.style:1696 | 15 |
| `menu.iconTop` | `menuWithIcons.itemIconPosition.y` | lib_ui/ui/widgets/widgets.style:1696 | 5 |
| `menu.iconSize` | `menuIconCopy canvas (replacement Lucide viewBox)` | tdesktop/Telegram/Resources/icons/menu/copy.png | 24 |
| `menu.scrollPadding` | `defaultPopupMenu.scrollPadding.top/bottom` | lib_ui/ui/widgets/widgets.style:990 | 8 |
| `menu.iconScrollPadding` | `popupMenuWithIcons.scrollPadding.top/bottom` | lib_ui/ui/widgets/widgets.style:1710 | 5 |
| `menu.rightSkip` | `defaultMenu.itemRightSkip` | lib_ui/ui/widgets/widgets.style:970 | 6 |
| `menu.separatorPadding` | `defaultMenuSeparator.padding.top/bottom` | lib_ui/ui/widgets/widgets.style:953 | 5 |
| `menu.separatorWidth` | `defaultMenuSeparator.width` | lib_ui/ui/widgets/widgets.style:954 | 1 |
| `menu.expandedSeparatorPadding` | `expandedMenuSeparator.padding.top/bottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:616 | 4 |
| `menu.expandedSeparatorWidth` | `expandedMenuSeparator.width` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:617 | 6 |
| `menu.shadowBlur` | `defaultBoxShadow.blurRadius` | lib_ui/ui/widgets/widgets.style:914 | 5 |
| `menu.shadowY` | `defaultBoxShadow.offset.y` | lib_ui/ui/widgets/widgets.style:915 | 1 |
| `menu.shadowOpacity` | `defaultBoxShadow.opacity` | lib_ui/ui/widgets/widgets.style:916 | 0.25 |
| `menu.startWidth` | `defaultPanelAnimation.startWidth` | lib_ui/ui/widgets/widgets.style:932 | 0.5 |
| `menu.widthFraction` | `defaultPanelAnimation.widthDuration` | lib_ui/ui/widgets/widgets.style:933 | 0.6 |
| `menu.startHeight` | `defaultPanelAnimation.startHeight` | lib_ui/ui/widgets/widgets.style:934 | 0.3 |
| `menu.heightFraction` | `defaultPanelAnimation.heightDuration` | lib_ui/ui/widgets/widgets.style:935 | 0.9 |
| `menu.opacityFraction` | `defaultPanelAnimation.opacityDuration` | lib_ui/ui/widgets/widgets.style:937 | 0.3 |
| `menu.fadeHeight` | `defaultPanelAnimation.fadeHeight` | lib_ui/ui/widgets/widgets.style:939 | 0.2 |
| `tooltip.paddingX` | `defaultTooltip.textPadding.left/right` | lib_ui/ui/widgets/widgets.style:1281 | 5 |
| `tooltip.paddingY` | `defaultTooltip.textPadding.top/bottom` | lib_ui/ui/widgets/widgets.style:1281 | 2 |
| `tooltip.shiftX` | `abs(defaultTooltip.shift.x)` | lib_ui/ui/widgets/widgets.style:1283 | 20 |
| `tooltip.shiftY` | `defaultTooltip.shift.y` | lib_ui/ui/widgets/widgets.style:1283 | 20 |
| `tooltip.edgeSkip` | `defaultTooltip.skip` | lib_ui/ui/widgets/widgets.style:1284 | 10 |
| `tooltip.maxWidth` | `defaultTooltip.widthMax` | lib_ui/ui/widgets/widgets.style:1286 | 800 |
| `tooltip.maxLines` | `defaultTooltip.linesMax` | lib_ui/ui/widgets/widgets.style:1287 | 12 |
| `dialog.width` | `boxWidth` | lib_ui/ui/layers/layers.style:117 | 320 |
| `dialog.wideWidth` | `boxWideWidth` | lib_ui/ui/layers/layers.style:118 | 364 |
| `dialog.radius` | `boxRadius` | lib_ui/ui/layers/layers.style:38 | 8 |
| `dialog.paddingLeft` | `boxPadding.left` | lib_ui/ui/layers/layers.style:119 | 24 |
| `dialog.paddingTop` | `boxPadding.top` | lib_ui/ui/layers/layers.style:119 | 14 |
| `dialog.paddingRight` | `boxPadding.right` | lib_ui/ui/layers/layers.style:119 | 24 |
| `dialog.paddingBottom` | `boxPadding.bottom` | lib_ui/ui/layers/layers.style:119 | 8 |
| `dialog.titleLeft` | `boxTitlePosition.x` | lib_ui/ui/layers/layers.style:81 | 24 |
| `dialog.titleTop` | `boxTitlePosition.y` | lib_ui/ui/layers/layers.style:81 | 13 |
| `dialog.titleHeight` | `boxTitleHeight` | lib_ui/ui/layers/layers.style:82 | 48 |
| `dialog.titleGap` | `boxTitleAdditionalSkip` | lib_ui/ui/layers/layers.style:83 | 9 |
| `dialog.lineHeight` | `boxLabelStyle.lineHeight` | lib_ui/ui/layers/layers.style:52 | 22 |
| `dialog.buttonHeight` | `defaultBox.buttonHeight` | lib_ui/ui/layers/layers.style:126 | 34 |
| `dialog.buttonGap` | `defaultBox.buttonPadding.left` | lib_ui/ui/layers/layers.style:125 | 6 |
| `dialog.buttonPadding` | `abs(defaultBoxButton.width)/2` | lib_ui/ui/layers/layers.style:45 | 15 |
| `dialog.buttonTop` | `defaultBox.buttonPadding.top` | lib_ui/ui/layers/layers.style:125 | 10 |
| `dialog.buttonRight` | `defaultBox.buttonPadding.right` | lib_ui/ui/layers/layers.style:125 | 10 |
| `dialog.buttonBottom` | `defaultBox.buttonPadding.bottom` | lib_ui/ui/layers/layers.style:125 | 10 |
| `dialog.margin` | `defaultBox.margin.top/bottom` | lib_ui/ui/layers/layers.style:128 | 10 |
| `dialog.maxBodyHeight` | `boxMaxListHeight` | lib_ui/ui/layers/layers.style:120 | 492 |
| `toast.paddingLeft` | `defaultToast.padding.left` | lib_ui/ui/widgets/widgets.style:1553 | 19 |
| `toast.paddingTop` | `defaultToast.padding.top` | lib_ui/ui/widgets/widgets.style:1553 | 13 |
| `toast.paddingRight` | `defaultToast.padding.right` | lib_ui/ui/widgets/widgets.style:1553 | 19 |
| `toast.paddingBottom` | `defaultToast.padding.bottom` | lib_ui/ui/widgets/widgets.style:1553 | 12 |
| `toast.margin` | `defaultToast.margin` | lib_ui/ui/widgets/widgets.style:1554 | 13 |
| `toast.maxWidth` | `defaultToast.maxWidth` | lib_ui/ui/widgets/widgets.style:1555 | 480 |
| `toast.radius` | `defaultToast.radius` | lib_ui/ui/widgets/widgets.style:1556 | 8 |
| `toast.multilineMinWidth` | `defaultMultilineToast.minWidth` | lib_ui/ui/widgets/widgets.style:1563 | 160 |
| `toast.multilineMaxWidth` | `defaultMultilineToast.maxWidth` | lib_ui/ui/widgets/widgets.style:1564 | 360 |
| `toast.iconSize` | `defaultToastLottieSize.width` | lib_ui/ui/widgets/widgets.style:1548 | 32 |
| `toast.iconPaddingX` | `defaultToastIconPadding.left/right` | lib_ui/ui/widgets/widgets.style:1549 | 12 |
| `toast.iconPaddingY` | `defaultToastIconPadding.top/bottom` | lib_ui/ui/widgets/widgets.style:1549 | 2 |
| `viewer.controlWidth` | `mediaviewControlSize` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:272 | 90 |
| `viewer.actionWidth` | `mediaviewIconSize.width` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:273 | 46 |
| `viewer.actionHeight` | `mediaviewIconSize.height` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:273 | 54 |
| `viewer.hoverSize` | `mediaviewIconOver` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:274 | 36 |
| `viewer.infoLeft` | `mediaviewTextLeft` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:266 | 14 |
| `viewer.infoGap` | `mediaviewTextSkip` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:267 | 10 |
| `viewer.headerOffset` | `mediaviewHeaderTop` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:269 | 47 |
| `viewer.textOffset` | `mediaviewTextTop` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:270 | 26 |
| `viewer.maxZoomLevel` | `kMaxZoomLevel` | tdesktop/Telegram/SourceFiles/media/view/media_view_overlay_widget.cpp:188 | 7 |
| `viewer.pinchStep` | `kPinchZoomStep` | tdesktop/Telegram/SourceFiles/media/view/media_view_overlay_widget.cpp:190 | 0.25 |
| `viewer.captionPaddingX` | `mediaviewCaptionPadding.left/right` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:287 | 11 |
| `viewer.captionPaddingY` | `mediaviewCaptionPadding.top/bottom` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:287 | 6 |
| `viewer.captionMargin` | `mediaviewCaptionMargin.width/height` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:288 | 11 |
| `viewer.captionRadius` | `mediaviewCaptionRadius` | tdesktop/Telegram/SourceFiles/media/view/media_view.style:289 | 6 |
| `viewer.loadingSize` | `radialSize` | lib_ui/ui/basic.style:122 | 50 |
| `viewer.loadingStroke` | `radialLine` | lib_ui/ui/basic.style:123 | 3 |
| `viewer.loadingMinArc` | `arc::kMinLength / 16` | lib_ui/ui/arc_angles.h:15 | 1 |
| `viewer.loadingMinProgress` | `RadialAnimation::start safe progress` | lib_ui/ui/effects/radial_animation.cpp:24 | 0.0001 |
| `layout.pageShift` | `slideShift` | lib_ui/ui/basic.style:97 | 100 |
| `layout.pageShadowWidth` | `defaultBoxShadow.blurRadius (procedural shadow mapping)` | lib_ui/ui/widgets/widgets.style:914 | 5 |
| `layout.pageShadowOpacity` | `defaultBoxShadow.opacity (procedural shadow mapping)` | lib_ui/ui/widgets/widgets.style:916 | 0.25 |
| `message.historyBottom` | `historyPaddingBottom` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:205 | 8 |
| `media.bubbleMinWidth` | `historyPhotoBubbleMinWidth` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:330 | 200 |
| `spoiler.canvasSize` | `DefaultDescriptorText.canvasSize` | lib_ui/ui/effects/spoiler_mess.cpp:38 | 128 |
| `spoiler.sizeMin` | `particleSizeMin` | lib_ui/ui/effects/spoiler_mess.cpp:43 | 1.5 |
| `spoiler.sizeMax` | `particleSizeMax` | lib_ui/ui/effects/spoiler_mess.cpp:44 | 2 |
| `spoiler.speedMin` | `particleSpeedMin (DIP per lifetime)` | lib_ui/ui/effects/spoiler_mess.cpp:45 | 4 |
| `spoiler.speedMax` | `particleSpeedMax (DIP per lifetime)` | lib_ui/ui/effects/spoiler_mess.cpp:46 | 8 |
| `spoiler.sprites` | `particleSpritesCount` | lib_ui/ui/effects/spoiler_mess.cpp:47 | 5 |
| `spoiler.particles` | `particlesCount` | lib_ui/ui/effects/spoiler_mess.cpp:48 | 9000 |
| `spoiler.frames` | `kDefaultFramesCount` | lib_ui/ui/effects/spoiler_mess.cpp:33 | 60 |
| `spoiler.imageSpeedMin` | `DefaultDescriptorImage.particleSpeedMin` | lib_ui/ui/effects/spoiler_mess.cpp:64 | 10 |
| `spoiler.imageSpeedMax` | `DefaultDescriptorImage.particleSpeedMax` | lib_ui/ui/effects/spoiler_mess.cpp:65 | 20 |
| `spoiler.imageParticles` | `DefaultDescriptorImage.particlesCount` | lib_ui/ui/effects/spoiler_mess.cpp:67 | 3000 |
| `conversation.storyWidth` | `dialogsStoriesFull.lineTwice / 2` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:702 | 2 |
| `conversation.storyReadWidth` | `dialogsStoriesFull.lineReadTwice / 2` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:703 | 1 |
| `conversation.callSize` | `dialogsCallBadgeSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:190 | 16 |
| `conversation.callOutset` | `abs(dialogsCallBadgeSkip.x/y)` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:191 | 3 |
| `conversation.subscriptionSize` | `dialogsSubscriptionBadgeSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:193 | 16 |
| `conversation.subscriptionOutset` | `abs(dialogsSubscriptionBadgeSkip.x/y)` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:194 | 4 |
| `conversation.subscriptionStroke` | `dialogsSubscriptionBadgeOutlineTwice / 2` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:195 | 1.5 |
| `conversation.communitySize` | `dialogsCommunityBadgeSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:197 | 16 |
| `conversation.communityOutset` | `abs(dialogsCommunityBadgeSkip.x/y)` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:198 | 3 |
| `conversation.communityStroke` | `dialogsCommunityBadgeStroke` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:199 | 2 |
| `conversation.communityLine` | `dialogsCommunityBadgeLine` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:200 | 1 |
| `conversation.ttlSize` | `dialogsTTLBadgeSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:202 | 20 |
| `conversation.ttlInset` | `dialogsTTLBadgeInnerMargins` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:203 | 2 |
| `conversation.ttlOffset` | `dialogsTTLBadgeSkip.x/y` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:205 | 1 |
| `conversation.speakingStroke` | `dialogsSpeakingStrokeNumerator / Denominator` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:212-213 | 2 |
| `search.scopeHeight` | `dialogsSearchInHeight` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:474 | 38 |
| `search.scopePhoto` | `dialogsSearchInPhotoSize` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:475 | 28 |
| `search.scopeLeft` | `dialogsSearchInPhotoPadding` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:476 | 10 |
| `search.scopeGap` | `dialogsSearchInSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:477 | 10 |
| `search.scopeTop` | `dialogsSearchInNameTop` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:478 | 9 |
| `search.scopeArrowGap` | `dialogsSearchInDownSkip` | tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:481 | 4 |
| `activity.recordLeft` | `historySendActionRecordPosition.x` | lib_ui/ui/widgets/widgets.style:1347 | 1 |
| `activity.recordGap` | `historySendActionRecordDelta` | lib_ui/ui/widgets/widgets.style:1348 | 4 |
| `activity.recordStroke` | `historySendActionRecordStrokeNumerator / Denominator` | lib_ui/ui/widgets/widgets.style:1349-1350 | 2 |
| `activity.uploadLeft` | `historySendActionUploadPosition.x` | lib_ui/ui/widgets/widgets.style:1353 | 0 |
| `activity.uploadGap` | `historySendActionUploadDelta` | lib_ui/ui/widgets/widgets.style:1354 | 5 |
| `activity.uploadStroke` | `historySendActionUploadStrokeNumerator / Denominator` | lib_ui/ui/widgets/widgets.style:1355-1357 | 2 |
| `activity.uploadSize` | `historySendActionUploadSizeNumerator / Denominator` | lib_ui/ui/widgets/widgets.style:1356-1357 | 4 |
| `activity.eyeLeft` | `historySendActionChooseStickerPosition.x` | lib_ui/ui/widgets/widgets.style:1360 | 1 |
| `activity.eyeWidth` | `historySendActionChooseStickerEyeWidth` | lib_ui/ui/widgets/widgets.style:1361 | 7 |
| `activity.eyeHeight` | `historySendActionChooseStickerEyeHeight` | lib_ui/ui/widgets/widgets.style:1362 | 11 |
| `activity.eyeGap` | `historySendActionChooseStickerEyeStep` | lib_ui/ui/widgets/widgets.style:1363 | 2 |
| `activity.eyeOffset` | `ChooseStickerAnimation eye offsets` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:575,578,583 | 1.5 |
| `activity.eyeRightInset` | `ChooseStickerAnimation inRightOffset` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:576 | 2.5 |
| `activity.eyeStroke` | `ChooseStickerAnimation outStrokeWidth` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:579 | 1.04 |
| `activity.pupilStroke` | `ChooseStickerAnimation inStrokeWidth` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:580 | 1.56 |
| `activity.pupilSize` | `ChooseStickerAnimation inSize` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:581 | 2 |
| `activity.eyeStage` | `ChooseStickerAnimation minProgress` | tdesktop/Telegram/SourceFiles/ui/effects/send_action_animations.cpp:582 | 0.3 |
| `conversation.ttlBlur` | `kBlurRadius` | tdesktop/Telegram/SourceFiles/dialogs/dialogs_row.cpp:47 | 24 |
| `conversation.ttlDarken` | `kDarkerBy` | tdesktop/Telegram/SourceFiles/dialogs/dialogs_row.cpp:270 | 0.2 |
| `attachment.maxMediaHeight` | `confirmMaxHeight` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:169 | 245 |
| `attachment.minMediaWidth` | `kMinPreviewWidth` | tdesktop/Telegram/SourceFiles/ui/chat/attach/attach_abstract_single_media_preview.cpp:27 | 20 |
| `attachment.mediaHeightRatio` | `preparePreview maximum height ratio` | tdesktop/Telegram/SourceFiles/ui/chat/attach/attach_abstract_single_media_preview.cpp:143 | 1.5 |
| `attachment.controlWidth` | `sendBoxAlbumGroupSize.width` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:695 | 48 |
| `attachment.controlHeight` | `sendBoxAlbumGroupSize.height` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:695 | 26 |
| `attachment.controlTop` | `sendBoxAlbumGroupSkipTop` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:694 | 5 |
| `attachment.controlRight` | `sendBoxAlbumGroupSkipRight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:693 | 5 |
| `attachment.fileControlTop` | `sendBoxFileGroupSkipTop` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:700 | 2 |
| `attachment.fileControlRight` | `sendBoxFileGroupSkipRight` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:701 | 1 |
| `attachment.captionGap` | `attachPreviewCaptionTopOffset` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:420 | 6 |
| `compose.referenceIconLeft` | `historyReplyIconPosition.x` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1147 | 7 |
| `compose.referenceIconTop` | `historyReplyIconPosition.y` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1147 | 7 |
| `compose.referenceIconSize` | `historyReplyHeight - 2 * historyReplyIconPosition.y (Lucide slot)` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1146-1147 | 35 |
| `conversation.forumRadiusRatio` | `ForumUserpicRadiusMultiplier` | tdesktop/Telegram/SourceFiles/ui/userpic_view.cpp:34-35 | 0.3 |
| `conversation.ttlStroke` | `PaintTimerIcon kPenWidth` | tdesktop/Telegram/SourceFiles/ui/effects/ttl_icon.cpp:28 | 1.5 |
| `attachment.albumMinimum` | `historyGroupWidthMin / 2` | tdesktop/Telegram/SourceFiles/ui/chat/attach/attach_album_preview.cpp:163 | 50 |
| `attachment.albumGap` | `historyGroupSkip / 2` | tdesktop/Telegram/SourceFiles/ui/chat/attach/attach_album_preview.cpp:164 | 2 |
| `sidebar.slideShift` | `slideShift` | lib_ui/ui/basic.style:97 | 100 |
| `settings.sectionSkip` | `defaultVerticalListSkip` | lib_ui/ui/basic.style:130 | 6 |
| `settings.sectionTitlePaddingLeft` | `defaultSubsectionTitlePadding.left` | lib_ui/ui/layers/layers.style:155 | 22 |
| `settings.sectionTitlePaddingTop` | `defaultSubsectionTitlePadding.top` | lib_ui/ui/layers/layers.style:155 | 7 |
| `settings.sectionTitlePaddingRight` | `defaultSubsectionTitlePadding.right` | lib_ui/ui/layers/layers.style:155 | 10 |
| `settings.sectionTitlePaddingBottom` | `defaultSubsectionTitlePadding.bottom` | lib_ui/ui/layers/layers.style:155 | 9 |
| `settings.sectionTitleMinWidth` | `defaultSubsectionTitle.minWidth` | lib_ui/ui/layers/layers.style:153 | 240 |
| `settings.dividerHeight` | `boxDividerHeight` | lib_ui/ui/widgets/widgets.style:677 | 8 |
| `settings.dividerEdgeHeight` | `box_divider_top/bottom canvas height` | lib_ui/icons/box_divider_top.png; box_divider_bottom.png; ui/widgets/widgets.style:675-676 | 5 |
| `settings.dividerTextPaddingLeft` | `defaultBoxDividerLabelPadding.left` | lib_ui/ui/widgets/widgets.style:690 | 22 |
| `settings.dividerTextPaddingTop` | `defaultBoxDividerLabelPadding.top` | lib_ui/ui/widgets/widgets.style:690 | 8 |
| `settings.dividerTextPaddingRight` | `defaultBoxDividerLabelPadding.right` | lib_ui/ui/widgets/widgets.style:690 | 22 |
| `settings.dividerTextPaddingBottom` | `defaultBoxDividerLabelPadding.bottom` | lib_ui/ui/widgets/widgets.style:690 | 16 |
| `settings.dividerTextMinWidth` | `boxDividerLabel.minWidth` | lib_ui/ui/widgets/widgets.style:685 | 245 |
| `settings.buttonContentHeight` | `defaultSettingsButton.height` | lib_ui/ui/widgets/widgets.style:1519 | 20 |
| `settings.buttonPaddingLeft` | `settingsButton.padding.left` | tdesktop/Telegram/SourceFiles/settings/settings.style:15 | 60 |
| `settings.buttonPaddingTop` | `settingsButton.padding.top` | tdesktop/Telegram/SourceFiles/settings/settings.style:15 | 10 |
| `settings.buttonPaddingRight` | `settingsButton.padding.right` | tdesktop/Telegram/SourceFiles/settings/settings.style:15 | 22 |
| `settings.buttonPaddingBottom` | `settingsButton.padding.bottom` | tdesktop/Telegram/SourceFiles/settings/settings.style:15 | 10 |
| `settings.buttonNoIconPaddingLeft` | `settingsButtonNoIcon.padding.left` | tdesktop/Telegram/SourceFiles/settings/settings.style:26 | 22 |
| `settings.buttonNoIconPaddingTop` | `settingsButtonNoIcon.padding.top` | tdesktop/Telegram/SourceFiles/settings/settings.style:26 | 10 |
| `settings.buttonNoIconPaddingRight` | `settingsButtonNoIcon.padding.right` | tdesktop/Telegram/SourceFiles/settings/settings.style:26 | 22 |
| `settings.buttonNoIconPaddingBottom` | `settingsButtonNoIcon.padding.bottom` | tdesktop/Telegram/SourceFiles/settings/settings.style:26 | 8 |
| `settings.buttonIconLeft` | `settingsButton.iconLeft` | tdesktop/Telegram/SourceFiles/settings/settings.style:16 | 20 |
| `settings.buttonValueRight` | `settingsButtonRightSkip` | tdesktop/Telegram/SourceFiles/settings/settings.style:89 | 23 |
| `settings.buttonValueMaxHeight` | `defaultSettingsRightLabel.maxHeight` | lib_ui/ui/widgets/widgets.style:1508 | 20 |
| `settings.toggleBorder` | `defaultToggle.border` | lib_ui/ui/widgets/widgets.style:868 | 2 |
| `settings.toggleDiameter` | `defaultToggle.diameter` | lib_ui/ui/widgets/widgets.style:870 | 16 |
| `settings.toggleTravel` | `defaultToggle.width` | lib_ui/ui/widgets/widgets.style:871 | 14 |
| `settings.toggleTrackInset` | `defaultToggle.shift` | lib_ui/ui/widgets/widgets.style:869 | 1 |
| `settings.toggleRight` | `infoProfileButton.toggleSkip` | tdesktop/Telegram/SourceFiles/info/info.style:619 | 20 |
| `settings.toggleRipplePadding` | `defaultToggle.rippleAreaPadding` | lib_ui/ui/widgets/widgets.style:876 | 8 |
| `choice.radioDiameter` | `defaultRadio.diameter` | lib_ui/ui/widgets/widgets.style:855 | 22 |
| `choice.radioStroke` | `defaultRadio.thickness` | lib_ui/ui/widgets/widgets.style:856 | 2 |
| `choice.radioOuterInset` | `defaultRadio.outerSkip / 10` | lib_ui/ui/widgets/widgets.style:857; widgets/checkbox.cpp:413 | 1 |
| `choice.radioDotInset` | `defaultRadio.skip / 10` | lib_ui/ui/widgets/widgets.style:858; widgets/checkbox.cpp:426 | 6 |
| `choice.checkDiameter` | `defaultCheck.diameter` | lib_ui/ui/widgets/widgets.style:845 | 22 |
| `choice.checkStroke` | `defaultCheck.thickness` | lib_ui/ui/widgets/widgets.style:846 | 2 |
| `choice.ripplePadding` | `defaultCheck/Radio.rippleAreaPadding` | lib_ui/ui/widgets/widgets.style:849,860 | 8 |
| `choice.marginLeft` | `defaultCheckbox.margin.left` | lib_ui/ui/widgets/widgets.style:883 | 8 |
| `choice.marginTop` | `defaultCheckbox.margin.top` | lib_ui/ui/widgets/widgets.style:883 | 8 |
| `choice.marginRight` | `defaultCheckbox.margin.right` | lib_ui/ui/widgets/widgets.style:883 | 8 |
| `choice.marginBottom` | `defaultCheckbox.margin.bottom` | lib_ui/ui/widgets/widgets.style:883 | 8 |
| `choice.checkLeft` | `defaultCheckbox.checkPosition.x` | lib_ui/ui/widgets/widgets.style:886 | 8 |
| `choice.checkTop` | `defaultCheckbox.checkPosition.y` | lib_ui/ui/widgets/widgets.style:886 | 8 |
| `choice.textGap` | `defaultCheckbox.textPosition.x` | lib_ui/ui/widgets/widgets.style:885 | 10 |
| `choice.textTop` | `defaultCheckbox.textPosition.y` | lib_ui/ui/widgets/widgets.style:885 | 2 |
| `choice.boxTextGap` | `defaultBoxCheckbox.textPosition.x` | lib_ui/ui/layers/layers.style:67 | 12 |
| `choice.boxTextTop` | `defaultBoxCheckbox.textPosition.y` | lib_ui/ui/layers/layers.style:67 | 1 |
| `settings.checkTextGap` | `settingsCheckbox.textPosition.x` | tdesktop/Telegram/SourceFiles/settings/settings.style:171 | 15 |
| `settings.checkTextTop` | `settingsCheckbox.textPosition.y` | tdesktop/Telegram/SourceFiles/settings/settings.style:171 | 1 |
| `settings.checkPaddingLeft` | `settingsCheckboxPadding.left` | tdesktop/Telegram/SourceFiles/settings/settings.style:173 | 22 |
| `settings.checkPaddingTop` | `settingsCheckboxPadding.top` | tdesktop/Telegram/SourceFiles/settings/settings.style:173 | 10 |
| `settings.checkPaddingRight` | `settingsCheckboxPadding.right` | tdesktop/Telegram/SourceFiles/settings/settings.style:173 | 10 |
| `settings.checkPaddingBottom` | `settingsCheckboxPadding.bottom` | tdesktop/Telegram/SourceFiles/settings/settings.style:173 | 10 |
| `choice.checkMarkLeft` | `defaultCheckboxIcon.position.x` | lib_ui/ui/widgets/widgets.style:839 | 4 |
| `choice.checkMarkTop` | `defaultCheckboxIcon.position.y` | lib_ui/ui/widgets/widgets.style:839 | 7 |
| `choice.checkMarkWidth` | `default_checkbox_check canvas width` | lib_ui/icons/default_checkbox_check.png | 14 |
| `choice.checkMarkHeight` | `default_checkbox_check canvas height` | lib_ui/icons/default_checkbox_check.png | 10 |
| `dialog.titleMaxHeight` | `boxTitle.maxHeight` | lib_ui/ui/layers/layers.style:76 | 24 |
| `dialog.topMargin` | `boxTopMargin` | lib_ui/ui/layers/layers.style:89 | 8 |
| `dialog.buttonTextTop` | `defaultBoxButton.textTop` | lib_ui/ui/layers/layers.style:47 | 7 |
| `dialog.buttonRadius` | `buttonRadius` | lib_ui/ui/widgets/widgets.style:716 | 4 |
| `dialog.shadowExtend` | `roundShadowRadius8px.extend` | lib_ui/ui/widgets/widgets.style:927 | 10 |
| `choose.searchPaddingLeft` | `defaultMultiSelect.padding.left` | lib_ui/ui/widgets/widgets.style:1129 | 8 |
| `choose.searchPaddingTop` | `defaultMultiSelect.padding.top` | lib_ui/ui/widgets/widgets.style:1129 | 6 |
| `choose.searchPaddingRight` | `defaultMultiSelect.padding.right` | lib_ui/ui/widgets/widgets.style:1129 | 8 |
| `choose.searchPaddingBottom` | `defaultMultiSelect.padding.bottom` | lib_ui/ui/widgets/widgets.style:1129 | 6 |
| `choose.searchFieldMinHeight` | `field.heightMin` | lib_ui/ui/widgets/widgets.style:1102 | 32 |
| `choose.searchFieldMinWidth` | `fieldMinWidth` | lib_ui/ui/widgets/widgets.style:1145 | 42 |
| `choose.searchIconSkip` | `fieldIconSkip` | lib_ui/ui/widgets/widgets.style:1144 | 36 |
| `choose.searchCancelSkip` | `fieldCancelSkip` | lib_ui/ui/widgets/widgets.style:1149 | 40 |
| `choose.searchTextLeft` | `field.textMargins.left` | lib_ui/ui/widgets/widgets.style:1090 | 2 |
| `choose.searchTextTop` | `field.textMargins.top` | lib_ui/ui/widgets/widgets.style:1090 | 7 |
| `choose.searchTextRight` | `field.textMargins.right` | lib_ui/ui/widgets/widgets.style:1090 | 2 |
| `choose.searchTextBottom` | `field.textMargins.bottom` | lib_ui/ui/widgets/widgets.style:1090 | 0 |
| `choose.searchPlaceholderLeft` | `field.placeholderMargins.left` | lib_ui/ui/widgets/widgets.style:1095 | 2 |
| `choose.searchPlaceholderRight` | `field.placeholderMargins.right` | lib_ui/ui/widgets/widgets.style:1095 | 2 |
| `choose.searchIconLeft` | `boxFieldSearchIcon.position.x` | lib_ui/ui/widgets/widgets.style:1107 | 10 |
| `choose.searchIconTop` | `boxFieldSearchIcon.position.y` | lib_ui/ui/widgets/widgets.style:1107 | 9 |
| `choose.searchIconSize` | `box_search canvas size` | lib_ui/icons/box_search.png | 16 |
| `choose.clearWidth` | `cancel.width` | lib_ui/ui/widgets/widgets.style:1110 | 44 |
| `choose.clearHeight` | `cancel.height` | lib_ui/ui/widgets/widgets.style:1111 | 44 |
| `choose.clearCanvas` | `cancel.cross.size` | lib_ui/ui/widgets/widgets.style:1118 | 36 |
| `choose.clearInset` | `cancel.cross.skip` | lib_ui/ui/widgets/widgets.style:1119 | 12 |
| `choose.clearStroke` | `cancel.cross.stroke` | lib_ui/ui/widgets/widgets.style:1120 | 1.5 |
| `choose.clearMinScale` | `cancel.cross.minScale` | lib_ui/ui/widgets/widgets.style:1121 | 0.3 |
| `choose.clearPositionX` | `cancel.crossPosition.x` | lib_ui/ui/widgets/widgets.style:1114 | 4 |
| `choose.clearPositionY` | `cancel.crossPosition.y` | lib_ui/ui/widgets/widgets.style:1114 | 4 |
| `choose.groupPaddingTop` | `defaultBox.margin.top` | lib_ui/ui/layers/layers.style:128; tdesktop/Telegram/SourceFiles/boxes/language_box.cpp:1233-1243 | 10 |
| `choose.groupPaddingBottom` | `defaultBox.margin.bottom` | lib_ui/ui/layers/layers.style:128; tdesktop/Telegram/SourceFiles/boxes/language_box.cpp:1233-1243 | 10 |
| `choose.rowPaddingLeft` | `passportRowPadding.left` | tdesktop/Telegram/SourceFiles/passport/passport.style:108 | 22 |
| `choose.rowPaddingTop` | `passportRowPadding.top` | tdesktop/Telegram/SourceFiles/passport/passport.style:108 | 8 |
| `choose.rowPaddingRight` | `passportRowPadding.right` | tdesktop/Telegram/SourceFiles/passport/passport.style:108 | 25 |
| `choose.rowPaddingBottom` | `passportRowPadding.bottom` | tdesktop/Telegram/SourceFiles/passport/passport.style:108 | 8 |
| `choose.rowTextGap` | `passportRowSkip` | tdesktop/Telegram/SourceFiles/passport/passport.style:109 | 2 |
| `choose.rowIconGap` | `passportRowIconSkip` | tdesktop/Telegram/SourceFiles/passport/passport.style:110 | 10 |
| `choose.emptyFontHeights` | `LanguageBox empty font-height multiplier` | tdesktop/Telegram/SourceFiles/boxes/language_box.cpp:1262 | 9 |
| `choose.emptyMinWidth` | `membersAbout.minWidth` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:129 | 240 |
| `choose.scrollTrackWidth` | `defaultSolidScroll.width` | lib_ui/ui/widgets/widgets.style:830 | 14 |
| `choose.scrollInsetX` | `defaultSolidScroll.deltax` | lib_ui/ui/widgets/widgets.style:831 | 5 |
| `choose.scrollInsetTop` | `defaultSolidScroll.deltat` | lib_ui/ui/widgets/widgets.style:832 | 6 |
| `choose.scrollInsetBottom` | `defaultSolidScroll.deltab` | lib_ui/ui/widgets/widgets.style:833 | 6 |
| `choose.scrollHideDelay` | `defaultSolidScroll.hiding` | lib_ui/ui/widgets/widgets.style:836 | 0 |
| `input.numberPortWidth` | `connectionPortInputField.width` | tdesktop/Telegram/SourceFiles/boxes/connection_box.style:14 | 55 |
| `input.hostInitialWidth` | `connectionHostInputField.width` | tdesktop/Telegram/SourceFiles/boxes/connection_box.style:11 | 160 |
| `input.fieldGap` | `proxyEditSkip` | tdesktop/Telegram/SourceFiles/boxes/connection_box.style:46 | 16 |
| `input.paddingLeft` | `proxyEditInputPadding.left` | tdesktop/Telegram/SourceFiles/boxes/connection_box.style:45 | 22 |
| `input.paddingRight` | `proxyEditInputPadding.right` | tdesktop/Telegram/SourceFiles/boxes/connection_box.style:45 | 22 |
| `input.heightMin` | `defaultInputField.heightMin` | lib_ui/ui/widgets/widgets.style:1058 | 55 |
| `input.heightMax` | `defaultInputField.heightMax` | lib_ui/ui/widgets/widgets.style:1059 | 148 |
| `input.widthMin` | `defaultInputField.widthMin` | lib_ui/ui/widgets/widgets.style:1060 | 64 |
| `input.textLeft` | `defaultInputField.textMargins.left` | lib_ui/ui/widgets/widgets.style:1033 | 0 |
| `input.textTop` | `defaultInputField.textMargins.top` | lib_ui/ui/widgets/widgets.style:1033 | 28 |
| `input.textRight` | `defaultInputField.textMargins.right` | lib_ui/ui/widgets/widgets.style:1033 | 0 |
| `input.textBottom` | `defaultInputField.textMargins.bottom` | lib_ui/ui/widgets/widgets.style:1033 | 4 |
| `input.placeholderScale` | `defaultInputField.placeholderScale` | lib_ui/ui/widgets/widgets.style:1041 | 0.9 |
| `input.placeholderRise` | `defaultInputField.placeholderShift` | lib_ui/ui/widgets/widgets.style:1042 | 20 |
| `input.border` | `defaultInputField.border` | lib_ui/ui/widgets/widgets.style:1050 | 1 |
| `input.borderActive` | `defaultInputField.borderActive` | lib_ui/ui/widgets/widgets.style:1051 | 2 |
| `settings.limitPaddingLeft` | `autoDownloadLimitButton.padding.left` | tdesktop/Telegram/SourceFiles/settings/settings.style:314 | 22 |
| `settings.limitPaddingTop` | `autoDownloadLimitButton.padding.top` | tdesktop/Telegram/SourceFiles/settings/settings.style:314 | 10 |
| `settings.limitPaddingRight` | `autoDownloadLimitButton.padding.right` | tdesktop/Telegram/SourceFiles/settings/settings.style:314 | 22 |
| `settings.limitPaddingBottom` | `autoDownloadLimitButton.padding.bottom` | tdesktop/Telegram/SourceFiles/settings/settings.style:314 | 0 |
| `settings.sliderPaddingLeft` | `autoDownloadLimitPadding.left` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:167 | 22 |
| `settings.sliderPaddingTop` | `autoDownloadLimitPadding.top` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:167 | 8 |
| `settings.sliderPaddingRight` | `autoDownloadLimitPadding.right` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:167 | 22 |
| `settings.sliderPaddingBottom` | `autoDownloadLimitPadding.bottom` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:167 | 8 |
| `slider.trackHeight` | `defaultContinuousSlider.width` | lib_ui/ui/widgets/widgets.style:1211 | 3 |
| `slider.markerWidth` | `autoDownloadLimitSlider.seekSize.width` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:165 | 15 |
| `slider.markerHeight` | `autoDownloadLimitSlider.seekSize.height` | tdesktop/Telegram/SourceFiles/boxes/boxes.style:165 | 15 |
| `reaction.inBubbleShift` | `abs(reactionInlineInBubbleLeft), applied negatively` | paint/message-bottom.md; chat.style:688 | 3 |
| `reaction.emptySkip` | `reactionInlineEmptySkip` | paint/message-bottom.md; chat.style:683 | 2 |
| `reaction.userpicPadding` | `reactionInlineUserpicsPadding` | paint/message-bottom.md; chat.style:689 | 1 |
| `reaction.outsideOwnAlpha` | `kChosenOutsideAlphaMin 192/255` | paint/message-bottom.md; history_view_reactions.cpp:43-45 | 0.7529411764705882 |
| `message.receiptSpace` | `historySendStateSpace` | paint/message-bottom.md; chat.style:238 | 24 |
| `message.receiptRight` | `-historySendStatePosition.x - sent offset.x` | paint/message-bottom.md; chat.style:239; chat_style.style:214 | 15 |
| `message.receiptTop` | `-historySendStatePosition.y - sent offset.y` | paint/message-bottom.md; chat.style:239; chat_style.style:214 | 15 |
| `message.sentWidth` | `history_sent canvas width` | paint/message-bottom.md#基线与图标槽 | 13 |
| `message.readWidth` | `history_received canvas width` | paint/message-bottom.md#基线与图标槽 | 18 |
| `message.receiptHeight` | `history_sent/received canvas height` | paint/message-bottom.md#基线与图标槽 | 11 |
| `message.sendingSize` | `dialogs_sending canvas size` | paint/message-bottom.md#基线与图标槽 | 11 |
| `message.sendingRight` | `17 - dialogs sending offset.x 5` | paint/message-bottom.md; chat.style:239; dialogs.style:448-449 | 12 |
| `message.sendingTop` | `19 - dialogs sending offset.y 5` | paint/message-bottom.md; chat.style:239; dialogs.style:448-449 | 14 |
| `message.incomingSendingLeft` | `dialogs sending offset.x` | paint/message-bottom.md; dialogs.style:450-451 | 3 |
| `message.viewsSpace` | `historyViewsSpace` | paint/message-bottom.md; chat.style:242 | 8 |
| `message.viewsWidth` | `historyViewsWidth` | paint/message-bottom.md; chat.style:243 | 20 |
| `message.viewsIconWidth` | `history_views/replies canvas width` | paint/message-bottom.md#基线与图标槽 | 16 |
| `message.pinWidth` | `historyPinWidth` | paint/message-bottom.md; chat.style:244 | 24 |
| `message.pinSize` | `history_pin canvas size` | paint/message-bottom.md#基线与图标槽 | 18 |
| `message.silentWidth` | `historySilentWidth` | paint/message-bottom.md; chat.style:247 | 12 |
| `message.silentSize` | `dialogs_mute canvas size` | paint/message-bottom.md#基线与图标槽 | 8 |
| `message.silentTop` | `abs(historySilentTop)` | paint/message-bottom.md; chat.style:248 | 13 |
| `message.ephemeralWidth` | `historyEphemeralStateWidth` | paint/message-bottom.md; chat.style:341 | 16 |
| `message.ephemeralSize` | `ephemeral_badge canvas size` | paint/message-bottom.md#基线与图标槽 | 14 |
| `message.signatureMaxWidth` | `maxSignatureSize` | paint/message-bottom.md; chat.style:174 | 144 |
| `button.height` | `defaultActiveButton.height` | lib_ui/ui/widgets/widgets.style:728 | 34 |
| `button.horizontalExtra` | `abs(defaultActiveButton.width)` | lib_ui/ui/widgets/widgets.style:729 | 34 |
| `button.textTop` | `defaultActiveButton.textTop` | lib_ui/ui/widgets/widgets.style:732 | 8 |
| `button.numbersGap` | `defaultActiveButton.numbersSkip` | lib_ui/ui/widgets/widgets.style:726 | 7 |
| `button.paddingLeft` | `defaultActiveButton.padding.left` | lib_ui/ui/widgets/widgets.style:730 | 0 |
| `button.paddingTop` | `defaultActiveButton.padding.top` | lib_ui/ui/widgets/widgets.style:730 | 0 |
| `button.paddingRight` | `defaultActiveButton.padding.right` | lib_ui/ui/widgets/widgets.style:730 | 0 |
| `button.paddingBottom` | `defaultActiveButton.padding.bottom` | lib_ui/ui/widgets/widgets.style:730 | 0 |
| `iconButton.width` | `backButton.width` | lib_ui/ui/widgets/widgets.style:1487-1497 | 40 |
| `iconButton.height` | `backButton.height` | lib_ui/ui/widgets/widgets.style:1487-1497 | 40 |
| `iconButton.rippleSize` | `backButton.rippleAreaSize` | lib_ui/ui/widgets/widgets.style:1487-1497 | 40 |
| `iconButton.rippleLeft` | `backButton.rippleAreaPosition.x` | lib_ui/ui/widgets/widgets.style:1487-1497 | 0 |
| `iconButton.rippleTop` | `backButton.rippleAreaPosition.y` | lib_ui/ui/widgets/widgets.style:1487-1497 | 0 |
| `segments.height` | `defaultSettingsSlider.height` | lib_ui/ui/widgets/widgets.style:1177 | 39 |
| `segments.padding` | `defaultSettingsSlider.padding` | lib_ui/ui/widgets/widgets.style:1178 | 0 |
| `segments.barTop` | `defaultSettingsSlider.barTop` | lib_ui/ui/widgets/widgets.style:1179 | 5 |
| `segments.gap` | `defaultSettingsSlider.barSkip` | lib_ui/ui/widgets/widgets.style:1180 | 3 |
| `segments.barHeight` | `defaultSettingsSlider.barStroke` | lib_ui/ui/widgets/widgets.style:1181 | 3 |
| `segments.radius` | `defaultSettingsSlider.barRadius` | lib_ui/ui/widgets/widgets.style:1182 | 1 |
| `segments.labelTop` | `defaultSettingsSlider.labelTop` | lib_ui/ui/widgets/widgets.style:1185 | 17 |
| `segments.strictSkip` | `strictSkip default` | paint/settings-slider.md#几何 | 0 |
| `segments.outerLeft` | `settingsSliderPadding.left` | tdesktop/Telegram/SourceFiles/settings/settings.style:91 | 21 |
| `segments.outerTop` | `settingsSliderPadding.top` | tdesktop/Telegram/SourceFiles/settings/settings.style:91 | 7 |
| `segments.outerRight` | `settingsSliderPadding.right` | tdesktop/Telegram/SourceFiles/settings/settings.style:91 | 21 |
| `segments.outerBottom` | `settingsSliderPadding.bottom` | tdesktop/Telegram/SourceFiles/settings/settings.style:91 | 4 |
| `tabs.height` | `defaultTabsSlider.height` | lib_ui/ui/widgets/widgets.style:1193 | 43 |
| `tabs.barTop` | `defaultTabsSlider.barTop` | lib_ui/ui/widgets/widgets.style:1194 | 40 |
| `tabs.gap` | `defaultTabsSlider.barSkip` | lib_ui/ui/widgets/widgets.style:1195 | 0 |
| `tabs.barStroke` | `defaultTabsSlider.barStroke` | lib_ui/ui/widgets/widgets.style:1196 | 6 |
| `tabs.radius` | `defaultTabsSlider.barRadius` | lib_ui/ui/widgets/widgets.style:1197 | 2 |
| `tabs.labelTop` | `defaultTabsSlider.labelTop` | lib_ui/ui/widgets/widgets.style:1200 | 12 |
| `tabs.rippleBottomSkip` | `defaultTabsSlider.rippleBottomSkip` | lib_ui/ui/widgets/widgets.style:1204 | 1 |
| `profileTabs.height` | `infoProfileTabsStrip.height` | tdesktop/Telegram/SourceFiles/info/info.style:719 | 48 |
| `profileTabs.marginLeft` | `infoProfileTabsStrip.margin.left` | tdesktop/Telegram/SourceFiles/info/info.style:720 | 8 |
| `profileTabs.marginTop` | `infoProfileTabsStrip.margin.top` | tdesktop/Telegram/SourceFiles/info/info.style:720 | 6 |
| `profileTabs.marginRight` | `infoProfileTabsStrip.margin.right` | tdesktop/Telegram/SourceFiles/info/info.style:720 | 8 |
| `profileTabs.marginBottom` | `infoProfileTabsStrip.margin.bottom` | tdesktop/Telegram/SourceFiles/info/info.style:720 | 6 |
| `profileTabs.tabPadding` | `infoProfileTabsStrip.tabPadding.left` | tdesktop/Telegram/SourceFiles/info/info.style:722 | 16 |
| `profileTabs.activeInset` | `infoProfileTabsStrip.activeSkip` | tdesktop/Telegram/SourceFiles/info/info.style:723 | 4 |
| `profileTabs.shadowBlur` | `infoProfileTabsStrip.shadow.blurRadius` | tdesktop/Telegram/SourceFiles/info/info.style:735 | 8 |
| `profileTabs.shadowY` | `infoProfileTabsStrip.shadow.offset.y` | tdesktop/Telegram/SourceFiles/info/info.style:736 | 2 |
| `profileTabs.shadowOpacity` | `infoProfileTabsStrip.shadow.opacity` | tdesktop/Telegram/SourceFiles/info/info.style:737 | 0.18 |
| `profileTabs.lightStrokeRatio` | `light stroke ratio` | paint/tabs.md#info岛状变体 | 0.6 |
| `profileTabs.lightStrokeAlpha` | `20/255` | paint/tabs.md#info岛状变体 | 0.0784313725490196 |
| `profileTabs.darkTopAlpha` | `40/255` | paint/tabs.md#info岛状变体 | 0.1568627450980392 |
| `profileTabs.darkMidAlpha` | `0/255` | paint/tabs.md#info岛状变体 | 0 |
| `profileTabs.darkBottomAlpha` | `20/255` | paint/tabs.md#info岛状变体 | 0.0784313725490196 |
| `profileTabs.lightnessThreshold` | `outline lightness boundary` | paint/tabs.md#info岛状变体 | 128 |
| `profileTabs.fadeFactor` | `fade distance ratio` | paint/tabs.md#info岛状变体 | 0.5 |
| `pageTitle.height` | `infoTopBarHeight` | tdesktop/Telegram/SourceFiles/info/info.style:138 | 54 |
| `pageTitle.left` | `infoTopBarTitle.position.x` | tdesktop/Telegram/SourceFiles/info/info.style:237 | 24 |
| `pageTitle.top` | `infoTopBarTitle.position.y` | tdesktop/Telegram/SourceFiles/info/info.style:237 | 17 |
| `pageTitle.textMaxHeight` | `infoTopBarTitle.maxHeight` | tdesktop/Telegram/SourceFiles/info/info.style:153 | 20 |
| `pageTitle.subtitleLeft` | `infoTopBarSubtitle.position.x` | tdesktop/Telegram/SourceFiles/info/info.style:246 | 16 |
| `pageTitle.titleWithSubtitleTop` | `infoTopBarTitleWithSubtitle.position.y` | tdesktop/Telegram/SourceFiles/info/info.style:241 | 8 |
| `pageTitle.subtitleTop` | `infoTopBarSubtitle.position.y` | tdesktop/Telegram/SourceFiles/info/info.style:246 | 28 |
| `pageTitle.badgeGap` | `infoTopBar.badgeSkip` | tdesktop/Telegram/SourceFiles/info/info.style:250 | 8 |
| `pageTitle.backWidth` | `infoTopBarBack.width` | tdesktop/Telegram/SourceFiles/info/info.style:140 | 60 |
| `pageTitle.backIconLeft` | `infoTopBarBack.iconPosition.x` | tdesktop/Telegram/SourceFiles/info/info.style:145 | 11 |
| `pageTitle.backRippleSize` | `infoTopBarBack.rippleAreaSize` | tdesktop/Telegram/SourceFiles/info/info.style:147 | 42 |
| `pageTitle.backRippleLeft` | `infoTopBarBack.rippleAreaPosition.x` | tdesktop/Telegram/SourceFiles/info/info.style:148 | 6 |
| `pageTitle.backRippleTop` | `infoTopBarBack.rippleAreaPosition.y` | tdesktop/Telegram/SourceFiles/info/info.style:148 | 6 |
| `pageTitle.closeWidth` | `infoTopBarClose.width` | tdesktop/Telegram/SourceFiles/info/info.style:164 | 48 |
| `pageTitle.closeIconLeft` | `infoTopBarClose.iconPosition.x` | tdesktop/Telegram/SourceFiles/info/info.style:165 | 5 |
| `pageTitle.closeRippleLeft` | `infoTopBarClose.rippleAreaPosition.x` | tdesktop/Telegram/SourceFiles/info/info.style:166 | 0 |
| `pageTitle.layerHeight` | `infoLayerTopBarHeight` | tdesktop/Telegram/SourceFiles/info/info.style:269 | 56 |
| `pageTitle.layerTitleTop` | `infoLayer titleWithSubtitle.position.y` | tdesktop/Telegram/SourceFiles/info/info.style:360 | 9 |
| `pageTitle.layerSubtitleTop` | `infoLayer subtitle.position.y` | tdesktop/Telegram/SourceFiles/info/info.style:361 | 30 |
| `pageTitle.hiddenScale` | `infoTopBarTitleScale` | tdesktop/Telegram/SourceFiles/info/info.style:264 | 0.7 |
| `badge.textPaddingLeft` | `settingsPremiumNewBadge.padding.left` | tdesktop/Telegram/SourceFiles/settings/settings.style:161 | 4 |
| `badge.textPaddingTop` | `settingsPremiumNewBadge.padding.top` | tdesktop/Telegram/SourceFiles/settings/settings.style:161 | 1 |
| `badge.textPaddingRight` | `settingsPremiumNewBadge.padding.right` | tdesktop/Telegram/SourceFiles/settings/settings.style:161 | 4 |
| `badge.textPaddingBottom` | `settingsPremiumNewBadge.padding.bottom` | tdesktop/Telegram/SourceFiles/settings/settings.style:161 | 1 |
| `badge.textRadius` | `NewBadge padding.left radius` | paint/badge.md#紧凑文字标签 | 4 |
| `badge.afterLabelX` | `settingsPremiumNewBadge.position.x` | tdesktop/Telegram/SourceFiles/settings/settings.style:160 | 4 |
| `badge.afterLabelY` | `settingsPremiumNewBadge.position.y` | tdesktop/Telegram/SourceFiles/settings/settings.style:160 | 1 |
| `approval.actionHeight` | `msgBotKbButton.height` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:282 | 36 |
| `approval.actionGap` | `msgBotKbButton.margin` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:283 | 2 |
| `approval.actionPadding` | `msgBotKbButton.padding` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:284 | 10 |
| `approval.actionTextTop` | `msgBotKbButton.textTop` | tdesktop/Telegram/SourceFiles/ui/chat/chat.style:285 | 8 |
| `forwardSummary.maxSummaryLines` | `CTL-FORWARD independent summary budget` | paint/forward-summary.md#内容排版我定的 | 3 |
| `compose.stopSize` | `historySend.stopSize` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1538 | 14 |
| `compose.stopRadius` | `historySend.stopRadius` | tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1539 | 3 |
| `glyph.stroke` | `Measured straight stems 1.5–2 DIP; replacement mean pen` | paint/icon-glyph.md#图形限框; tdesktop/Telegram/Resources/icons/top_bar_search.png | 1.75 |
| `glyph.smallStroke` | `Measured small receipt stems 1–1.5 DIP` | paint/icon-glyph.md#量测; tdesktop/Telegram/Resources/icons/history_sent.png | 1.25 |
| `glyph.hostInkRatio` | `Lucide house: stroked bounds20 / viewBox24; unknown host fallback` | CrychicUI/icon/svg/house.svg; paint/icon-glyph.md#图形限框 (我定的) | 0.8333333 |
| `glyph.headerSearch.width` | `top_bar_search visible bounds (16–17)` | tdesktop/Telegram/Resources/icons/top_bar_search.png | 16.5 |
| `glyph.headerSearch.height` | `top_bar_search visible bounds (16–17)` | tdesktop/Telegram/Resources/icons/top_bar_search.png | 16.5 |
| `glyph.headerCall.width` | `top_bar_call visible width` | tdesktop/Telegram/Resources/icons/top_bar_call.png | 16 |
| `glyph.headerCall.height` | `top_bar_call visible height` | tdesktop/Telegram/Resources/icons/top_bar_call.png | 16 |
| `glyph.headerMenu.width` | `title_menu_dots visible width` | lib_ui/icons/title_menu_dots.png | 4 |
| `glyph.headerMenu.height` | `title_menu_dots visible height` | lib_ui/icons/title_menu_dots.png | 16 |
| `glyph.back.width` | `title_back visible width` | tdesktop/Telegram/Resources/icons/title_back.png | 10 |
| `glyph.back.height` | `title_back visible height` | tdesktop/Telegram/Resources/icons/title_back.png | 16 |
| `glyph.close.width` | `box_button_close visible width` | lib_ui/icons/box_button_close.png | 12 |
| `glyph.close.height` | `box_button_close visible height` | lib_ui/icons/box_button_close.png | 12 |
| `glyph.attach.width` | `input_attach visible width` | tdesktop/Telegram/Resources/icons/chat/input_attach.png | 19 |
| `glyph.attach.height` | `input_attach visible height` | tdesktop/Telegram/Resources/icons/chat/input_attach.png | 22 |
| `glyph.emoji.width` | `historyEmojiCircleLine outer diameter, existing 24 canvas` | paint/icon-glyph.md#画布; tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1373 | 22 |
| `glyph.emoji.height` | `historyEmojiCircleLine outer diameter, existing 24 canvas` | paint/icon-glyph.md#画布; tdesktop/Telegram/SourceFiles/chat_helpers/chat_helpers.style:1373 | 22 |
| `glyph.send.width` | `input_send visible width` | tdesktop/Telegram/Resources/icons/chat/input_send.png | 22 |
| `glyph.send.height` | `input_send visible height` | tdesktop/Telegram/Resources/icons/chat/input_send.png | 22 |
| `glyph.save.width` | `input_save visible width` | tdesktop/Telegram/Resources/icons/chat/input_save.png | 20 |
| `glyph.save.height` | `input_save visible height` | tdesktop/Telegram/Resources/icons/chat/input_save.png | 15 |
| `glyph.reply.width` | `input_reply_settings visible width` | tdesktop/Telegram/Resources/icons/chat/input_reply_settings.png | 22 |
| `glyph.reply.height` | `input_reply_settings visible height` | tdesktop/Telegram/Resources/icons/chat/input_reply_settings.png | 21 |
| `glyph.edit.width` | `input_edit visible width` | tdesktop/Telegram/Resources/icons/chat/input_edit.png | 17 |
| `glyph.edit.height` | `input_edit visible height` | tdesktop/Telegram/Resources/icons/chat/input_edit.png | 17 |
| `glyph.play.width` | `history_file_play visible width` | tdesktop/Telegram/Resources/icons/history_file_play.png | 14 |
| `glyph.play.height` | `history_file_play visible height` | tdesktop/Telegram/Resources/icons/history_file_play.png | 18 |
| `glyph.pause.width` | `history_file_pause visible width` | tdesktop/Telegram/Resources/icons/history_file_pause.png | 14 |
| `glyph.pause.height` | `history_file_pause visible height` | tdesktop/Telegram/Resources/icons/history_file_pause.png | 16 |
| `glyph.download.width` | `history_file_download visible width` | tdesktop/Telegram/Resources/icons/history_file_download.png | 18 |
| `glyph.download.height` | `history_file_download visible height` | tdesktop/Telegram/Resources/icons/history_file_download.png | 19 |
| `glyph.fileCancel.width` | `history_file_cancel visible width` | tdesktop/Telegram/Resources/icons/history_file_cancel.png | 14 |
| `glyph.fileCancel.height` | `history_file_cancel visible height` | tdesktop/Telegram/Resources/icons/history_file_cancel.png | 14 |
| `glyph.file.width` | `history_file_document visible width` | tdesktop/Telegram/Resources/icons/history_file_document.png | 18 |
| `glyph.file.height` | `history_file_document visible height` | tdesktop/Telegram/Resources/icons/history_file_document.png | 18 |
| `glyph.copy.width` | `menu/copy visible width` | tdesktop/Telegram/Resources/icons/menu/copy.png | 15 |
| `glyph.copy.height` | `menu/copy visible height` | tdesktop/Telegram/Resources/icons/menu/copy.png | 17 |
| `glyph.submenu.width` | `menu/submenu_arrow visible width` | tdesktop/Telegram/Resources/icons/menu/submenu_arrow.png | 7 |
| `glyph.submenu.height` | `menu/submenu_arrow visible height` | tdesktop/Telegram/Resources/icons/menu/submenu_arrow.png | 12 |
| `glyph.check.width` | `default_checkbox_check visible width` | lib_ui/icons/default_checkbox_check.png | 12 |
| `glyph.check.height` | `default_checkbox_check visible height` | lib_ui/icons/default_checkbox_check.png | 10 |
| `glyph.search.width` | `box_search visible width` | lib_ui/icons/box_search.png | 14 |
| `glyph.search.height` | `box_search visible height` | lib_ui/icons/box_search.png | 15 |
| `glyph.pin.width` | `dialogs_pinned visible width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_pinned.png | 13 |
| `glyph.pin.height` | `dialogs_pinned visible height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_pinned.png | 12 |
| `glyph.verified.width` | `dialogs_verified_star visible width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_verified_star.png | 14 |
| `glyph.verified.height` | `dialogs_verified_star visible height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_verified_star.png | 14 |
| `glyph.latest.width` | `history_down_arrow visible width` | tdesktop/Telegram/Resources/icons/history_down_arrow.png | 20 |
| `glyph.latest.height` | `history_down_arrow visible height` | tdesktop/Telegram/Resources/icons/history_down_arrow.png | 10 |
| `glyph.folder.width` | `folders_custom visible width` | tdesktop/Telegram/Resources/icons/folders/folders_custom.png | 22 |
| `glyph.folder.height` | `folders_custom visible height` | tdesktop/Telegram/Resources/icons/folders/folders_custom.png | 17 |
| `glyph.recent.width` | `emoji_recent visible width` | tdesktop/Telegram/Resources/icons/emoji/emoji_recent.png | 18 |
| `glyph.recent.height` | `emoji_recent visible height` | tdesktop/Telegram/Resources/icons/emoji/emoji_recent.png | 18 |
| `glyph.messagePin.width` | `history_pin visible width` | tdesktop/Telegram/Resources/icons/history_pin.png | 12 |
| `glyph.messagePin.height` | `history_pin visible height` | tdesktop/Telegram/Resources/icons/history_pin.png | 13 |
| `glyph.messageReply.width` | `history_replies visible width` | tdesktop/Telegram/Resources/icons/history_replies.png | 11 |
| `glyph.messageReply.height` | `history_replies visible height` | tdesktop/Telegram/Resources/icons/history_replies.png | 10 |
| `glyph.rail.width` | `folders_home visible width (custom host symbol envelope)` | tdesktop/Telegram/Resources/icons/folders/folders_home.png | 26 |
| `glyph.rail.height` | `folders_home visible height (custom host symbol envelope)` | tdesktop/Telegram/Resources/icons/folders/folders_home.png | 23 |
| `glyph.group.width` | `dialogs_chat visible width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_chat.png | 14 |
| `glyph.group.height` | `dialogs_chat visible height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_chat.png | 9 |
| `glyph.channel.width` | `dialogs_channel visible width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_channel.png | 11 |
| `glyph.channel.height` | `dialogs_channel visible height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_channel.png | 11 |
| `glyph.bot.width` | `dialogs_bot visible width` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_bot.png | 14 |
| `glyph.bot.height` | `dialogs_bot visible height` | tdesktop/Telegram/Resources/icons/dialogs/dialogs_bot.png | 11 |
| `glyph.lock.width` | `mini_tag_lock visible width` | tdesktop/Telegram/Resources/icons/dialogs/mini_tag_lock.png | 8 |
| `glyph.lock.height` | `mini_tag_lock visible height` | tdesktop/Telegram/Resources/icons/dialogs/mini_tag_lock.png | 10 |
| `glyph.controlBack.width` | `box_button_back visible width` | lib_ui/icons/box_button_back.png | 16 |
| `glyph.controlBack.height` | `box_button_back visible height` | lib_ui/icons/box_button_back.png | 16 |
| `glyph.dropdown.width` | `menu/submenu_arrow rotated visible width` | tdesktop/Telegram/Resources/icons/menu/submenu_arrow.png | 12 |
| `glyph.dropdown.height` | `menu/submenu_arrow rotated visible height` | tdesktop/Telegram/Resources/icons/menu/submenu_arrow.png | 7 |
| `glyph.hostHeightRatio` | `Lucide house stroked height21 / viewBox24; unknown host fallback` | CrychicUI/icon/svg/house.svg; paint/icon-glyph.md#图形限框 (我定的) | 0.875 |
| `glyph.archive.width` | `archive_userpic visible width` | tdesktop/Telegram/Resources/icons/archive_userpic.png | 20 |
| `glyph.archive.height` | `archive_userpic visible height` | tdesktop/Telegram/Resources/icons/archive_userpic.png | 21 |

共 806 个数值。Scrollbar.Width 为 10px 总槽宽扣除两侧各 3px 的实际拇指宽 4 DIP。

输入框 FieldMaxHeight=72 与 ExpandedMaxHeight=224 分别保留；ToLatest 的 52×62 是外框，42 是 ripple 区域。

字阶、动效和材质的来源与扩展见 [theme-behavior.md](theme-behavior.md)。
