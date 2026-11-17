# 测试字体

`WenYuanTiny.ttf` 是文渊圆体（WenYuan Rounded SC VF，Takushun Wu，https://github.com/takushun-wu/）的改名子集，只给测试用，不随程序发布。

- 生成命令（在仓库根目录）：

  ```
  uv run --no-project --with fonttools python -I scripts/subset-font.py \
    native/fonts/WenYuanRoundedSCVF.ttf native/testdata/fonts/WenYuanTiny.ttf 你好 --rename SumikaTestRounded
  ```

  原始字体不在 git 里（`native/fonts/*.ttf` 被忽略），照 `native/README.md`「界面字体」把完整版放进 `native/fonts/` 后重跑一次就能生成同样内容。
- 只留 ASCII 和“你好”，保留 `wght` 轴；测试可变字重和缺字回退用，测试里不按内部字体名查找。
- 原字体声明了保留字体名 WenYuan Rounded / 文渊圆体（`native/fonts/WenYuan-LICENSE.md`），SIL OFL 1.1 要求修改版不能再用。内部 name 表的 family、full name、PostScript name、unique ID、各字重实例名已改成 `SumikaTestRounded`；name ID 0 保留作者、出处和版权声明，只把保留字体名的列举改成指向同目录 `OFL.txt`，字体文件里不再出现保留字体名。
- 许可证：同目录 [OFL.txt](OFL.txt)，含上游各部分版权和 SIL OFL 1.1 全文。
