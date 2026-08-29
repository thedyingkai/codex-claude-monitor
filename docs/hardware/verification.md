# 旧载板验证记录与当前 E32R28T 状态

> 旧 KiCad/外壳结果只适用于已停用的 DFR0975/DFR0665 载板，不能用于当前生产。
> 2026-08-29 当前固件 `native` 40/40 测试通过；`e32r28t` 编译使用
> 101,792/327,680 bytes RAM 和 1,466,009/1,966,080 bytes Flash；旧
> `firebeetle2_esp32s3` 目标也回归编译通过。E32R28T 已通过 COM5 实机烧录、
> Wi-Fi 重连和 HTTPS 快照请求，屏幕观感、电池及长期运行项目仍按本文末尾/测试总表验收。

## 当前 E32R28T 自动化结果

- `platformio.ini` 默认目标为 `e32r28t`，经典 ESP32 4MB/DIO 配置与 S3 配置隔离；
- ILI9341V、XPT2046 独立 SPI、GPIO34 电池 ADC、BOOT 键和三档背光代码均完成编译；
- E32 不会引用旧载板的 GPIO6/GPIO7、MAX17048、显示负载开关或双键逻辑；可选的
  USB 检测使用 GPIO35，且默认关闭，未安装外部分压时不会读取悬空输入；
- 电压-SOC 查表、显示文案、JSON 快照、64KiB 响应限制、运行状态机、OTA 策略、
  三 Wi-Fi 严格优先级、USB 检测去抖/插拔计时和旧目标回归共 40 个测试通过；
- XPT2046 驱动固定到上游提交 `f956c5d8ce3bf39169c7378416b89e7cfe70a034`。
- 320×240 RGB565 背景、顶部 Wi-Fi/时钟/电池图标、半透明额度卡片和套餐档位显示已编译并烧录；
- 真机串口 `show` 确认 NVS 配置保留，`test` 返回 `OK snapshot received`。

CH340C 上传、屏幕方向、Wi-Fi 与 API 已通过；Noto Sans CJK SC Medium 16px、提高
卡片/顶栏不透明度以及灰色过期态已经通过 Windows 相机真机核对。触摸五点、ADC
标定、电池极性、功耗、温升和续航仍需实物验收。

v0.3.2 的 USB 省电旁路必须先增加外部分压：P2 pin 1 `+5V` 经 `100kΩ 1%` 到
GPIO35，GPIO35 经 `150kΩ 1%` 到 GND，可选并联 `100nF` 到 GND；禁止 +5V 直连
GPIO35。完成接线前保持 `external_power_sense_enabled=0`。接线后的 USB 插入常亮、
拔出重新计时仍属于真机验收项，当前自动化结果不代表已完成该电气验证。

## 已停用载板的历史自动化验证

- KiCad 9.0.9 原理图 ERC：0 错误、0 警告。
- 原理图/PCB 网表：34 个实体器件、32 个网络、123 个编号端点一致。
- PCB DRC：0 违规、0 未连接、0 封装错误。
- 关键器件封装、焊盘和引脚网络：JST、MF-NSMF150、Si5908、LTC4365、
  AP22804、MAX17048、Murata GRM21、PTS850、Keystone 5015、J5 均通过脚本断言。
- BOM、设计坐标和 PCB 器件的引用、数值、封装、坐标集合一致。
- PlatformIO 6.1.18 旧记录 `native`：11/11 测试通过；`firebeetle2_esp32s3` 从 clean
  状态完整编译、链接通过，RAM 为 97,920 / 327,680 bytes（29.9%），Flash 为
  1,158,829 / 6,553,600 bytes（17.7%）。
- OpenSCAD 导出脚本的尺寸、包络和间隙断言通过；最终四个 STL 均通过流形检查，
  四个 STEP 均由 OpenCascade 回读为有效 BRep（每件 1 solid / 1 shell）。
- PTS850 焊盘、定位柱、执行器方向以及 PCM12 的旋转和运动方向均已按原厂图纸
  完成脚本断言；外壳按键帽/开关帽也具有独立限位。手感、行程、打印收缩和寿命
  仍必须通过实物验证。

以上结果可通过 `hardware/scripts/export_manufacturing.ps1` 或 `.sh` 重现；
报告、Gerber、钻孔、贴片坐标、渲染图和摘要写入 `hardware/rendered/pcb`。
完整的软件、固件、PCB 和外壳自动化结果见[统一验证报告](../verification-report.md)。

## 已停用载板的历史生产门槛

- 受保护 755060 电池断开后，J1/VIN 接近 0V 时的 USB 充电唤醒；
- LTC4365 外部 UV/OV 阈值网络尚未实现，当前不具备 2S 误接拒绝声明；
- J1/J2 与屏幕的 Z 向干涉，以及 J5 的量产低矮替代连接方式；
- H1--H4 未用螺钉时，打印导轨/压持舌片的防抬起、防异响和跌落可靠性；
- 关机电流 ≤1.5mA、60% 亮度 15 秒刷新续航 ≥8h；
- 充电温升、显示浪涌、C4/C7 瞬态、C7 的 4.2V DC-bias 有效容量、SPI 信号、
  FPC 极性和按键手感；
- 电池膨胀、线束应力释放、打印收缩、整机质量和完整 3D 实物干涉。

这些项目必须用实际采购批次、限流电源、示波器和热测试完成。ERC/DRC 清零
不能自动解除 `hardware/pcb/PRODUCTION_STATUS.md` 的量产禁令。
