# Changelog

本项目的所有重要变更将记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)，
本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [1.0.0] - 2026-03-22

### 新增

- 支持从 SSM 自动获取数据库凭据
- 定期监控凭据变化，自动更新数据库连接
- 线程安全的连接池管理（基于 Go database/sql）
- 三种认证方式：CamRole（推荐）、Temporary、Permanent
- 健康检查 API（`IsHealthy()` / `GetHealthCheckResult()`）
- 轮转时旧连接延迟退休机制（RotationGracePeriod）
- Watch 启动随机初始延时，避免多实例请求风暴
- 支持自定义 SSM 接入点（endpoint）
- 支持自定义读写超时参数
- 支持额外数据库连接参数透传
