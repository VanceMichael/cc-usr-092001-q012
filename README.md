# 畜禽表型标准接入网关

肉禽与生猪智能表型数据在统一采集规范落地后，参与编制的科研院所与养殖企业仍使用不同设备、编号体系与采样节奏。本服务是联合体侧的后端接入网关，负责：

1. **按可生效的标准版本登记**物种、性状定义、采集流程、数据格式与设备约束；
2. 登记**测定设备与校准证书**，上传时校验单位、精度、量值范围、测定日龄窗口、时钟偏差、补传时限、设备适用范围与不确定度；
3. 管理**个体谱系与机构本地编号**，离线设备批量补传按设备序号幂等去重，个体合并与标识纠错不切断亲缘链；
4. **原始读数、修订值、质量标记三表分离**，提供跨机构拒收原因与质量反馈；
5. 研究机构就**经批准的数据切片**申请数据，导出按用途范围过滤并假名化；
6. 为育种分析数据集生成**溯源清单**，证明其来自哪些原始采集、校准证书与规则版本。

## 目录

- `cmd/server/` 服务入口
- `internal/api/` HTTP 路由与鉴权
- `internal/standards/` 标准版本冻结与生效选择
- `internal/devices/` 设备登记、适用范围、校准证书
- `internal/subjects/` 个体、别名、合并、谱系纠错
- `internal/ingest/` 上传校验管道与离线去重
- `internal/records/` 原始读数视图、修订链、质量标记
- `internal/quality/` 跨机构拒收反馈
- `internal/slices/` 切片申请、审批、用途过滤、假名化导出
- `internal/provenance/` 数据集溯源清单与重算核对
- `internal/store/` 并发安全内存存储（可替换为持久化实现）
- `internal/domain/`、`internal/canon/` 领域模型与规范指纹
- `contracts/` 外部交换字段示例

## 关键业务规则

| 主题 | 规则 |
| --- | --- |
| 标准版本 | 登记即冻结 `rules_hash`；按采集发生时间选生效版本；升级不重释旧数据 |
| 去重 | 键 = 设备序列号 + `source_sequence`；同摘要幂等，不同摘要冲突拒收 |
| 校准 | 过期/吊销拒收，过复校期未失效则接收并挂告警 |
| 谱系 | 合并立墓碑、改写亲子边与别名、双向祖先环检测，旧编号永久可解析 |
| 三表分离 | `Reading` 不可变；`Revision` 修订链并列；`QualityFlag` 独立闭环 |
| 切片 | 申请-审批-有效期；按用途过滤；个体假名化、出生日裁剪到月、谱系边按可见集合裁剪 |
| 溯源 | 清单三元组：原始载荷摘要 + 规则哈希 + 校准证书摘要，整体再取摘要并可重算 |

## 运行

```bash
make test      # 全部单元测试与端到端测试（含 -race 检查）
make run       # 启动服务，默认 :8080
```

配置通过环境变量：

- `PORT`：监听端口，默认 `8080`
- `GATEWAY_TOKEN`：联合体管理接口（机构登记、标准发布、切片审批、证书吊销）的 Bearer 令牌；未设置时管理接口不可用

## 接口概览

业务接口需 `X-Org-ID` 请求头；管理接口另需 `Authorization: Bearer <GATEWAY_TOKEN>`。

```
POST   /v1/orgs                          联合体登记参与机构
POST   /v1/standards/{id}/versions       发布冻结标准版本
GET    /v1/standards/{id}/effective?at=  查询某时刻生效版本
POST   /v1/devices                       登记设备（声明物种/性状适用范围）
POST   /v1/devices/{sn}/calibrations     上传校准证书
POST   /v1/calibrations/{id}/revoke      吊销证书
POST   /v1/subjects                      登记个体
PUT    /v1/orgs/{org}/aliases/{local}    绑定机构本地编号
POST   /v1/orgs/{org}/aliases/{local}/corrections  标识纠错（留痕）
POST   /v1/subjects/merges               个体合并（不断链）
POST   /v1/subjects/lineage-corrections  谱系纠错（环检测）
POST   /v1/ingest/batches                上传/离线补传采集批次
GET    /v1/readings/{id}                 原始值 + 修订链 + 质量标记
POST   /v1/readings/{id}/revisions       追加修订值（不改原值）
POST   /v1/readings/{id}/flags           追加质量标记（可跨机构）
GET    /v1/quality/rejections            本机构拒收收件箱（?device_serial= 查跨机构反馈）
POST   /v1/slices                        研究机构申请切片
POST   /v1/slices/{id}/review            联合体审批（可收窄主体范围/设有效期）
GET    /v1/slices/{id}/export            假名化导出（自动生成溯源清单）
POST   /v1/manifests                     为数据集构建溯源清单
POST   /v1/manifests/{id}/verify         重算核对三元组与清单摘要
```

`contracts/` 下提供标准版本登记与采集批次两份示例载荷。

## 持久化说明

当前默认实现为进程内并发安全存储，用于规范验证与测试；`internal/store` 的接口边界即为持久化替换点，切换数据库不应影响领域规则。
