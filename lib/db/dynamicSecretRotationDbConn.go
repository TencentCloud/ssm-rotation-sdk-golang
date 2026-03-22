/*
 * Copyright (c) 2017-2026 Tencent. All Rights Reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tencentcloud/ssm-rotation-sdk-golang/lib/ssm"
	ssmapi "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923"
)

const (
	defaultRotationGracePeriod = 30 * time.Second
	maxWatchFailures           = 5
	defaultPingTimeout         = 5 * time.Second
	// maxBackoffMultiplier 指数退避最大倍数（2^5 = 32 倍）
	maxBackoffMultiplier = 5
)

// DynamicSecretRotationDb 动态凭据轮转数据库连接管理器
//
// 核心功能：
//   - 后台自动监控 SSM 凭据变化
//   - 使用最新凭据管理数据库连接池
//   - 健康检查 API
//
// 注意：Go 的 database/sql 自带连接池管理，每次调用 GetConn() 获取的是连接池对象。
// 请勿在业务代码中缓存 GetConn() 返回的 *sql.DB，以确保凭据轮转后能及时使用新连接。
type DynamicSecretRotationDb struct {
	config      *Config
	dbConn      atomic.Value
	stopCh      chan struct{}
	watcherDone chan struct{} // watcher goroutine 退出信号
	closeOnce   sync.Once
	initOnce    sync.Once
	closed      int32
	retiredMu   sync.Mutex
	retired     []retiredConn

	// 缓存的 SSM 客户端，避免每次轮询都重新创建 TCP/TLS 连接
	cachedSsmClient *ssmapi.Client

	// 健康状态
	watchFailures int32
	lastError     atomic.Value // 存储 string 类型
}

// connCache 连接缓存，存储当前活跃的数据库连接及其凭据信息
type connCache struct {
	userName string
	password string
	conn     *sql.DB
}

type connInfo struct {
	account  *ssm.DbAccount
	connStr  string
	userName string
	password string
}

// Config 动态凭据轮转配置
type Config struct {
	// DbConfig 数据库配置（必填）
	DbConfig *DbConfig

	// SsmServiceConfig SSM 服务配置（必填）
	SsmServiceConfig *ssm.SsmAccount

	// WatchChangeInterval 监控凭据变化的间隔时间，范围 1-60 秒，默认 10 秒
	WatchChangeInterval time.Duration

	// RotationGracePeriod 轮转后旧连接的延迟退休时间（可选）
	// 降低并发请求被切断的概率
	RotationGracePeriod time.Duration
}

// DbConfig 数据库连接配置
type DbConfig struct {
	// MaxOpenConns 最大打开连接数
	MaxOpenConns int

	// MaxIdleConns 最大空闲连接数
	MaxIdleConns int

	// IdleTimeoutSeconds 空闲连接超时（秒）
	IdleTimeoutSeconds int

	// ReadTimeoutSeconds 读超时（秒）
	ReadTimeoutSeconds int

	// WriteTimeoutSeconds 写超时（秒）
	WriteTimeoutSeconds int

	// SecretName SSM 凭据名称（必填）
	SecretName string

	// IpAddress 数据库 IP 地址（必填）
	IpAddress string

	// Port 数据库端口（必填），范围 1-65535
	Port uint64

	// DbName 数据库名称（可选）
	DbName string

	// ParamStr 额外的连接参数（可选），例如：charset=utf8&loc=Local
	ParamStr string
}

// HealthCheckResult 健康检查结果
type HealthCheckResult struct {
	// Healthy 是否健康
	Healthy bool

	// Closed 是否已关闭
	Closed bool

	// CurrentUser 当前凭据用户名
	CurrentUser string

	// WatchFailures 监控失败次数
	WatchFailures int32

	// LastError 最后一次错误信息
	LastError string
}

type retiredConn struct {
	db       *sql.DB
	expireAt time.Time
}

func (c *Config) validate() error {
	if c == nil {
		return errors.New("config cannot be nil")
	}
	if c.DbConfig == nil {
		return errors.New("dbConfig cannot be nil")
	}
	if c.SsmServiceConfig == nil {
		return errors.New("ssmServiceConfig cannot be nil")
	}
	if c.DbConfig.SecretName == "" {
		return errors.New("secretName cannot be empty")
	}
	if c.DbConfig.IpAddress == "" {
		return errors.New("ipAddress cannot be empty")
	}
	if c.DbConfig.Port == 0 || c.DbConfig.Port > 65535 {
		return fmt.Errorf("invalid port: %d, must be between 1 and 65535", c.DbConfig.Port)
	}
	if err := c.SsmServiceConfig.Validate(); err != nil {
		return err
	}
	if c.WatchChangeInterval <= 0 {
		return errors.New("watchChangeInterval must be positive")
	}
	if c.WatchChangeInterval < time.Second {
		return errors.New("watchChangeInterval should be at least 1s to avoid excessive API calls")
	}
	if c.WatchChangeInterval > 60*time.Second {
		return errors.New("watchChangeInterval should not exceed 60s to ensure timely credential rotation detection")
	}
	return nil
}

func (c *Config) buildConnInfo(client *ssmapi.Client) (*connInfo, error) {
	account, err := ssm.GetCurrentAccountWithClient(c.DbConfig.SecretName, client)
	if err != nil {
		return nil, err
	}

	// 对用户名和密码进行转义，防止特殊字符破坏 DSN 格式
	escapedUser := url.PathEscape(account.UserName)
	escapedPass := url.PathEscape(account.Password)

	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s",
		escapedUser,
		escapedPass,
		c.DbConfig.IpAddress,
		c.DbConfig.Port,
		c.DbConfig.DbName,
	)

	paramStr := buildParamString(c.DbConfig)
	if paramStr != "" {
		connStr = fmt.Sprintf("%s?%s", connStr, paramStr)
	}

	return &connInfo{
		account:  account,
		connStr:  connStr,
		userName: account.UserName,
		password: account.Password,
	}, nil
}

func buildParamString(dbConfig *DbConfig) string {
	if dbConfig == nil {
		return ""
	}

	values := url.Values{}
	if dbConfig.ParamStr != "" {
		parsed, err := url.ParseQuery(dbConfig.ParamStr)
		if err != nil {
			return dbConfig.ParamStr
		}
		values = parsed
	}
	if dbConfig.ReadTimeoutSeconds > 0 && values.Get("readTimeout") == "" {
		values.Set("readTimeout", fmt.Sprintf("%ds", dbConfig.ReadTimeoutSeconds))
	}
	if dbConfig.WriteTimeoutSeconds > 0 && values.Get("writeTimeout") == "" {
		values.Set("writeTimeout", fmt.Sprintf("%ds", dbConfig.WriteTimeoutSeconds))
	}
	return values.Encode()
}

// Init 初始化数据库连接管理器
//
// 根据提供的配置信息，获取数据库凭据并建立连接，然后启动后台凭据监控。
// 在服务初始化时调用本方法。注意：不可重复调用。
func (d *DynamicSecretRotationDb) Init(config *Config) error {
	var initErr error
	d.initOnce.Do(func() {
		if err := config.validate(); err != nil {
			initErr = err
			return
		}
		d.config = config
		d.stopCh = make(chan struct{})
		d.watcherDone = make(chan struct{})

		// 创建并缓存 SSM 客户端，后续 watcher 轮询复用
		client, err := ssm.CreateSsmClient(config.SsmServiceConfig)
		if err != nil {
			initErr = fmt.Errorf("create ssm client error: %s", err)
			return
		}
		d.cachedSsmClient = client

		info, err := d.config.buildConnInfo(d.cachedSsmClient)
		if err != nil {
			initErr = err
			return
		}
		if err := d.initDbConn(info); err != nil {
			initErr = err
			return
		}
		log.Printf("succeed to init dbConn for user=%s", info.account.UserName)

		// 临时凭据模式提示：SDK 不会自动刷新临时凭据
		if config.SsmServiceConfig.CredentialType == ssm.Temporary {
			log.Println("[WARN] Using TEMPORARY credential type. The SDK will NOT auto-refresh the temporary credential. " +
				"Please ensure the token remains valid during the SDK's lifecycle, or consider using CamRole credential type.")
		}

		go d.watchSecretChange()
	})
	if initErr != nil {
		return initErr
	}
	return nil
}

// GetConn 获取数据库连接
//
// 调用方每次访问数据库时，需通过本方法获取连接。
// 注意：请不要在调用端缓存获取到的 *sql.DB，以确保凭据轮转后能及时使用新连接。
func (d *DynamicSecretRotationDb) GetConn() *sql.DB {
	if atomic.LoadInt32(&d.closed) != 0 {
		return nil
	}
	if conn := d.currentConnCache(); conn != nil {
		return conn.conn
	}
	return nil
}

// Close 关闭连接管理器
//
// 停止后台 watcher，等待其退出后关闭当前数据库句柄和所有退休句柄。
// 建议在应用退出时调用。
func (d *DynamicSecretRotationDb) Close() error {
	var closeErr error
	d.closeOnce.Do(func() {
		atomic.StoreInt32(&d.closed, 1)
		if d.stopCh != nil {
			close(d.stopCh)
		}
		// 等待 watcher goroutine 完全退出，避免与 cleanupRetired 竞争
		if d.watcherDone != nil {
			<-d.watcherDone
		}
		d.cleanupRetired(true)
		if cache := d.currentConnCache(); cache != nil && cache.conn != nil {
			closeErr = cache.conn.Close()
		}
	})
	return closeErr
}

// IsHealthy 检查服务是否健康
//
// 当连接管理器未关闭且 watch 连续失败次数未超过阈值时返回 true。
func (d *DynamicSecretRotationDb) IsHealthy() bool {
	return atomic.LoadInt32(&d.closed) == 0 &&
		atomic.LoadInt32(&d.watchFailures) < maxWatchFailures
}

// GetHealthCheckResult 获取健康检查详情
func (d *DynamicSecretRotationDb) GetHealthCheckResult() *HealthCheckResult {
	currentUser := ""
	if cache := d.currentConnCache(); cache != nil {
		currentUser = cache.userName
	}

	lastErr := ""
	if v := d.lastError.Load(); v != nil {
		lastErr = v.(string)
	}

	return &HealthCheckResult{
		Healthy:       d.IsHealthy(),
		Closed:        atomic.LoadInt32(&d.closed) != 0,
		CurrentUser:   currentUser,
		WatchFailures: atomic.LoadInt32(&d.watchFailures),
		LastError:     lastErr,
	}
}

// GetCurrentUser 获取当前凭据用户名（用于监控轮转）
func (d *DynamicSecretRotationDb) GetCurrentUser() string {
	if cache := d.currentConnCache(); cache != nil {
		return cache.userName
	}
	return ""
}

func (d *DynamicSecretRotationDb) currentConnCache() *connCache {
	if cache, ok := d.dbConn.Load().(*connCache); ok {
		return cache
	}
	return nil
}

func (d *DynamicSecretRotationDb) initDbConn(info *connInfo) error {
	if info == nil {
		return errors.New("connection info cannot be nil")
	}
	tmpDbConn, err := sql.Open("mysql", info.connStr)
	if err != nil {
		return fmt.Errorf("connect to cdb error: %s", err)
	}
	dbConfig := d.config.DbConfig
	tmpDbConn.SetMaxOpenConns(dbConfig.MaxOpenConns)
	tmpDbConn.SetMaxIdleConns(dbConfig.MaxIdleConns)

	connLifeTime := time.Duration(dbConfig.IdleTimeoutSeconds) * time.Second
	tmpDbConn.SetConnMaxLifetime(connLifeTime)

	// 使用带超时的 Ping，防止数据库不可达时长时间阻塞
	ctx, cancel := context.WithTimeout(context.Background(), defaultPingTimeout)
	defer cancel()
	err = tmpDbConn.PingContext(ctx)
	if err != nil {
		_ = tmpDbConn.Close()
		return fmt.Errorf("ping cdb error: %s", err)
	}

	curConn := d.currentConnCache()
	d.dbConn.Store(&connCache{
		userName: info.userName,
		password: info.password,
		conn:     tmpDbConn,
	})
	if curConn != nil && curConn.conn != nil {
		d.retireConn(curConn.conn)
	}
	return nil
}

func (d *DynamicSecretRotationDb) watchSecretChange() {
	defer close(d.watcherDone) // 通知 Close() 方法 watcher 已完全退出

	initialDelay := d.randomizedInitialDelay()
	if initialDelay > 0 {
		timer := time.NewTimer(initialDelay)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-d.stopCh:
			return
		}
	}

	interval := d.config.WatchChangeInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.cleanupRetired(false)
			d.watchChange()

			// 指数退避：连续失败超过阈值后，逐步增大轮询间隔
			failures := atomic.LoadInt32(&d.watchFailures)
			if failures >= maxWatchFailures {
				exponent := failures - maxWatchFailures
				if exponent > maxBackoffMultiplier {
					exponent = maxBackoffMultiplier
				}
				backoff := interval * time.Duration(int64(1)<<uint(exponent))
				ticker.Reset(backoff)
			} else if failures == 0 {
				// 恢复正常后，重置为原始间隔
				ticker.Reset(interval)
			}
		case <-d.stopCh:
			return
		}
	}
}

func (d *DynamicSecretRotationDb) watchChange() {
	if atomic.LoadInt32(&d.closed) != 0 {
		return
	}

	info, err := d.config.buildConnInfo(d.cachedSsmClient)
	if err != nil {
		failures := atomic.AddInt32(&d.watchFailures, 1)
		d.lastError.Store(err.Error())

		if failures >= maxWatchFailures {
			log.Printf("watch failed %d times: %s", failures, err.Error())
		} else {
			log.Printf("watch failed (%d/%d): %s", failures, maxWatchFailures, err.Error())
		}
		return
	}

	// 成功，重置失败计数
	atomic.StoreInt32(&d.watchFailures, 0)
	d.lastError.Store("")

	// 检测凭据是否变化（用户名或密码任一变化即更新）
	current := d.currentConnCache()
	if !isCredentialChanged(current, info) {
		return
	}

	if current != nil {
		log.Printf("credential rotated: %s -> %s", current.userName, info.account.UserName)
	} else {
		log.Printf("credential initialized for user=%s", info.account.UserName)
	}

	if err := d.initDbConn(info); err != nil {
		log.Println("failed to initDbConn, err=", err)
		return
	}
	log.Printf("succeed to rotate db connection for user=%s", info.account.UserName)
}

// isCredentialChanged 检测凭据是否变化（用户名或密码任一变化）
func isCredentialChanged(current *connCache, newInfo *connInfo) bool {
	if current == nil || newInfo == nil {
		return true
	}
	// 用户名变化
	if current.userName != newInfo.userName {
		return true
	}
	// 密码变化（同一用户密码轮转场景）
	if current.password != newInfo.password {
		log.Printf("password rotated for user: %s", newInfo.userName)
		return true
	}
	return false
}

func (d *DynamicSecretRotationDb) randomizedInitialDelay() time.Duration {
	interval := d.config.WatchChangeInterval
	if interval <= 0 {
		return 0
	}
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	return time.Duration(rnd.Int63n(int64(interval)))
}

func (d *DynamicSecretRotationDb) rotationGracePeriod() time.Duration {
	if d.config != nil && d.config.RotationGracePeriod > 0 {
		return d.config.RotationGracePeriod
	}
	intervalGrace := d.config.WatchChangeInterval * 3
	if intervalGrace > defaultRotationGracePeriod {
		return intervalGrace
	}
	return defaultRotationGracePeriod
}

func (d *DynamicSecretRotationDb) retireConn(db *sql.DB) {
	if db == nil {
		return
	}
	d.retiredMu.Lock()
	d.retired = append(d.retired, retiredConn{
		db:       db,
		expireAt: time.Now().Add(d.rotationGracePeriod()),
	})
	d.retiredMu.Unlock()
}

func (d *DynamicSecretRotationDb) cleanupRetired(force bool) {
	var toClose []retiredConn

	d.retiredMu.Lock()
	if force {
		toClose = d.retired
		d.retired = nil
	} else {
		now := time.Now()
		remaining := make([]retiredConn, 0, len(d.retired))
		for _, item := range d.retired {
			if item.db == nil {
				continue
			}
			if now.Before(item.expireAt) {
				remaining = append(remaining, item)
			} else {
				toClose = append(toClose, item)
			}
		}
		d.retired = remaining
	}
	d.retiredMu.Unlock()

	// 在锁外执行 Close，避免持锁时间过长
	for _, item := range toClose {
		if item.db == nil {
			continue
		}
		if err := item.db.Close(); err != nil {
			log.Println("failed to close retired db handle, err=", err)
		}
	}
}
