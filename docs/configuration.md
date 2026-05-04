# 配置说明

<cite>
**本文档引用的文件**
- [config.example.json](file://config.example.json)
- [.env.example](file://.env.example)
- [internal/config/config.go](file://internal/config/config.go)
- [internal/config/account.go](file://internal/config/account.go)
- [internal/config/account_sqlite.go](file://internal/config/account_sqlite.go)
- [internal/config/dotenv.go](file://internal/config/dotenv.go)
- [internal/config/store_load.go](file://internal/config/store_load.go)
- [internal/config/store_runtime_accessors.go](file://internal/config/store_runtime_accessors.go)
- [internal/config/validation.go](file://internal/config/validation.go)
</cite>

## 目录

1. [简介](#简介)
2. [项目结构](#项目结构)
3. [核心组件](#核心组件)
4. [架构总览](#架构总览)
5. [详细组件分析](#详细组件分析)
6. [故障排查指南](#故障排查指南)
7. [结论](#结论)

## 简介

DeepSeek_Web_To_API 当前以 `.env` 作为推荐的单一部署入口：`DEEPSEEK_WEB_TO_API_CONFIG_JSON` 承载结构化运行配置，`DEEPSEEK_WEB_TO_API_ENV_WRITEBACK=true` 时会把初始配置回写到 `DEEPSEEK_WEB_TO_API_CONFIG_PATH`，便于管理台后续热更新。账号池不再要求维护 JSON，管理台批量导入的 `账号:密码` 会写入独立 `accounts.sqlite`。

**章节来源**
- [config.example.json](file://config.example.json)
- [.env.example](file://.env.example)

## 项目结构

```mermaid
graph TB
subgraph "Config Files"
ENV[".env<br/>主配置入口"]
ENVJSON["DEEPSEEK_WEB_TO_API_CONFIG_JSON<br/>结构化配置"]
WRITEBACK["data/config.json<br/>可选回写文件"]
ACCOUNTS["data/accounts.sqlite<br/>账号池"]
end
subgraph "Config Code"
DOTENV["internal/config/dotenv.go<br/>读取 .env"]
STRUCT["internal/config/config.go<br/>配置结构"]
LOAD["internal/config/store_load.go<br/>加载优先级"]
ACCDB["internal/config/account_sqlite.go<br/>账号 SQLite"]
ACCESS["internal/config/store_runtime_accessors.go<br/>运行时访问器"]
VALIDATE["internal/config/validation.go<br/>配置校验"]
end
ENV --> DOTENV
DOTENV --> ENVJSON
ENVJSON --> LOAD
LOAD --> WRITEBACK
LOAD --> ACCDB
ACCDB --> ACCOUNTS
LOAD --> STRUCT
STRUCT --> VALIDATE
STRUCT --> ACCESS
```

**图表来源**
- [.env.example](file://.env.example)
- [internal/config/dotenv.go](file://internal/config/dotenv.go)
- [internal/config/store_load.go](file://internal/config/store_load.go)

**章节来源**
- [internal/config/config.go](file://internal/config/config.go)
- [internal/config/store_load.go](file://internal/config/store_load.go)

## 核心组件

- `Config`：包含 API Key、账号、代理、模型别名、Admin、Server、Storage、Cache、Runtime、Compat 等字段。
- `LoadDotEnv`：启动时读取工作目录 `.env`，不覆盖外部已经注入的环境变量。
- `LoadStoreWithError`：读取 `.env` / 环境变量 / 回写文件，归一化凭据、校验配置并建立索引。
- `ConfigPath`：`DEEPSEEK_WEB_TO_API_ENV_WRITEBACK=true` 时作为管理台热更新的持久化文件。
- `DEEPSEEK_WEB_TO_API_CONFIG_JSON`：允许写入原始 JSON、带引号 JSON、`base64:<value>` 或纯 Base64 JSON。
- `accountSQLiteStore`：将账号池迁入 `accounts.sqlite`，保存配置时不再把 `accounts` 写回 JSON。
- `ValidateConfig`：校验代理、Admin、Server、Cache、Runtime、Responses、Embeddings 等配置范围。

**章节来源**
- [internal/config/config.go](file://internal/config/config.go)
- [internal/config/account_sqlite.go](file://internal/config/account_sqlite.go)
- [internal/config/dotenv.go](file://internal/config/dotenv.go)
- [internal/config/store_load.go](file://internal/config/store_load.go)
- [internal/config/validation.go](file://internal/config/validation.go)

## 架构总览

```mermaid
graph TB
subgraph "Load Priority"
ENVJSON["DEEPSEEK_WEB_TO_API_CONFIG_JSON"]
FILE["DEEPSEEK_WEB_TO_API_CONFIG_PATH<br/>可选回写文件"]
LEGACY["容器旧路径兜底"]
end
subgraph "Runtime"
STORE["config.Store<br/>线程安全快照与更新"]
INDEX["Key/Account 索引"]
ACCDB["accounts.sqlite<br/>账号单独持久化"]
SAVE["保存时剥离 accounts"]
end
ENVJSON --> STORE
FILE --> STORE
LEGACY --> STORE
STORE --> INDEX
STORE --> ACCDB
STORE --> SAVE
```

**图表来源**
- [internal/config/store.go](file://internal/config/store.go)
- [internal/config/store_load.go](file://internal/config/store_load.go)

**章节来源**
- [internal/config/store.go](file://internal/config/store.go)
- [internal/config/codec.go](file://internal/config/codec.go)

## 详细组件分析

### 必填配置

| 配置 | 说明 |
| --- | --- |
| `keys` / `api_keys` | 客户端使用的 API Key，命中后进入托管账号模式 |
| `admin.key` 或 `admin.password_hash` | 管理端登录凭据 |
| `admin.jwt_secret` | 管理端 JWT 签名密钥 |
| `storage.accounts_sqlite_path` / `DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH` | 账号池 SQLite 文件路径 |

### 运行建议

- 反代部署时将 `server.bind_addr` 设置为 `127.0.0.1`，由 Caddy/Nginx 暴露公网端口；Docker Compose 会通过环境变量覆盖为 `0.0.0.0`。
- 容器部署时保留容器内 `server.port=5001`，只通过 Compose 的宿主机端口映射调整外部端口。
- 账号不再需要写入 JSON；在管理台“批量导入”中粘贴 `账号:密码` 文本即可写入 `accounts.sqlite`。
- 若旧 JSON 中仍有 `accounts`，账号 SQLite 为空时会自动迁移，随后保存配置时会剥离 `accounts` 字段；兼容旧导入格式中把邮箱误写到 `mobile` 字段的账号，加载和导入时会自动归一到 `email`。
- 账号 token 不写回结构化配置文件；运行态 token 保存在账号 SQLite 中。
- 代理只支持 `socks5` 与 `socks5h`，账号的 `proxy_id` 必须引用已存在代理。

### 缓存默认值

| 配置 | 默认/示例 |
| --- | --- |
| `cache.response.memory_ttl_seconds` | `300` |
| `cache.response.memory_max_bytes` | `3800000000` |
| `cache.response.disk_ttl_seconds` | `14400` |
| `cache.response.disk_max_bytes` | `16000000000` |
| `cache.response.max_body_bytes` | `67108864` |

**章节来源**
- [config.example.json](file://config.example.json)
- [internal/config/store_runtime_accessors.go](file://internal/config/store_runtime_accessors.go)
- [internal/config/validation.go](file://internal/config/validation.go)

## 故障排查指南

- 启动失败并提示 `admin credential is missing`：配置 `admin.key` 或 `admin.password_hash`。
- 启动失败并提示 `admin.jwt_secret is required`：配置足够随机的 `admin.jwt_secret`。
- Docker 容器内无法保存配置：确认挂载了 `./data:/app/data`，并设置 `DEEPSEEK_WEB_TO_API_CONFIG_PATH=/app/data/config.json`。
- 批量导入账号失败：优先使用一行一个 `账号:密码`；邮箱账号包含 `@`，手机号账号会归一化为带国家码格式。旧 JSON 里的 `mobile:"user@example.com"` 会被兼容成邮箱账号。
- 代理配置报错：检查 `type` 是否为 `socks5` 或 `socks5h`，端口是否在 `1-65535`。

**章节来源**
- [internal/auth/admin.go](file://internal/auth/admin.go)
- [internal/config/validation.go](file://internal/config/validation.go)

## 结论

项目配置已经收敛到 `.env` 入口：结构化配置通过 `DEEPSEEK_WEB_TO_API_CONFIG_JSON` 注入，管理台热更新可回写到 `data/config.json`，账号池则单独落到 `data/accounts.sqlite`。这样部署者只需要维护 `.env` 与 `data/`，开发者不再需要同步两份配置文件。

**章节来源**
- [config.example.json](file://config.example.json)
- [.env.example](file://.env.example)
