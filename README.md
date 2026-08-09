# Codex / Claude 配额监视器

这套项目把 Codex 和 Claude Code CLI 的配额、重置时间与活动任务数汇总到一个只读 HTTPS API，并由 Keyes E32R28T（ESP32-32E + 2.8 英寸触摸屏）便携终端显示。

推荐直接在云服务器运行 `quota-monitor standalone`。一个进程同时负责额度采集、Hooks、SQLite 和 API，本地电脑无需再运行 Agent。Codex/Claude 的登录凭据仍由官方 CLI 保存在云服务器的当前用户目录，本项目不读取或复制 OAuth Token。

> v1 只服务同一位用户的一个 Codex 账号和一个 Claude 账号。它不提供网页后台，也不显示订阅到期日期。某个账号没有 5h 或 7d 窗口时，API 返回 `null`，终端显示 `N/A`。Standalone 只能统计云服务器上运行的 Codex/Claude 任务；本地电脑上的任务不会出现在计数中。

服务器内存要按 Go 服务和官方 CLI 的总占用估算。[Codex CLI 官方要求](https://github.com/openai/codex/blob/main/docs/install.md#system-requirements)至少 4GB、推荐 8GB，[Claude Code 官方要求](https://code.claude.com/docs/en/setup#system-requirements)4GB 以上，因此官方支持配置至少应有 4GB。Standalone 默认按 Codex → 释放 app-server → Claude 的顺序串行探测，减少两个 CLI 的内存重叠，但无法降低单个 CLI 的峰值。1 核、512MB 同时启用两者仍只适合无稳定保证的实验性试跑；至少应配置 1GB swap、保持默认串行模式并持续检查 OOM，现有业务出现内存压力时应关闭一个 provider 或升级内存。

## 交付内容

- `quota-monitor standalone`：推荐模式；在云服务器单进程运行 API、SQLite、额度采集和 Hooks，本地无需 Agent。
- `quota-monitor login codex|claude`：调用官方 CLI 完成交互登录；登录失效时重新执行即可，Standalone 会自动恢复采集。
- `quota-monitor server` + `quota-monitor agent`：兼容多机上报模式，适合确实需要统计多台电脑任务的场景。
- `quota-monitor hooks install/uninstall`：合并 Codex/Claude 用户配置、保留原 Hooks 和 Claude status line，并支持精确卸载。
- `quota-monitor token create/list/revoke`：在服务器本地管理 `agent:write` 和 `display:read` 令牌。
- `deploy/`：Standalone 的用户级 systemd/Caddy 示例，以及兼容模式的 Docker Compose 部署。
- `firmware/`：PlatformIO + Arduino ESP32 + LVGL 固件；默认目标为 Keyes 62520093 E32R28T，旧 FireBeetle 目标仍可回归构建。
- `hardware/`：E32R28T 当前接线/BOM，以及已明确标记为停用的旧 DFRobot 载板和外壳资料。

```text
Codex app-server / Hooks ─┐
                          ├─> quota-monitor standalone + SQLite <──HTTPS── E32R28T
Claude /usage / Hooks ────┘             （云服务器）
```

Standalone 会直接把云端采集结果写入同一进程打开的 SQLite，不需要 `agent:write` 令牌。需要多机任务统计时仍可使用 Agent 模式：额度取 `observedAt` 最新的一份，不相加；任务按 `agentId + provider + sessionId` 去重。Agent 超过 45 秒未上报后不再计入在线或任务统计，任务事件 15 分钟没有更新则失效，额度样本超过 5 分钟标记为 `stale`。

## 开始使用

需要 Go 1.23 或更高版本。Windows、Linux amd64 和 Linux arm64 的构建方法见[快速开始](docs/quickstart.md)。云服务器推荐按以下顺序部署：

1. 在普通用户下安装官方 Codex/Claude CLI 和 `quota-monitor` 单文件程序。
2. 运行 `quota-monitor login codex`；需要 Claude 时再运行 `quota-monitor login claude`。
3. 安装 Hooks，并用 `systemd --user` 常驻运行 `quota-monitor standalone`。
4. 用宿主机 Caddy 把回环地址 `127.0.0.1:8787` 代理到 HTTPS 域名。
5. 把 Standalone 首次创建的 `display.token` 写入 ESP32。

完整命令和服务文件见 [Standalone 云端部署](docs/standalone.md)。原来的 Docker server + 本地 Agent 流程仍保留在[快速开始](docs/quickstart.md)中，作为多机兼容方案。

生产环境必须使用受信任证书的 HTTPS。`allowInsecureHttp` 只用于回环地址上的本地开发；ESP32 固件不提供跳过证书校验的选项。

## 文档

- [快速开始与首次部署](docs/quickstart.md)
- [Standalone 单进程云端部署](docs/standalone.md)
- [运行维护、升级、备份与故障排查](docs/operations.md)
- [测试与验收清单](docs/testing.md)
- [R0.1 自动验证报告](docs/verification-report.md)
- [系统架构](docs/architecture.md)
- [安全说明](docs/security.md)
- [OpenAPI 3.1 契约](api/openapi.yaml)
- [E32R28T 购买、接线、烧录与装配](docs/hardware/README.md)

## API

公开路径固定为：

- `GET /healthz`：无需令牌，仅表示服务与数据库可用。
- `POST /api/v1/agent/report`：多机 Agent 兼容接口，需要与 `agentId` 绑定的 `agent:write` Bearer 令牌；Standalone 自身不经过此接口。
- `GET /api/v1/display/snapshot`：需要 `display:read` Bearer 令牌。

服务不开放 CORS、管理页面或远程令牌管理接口。单次 Agent 上报上限为 64 KiB；
默认每个有效令牌每分钟最多 120 次请求，令牌校验前另有哈希指纹限速、全局上限
和并发上限。

## 安全与隐私

SQLite 只保存令牌 SHA-256 摘要、最新规范化额度、Agent 心跳和不透明任务 ID；不保存提示词、对话、路径、邮箱或 OAuth Token。Standalone 首次运行会创建一个只读显示令牌文件，必须像密码一样保护。多机模式的原始 Bearer 令牌只在创建时显示一次；应为每台设备单独签发，丢失设备后立即撤销。

仓库不应提交 `.env`、数据库、显示令牌、OAuth/API 凭据、Wi-Fi 配置、完整 Flash 备份、服务器私钥或现网专用 CA。`work/`、`dist/`、`.tools/` 和 `firmware/.pio/` 均为本地目录并已被 Git 忽略。编译固件前，将部署环境的公开设备根证书放到 `firmware/certs/device-root-ca.pem`；该文件只在本机使用，仓库仅提供不连接任何生产服务器的示例证书。

锂电池必须使用带保护板、极性匹配的 3.7V/1S 成品电池，禁止直接焊接软包电芯极耳。MX1.25-2P 只说明接口外形，插入 E32R28T 前仍必须按板上 `+/-` 丝印和万用表复核线序；充电温升、续航和外壳膨胀余量未经真机测量前不能视为量产验证完成。

## 许可证

[MIT](LICENSE)
