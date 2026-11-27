# 配置包格式与兼容约定

这份文档是**约定**，不只是说明。以后给 SIPVoice 加新功能、新配置项，
都要按这里的规则来，否则跨版本导入导出会慢慢失效。

## 为什么要有它

用户的部署里有一堆文件：AI 与语音、会话、通知、传真、邮局与收信、话机台账、
控制台账号、云端密钥。重装、换机、做实验性改动前，需要能一次性搬走再搬回来。

单靠"复制 /etc/sipvoice/*.json"有几个问题：

- 不同版本的字段不一样，旧文件搬进新程序会有字段缺失或多余；
- 新版本多出来的配置，旧程序读到会报错或悄悄丢掉；
- 密钥是跟着 machine-id 加密的，直接复制到别的机器解不开。

配置包就是把这些收拢成一个可携带、可演进的格式。

## 包结构

```json
{
  "format": "sipvoice.config-bundle",
  "format_version": 1,
  "exported_at": "2026-11-27T20:00:00Z",
  "producer": {
    "app_version": "2.1.12",
    "hostname": "example",
    "os": "linux/amd64"
  },
  "sections": {
    "config":  { },
    "secrets": { },
    "fax":     { },
    "mail":    { },
    "phones":  [ ],
    "users":   [ ]
  }
}
```

- `format`：固定字符串，用于识别是不是本产品的包。
- `format_version`：**包格式版本**，与程序版本无关。
- `producer`：产出方信息，仅供人看，**不参与任何兼容判断**。
- `sections`：按功能区段。每个 key 对应一份配置。
- 不在 `sections` 里的顶层字段会被忽略（不报错）。

## 三条兼容规则

### 1. 版本无关

程序版本升到 3.x，包格式可能还是 `format_version: 1`。

读取端只做一件事：`format_version <= 本实现支持的版本` 才接受。
所以老包永远能进新程序。

### 2. 向后兼容（老包 → 新程序）

区段是**可选**的：

- 包里没有某个区段 → 跳过，不动本机这部分配置；
- 区段里缺字段 → 由该结构体的默认值与 `Normalize()` 补齐；
- 区段里有本版本不认识的字段 → JSON 解码时自然忽略，不影响其它字段。

结论：新增配置项不需要改包格式，加字段即可。

### 3. 向前兼容（新包 → 老程序）

**这是最容易被忽略、也最重要的一条。**

老程序读到不认识的区段时：

- **不报错**；
- **不丢弃**；原样写入 `/etc/sipvoice/config-bundle.extra.json`；
- 下次导出时自动并回 `sections`。

于是"用旧版导一次、导回去"不会把新版配置弄丢。区段名就是隔离边界。

## 新增功能时要做什么

假设要加"多语言语音包"，配置存在 `/etc/sipvoice/voices.json`：

1. 在 `internal/configbundle/bundle.go` 里加常量：

   ```go
   const SectionVoices = "voices"
   ```

2. 把 `SectionVoices` 加进 `knownSections`（决定导入顺序）。

3. 在 `Export` 里读 `voices.json` 塞进 `Sections[SectionVoices]`。

4. 在 `importSection` 里加一个 `case SectionVoices`，落到目标路径。

5. 加一条往返测试（参考 `bundle_test.go` 的 `TestExportImportRoundTrip`）。

**不需要**提升 `FormatVersion`——除非你改变了**已有区段内部**的语义
（比如把 `fax.prompt_wait` 从秒改成毫秒）。那种情况才提升版本号，
并在导入逻辑里按版本做转换。

## 密钥怎么处理

`secrets.json` 是用本机 `/etc/machine-id` 派生的 AES-256 密钥加密的，
直接复制到别的机器解不开——这是有意的设计。

所以配置包里：

- 导出**默认不含** `secrets` 区段；面板上要显式选"包含密钥"才带；
- 带密钥时写入的是**解密后的明文**，因为包里必须能跨机使用；
- 导入时按目标机器的 machine-id **重新加密**落盘。

代价是：含密钥的包等于一份明文凭据，保管责任在使用者。

## 导入时的安全行为

- 导入前自动把当前 `/etc/sipvoice/*.json` 备份到
  `/etc/sipvoice/backups/pre-import-<时间戳>/`；
- 单区段解析失败不影响其它区段，失败原因逐条写进报告；
- 已有控制台账号**不覆盖**，避免把当前登录的人踢掉；
- `mail.receive` 的轮询状态、`last_test` 等运行时数据不被导入覆盖；
- 导入话机或传真配置后，自动重渲染 `pjsip.conf` / `extensions.conf` /
  `extensions_sipfax.conf`，不需要手工 reload。

## 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/config/export?secrets=0\|1` | 下载配置包 |
| POST | `/api/config/import?dry_run=1` | 只校验，不写盘 |
| POST | `/api/config/import` | 实际导入，返回逐区段报告 |

面板入口：「系统」→「配置导入 / 导出」。
