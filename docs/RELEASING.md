# 发布流程

## 1. 改版本号

`cmd/voicebridge/main.go` 第一行的 `version`：

```go
var version = "1.3.0"
```

同时在 `CHANGELOG.md` 顶部补一节。

## 2. 本地自检

```bash
go vet ./...
go test ./...
make release          # 产物在 dist/release/，含 amd64/arm64 与 sha256
```

## 3. 打标签并推送

```bash
git add -A && git commit -m "release: v1.3.0"
git tag v1.3.0
git push origin main --tags
```

推 tag 会触发 `.github/workflows/release.yml`：

1. 跑单元测试
2. `make release` 构建 `linux/amd64` 与 `linux/arm64`
3. 自动创建 GitHub Release，附上两个 tar.gz 与对应 `.sha256`

## 4. 发布包内容

```
sipvoice-<版本>-linux-<架构>.tar.gz
├── voicebridge
├── install.sh / upgrade.sh / uninstall.sh
├── sipvoice.service
├── config.example.json
├── asterisk/{pjsip,extensions,rtp}.conf
└── README.txt
```

安装：解包后 `sudo bash install.sh`。

## 5. 版本号约定

`主.次.修订`：

- **主**：架构性变化（协议、部署形态）
- **次**：新增功能
- **修订**：修复

正式发布用 tag `vX.Y.Z`；开发中的快照用内部编号即可，不必推 tag。
