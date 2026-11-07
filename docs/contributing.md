# 贡献指南

完整指南见仓库根目录的 [CONTRIBUTING.md](https://github.com/hzc19970630/rag-platform/blob/main/CONTRIBUTING.md)。

## 快速流程

```bash
git clone https://github.com/hzc19970630/rag-platform.git
cd rag-platform
cp .env.example .env
./scripts/quickstart.sh
```

提交前运行与改动相关的检查：

```bash
gofmt -w cmd internal tests
go run ./cmd/checkdeps
go run ./cmd/openapi -check
go run ./cmd/checktenancy
go test ./...
go vet ./...

cd web
npm ci
npm run codegen:check
npm test
npm run build
```

## 约定

- 使用 Conventional Commits，并通过 `git commit -s` 签署 DCO。
- HTTP DTO/路由只改 `contracts/httpapi`，重新生成 OpenAPI 与前端类型。
- 新增租户数据查询必须 scoped；`cmd/checktenancy` 会拦截遗漏。
- 安全修复先补会失败的负向测试，再改生产代码。
- 公开文档不得包含内部里程碑编号或本地路径。
- 不要提交密钥、客户数据或真实供应商凭据。

## 治理

维护者角色、决策方式与发布权限见 [GOVERNANCE.md](https://github.com/hzc19970630/rag-platform/blob/main/GOVERNANCE.md)。行为规范见 [CODE_OF_CONDUCT.md](https://github.com/hzc19970630/rag-platform/blob/main/CODE_OF_CONDUCT.md)。
