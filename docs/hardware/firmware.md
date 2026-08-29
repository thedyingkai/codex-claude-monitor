# E32R28T 固件构建、烧录与配置

## 构建目标

项目锁定 PlatformIO 6.1.18、`espressif32@6.10.0`、Arduino-ESP32 2.0.17。
`e32r28t` 是默认目标，使用经典 ESP32-32E 的 4MB Flash、DIO 80MHz 和
`min_spiffs.csv` 双 1.875MB 应用分区；没有 PSRAM。不要套用 ESP32-S3 的
16MB/PSRAM/USB-CDC 参数。

E32R28T 的每个应用分区为 1,966,080 字节。CI 要求 `firmware.bin` 小于该值且至少
保留 131,072 字节余量，也就是发布镜像不得超过 1,835,008 字节。

```powershell
Set-Location C:\path\to\codex-claude-monitor\firmware
pio test -e native
pio run -e e32r28t
pio run -e e32r28t -t upload
pio device monitor -b 115200
```

旧硬件仍可用 `pio run -e firebeetle2_esp32s3` 回归编译，但不是当前采购目标。
本机发布流程可以生成从地址 `0x0` 写入的合并镜像；`dist/` 属于本地构建产物，
不会提交到 Git。合并镜像会覆盖整片 Flash（包括 NVS），日常升级应使用上面的
PlatformIO 上传命令以保留 Wi-Fi/API 配置。

编译前必须把部署环境的公开设备根证书放在
`certs/device-root-ca.pem`。仓库中的 `certs/device-root-ca.example.pem` 仅供 CI
和离线编译，不能用于生产连接；任何 CA 或服务器私钥都不得放进固件目录。

## 已固定的板载引脚

| 功能 | GPIO |
|---|---:|
| ILI9341 CS / DC / SCLK / MOSI / MISO / BL | 15 / 2 / 14 / 13 / 12 / 21 |
| XPT2046 CS / SCLK / MOSI / MISO / IRQ | 33 / 25 / 32 / 39 / 36 |
| 电池 1:2 分压 ADC | 34 |
| USB +5V 检测（可选外接分压） | 35 |
| BOOT 用户键 | 0 |
| microSD CS（不用，保持高） | 5 |
| 音频使能（低有效，不用时保持高） | 4 |
| RGB 红 / 绿 / 蓝（共阳、低有效，不用时保持高） | 22 / 16 / 17 |

LCD 使用 TFT_eSPI/VSPI，触摸使用单独的 HSPI 和固定提交版本的 MIT 许可
`XPT2046_Touchscreen`。屏幕复位与 ESP32 EN 共线，因此 `TFT_RST=-1`。GPIO6/7
属于经典 ESP32 的 Flash 总线；E32 目标不会编译旧载板的 GPIO6 USB 检测或 GPIO7
显示负载开关逻辑。

## 串口命令

```text
show
set ssid VALUE
set password VALUE
set ssid2 OPTIONAL_BACKUP_VALUE
set password2 OPTIONAL_BACKUP_VALUE
set ssid3 OPTIONAL_BACKUP_2_VALUE
set password3 OPTIONAL_BACKUP_2_VALUE
set base_url https://quota.example.com
set token DISPLAY_READ_TOKEN
set timezone CST-8
set refresh_seconds 15
set brightness_percent 60
set dim_after_seconds 60
set screen_off_after_seconds 300
set screen_off_refresh_seconds 60
set external_power_sense_enabled 0
save
test
portal
wifi-promote {"ssid":"NEW_PRIMARY","password":"NEW_PASSWORD"}
factory-reset
```

`ssid/password` 是主网络，`ssid2/password2` 和 `ssid3/password3` 分别是可选备用
网络 1、备用网络 2。三组非空 SSID 不能相同，主网络不可为空。每轮连接严格按
“主 → 备用 1 → 备用 2”尝试；整轮失败后的下一轮仍从主网络重新开始，最后成功的
网络只用于状态记录，不会改变优先级。`wifi-promote` 可把给定网络提升为主网络，原
主网络顺移为备用 1、原备用 1 顺移为备用 2；随后仍需执行 `save` 才会持久化。
`set` 也只先暂存在内存，`save` 验证后才写入 NVS。`show` 会遮蔽三组 Wi-Fi 密码和
令牌。`portal` 会打开十分钟的临时 WPA2 配网页。E32R28T 没有第二实体键；恢复出厂
配置仍以串口 `factory-reset` 为准。

厂家横屏示例的默认触摸校准为 `495,3398,721,3448`。如果实购屏偏移，可运行厂家
资料包的 `Touch_Calibrate` 示例获得新值，再暂存并保存：

```text
set touch_x_min 495
set touch_x_max 3398
set touch_y_min 721
set touch_y_max 3448
save
```

四个值范围为 0–4095，最小值必须明显小于最大值。电阻屏有个体差异，默认值只是
可用起点，不能代替真机五点测试。

## 电池与电源行为

GPIO34 位于 ADC1，Wi-Fi 工作时仍可使用。固件每 5 秒取 9 个校准毫伏读数的中值，
按厂家原理图的 1:2 分压乘 2，再用通用 1S LiPo 放电曲线估算百分比。充电时、重载
时和无电池而充电节点悬空时，电压百分比都可能偏差；必须和万用表对比，不把它当
精密电量计。低于 20% 标红，ADC 无效显示 `N/A`。

v0.3.3 默认还会根据 GPIO34 电池电压的持续上升趋势，保守推断“可能正在充电”。
短暂尖峰、背光负载变化后的电压回弹以及平稳噪声都不应触发；推断一旦成立，只在
持续下降趋势出现后解除，并从解除时重新开始无操作计时。这种趋势推断不是 USB/VBUS
检测：稳定或已充满的电池没有上升证据，设备开机前就插着 USB 时也可能没有可用
基线，因此这些情况不能保证被识别。

E32R28T 没有旧方案的 AP22804 显示负载开关、板载 USB sense 或电源拨杆。v0.3.0
默认 60 秒无操作降到 10% 亮度、300 秒关闭 GPIO21 背光；触摸、ESP32 和 Wi-Fi
继续工作，第一次触摸会亮屏并刷新。它不启用深度睡眠，因此背光关闭不是整机关机。
需要真正关机时应增加经验证的独立电源方案。

v0.3.2 支持一条可选的 USB 存在检测线：从串口排针 P2 的 pin 1 取得 Type-C 原始
`+5V`，经 `100kΩ 1%` 电阻接到 GPIO35；GPIO35 再经 `150kΩ 1%` 电阻接 GND，
并可选在 GPIO35 与 GND 之间并联 `100nF` 电容。**禁止把 +5V 直接接到 GPIO35**；
GPIO35 没有内部上下拉，`150kΩ` 下拉不可省略。未完成并核对这组分压前，必须保持
`external_power_sense_enabled=0`（默认值），避免读取悬空引脚；接线完成后才可执行
`set external_power_sense_enabled 1` 和 `save`，也可在临时配网页勾选对应的已安装项。

GPIO34 趋势推断成立，或 GPIO35 检测启用且 USB 存在时，固件都会把 GPIO21 背光
强制设为 100% PWM，并停用自动降亮和熄屏。省电旁路状态的进入与退出都会清零原
无操作计时，退出后再从头计算 60/300 秒阈值。GPIO35 路径直接判断 Type-C VBUS
是否存在，不依赖充电阶段；因此它仍是稳定电压、满电和开机即插电场景下的可靠扩展。
原板本身不能直接检测 USB，未安装上述分压时不要把 GPIO34 推断描述为 USB 检测。

## 数据与故障显示

- 横屏顶部保留网络、电量和数据年龄；下方两张高对比度 Codex/Claude 卡片从
  `y=36` 延伸到 `y=232`，标题继续显示套餐细档。每张卡包含 5h、7d 剩余额度与
  加高进度条，第二行统一显示明确的 `重置 MM/DD HH:MM`。Claude 明确报告当前 5h
  会话尚未开始（0% 已用且无重置倒计时）时显示 `剩100%` 和 `重置 未开始`；真正
  缺失的窗口才显示 `N/A` 和 `重置 --`。
- 屏幕不再绘制最底部任务统计条，释放的高度全部用于额度卡。API 快照中的任务
  字段仍会解析和校验以保持 schema v1 兼容，但不会出现在当前单页界面。
- JSON v1 响应硬限制 64KiB，并校验必填 provider/task/agent/window 字段、时间和
  百分比关系；未知扩展字段忽略。
- 设备先通过 NTP 获得可信时间，拒绝未来超过 120 秒及不晚于上次接收时间的快照。
  最近规范化快照限频写入 NVS，离线重启后仍可显示并标为过期。
- 某窗口缺失时显示 `N/A`；服务器数据超过 90 秒时显示灰色过期状态，避免与
  Claude 的橙黄色额度强调色混淆。离线时仍保留红色 Wi-Fi 图标作为连接故障提示。
- TLS 使用内置 ISRG Root X1，不调用 `setInsecure()`；DNS/TLS/API 和 Wi-Fi 失败
  均指数退避到 60 秒并保留缓存值。

首次启用配网页与 OTA 必须通过 USB 烧录 v0.3.0；后续版本才可从临时热点手动升级。
完整操作、安全边界和回滚步骤见[手机配网与安全 OTA](../firmware-ota.md)。
