# 畜禽表型标准接入网关

本项目用于管理表型采集、设备校准与个体谱系中的稳定事实和交换边界。仓库提供基础服务、数据库初始化入口、领域说明和一份脱敏示例，便于不同参与方在一致约定下协作。

## 目录

- `contracts/` 保存外部交换字段示例（批量上传请求体）。
- `docs/` 说明领域对象、时间和标识约定。
- `cmd/`、`internal/` 保存服务代码：`domain` 为事实模型，`store` 为本地 JSON 持久化，`service` 为业务规则，`api` 为 HTTP 接口。

## 运行

执行 `make test` 检查基础行为，执行 `make migrate` 初始化本地数据目录，执行 `make run` 启动服务。默认监听 `8080` 端口，健康检查地址为 `/health`。

配置通过环境变量传入（`PORT`、`DATABASE_PATH`），敏感值和本地数据库文件不得提交到仓库。

## 接口概览

- 标准版本：`POST /v1/standards`，`POST /v1/standards/{id}/activate|retire`，其下登记 `species`、`traits`、`protocols`、`formats`。
- 登记：`POST /v1/devices`、`POST /v1/devices/{serial}/calibrations`、`POST /v1/subjects`、`POST /v1/subjects/merge`、`GET /v1/subjects/{ref}/pedigree`。
- 采集：`POST /v1/uploads`（批量上传，逐条返回接受/拒收/重复及原因），`GET /v1/observations/{id}`，`POST /v1/observations/{id}/revisions|quality-marks|feedback`。
- 反馈：`GET /v1/observations/{id}/feedback`、`GET /v1/orgs/{org}/rejections`。
- 切片：`POST /v1/slice-requests`、`POST /v1/slice-requests/{id}/decision`、`GET /v1/slice-requests/{id}/data`。
- 溯源：`POST /v1/datasets`、`GET /v1/datasets/{id}/provenance`。

## 关键规则

- 物种、性状、采集流程与数据格式登记在标准版本下，生效后冻结；采集记录按 `occurred_at` 绑定当时生效的版本，标准升级不重释旧数据。
- 上传校验单位、精度、取值范围、日龄窗口、设备适用范围与校准证书有效期；按（设备序号, 来源序号）去重。
- 原始读数、修订值、质量标记分别保存，原始读数不可改写。
- 个体合并只追加映射，谱系沿合并链解析，亲缘链不中断。
- 数据切片须先获批；未授权主体信息时输出假名且不包含谱系。
