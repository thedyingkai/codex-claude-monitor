# R0.1 旧 DFRobot 载板验证报告（已被 E32R28T 取代）

> 本报告中的 Go/Agent 自动化结果仍可作历史记录；PCB、Gerber、DFR0975/DFR0665
> 外壳、JST-PH2.0 和关机电路不属于当前 E32R28T 硬件，禁止据此采购或生产。

验证时间：2026-08-03（Asia/Shanghai）

结论：软件、固件和 PCB/机械输出的自动化门禁通过；硬件包仍是
**verified prototype / production blocked**，不得据此直接批量投产。

## 已通过的自动化与本机验证

### Go 服务端与 Agent

- `go mod verify` 通过；`gofmt -l .` 无输出。
- `go test -race ./...` 全包通过；`go vet ./...` 通过。
- Windows amd64、Linux amd64、Linux arm64 均以 `CGO_ENABLED=0` 构建，版本
  `0.1.0-prototype`。`dist/SHA256SUMS`：
  - Windows amd64：`7b967bf607cc4fef83dc8f6429f14aaa34c143ad8c2d107b523d6fe78805e0fd`
  - Linux amd64：`f10054fe8596e739d0f2a7135110a0e6ee45c23b3aca3e0b5c1fce361aaafb06`
  - Linux arm64：`59a0831f95860111bf21a756ea174ee6de5cf532015277cbe3d06e06f34b86f3`
- Docker scratch 镜像以 UID/GID `65532:65532`、只读根文件系统启动；容器内版本
  正确，`GET /healthz` 返回 `{"status":"ok"}`。
- Docker Compose 展开和 Caddy 2.10 配置校验通过；OpenAPI 3.1 使用 Redocly CLI
  1.34.5 推荐规则校验通过。
- 多机额度严格采用最新 `observedAt` 报告，即使最新报告窗口为 `null` 也不会回退
  旧值；Codex 重复窗口的选择只依据 300/10080 分钟、规范来源和额度内容，不读取
  opaque Limit ID。Agent 配置拒绝尾随 JSON，`--once` 使用独立 75 秒预算。
- Hooks 安装把状态、密钥、备份及 Codex/Claude 两份配置作为同一回滚事务；后续
  写入失败会恢复本次安装前状态，相关 Windows rename/权限故障注入测试通过。
- 当前已登录 Codex 的真实 `app-server` 采集成功：认证状态 `authenticated`、计划
  `pro`；采样时官方接口返回 7d 已用 23%/剩余 77%，重置时间
  `2026-08-09T09:12:23Z`，5h 窗口缺失并正确规范化为 `null`。
- Claude Code 2.1.195 已检测到，但 `claude auth status --json` 为
  `loggedIn:false`；真实 Pro/Max `/usage` 字段尚未验收。

### ESP32 固件

- PlatformIO 6.1.18 `native`：11/11 测试通过，包括快照 schema/时间/重放及
  NVS 缓存往返、分块响应 64KiB 流式边界，以及 USB sense 中值、启动边界和迟滞。
- `firebeetle2_esp32s3` 从 clean 状态完整构建并链接成功：
  - RAM：97,920 / 327,680 bytes（29.9%）
  - Flash：1,158,829 / 6,553,600 bytes（17.7%）
- HTTPS 响应通过有界流读取，未知长度与 chunked 响应也无法在校验前超过 64KiB；
  规范化快照限频写入 NVS，离线重启可恢复显示，成功刷新会清除旧诊断错误。
- 为控制 NVS 写入量，突然硬断电时防回放水位仍可能回退最多约 120 秒；软关机会
  强制保存，会话内重放不受此权衡影响。该残余风险已记录，不能宣称为安全单调存储。
- 固件构建产物位于 `firmware/.pio/build/firebeetle2_esp32s3`；该目录为可重建缓存，
  不作为受版本控制的生产固件包。

### PCB 与制造输出

- KiCad 9 ERC：0 error / 0 warning。
- PCB DRC：0 violation / 0 unconnected pad / 0 footprint error。
- 原理图与 PCB 编号端点一致，PnP 33 项一致；BOM、设计坐标、导出坐标和器件集合
  通过脚本比对。
- PTS850 已按原厂 with-boss 图修正为执行器 +X，焊盘 1/3 为信号、2/4 为 GND，
  两个 0.9mm 定位孔与走线均通过专用断言；PCM12 为 270°，执行器朝 +X、沿 Y
  滑动。
- Gerber ZIP 含 14 项并与 `hardware/rendered/pcb/gerber` 逐文件哈希一致；
  `SHA256SUMS.txt` 全部回读通过。关键摘要：
  - PCB：`1203e44faf57a0501d17721ee7e7316891b6a1a589fe520a173ac3c9963e53f0`
  - 原理图：`8cecdf886a8a92cd6481c2ae61d861dd0df252a5e9611799072f278f142ece92`
  - Gerber ZIP：`9e197b5dfea461498ae12ce6ef196480c6f7cf740de78c440963b027fbb2281f`

### 外壳

- 最终 SCAD 成功重导 base、lid、button、switch 四个 STL；STL 经 OpenCascade
  缝合时无 free/multiple edge。
- 四个 STEP 均回读为有效 BRep，且各包含 1 solid / 1 shell：
  - base：93 × 65 × 16.3mm
  - lid：93 × 65 × 12.35mm（局部坐标包络）
  - button：6.15 × 8.4 × 3.9mm
  - switch：11 × 4.6 × 3.9mm
- PTS 按键和 PCM 滑帽的 PCB 坐标、致动面、吃隙、防脱法兰、装配顺序及盖内独立
  止挡已做数字复核。结果只能表述为“可打印原型几何通过”。
- 最终 STEP SHA-256：base
  `7fb72f793a835f2cf5ac27aaabc20780f48dbdc4f9b6dbb3a8e277da5c691f2c`，lid
  `5e19188dbed4804fb84b32682d43817da9f119438c5dfa61d61710ac9f1b7e24`，button
  `118e0140a0ba5e4ecd6692816e9397e8b35a580939437a31674f1f21a93e9782`，switch
  `5de6cd3810d5abc50b17595c839e8da6d9e51a563e1b7a60c70a59eff46bd0f7`。

## 未完成、不能由本机替代的验收门

1. 用户完成 `claude auth login` 后，验证真实 `/usage` 与 statusLine 的 5h/7d
   数据一致性；不得用提示词代替本地 slash command。
2. 云部署需要真实域名、DNS、可开放的 80/443 端口，并在 ESP32 上验证实际证书链。
3. 必须锁定电池供应商完整 P/N、保护板参数、线序和对应 UN38.3/IEC62133 报告编号；
   当前通用 `755060` 描述不足以放行采购。
4. LTC4365 R0.1 仅作反向电源门控，外部 UV/OV 阈值未实现；受保护电池深放断开后
   从 USB 唤醒充电的路径仍有架构风险。需用电池模拟器验证或改电源架构。
5. J1/J2 约 4.5mm 高，而当前屏下空间约 3.1mm；J5 竖直排针只用于开放样机并在
   封闭版本 DNP。低矮线束、AUX 终端和应力释放尚未冻结。
6. 需用实购器件完成 PTS/PCM 执行面 Z 高、0.1mm PCB 间隙、3.5N 按压、打印收缩、
   防脱/止挡寿命、FPC/JST/USB 干涉和跌落检查。
7. 需实测反接、USB-only、battery-only、同时供电、显示浪涌、C4/C7 瞬态、PTC/MOSFET
   温升、充电温升、关机电流不超过 1.5mA，以及 60% 亮度/15 秒刷新续航不少于 8h。
8. 最初约 90 × 65 × 16mm 的厚度目标无法容纳原厂模块真实包络；当前可打印基线为
   93 × 65 × 25.7mm。若必须回到 16mm，需要更换器件/堆叠后重新设计，不是缩放 STL。

完成以上项目并形成带仪器、批次和照片/波形的记录前，`hardware/pcb/PRODUCTION_STATUS.md`
不得删除或改成生产放行。
