# 快速开始

推荐在云服务器直接运行 `quota-monitor standalone`：API、SQLite、额度采集和 Hooks 都在同一个进程中，本地电脑无需安装或常驻 Agent。完整步骤见 [Standalone 云端部署](standalone.md)。本页后续保留 Docker server + Windows/Linux Agent 流程，供确实需要多机任务统计的场景使用。示例域名为 `monitor.example.com`，使用时请替换。

## 推荐路径：Standalone

Standalone 必须和官方 Codex/Claude CLI 使用同一个云服务器用户。先登录，再启动服务：

```sh
quota-monitor login codex
# 只有购买并准备启用 Claude 时才运行下一条
quota-monitor login claude

quota-monitor hooks install --executable "$HOME/.local/bin/quota-monitor"
quota-monitor standalone --claude=false
```

首次运行会创建 SQLite、Hook 密钥和 `display.token`。生产环境让程序只监听 `127.0.0.1:8787`，再由宿主机 Caddy 提供公网 HTTPS；不要直接开放明文 8787。登录失效时，日志和显示快照会提示重新登录。再次执行对应的 `quota-monitor login` 后，采集会在下一轮自动恢复，无需重启服务。

Standalone 的任务数只来自云服务器上的 Codex/Claude 会话。本地电脑不运行 Agent 时，本地任务无法计入；账号额度仍可由云服务器正常查询。

资源核算必须包含官方 CLI。[Codex CLI 官方要求](https://github.com/openai/codex/blob/main/docs/install.md#system-requirements)至少 4GB、推荐 8GB，[Claude Code 官方要求](https://code.claude.com/docs/en/setup#system-requirements)4GB 以上，因此官方支持配置至少应有 4GB。Standalone 默认串行执行 Codex → 释放 app-server → Claude，以减少两个 CLI 的内存重叠；这不能降低单个 CLI 的峰值。1 核、512MB 主机同时启用两者仍只是无稳定保证的实验性运行，至少应配置 1GB swap、先以前台方式测试并持续监控 OOM，且不要关闭默认串行模式。

## 1. 构建程序

需要 Go 1.23 或更高版本。项目本身不要求 CGO。

Windows PowerShell：

```powershell
Set-Location C:\path\to\codex-claude-monitor
go test ./...
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -trimpath -o .\bin\quota-monitor.exe .\cmd\quota-monitor
.\bin\quota-monitor.exe version
```

Linux：

```sh
cd /path/to/codex-claude-monitor
go test ./...
mkdir -p bin
go build -trimpath -o bin/quota-monitor ./cmd/quota-monitor
./bin/quota-monitor version
```

项目还提供 `scripts/build.ps1` 和 `scripts/build.sh`，一次生成 Windows amd64、
Linux amd64、Linux arm64 三个文件及 `SHA256SUMS` 到 `dist/`。构建号可通过
`-Version 1.0.0`（PowerShell）或 `VERSION=1.0.0`（Shell）注入。

### 仅在本机试运行服务

Windows 可直接启动回环 HTTP 服务：

```powershell
.\bin\quota-monitor.exe server `
  --listen 127.0.0.1:8787 --db .\data\monitor.db --log-format text
```

Linux 对应命令为：

```sh
./bin/quota-monitor server \
  --listen 127.0.0.1:8787 --db ./data/monitor.db --log-format text
```

另开终端访问 `http://127.0.0.1:8787/healthz`。只有本地测试 Agent 才可把 `serverUrl` 设为该地址并启用 `allowInsecureHttp: true`。不要把无 TLS 的 8787 绑定到 `0.0.0.0`、局域网或公网；正式部署继续使用下一节的 Caddy HTTPS。

## 2. 部署云端服务

准备工作：

- 一台装有 Docker Engine 与 Docker Compose v2 的 Linux 云服务器；
- 一个 A/AAAA 记录已指向该服务器的域名；
- 公网能够访问 TCP 80 和 443；
- 云防火墙不要开放应用端口 8787。

在项目根目录执行：

```sh
cp deploy/.env.example deploy/.env
```

编辑 `deploy/.env`：

```dotenv
MONITOR_DOMAIN=monitor.example.com
MONITOR_VERSION=1.0.0
```

启动并检查：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --tail=100 caddy monitor
curl --fail --show-error https://monitor.example.com/healthz
```

正常响应是 `{"status":"ok"}`。Caddy 自动申请证书；首次启动失败时先核对 DNS、系统时间、80/443 入站规则和 Caddy 的外网连通性。Compose 不把 `8787` 发布到宿主机。

Compose 把数据库和 `/data/firmware` 一起保存在 `monitor-data` 卷中。构建 E32R28T
固件并通过容量门槛后，可用一次性容器在同一数据卷原子发布，服务无需重启：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml run --rm --no-deps \
  -v "$PWD/firmware/.pio/build/e32r28t/firmware.bin:/tmp/firmware.bin:ro" \
  monitor firmware publish --firmware-dir /data/firmware \
  --board e32r28t --version 0.3.3 --file /tmp/firmware.bin
```

Windows 应把 `-v` 左侧换成 `firmware.bin` 的绝对路径。不要通过 Caddy/nginx 增加上传
路径；公网固件接口只读并继续使用显示令牌。

## 3. 创建令牌

令牌没有远程管理 API，只能由能够访问 SQLite 文件的服务器管理员创建。Docker 部署中应在正在运行的 `monitor` 容器内调用完整程序路径：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec monitor \
  /app/quota-monitor token create --db /data/monitor.db \
  --scope agent:write --agent-id desktop-01 --label "Windows desktop"

docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec monitor \
  /app/quota-monitor token create --db /data/monitor.db \
  --scope display:read --label "Desk display"
```

每条 Bearer 令牌只显示一次。第一条保存到对应电脑的 Agent 令牌文件，第二条稍后通过 USB 串口写入 ESP32。不要截图、提交到 Git、放入 JSON 配置或发到聊天记录。每台电脑和每块显示器都应创建独立令牌。

非 Docker 服务使用同一个命令并把 `--db` 指向服务实际使用的数据库，例如：

```sh
quota-monitor token create --db /var/lib/quota-monitor/monitor.db \
  --scope agent:write --agent-id server-01 --label "Cloud agent"
```

## 4. 在 Agent 主机登录 Codex 和 Claude

必须使用将来运行 Agent 的同一个操作系统用户登录，不能以管理员/root 登录后再让普通用户运行 Agent。

Windows 或 Linux 均执行：

```text
codex login
codex login status
claude auth login
claude auth status --json
```

如云服务器也要常驻采集，需在云服务器的目标普通用户下重复登录，并在该用户下运行 `systemd --user` Agent。OAuth 凭据保留在 Codex/Claude 自己的用户凭据存储中；本项目不直接解析这些凭据，也不会把它们上传到监视服务器。

当前未登录 Claude 也可以先完成其余配置；快照会把 Claude 标为不可用。登录完成后按本页“真实 Claude 验收”验证。

## 5. 配置 Agent

配置 JSON 不直接保存写入令牌。令牌和 Hook 共享密钥分别放在独立文件中。

### Windows

先建立目录，把刚才创建的 `agent:write` 令牌单独保存为 `%USERPROFILE%\.quota-monitor\agent.token`，文件中只能有令牌本身。限制该目录和文件只允许当前用户访问。

然后安装 Hooks。安装程序会创建 `%USERPROFILE%\.quota-monitor\hook-secret`，备份并合并现有 `%USERPROFILE%\.codex\hooks.json` 和 `%USERPROFILE%\.claude\settings.json`：

```powershell
$Binary = (Resolve-Path .\bin\quota-monitor.exe).Path
& $Binary hooks install --executable $Binary
```

把 `config/agent.example.json` 复制到 `%USERPROFILE%\.quota-monitor\agent.json`，至少修改 `agentId` 和 `serverUrl`：

```json
{
  "agentId": "desktop-01",
  "serverUrl": "https://monitor.example.com",
  "tokenFile": "~/.quota-monitor/agent.token",
  "hookAddr": "127.0.0.1:47632",
  "hookSecretFile": "~/.quota-monitor/hook-secret",
  "codexCommand": "codex",
  "claudeCommand": "claude",
  "planOverrides": {},
  "reportInterval": "15s",
  "collectInterval": "60s",
  "sequentialCollection": false,
  "maxBackoff": "2m",
  "allowInsecureHttp": false
}
```

兼容 Agent 的 `sequentialCollection` 默认为 `false`，因此仍会并发查询两个 provider；只有需要在低内存主机上压低 CLI 重叠峰值时才改为 `true`。Standalone 的对应命令行选项默认值相反，为串行采集。

`planOverrides` 仅用于补齐 CLI 没有明确暴露的套餐细档，不改变额度或认证状态。
Codex app-server 当前用 `prolite` 表示 Pro 5x、`pro` 表示 Pro 20x，Agent 会自动规范为
`pro5` / `pro20`。如果将来 CLI 未返回细档，也可按真实订阅显式填写
`{"codex":"pro5"}` 或 `{"codex":"pro20"}`。Claude 只返回笼统的 `max` 时，可填写
`{"claude":"max5"}` 或 `{"claude":"max20"}`；未订阅可填
`{"claude":"none"}`。不要根据额度百分比猜测 5x/20x，也不要填写并未购买的档位。

先做一次采集上报：

```powershell
& $Binary agent --config "$env:USERPROFILE\.quota-monitor\agent.json" --once
```

成功时输出 `agent report accepted`。`--once` 只验证采集和上报，不会持续接收 Hooks。

### Linux

```bash
install -d -m 700 "$HOME/.quota-monitor" "$HOME/.config/quota-monitor" "$HOME/.local/bin"
read -r -s -p 'Agent token: ' QMON_TOKEN; printf '\n'
printf '%s\n' "$QMON_TOKEN" > "$HOME/.quota-monitor/agent.token"
unset QMON_TOKEN
chmod 600 "$HOME/.quota-monitor/agent.token"

install -m 755 ./bin/quota-monitor "$HOME/.local/bin/quota-monitor"
"$HOME/.local/bin/quota-monitor" hooks install \
  --executable "$HOME/.local/bin/quota-monitor"
cp config/agent.example.json "$HOME/.config/quota-monitor/agent.json"
chmod 600 "$HOME/.config/quota-monitor/agent.json"
```

编辑 `~/.config/quota-monitor/agent.json` 中的 `agentId` 与 `serverUrl`，再验证：

同时执行 `command -v codex` 和 `command -v claude`，把返回的绝对路径分别写入
`codexCommand`、`claudeCommand`。这对 npm/nvm/asdf 安装尤其重要：交互式 shell
能找到的命令，不一定在 `systemd --user` 的 PATH 中。生成的 unit 已包含
`~/.local/bin`、pnpm、npm-global 和系统目录，但绝对路径最可靠。

```sh
"$HOME/.local/bin/quota-monitor" agent \
  --config "$HOME/.config/quota-monitor/agent.json" --once
```

`allowInsecureHttp: true` 仅允许配置 `http://` 开发服务器，绝不能用于公网部署；正常配置保持 `false`。

## 6. 确认 Hooks 信任

`hooks install` 只添加带 `quota-monitor-managed-v1` 标记的条目，原配置会保留，并在同目录生成带 UTC 时间戳的 `.bak` 备份。已有 Claude `statusLine` 会由包装器继续执行并原样显示。

安装后重新启动 Codex。在 Codex 中打开 `/hooks`，逐项检查命令路径和事件，再按 Codex 的信任流程人工批准；安装器不会绕过确认。Claude Code 也应重启，使 `~/.claude/settings.json` 或 Windows 对应用户目录中的设置重新加载。

Agent 必须常驻运行，Hooks 才能连接回 `127.0.0.1:47632`。Agent 未运行时 Hook 会快速跳过，不阻塞 Codex/Claude，但任务计数不会更新。

卸载命令为：

```text
quota-monitor hooks uninstall
```

卸载只移除本项目标记的 Hooks；若 Claude status line 仍是本项目包装器，就恢复安装前保存的值。它还会删除本项目的 Hook 状态文件和共享密钥，但不会删除 Agent 令牌、服务或历史 `.bak` 文件。

## 7. 设置 Agent 自动启动

### Windows 登录任务

在当前用户的 PowerShell 中生成任务 XML：

```powershell
$Binary = (Resolve-Path .\bin\quota-monitor.exe).Path
$Config = "$env:USERPROFILE\.quota-monitor\agent.json"
$TaskXml = "$env:USERPROFILE\.quota-monitor\quota-monitor-task.xml"
& $Binary service generate --target windows --executable $Binary `
  --config $Config --output $TaskXml
schtasks.exe /Create /TN "Quota Monitor Agent" /XML $TaskXml /F
schtasks.exe /Run /TN "Quota Monitor Agent"
schtasks.exe /Query /TN "Quota Monitor Agent" /V /FO LIST
```

该任务使用当前用户的交互式登录令牌和最低权限，在登录时启动，因此能访问同一用户的 CLI 登录状态。不要把它改成 SYSTEM 账户。

### Linux `systemd --user`

```sh
mkdir -p "$HOME/.config/systemd/user"
"$HOME/.local/bin/quota-monitor" service generate --target systemd \
  --executable "$HOME/.local/bin/quota-monitor" \
  --config "$HOME/.config/quota-monitor/agent.json" \
  --output "$HOME/.config/systemd/user/quota-monitor-agent.service"
systemctl --user daemon-reload
systemctl --user enable --now quota-monitor-agent.service
systemctl --user status quota-monitor-agent.service
journalctl --user -u quota-monitor-agent.service -n 100 --no-pager
```

若云服务器在用户退出登录后仍需采集，由管理员执行 `loginctl enable-linger <用户名>`。仍应使用普通用户服务，不要改成系统级 root 服务。

## 8. 检查 API 快照

把显示令牌放入环境变量，避免直接写进命令历史：

Linux：

```bash
read -r -s -p 'Display token: ' DISPLAY_TOKEN; printf '\n'
curl --fail --show-error \
  -H "Authorization: Bearer $DISPLAY_TOKEN" \
  https://monitor.example.com/api/v1/display/snapshot
unset DISPLAY_TOKEN
```

Windows PowerShell 可在当前会话设置 `$env:DISPLAY_TOKEN` 后执行：

```powershell
curl.exe --fail --show-error `
  -H "Authorization: Bearer $env:DISPLAY_TOKEN" `
  https://monitor.example.com/api/v1/display/snapshot
Remove-Item Env:DISPLAY_TOKEN
```

还可运行 `quota-monitor doctor --health-url https://monitor.example.com/healthz`。当前 `doctor` 只检查健康接口，不检查令牌、CLI 登录或配额解析。

## 9. 配置 ESP32 显示终端

当前默认目标是 Keyes 62520093/62520094 系列中的触摸版 `E32R28T`（经典 ESP32-32E、4MB Flash），不是 ESP32-S3。先准备一根可传数据的 Type-C 线并安装 CH340 驱动，再按固定版本的 `firmware/platformio.ini` 构建。首次测试只接 USB，不接电池。Windows PowerShell：

生产构建前，把部署环境的公开设备根证书保存为
`firmware/certs/device-root-ca.pem`。该文件不会进入 Git；只做离线编译测试时，可以
复制 `device-root-ca.example.pem`，但示例证书不能连接生产服务器。

```powershell
py -m venv .tools\pio-venv
.\.tools\pio-venv\Scripts\python.exe -m pip install platformio==6.1.18
.\.tools\pio-venv\Scripts\pio.exe test -d firmware -e native
.\.tools\pio-venv\Scripts\pio.exe run -d firmware -e e32r28t
.\.tools\pio-venv\Scripts\pio.exe run -d firmware -e e32r28t -t upload
.\.tools\pio-venv\Scripts\pio.exe device monitor -b 115200
```

Linux：

```sh
python3 -m venv .tools/pio-venv
.tools/pio-venv/bin/python -m pip install platformio==6.1.18
.tools/pio-venv/bin/pio test -d firmware -e native
.tools/pio-venv/bin/pio run -d firmware -e e32r28t
.tools/pio-venv/bin/pio run -d firmware -e e32r28t -t upload
.tools/pio-venv/bin/pio device monitor -b 115200
```

串口为 115200 baud，每条命令一行。依次输入：

```text
show
set ssid YourWiFiName
set password YourWiFiPassword
set ssid2 OptionalBackupWiFi
set password2 OptionalBackupPassword
set ssid3 OptionalBackupWiFi2
set password3 OptionalBackupPassword2
set base_url https://monitor.example.com
set token qmon_REPLACE_WITH_DISPLAY_TOKEN
set timezone CST-8
set refresh_seconds 15
set brightness_percent 60
set dim_after_seconds 60
set screen_off_after_seconds 300
set screen_off_refresh_seconds 60
set external_power_sense_enabled 0
save
test
```

`ssid/password` 是必需的主网络；`ssid2/password2` 是 v0.3.1 起可选的备用网络，
`ssid3/password3` 是 v0.3.2 起可选的备用网络 2，不需要的槽位可省略。三组非空
SSID 不能相同。设备每轮严格按“主 → 备用 1 → 备用 2”尝试，下一轮仍从主网络开始；
最后成功的网络不会改变这个优先级。也可输入
`wifi-promote {"ssid":"NewPrimary","password":"NewPassword"}`，把新网络提升为主网络、
原主网络和备用 1 依次后移，随后执行 `save`。`base_url` 也可包含完整
`/api/v1/display/snapshot` 路径；固件会避免重复追加。`refresh_seconds` 范围是
5–3600 秒，默认 15 秒。`show` 会遮蔽三组 Wi-Fi 密码和令牌；`save` 前的修改只在
内存中，必须执行 `save` 才会写入 NVS。`factory-reset` 会清空 Wi-Fi/API 配置并
重启，无法恢复。

运行时可短按板上 BOOT 键立即刷新，按住 1.2–5 秒查看诊断，持续 5 秒打开手机
配网页。亮屏时触摸任意位置都会刷新，长按左/右半屏分别显示网络诊断/设备信息；
熄屏后的第一次触摸会立即恢复亮度并刷新。E32R28T 没有旧方案的独立电源拨杆和
第二实体按键，因此恢复出厂配置以串口 `factory-reset` 为准。

E32R28T 原板没有可直接读取的 USB/VBUS 存在信号。v0.3.3 默认根据 GPIO34 电池
电压的持续上升趋势保守推断充电状态；推断成立时，背光强制使用 100% PWM，并停用
自动降亮和熄屏。拔出后要等持续下降趋势解除推断，随后无操作计时才从零重新开始。
稳定或已充满的电池、以及开机前就已插着 USB 的情况没有可靠的上升基线，因此这条
路径不能保证识别，也不能称为 USB 检测。

需要可靠判断 VBUS 时，v0.3.2 起可选从串口排针 P2 pin 1 的 Type-C 原始 `+5V`
经 `100kΩ 1%` 接到 GPIO35，再从 GPIO35 经 `150kΩ 1%` 接 GND；可选从 GPIO35
并联 `100nF` 到 GND。禁止把 +5V 直接接 GPIO35，也不能省略 150kΩ 下拉。完成并
核对接线后，执行 `set external_power_sense_enabled 1` 和 `save`；默认值 `0` 必须
保留到接线完成。GPIO35 确认 USB 存在时同样强制 100% PWM并停用省电行为，拔出后
重新开始无操作计时；该路径不依赖充电阶段，满电或开机即插电时仍可靠。

确认 USB 下屏幕、触摸和串口均正常后，拔掉 Type-C 并核对电池极性，再插入带保护板的 3.7V/1S、MX1.25-2P 成品电池。不要热插时强推插头，不要焊电芯极耳。完整步骤见[硬件装配说明](hardware/assembly.md)。

固件只接受 HTTPS，并使用内置 CA 校验证书。它会校验 `schemaVersion: 1`、
所有必填对象和计数字段、额度百分比合计、RFC3339 重置/观测时间以及 Agent 汇总
关系；未知扩展字段会忽略。API/Wi-Fi 失败时保留缓存值；最近的规范化快照也会
限频写入 NVS，所以离线重启后仍可显示，并按服务器 `generatedAt` 的真实年龄
标为过期。Claude 尚未开始的 5h 窗口是受限例外：只有 0% 已用、100% 剩余时才允许
`resetsAt: null`，并显示 `重置 未开始`。固件同时拒绝未来时间和重放/回滚快照。

v0.3.0 没有有效配置时会自动创建临时 WPA2 热点；已有配置时可长按 BOOT 5 秒或
串口输入 `portal`。手机连接屏幕显示的 `QMON-XXXXXX` 和随机密码，再访问
`http://192.168.4.1`。页面只在热点侧开放，10 分钟无操作后关闭。新配置全部通过
Wi-Fi、NTP、TLS、令牌和快照测试后才会写入 NVS。

首次启用手机配网和 OTA 仍需通过 USB 烧录 v0.3.0。以后可在临时配网页查看服务器
发布的更高版本，并在连接稳定 USB 电源后手动确认安装；设备从不自动升级。服务器
发布命令、哈希校验和自动回滚步骤见[手机配网与安全 OTA](firmware-ota.md)。

仅在首次实机联调且尚无域名时，可构建 `e32r28t_dev`。这个单独目标只额外接受
`10.0.0.0/8`、`172.16.0.0/12` 或 `192.168.0.0/16` 内的 HTTP 地址；它不会接受
公网 HTTP、主机名或带用户信息的 URL。应使用临时 `display:read` 令牌，让服务仅
监听当前可信局域网地址，联调结束后撤销令牌并重新烧录正式 `e32r28t` HTTPS
固件。不要把开发目标部署为日常使用固件。

## 10. 真实 Claude 登录后验收

在与 Agent 相同的用户账户中执行：

```sh
claude auth status --json
claude -p "/usage" --output-format stream-json --verbose --no-session-persistence
```

第一条应表示已登录；第二条必须是 Claude 自带 `/usage` 命令，不要替换成“最小提示词”。确认输出中包含可解析的 5h/7d 配额字段后，运行一次 Agent：

```sh
quota-monitor agent --config /absolute/path/to/agent.json --once
```

再读取显示快照，检查 `providers.claude.source`、`observedAt`、`fiveHour`/`sevenDay` 与 Claude 实际显示一致。若当前 5h 会话尚未开始，`fiveHour` 应是 `usedPercent: 0`、`remainingPercent: 100`、`resetsAt: null` 的有效对象，屏幕显示 `剩100%` 和 `重置 未开始`，不能误报为 `N/A`。随后启动一次交互式 Claude 会话，让 status line 更新，再次读取快照，确认双通道没有出现明显冲突。某个订阅确实不提供某一窗口时，`null`/`N/A` 才是正确结果。

真实账号字段会随 Claude Code 版本或订阅类型变化；若 `/usage` 能显示配额但 API 为 `unavailable`，请保留已脱敏的结构和 CLI 版本用于兼容性修复，绝不要提交账号标识、提示词或 OAuth 凭据。
