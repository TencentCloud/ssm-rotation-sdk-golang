# SSM Rotation SDK for Go

腾讯云凭据管理服务（SSM）轮转 SDK，支持数据库凭据自动轮转。

## 功能特性

- 自动从 SSM 获取数据库凭据
- 定期监控凭据变化，自动更新连接
- 线程安全的连接池管理（基于 Go database/sql）
- 支持多种凭据认证方式
- 健康检查 API

## 认证方式

| 方式 | 工厂方法 | 说明 | 推荐 |
|------|----------|------|------|
| **CamRole** | `WithCamRole()` | CVM 实例角色（元数据服务自动获取临时凭据） | ✅ 推荐 |
| **Temporary** | `WithTemporaryCredential()` | 临时 AK/SK/Token（需自行管理刷新） | ⚠️ 可选 |
| **Permanent** | `WithPermanentCredential()` | 固定 AK/SK（存在泄露风险） | ❌ 不推荐 |

> 使用 CAM 角色前需为 CVM 绑定 CAM 角色：[CVM 绑定角色](https://cloud.tencent.com/document/product/213/47668)

## 前置条件

1. 已在腾讯云平台开通了 SSM 服务（[开通 SSM 服务](https://console.cloud.tencent.com/ssm/cloud)）
2. 已在腾讯云平台购买了至少一台云数据库实例（目前只支持 MySQL 实例），完成了数据库的初始化，并创建了至少一个 database（[MySQL 控制台](https://console.cloud.tencent.com/cdb)）
3. 已在 SSM 控制台创建了一个[数据库凭据](https://cloud.tencent.com/document/product/1140/57647)，并和指定的数据库做了关联（[创建数据库凭据](https://cloud.tencent.com/document/product/1140/57648)）
4. 已在腾讯云平台的 [访问管理（CAM）控制台](https://console.cloud.tencent.com/cam/overview) 创建了能够访问 SSM 凭据资源和 MySQL 实例资源的子账号

## 支持的 Go 版本

Go 1.13 及以上版本

## 快速开始

### 安装

```bash
go get github.com/tencentcloud/ssm-rotation-sdk-golang/lib
```

### 使用示例

```go
import (
    _ "github.com/go-sql-driver/mysql"
    "github.com/tencentcloud/ssm-rotation-sdk-golang/lib/db"
    "github.com/tencentcloud/ssm-rotation-sdk-golang/lib/ssm"
)

// 1. SSM 账号配置（三选一）

// 方式一：CVM 角色绑定（推荐）
ssmAccount := ssm.WithCamRole("your-role-name", "ap-guangzhou")

// 方式二：临时凭据
// ssmAccount := ssm.WithTemporaryCredential("tmpSecretId", "tmpSecretKey", "token", "ap-guangzhou")

// 方式三：固定凭据（不推荐）
// ssmAccount := ssm.WithPermanentCredential("secretId", "secretKey", "ap-guangzhou")

// 2. 初始化连接管理器
dbConn := &db.DynamicSecretRotationDb{}
err := dbConn.Init(&db.Config{
    DbConfig: &db.DbConfig{
        MaxOpenConns:        100,
        MaxIdleConns:        50,
        IdleTimeoutSeconds:  100,
        ReadTimeoutSeconds:  5,
        WriteTimeoutSeconds: 5,
        SecretName:          "your-secret-name",
        IpAddress:           "127.0.0.1",
        Port:                3306,
        DbName:              "your_database",
        ParamStr:            "charset=utf8&loc=Local",
    },
    SsmServiceConfig:    ssmAccount,
    WatchChangeInterval: time.Second * 10,  // 监控间隔（范围 1-60 秒）
    RotationGracePeriod: time.Second * 30,  // 可选：旧连接延迟退休时间
})
if err != nil {
    log.Fatal(err)
}
defer dbConn.Close()

// 3. 获取连接（请勿缓存，每次调用 GetConn()）
c := dbConn.GetConn()
if err := c.Ping(); err != nil {
    log.Fatal(err)
}

// 4. 健康检查
healthy := dbConn.IsHealthy()
result := dbConn.GetHealthCheckResult()
```

## 配置参数

### DbConfig（数据库配置）

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|-----|------|-----|--------|------|
| SecretName | string | ✅ | - | SSM 凭据名称 |
| IpAddress | string | ✅ | - | 数据库 IP |
| Port | uint64 | ✅ | - | 数据库端口（1-65535） |
| DbName | string | ❌ | - | 数据库名称 |
| MaxOpenConns | int | ❌ | 0 | 最大打开连接数 |
| MaxIdleConns | int | ❌ | 0 | 最大空闲连接数 |
| IdleTimeoutSeconds | int | ❌ | 0 | 空闲连接超时（秒） |
| ReadTimeoutSeconds | int | ❌ | 0 | 读超时（秒） |
| WriteTimeoutSeconds | int | ❌ | 0 | 写超时（秒） |
| ParamStr | string | ❌ | - | 额外连接参数 |

### SsmAccount（SSM 账号配置）

| 参数 | 类型 | 必填 | 说明 |
|-----|------|-----|------|
| Region | string | ✅ | 地域，如 ap-guangzhou |
| RoleName | string | 条件 | 角色名称（CamRole 时必填） |
| SecretId | string | 条件 | AK（Permanent/Temporary 时必填） |
| SecretKey | string | 条件 | SK（Permanent/Temporary 时必填） |
| Token | string | 条件 | 临时 Token（Temporary 时必填） |
| Url | string | ❌ | 自定义 SSM 接入点 |

### Config（轮转配置）

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|-----|------|-----|--------|------|
| DbConfig | *DbConfig | ✅ | - | 数据库配置 |
| SsmServiceConfig | *SsmAccount | ✅ | - | SSM 账号配置 |
| WatchChangeInterval | time.Duration | ✅ | - | 凭据监控间隔（范围 1-60 秒） |
| RotationGracePeriod | time.Duration | ❌ | 30s | 旧连接延迟退休时间 |

## 健康检查 API

```go
// 简单健康检查
healthy := dbConn.IsHealthy()

// 详细健康检查
result := dbConn.GetHealthCheckResult()
// result.Healthy        - 是否健康
// result.Closed         - 是否已关闭
// result.CurrentUser    - 当前凭据用户名
// result.WatchFailures  - 监控失败次数
// result.LastError      - 最后一次错误信息
```

## 注意事项

- `Region` 必填
- 每次访问数据库请调用 `GetConn()` 获取最新连接，请勿缓存
- `Close()` 会停止后台 watcher，并关闭当前数据库句柄；建议在应用退出时调用
- Watcher 启动时会自动增加随机初始延时，降低多实例同时访问 SSM 的请求尖峰
- 轮转时旧 DB 句柄会按 `RotationGracePeriod` 延迟关闭，降低高并发下请求被切断的风险
- 临时凭据有过期时间，SDK 不会自动刷新 `Temporary` 类型凭据
- `CamRole` 方式通过元数据服务自动获取和刷新凭据，仅限 CVM 环境

## 项目结构

```
ssm-rotation-sdk-golang/
├── lib/
│   ├── db/
│   │   └── dynamicSecretRotationDbConn.go  # 连接管理器（核心类）
│   └── ssm/
│       └── requester.go                     # SSM 请求器
└── demo/
    └── demo.go                              # 使用示例
```

## License

Apache License 2.0
