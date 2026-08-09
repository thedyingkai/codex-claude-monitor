# E32R28T 固件构建、烧录与配置

## 构建目标

项目锁定 PlatformIO 6.1.18、`espressif32@6.10.0`、Arduino-ESP32 2.0.17。
`e32r28t` 是默认目标，使用经典 ESP32-32E 的 4MB Flash、DIO 80MHz 和
`min_spiffs.csv` 双 1.875MB 应用分区；没有 PSRAM。不要套用 ESP32-S3 的
16MB/PSRAM/USB-CDC 参数。

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
set base_url https://quota.example.com
set token DISPLAY_READ_TOKEN
set timezone CST-8
set refresh_seconds 15
save
test
factory-reset
```

`set` 先暂存在内存，`save` 验证后写入 NVS。`show` 会遮蔽 Wi-Fi 密码和令牌。
E32R28T 没有第二实体键，也没有可靠的 MCU USB 插入检测；为避免 BOOT 低电平导致
下载模式，恢复出厂配置以串口 `factory-reset` 为准。

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

E32R28T 没有旧方案的 AP22804 显示负载开关、USB sense 或电源拨杆。GPIO21 PWM
只能调暗/关闭背光，不能切断 ESP32 和充电电路。首版因此保持常开，不启用未经实测
的深睡/触摸唤醒；需要真正关机时应增加经验证的独立电源方案。

## 数据与故障显示

- 横屏顶部保留网络、电量和数据年龄；下方两张高对比度 Codex/Claude 卡片从
  `y=36` 延伸到 `y=232`，标题继续显示套餐细档。每张卡包含 5h、7d 剩余额度与
  加高进度条，第二行统一显示明确的 `重置 MM/DD HH:MM`；窗口缺失时显示
  `N/A` 和 `重置 --`。
- 屏幕不再绘制最底部任务统计条，释放的高度全部用于额度卡。API 快照中的任务
  字段仍会解析和校验以保持 schema v1 兼容，但不会出现在当前单页界面。
- JSON v1 响应硬限制 64KiB，并校验必填 provider/task/agent/window 字段、时间和
  百分比关系；未知扩展字段忽略。
- 设备先通过 NTP 获得可信时间，拒绝未来超过 120 秒及不晚于上次接收时间的快照。
  最近规范化快照限频写入 NVS，离线重启后仍可显示并标为过期。
- 某窗口缺失时显示 `N/A`；服务器数据超过 90 秒时显示黄色过期状态。
- TLS 使用内置 ISRG Root X1，不调用 `setInsecure()`；DNS/TLS/API 和 Wi-Fi 失败
  均指数退避到 60 秒并保留缓存值。
