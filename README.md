<div align="center">
  <h1>Remind · Backend</h1>
  <p>「岁记」备忘提醒服务后端 · Go 单二进制，农历/阳历备忘 + 邮件定时提醒</p>
</div>

<div align="center">

![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED)
![SQLite](https://img.shields.io/badge/Storage-SQLite-003B57)
![License](https://img.shields.io/badge/License-MIT-green)

</div>

## 项目定位

一个自托管的备忘提醒服务，最核心的能力是**农历备忘**：生日、祭日、节庆这类按农历
记的日子，每年自动折算成公历并在当天发邮件提醒。

前端是独立的 Vue 3 应用（[Remind-web](https://github.com/CheeseTJ/Remind-web)），
本仓库只提供 API、定时调度与邮件发送。

```text
Remind-web (Vue 3)  ──> 本仓库 (Go)
                            ├──> SQLite        备忘 / 用户 / 订阅
                            ├──> 定时调度      每日扫描 + 到期判定
                            ├──> 邮件          走 SMTP
                            └──> 统一身份中心  JWT 校验（JWKS）
```

设计取向是**单二进制、零外部依赖**：不需要数据库服务，状态全部落在 SQLite 文件里。

## 功能

- **备忘 CRUD** —— 标题、内容、提醒时间、重复规则
- **农历支持** —— 农历日期存储与逐年公历折算，含闰月处理
- **定时提醒** —— 内置调度器按天扫描到期项，命中后发邮件
- **邮件通知** —— SMTP 发送，支持自定义模板
- **鉴权** —— 统一身份中心签发的 JWT，JWKS 校验 `aud`
- **崩溃恢复** —— 仓库自带 systemd unit，配置文件变更自动重启

## 快速开始

```bash
cp config.yaml.example config.yaml
# 按需修改；密钥建议走环境变量

go build -o bin/remind ./cmd/remind
./bin/remind
```

或 Docker：

```bash
docker compose up -d --build
curl http://localhost:8085/healthz
```

## 配置说明

| 段 | 关键项 | 说明 |
|---|---|---|
| `server` | `addr` | 监听地址，默认 `:8085` |
| `server` | `allowed_origins` | CORS 白名单 |
| `store` | `path` | SQLite 文件路径 |
| `mailer` | SMTP 主机 / 端口 / 账号 | 密码走环境变量 |
| `ucenter` | `base_url` / `app` | JWT 校验来源 |

完整模板见 `config.yaml.example`。

## 测试

农历折算、调度判定、邮件组装都有单元测试：

```bash
go test ./...
```

## 目录结构

```text
cmd/remind/              入口
internal/memo/           备忘模型与 handler
internal/lunarcal/       农历换算（含闰月）
internal/scheduler/      定时扫描与到期判定
internal/mailer/         SMTP 与邮件模板
internal/store/          SQLite 访问层
internal/auth/           JWT / JWKS 校验
internal/config/         配置加载
deploy/remind.service    systemd unit
```

## 部署

仓库自带 `.github/workflows/deploy.yml`：push 到 `master` 后由 self-hosted runner
构建并部署，配合 systemd 做崩溃恢复。

> 该 job 含 `if: github.repository_owner == 'CheeseTJ'` 门禁——self-hosted runner 与
> 生产服务同机，这道门禁防止外部 fork/PR 触发在服务器上执行代码。

## License

[MIT](./LICENSE) © 2026 JuneT
