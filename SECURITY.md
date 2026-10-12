# stai 安全说明 / Security Notes

stai 是一个本地 git 工作流的 AI 伴侣工具。设计上坚持“最小权限、失败安全、默认安全”。
stai is a local git-workflow companion. It follows least-privilege, fail-safe defaults and secure-by-default settings.

## 威胁模型 / Threat model

| 边界 / Boundary | 描述 / Description | 缓解 / Mitigation |
|---|---|---|
| Git → stai | diff 内容可能包含恶意文本 | 不执行 diff 内容；只解析为文本后发送给模型 |
| stai → LLM API | 通过 HTTPS 调用外部模型 | 强制 TLS；不跳过证书校验；API key 不落日志 |
| 模型 → stai | LLM 返回的 location/file 路径可能越权 | 定位校验只接受在仓库根目录内、且出现在本次 diff 中的文件（见 review 定位校验） |
| 配置文件 | 含有 API key | 写入时强制 `0600`；读时若权限过宽则告警，并建议 `chmod 600` |
| 外部命令 | 只允许调用 `git`，且使用固定子命令和 `--` 分隔，避免 flag injection |

## 已实现的加固 / Implemented hardening

- 全局配置文件写入和备份时强制 `chmod 0600`。
- 读配置时若权限过宽，在 `stai doctor` 和普通命令输出中告警。
- 与 provider 通信使用 HTTPS，`net/http` 默认 TLS 1.2+，不设置 `InsecureSkipVerify`。
- API key 支持 `STAI_API_KEY` 环境变量，避免写入文件；若写入文件则文件权限为 `0600`。
- 诊断日志不会记录 API key 或完整 Authorization header。
- review 定位校验会拒绝指向仓库外或不在本次 diff 中的文件路径。

## 未实现 / Not implemented

- 未引入依赖漏洞扫描（govulncheck、Dependabot）。
- 未提供本地 web viewer，因此没有相关 HTTP 安全边界（Host allowlist、CSP 等）。
- 不做自动化代码修复，模型建议始终需要人工审阅。

## 报告问题 / Reporting issues

请在 GitHub Issues 中报告安全问题，避免在公开 issue 里粘贴 API key 或完整日志。
Please report security issues via GitHub Issues and avoid pasting API keys or full logs publicly.
