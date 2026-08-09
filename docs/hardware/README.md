# E32R28T 便携显示终端

当前硬件已经固定为 Keyes `62520093` 触摸版 `E32R28T`。它在一块
`50 × 86 × 5.6mm` 模块上集成了 ESP32-32E、2.8 英寸 ILI9341V 屏、
XPT2046 电阻触摸、2.4GHz Wi-Fi、Type-C/CH340C 下载电路和 1S 锂电充电电路。
不需要另买或焊接 Wi-Fi 模块，也不需要项目原先设计的转接 PCB。

当前装配只需要：

- E32R28T 触摸版模块；
- 一根确认能传数据的 Type-C 线和 5V、至少 1A 的合规 USB 电源；
- 带保护板的 3.7V/1S 成品锂电池，电池自带与实购板匹配的 MX1.25-2P 插头；
- 阻燃绝缘片/软垫和不会挤压电芯的固定材料；
- 需要便携外壳时，再按实测电池尺寸制作后壳。

除非以后增加独立硬电源开关，否则当前版本无需焊接。电池线也不得自行焊到软包
电芯极耳上。完整操作见[装配步骤](assembly.md)、[固件烧录](firmware.md)和
[手机配网与安全 OTA](../firmware-ota.md)、[锂电安全](lithium-safety.md)。当前物料表在
[`hardware/e32r28t/system_bom.csv`](../../hardware/e32r28t/system_bom.csv)。
到货后的尺寸、电流、温升和校准数据可直接填写[实测记录表](e32r28t-measurements.md)。

## 旧硬件资料

`hardware/pcb`、`hardware/rendered/pcb`、旧 `quota_display.scad` 及旧接线图属于
DFR0975 + DFR0665 + 自制载板 R0.1，已经被本方案取代。其生产状态文件已标为
`SUPERSEDED — DO NOT ORDER OR FABRICATE`；不要把其中的 Gerber、BOM 或 JST-PH2.0
线束用于 E32R28T。

## 已确认与待实测

官方手册已经确认屏幕/触摸引脚、GPIO34 电池分压、1.25mm 2P 电池座、约 290mA
实际充电电流和 4.24V 饱和电压。固件也已经通过 E32R28T 目标编译。

到货后仍必须实测：电池线序、ADC 与万用表偏差、触摸校准、10% 降亮、
30/60/100% 正常亮度和背光关闭功耗、充电温升、8 小时续航，以及电池和外壳的
实际包络。GPIO21 只能关闭背光，板上没有
可由固件切断整机电源的负载开关，因此现在不能宣称达到旧计划的 1.5mA 软关机目标。

官方资料：[产品参数](https://www.keyesrobot.cn/projects/62520093-62520094/zh-cn/latest/%E4%BA%A7%E5%93%81%E5%8F%82%E6%95%B0.html)、
[接口定义](https://www.keyesrobot.cn/projects/62520093-62520094/zh-cn/latest/%E6%8E%A5%E5%8F%A3%E5%AE%9A%E4%B9%89.html)、
[示例和手册下载](https://www.keyesrobot.cn/projects/62520093-62520094/zh-cn/latest/%E5%BF%AB%E9%80%9F%E4%BD%BF%E7%94%A8%E5%92%8C%E8%B5%84%E6%96%99%E4%B8%8B%E8%BD%BD.html)。
