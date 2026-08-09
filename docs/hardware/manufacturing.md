# 旧 DFRobot PCB 与外壳制造流程（已停用）

> **SUPERSEDED — DO NOT FABRICATE.** 当前 Keyes E32R28T 方案不需要自定义 PCB。
> 下文只保留旧 R0.1 工程追溯，不能作为当前采购或投板指令。

## PCB

载板设计值为 84 × 58mm、1.0mm 厚、双层、1oz 铜、ENIG；板厂能力要求至少
0.15/0.15mm 线宽/间距。`hardware/pcb/carrier.kicad_sch` 和
`carrier.kicad_pcb` 均由 `hardware/scripts/generate_kicad.py` 确定性生成，
不要只改生成物而不改脚本。

Windows 下生成“已验证原型包”：

```powershell
Set-Location C:\path\to\codex-claude-monitor
hardware\scripts\export_manufacturing.ps1
```

Linux/macOS 下运行：

```sh
hardware/scripts/export_manufacturing.sh
```

脚本使用固定的 KiCad 9 Docker 镜像，并在导出 Gerber 前依次执行：

- PCB 尺寸、层叠、关键引脚、精确焊盘、BOM 和坐标不变量检查；
- 原理图 ERC；
- 原理图 XML 网表与 PCB 全部编号端点比较；
- PCB DRC 与未连接检查；
- 官方贴片坐标与设计坐标比较。

任何一步失败都会终止。成功产物位于 `hardware/rendered/pcb`，其中
`PRODUCTION_STATUS.md`、`CONNECTIONS.md` 和 `HARNESS_SPEC.md` 会随包复制，
`SHA256SUMS.txt` 覆盖全部导出文件。

当前数字检查已清零，但这仍是**原型制造包，不是量产放行包**。下单前必须阅读
`hardware/pcb/PRODUCTION_STATUS.md`。尤其注意：

- 受保护电池在 0V/开路状态下从 USB 唤醒充电尚未实测，失败时必须改电源架构；
- LTC4365 的外部 UV/OV 阈值在 R0.1 被禁用，只实现反向电源门控，不能宣称过压、
  欠压或误接 2S 保护；
- J1/J2 的 4.5mm 本体高于当前屏下约 3.1mm 空间；
- J5 在封闭外壳版本中为 DNP，只允许开放式样机使用竖直排针；
- C7 已锁定 `GRM21BR61A106KE19L` 及 Murata 回流焊推荐范围中值焊盘，但 4.2V
  SimSurfing 有效容量、显示浪涌、关机电流和温升仍必须实测；
- H1--H4 没有被当前外壳螺钉固定，打印导轨和压持舌片必须做公差/跌落验证。

首板最多做 5 片，并先复核 J3/J4 pin 1、FPC 接触面和 0.3mm 线缆厚度、
JST 极性、MAX17048 T822+3、AP22804 SOT-25、LTC4365 TSOT-23-8、
Si5908 ChipFET 引脚及 PTS850 定位柱。

## 外壳

`hardware/enclosure/quota_display.scad` 是参数化真源。当前可打印基线为
93 × 65 × 25.7mm；指定模块的真实包络无法在 90 × 65 × 16mm 内无干涉堆叠。
按 `hardware/enclosure/export.ps1` 或 `.sh` 导出 STL，按
`docs/hardware/enclosure.md` 生成 STEP。打印件只能用于样机试装，不能替代
实物干涉、载板固定、电池膨胀、按键行程和充电温升验证。
