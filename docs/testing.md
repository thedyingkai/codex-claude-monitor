# 测试与验收

本页分开记录“仓库内可自动执行的检查”和“必须在真实账号/硬件上完成的验收”。运行命令本身不等于通过，生产包应保留日期、工具版本、原始日志和测量条件。仓库不能证明尚未执行的焊接、功耗、温升或机械实测。

## 软件自动化检查

在项目根目录运行：

```sh
gofmt -l ./cmd ./internal
go test -race ./...
go vet ./...
npx --yes @redocly/cli@1.34.5 lint api/openapi.yaml
```

`gofmt -l` 应没有输出。Windows 环境若 Go race detector 不可用，至少运行 `go test ./...`，并在 Linux CI 上补跑 race 测试。

当前单元/集成测试覆盖的主要行为包括：

- Codex app-server 初始化、RPC 调用、断线重连、超时、300/10080 分钟窗口映射与未知窗口；
- Claude `/usage` NDJSON/渲染文本、Claude Code 2.1.220 的英文绝对重置时间、`resets in 17h6m` 相对时间、跨年日期、statusLine 结构、缺失窗口、未登录命令返回非零但 stdout 有效；
- 明确断言 Claude 采集命令只有 `-p /usage`，并带无会话持久化和探测标记；
- Hooks 事件最小化、回环限制、共享密钥、探测排除、重复安装、卸载恢复和损坏配置拒绝；
- 主任务/子任务生命周期、心跳、无 ID 的子任务结束与 15 分钟孤儿失效；
- SQLite 迁移/WAL、令牌生命周期、原始令牌不落盘、多 Agent 选新、离线与任务汇总；
- API 认证作用域、Agent 绑定、完整状态替换、旧报告、未知字段、64 KiB 上限、频率限制和日志脱敏；
- Agent 默认强制 HTTPS、采集失败时保留最后有效窗口以及服务文件生成。

这些测试使用固定样本或本地测试服务器。它们不能替代真实 CLI 版本、账号订阅和公网 TLS 验收。

## 跨平台构建

Windows：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Version test
Get-ChildItem .\dist
```

Linux/macOS shell：

```sh
VERSION=test ./scripts/build.sh
file dist/quota-monitor-*
```

应生成：

- `quota-monitor-windows-amd64.exe`
- `quota-monitor-linux-amd64`
- `quota-monitor-linux-arm64`

至少在各目标操作系统/架构上执行 `quota-monitor version` 和 `quota-monitor --help`，仅交叉编译成功不足以证明运行兼容。

## CLI 与本地 API 冒烟测试

使用临时目录和独立数据库，避免污染生产数据。下面是检查顺序，令牌不要写入测试日志：

1. `quota-monitor server --listen 127.0.0.1:8787 --db <临时目录>/monitor.db --log-format text`；
2. `quota-monitor token create --db <同一数据库> --scope agent:write --agent-id test-agent`；
3. `quota-monitor token create --db <同一数据库> --scope display:read`；
4. 使用 Agent 令牌向 `/api/v1/agent/report` 上报 schema v1 固定样本，应返回 204；
5. 使用显示令牌读 `/api/v1/display/snapshot`，应返回 200，且不包含 session ID；
6. 交换两个令牌的用途，应返回 401；把报告 `agentId` 改成其他值，应返回 403；
7. 重放相同 `sentAt` 报告，应返回 409；发送超过 64 KiB 的报告，应返回 413；
8. 停止服务并重新启动同一数据库，快照和令牌应继续可用。

还应手动检查所有错误响应均为 JSON、没有 `Access-Control-Allow-Origin`，且服务器日志中没有 Bearer 令牌、提示词或 Hook 原始载荷。

## 多机与时间边界

用两个不同的 `agentId` 和两个写入令牌构造报告，验证：

- 同一 provider 的额度选择 `observedAt` 较新的一份，百分比不相加；
- 两个 Agent 上相同 session ID 仍是两个任务，不同 provider 的相同 session ID 也分别统计；
- 单个 Agent 的新完整报告移除旧任务后，计数随之下降；
- 45 秒没有新报告的 Agent 不再计入 `online`，其任务不再统计；
- 15 分钟未触达的孤儿任务失效；
- 5 分钟前的额度样本为 `stale`，完全没有样本为 `unavailable`；
- 空数据库快照的 `unavailable` provider 必须省略 `observedAt`，不得编码成
  `0001-01-01T00:00:00Z`；固件应接受该快照并显示 `N/A`；
- 5h/7d 分别测试缺失、0%、100%、重置前后与解析失败；
- 客户端时间向未来偏移超过 5 分钟或报告年龄超过 15 分钟时被拒绝。

跨重置测试应使用可控时钟或固定 RFC3339 样本，避免依赖真实等待。

## Hooks 安装/卸载验收

自动化测试使用临时 HOME；生产前还要在一份人工构造的用户配置副本上执行：

1. Codex Hooks 中预置一个无关命令；Claude 设置中预置 Hooks、status line 和其他设置；
2. 执行 `hooks install --home <测试HOME> --executable <绝对路径>`；
3. 确认原内容保留、只增加带本项目标记的条目，并产生 `.bak`；
4. 再安装一次，确认托管条目没有重复；
5. 执行 `hooks uninstall --home <测试HOME>`；
6. 确认无关 Hooks 未变、原 Claude status line 恢复、本项目共享密钥和状态文件删除；
7. 对损坏 JSON 执行安装，应报错且原文件字节不变。

真实用户目录测试前必须单独备份。新 Codex Hooks 仍需进入 `/hooks` 人工检查并信任；测试不能绕过这一机制。

活动任务端到端测试应启动常驻 Agent，而不是 `--once`。分别创建 Codex/Claude 主任务和子任务，观察开始后 30 秒内增加、正常结束后下降，并模拟 CLI 崩溃验证 15 分钟孤儿失效。

## 真实 Codex 采集

仓库提供显式选择加入的 live 测试；它会查询当前用户已登录的 Codex：

PowerShell：

```powershell
$env:QUOTA_MONITOR_LIVE_CODEX_TEST = "1"
go test -v ./internal/collector -run TestCodexCollectorLive
Remove-Item Env:QUOTA_MONITOR_LIVE_CODEX_TEST
```

Shell：

```sh
QUOTA_MONITOR_LIVE_CODEX_TEST=1 go test -v ./internal/collector \
  -run TestCodexCollectorLive
```

验收应记录 Codex CLI 版本、登录状态、计划类型、是否识别 300/10080 分钟窗口。日志中不得记录邮箱或 OAuth 数据。升级 Codex CLI 后重复执行，确保 app-server 协议仍兼容。

## 真实 Claude 采集

Claude 尚未登录时只能验证 `unavailable/not_authenticated` 路径。用户完成官方 OAuth 后，在 Agent 同一用户下执行：

```sh
claude --version
claude auth status --json
claude -p "/usage" --output-format stream-json --verbose --no-session-persistence
quota-monitor agent --config /absolute/path/to/agent.json --once
```

验收项目：

- `/usage` 是本地 slash command；采集器没有添加第二段普通提示词；
- stream-json 中真实 Pro/Max 5h/7d 数据能被解析；当前版本可能不提供结构化 `rate_limits`，而把 `Current session`、`Current week (all models)` 和人类可读重置时间放在 `result` 文本中，缺失窗口仍应保持 `null`；
- API 的 used/remaining 百分比和重置时间与 CLI 显示一致；
- 开启常驻 Agent 并进入 Claude 交互会话后，statusLine 数据能更新/回退；
- 原 status line 的 stdout 仍显示，失败不会阻断 Claude；
- 探测进程不增加活动任务数；
- 升级 Claude Code 后重复完整测试。

不要把真实 `/usage` 原始输出直接提交到仓库；先去除账号标识、计划细节以外的元数据和任何凭据。

## Docker/Caddy 验收

静态检查与构建：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml config
docker compose --env-file deploy/.env -f deploy/docker-compose.yml build --no-cache monitor
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

生产域名验收：

- 80 重定向到 HTTPS，443 证书链受信、域名匹配且在有效期内；
- Caddy 可以访问 ACME/DNS 外网，Go 服务仅连接私有 backend 网络；
- 宿主机没有对外监听 8787；
- `/healthz` 为 200，API 无 CORS，响应带 `Cache-Control: no-store`；
- 未授权、错误作用域、超限与频率限制行为符合 OpenAPI；
- 重启 `monitor` 后 SQLite 状态保留；更新 Caddy/服务镜像后证书和数据卷保留。

可使用 `curl -v`、`openssl s_client` 或独立 TLS 扫描器检查证书。不能用关闭证书校验的请求作为通过依据。

## 固件自动化检查

需要 Python。以下命令把 PlatformIO 6.1.18 安装在项目内的虚拟环境中；`firmware/platformio.ini` 还固定了 ESP32 平台和库版本。

```sh
python3 -m venv .tools/pio-venv
.tools/pio-venv/bin/python -m pip install platformio==6.1.18
.tools/pio-venv/bin/pio test -d firmware -e native
.tools/pio-venv/bin/pio run -d firmware -e e32r28t
.tools/pio-venv/bin/pio run -d firmware -e firebeetle2_esp32s3
python firmware/scripts/check_snapshot_fixture.py
```

E32R28T 的 OTA 槽为 1,966,080 字节，发布门禁还要求至少保留 128 KiB。Linux/macOS
可用与 CI 相同的检查：

```sh
firmware_bin=firmware/.pio/build/e32r28t/firmware.bin
size_bytes=$(stat -c '%s' "$firmware_bin")
slot_bytes=1966080
headroom_bytes=$((slot_bytes - size_bytes))
test "$size_bytes" -lt "$slot_bytes"
test "$headroom_bytes" -ge 131072
```

Windows PowerShell 使用 `.\.tools\pio-venv\Scripts\pio.exe` 替换上述 `pio` 路径。

目标板构建成功只证明编译与链接。烧录后还应验证：

- schema v1 正常、未知字段可忽略、错误 schema/损坏 JSON 被拒绝；
- `generatedAt` 过旧、明显位于未来和重放旧快照时被拒绝或明确标为过期；
- 5h/7d 缺失、0%、100%、低于 20%、过期和重置时间显示；
- Wi-Fi 丢失、DNS/TLS/API 失败时保留缓存并退避，离线重启后仍显示 NVS 中的上次
  快照并标为过期，网络恢复后自动刷新；
- 服务器证书错误必须失败，不能静默降级；
- NVS 损坏/恢复、串口 `show/set/test/save/factory-reset` 与秘密遮蔽；
- `AWAKE/DIMMED/BACKLIGHT_OFF/PORTAL/OTA` 阈值、禁用值、`millis()` 回卷、亮度恢复、
  熄屏刷新不点亮、触摸唤醒并刷新以及 1 秒请求合并；
- 同步 HTTPS 已移出 LVGL 主循环；在十秒请求超时期间触摸仍应在 200 ms 内亮屏；
- E32R28T 的 BOOT 短按/1.2–5 秒/5 秒长按、左右触摸区长按、三档亮度和串口
  `factory-reset`；BOOT 在复位时保持低电平会进入下载模式，不能当作开机复位组合键；
- 正常情况下额度变化 90 秒内到屏，任务变化 30 秒内到屏。

## 手机配网与 OTA 验收

配网页至少覆盖：无配置自动开启、BOOT/串口手动开启、十分钟超时、单客户端、Wi-Fi
扫描和手工 SSID、错误密码、NTP/DNS/TLS/401/JSON 失败、旧配置完整回退、保存中
断电恢复、空秘密保留旧值、恶意 SSID 转义、CSRF、超过 2 KiB 的 POST，以及从 STA
地址访问被拒绝。测试日志不得出现密码、Bearer 令牌或请求正文。

OTA 服务器/设备端到端至少覆盖：

- 无令牌、错误作用域、未发布 404、损坏发布 503、错误板型、同版本和降级版本；
- 超过非活动槽、截断文件、错误 Content-Length/SHA-256、下载断线、写入失败和重定向；
- manifest 只允许严格三段式版本且无外部 URL，下载必须和快照 API 同源；
- 连接稳定 USB 电源复选框未勾选时拒绝安装，服务器永不自动推送；
- 成功升级后 30 秒自检再标记有效；自检期崩溃/重启自动回滚，原固件和 NVS 可用；
- `firmware publish` 原子更新、文件权限 0600，发布不重启服务；nginx/Caddy 流式传输；
- 服务重启后 manifest/文件继续可用，未知服务器 JSON 字段不会破坏旧客户端。

真机测试要分别记录常亮、10% 降亮和背光关闭三态电流，并明确背光关闭不是整机关机。
OTA 断电测试必须在可恢复的 USB/串口条件下进行，不能连接存在安全疑问的锂电池。

烧录与真机测试需记录 PlatformIO 平台/库版本、固件 Git 修订、开发板批次与屏幕批次。

## E32R28T 与物理验收

当前 E32R28T 已集成主控、屏幕、触摸和充电器，不制造自定义 PCB。旧
`hardware/pcb` Gerber/BOM 及旧 DFRobot 外壳已经停用，不属于当前发布门禁。
实物到货后必须记录：

- 板型/修订、Type-C/CH340C 上传、ILI9341 颜色与横屏方向；
- XPT2046 四角/中心校准、左右触摸区和 BOOT 运行时按键；
- GPIO34 在电池供电、USB 充电和满电三种状态下与万用表的误差；
- MX1.25-2P 机械匹配、板端 `+/-` 丝印、电池实际线序和保护板资料；
- USB-only、battery-only、USB+battery、低电与掉电恢复；
- 30/60/100% 亮度功耗、充电电流/温升，以及 60% 亮度、15 秒刷新至少 8 小时续航；
- 实测模块、电池、连接器和线缆包络；绝缘、应力释放、膨胀空间、Type-C 插拔和
  闭壳温升无干涉。

当前板没有整机负载开关，GPIO21 只控制背光，所以旧方案“不超过 1.5mA 的软关机”
不是本硬件的已实现指标。若仍要求真正关机，应先增加并验证独立电源架构，再更新
固件和外壳。

锂电验收必须使用带保护板、极性核对无误的 3.7V/1S 成品电池。禁止直接焊接电芯
极耳、挤压或穿刺软包电池；出现膨胀、异味或异常温升时立即停止测试并按安全规范处理。

## 发布签字清单

发布前至少由一名未参与实现的人复核：

- [ ] Go 格式、race 测试、vet 和三个目标构建通过；
- [ ] OpenAPI 与实际 JSON/状态码一致；
- [ ] 本地和生产 HTTPS 端到端通过，日志无秘密；
- [ ] 当前 Codex 版本真实采集通过；
- [ ] 用户完成 Claude OAuth 后，`/usage` 与 statusLine 双通道通过；
- [ ] Hooks 重复安装/卸载及原配置恢复通过；
- [ ] Windows 登录任务与 Linux `systemd --user` 重启恢复通过；
- [ ] E32R28T 与旧目标均编译，E32 烧录、触摸、断网/TLS/NVS/按键测试通过；
- [ ] E32 固件小于 1,966,080 字节并保留至少 128 KiB，手机配网与 OTA/自动回滚通过；
- [ ] E32R28T、电池、MX1.25 极性、绝缘和外壳 3D/实物干涉复核通过；
- [ ] 充电温升、三档亮度功耗、续航与整机尺寸/重量实测通过；
- [ ] 真实域名、DNS、80/443、备份和令牌撤销演练通过。

没有原始报告或实测记录的项目保持未勾选，不以“预计”“设计目标”替代通过结论。
