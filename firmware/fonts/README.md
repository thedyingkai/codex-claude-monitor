# 中文子集字体

`src/lv_font_qmon_16.c` 是从 Noto Sans CJK SC 生成的 16 px、4 bpp LVGL
字体子集，只包含可打印 ASCII 和固件界面实际使用的简体中文字形。上游字体不随
本项目重复分发；其许可见 `LICENSE-NOTO.txt`。

重新生成时先安装 `lv_font_conv` 1.5.3，并把 Noto Sans CJK SC 字体路径传给：

```powershell
./scripts/generate_font.ps1 -FontPath C:\path\to\NotoSansCJKsc-Regular.otf
```

脚本固定了字形集合、大小、位深和输出符号名。新增中文界面文字时，必须同时更新
脚本中的 `$Symbols`，重新生成字体并完成 ESP32 构建。
