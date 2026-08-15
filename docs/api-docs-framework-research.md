# Go 后端自动 API 测试页面框架调研

目标：不写独立前端工程，在 Go 后端里直接提供“可调接口”的网页；当 `app/http/routes/routes.go` 发生变更后，页面自动反映新接口。

## 候选方案

### 1) `swaggo/http-swagger` + `swaggo/swag`
- 优点：社区成熟、接入快、UI 即开即用。
- 缺点：核心依赖注释生成文档，若仅改 `routes.go` 但未补注释，自动同步不稳定。
- 结论：适合注释驱动项目，不是本项目“路由变更即同步”的最稳方案。

### 2) `go-openapi/runtime/middleware` + 预生成 swagger.json
- 优点：稳定、规范，适合严格 OpenAPI 流程。
- 缺点：通常需要单独 spec 维护或额外生成步骤。
- 结论：适合大团队契约先行，但对“直接从路由自动同步”实现成本更高。

### 3) `kin-openapi`（Go 代码动态生成 OpenAPI）+ `swaggo/http-swagger/v2`（直接托管 UI）
- 优点：
  - OpenAPI 由 Go 代码在运行时生成；
  - 可以直接绑定 `routes.go` 的注册动作，路由一改文档即变；
  - UI 由后端托管，不需要前端工程。
- 缺点：需要在路由注册层维护“路径+方法”元数据（本次已落地）。
- 结论：**最匹配当前诉求，推荐采用。**

## 本项目落地结论

已采用：`kin-openapi` + `http-swagger/v2`。

实现策略：
1. 在 `RegisterRoutes` 中统一通过 `register(...)` 注册路由；
2. 注册同时写入路由目录（catalog）；
3. 运行时输出 `/api-docs/openapi.json`；
4. 通过 `/api-docs/` 提供 Swagger UI 交互测试页。

因此，后续只要改 `app/http/routes/routes.go` 中路由注册，API 测试页面会自动反映新增/删除接口。
