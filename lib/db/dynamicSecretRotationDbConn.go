/*
 * Copyright (c) 2017-2025 Tencent. All Rights Reserved.
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
)

const (
	defaultRotationGracePeriod = 30 * time.Second
	maxWatchFailures           = 5
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
	config    *Config
	dbConn    atomic.Value
	stopCh    chan struct{}
	closeOnce sync.Once
	closed    int32
	retiredMu sync.Mutex
	retired   []retiredConn

	// 健康状态
	watchFailures int32
	lastError     atomic.Value // 存储 string 类型
}

// ConnCache 连接缓存，存储当前活跃的数据库连接及其凭据信息
type ConnCache struct {
	UserName string
	Password string
	Conn     *sql.DB
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

func (c *Config) buildConnInfo() (*connInfo, error) {
	account, err := ssm.GetCurrentAccount(c.DbConfig.SecretName, c.SsmServiceConfig)
	if err != nil {
		return nil, err
	}

	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s",
		account.UserName,
		account.Password,
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
// 在服务初始化时调用本方法。
func (d *DynamicSecretRotationDb) Init(config *Config) error {
	if err := config.validate(); err != nil {
		return err
	}
	d.config = config
	d.stopCh = make(chan struct{})

	info, err := d.config.buildConnInfo()
	if err != nil {
		return err
	}
	if err := d.initDbConn(info); err != nil {
		return err
	}
	log.Printf("succeed to init dbConn for user=%s", info.account.UserName)
	go d.watchSecretChange()
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
		return conn.Conn
	}
	return nil
}

// Close 关闭连接管理器
//
// 停止后台 watcher，关闭当前数据库句柄和所有退休句柄。
// 建议在应用退出时调用。
func (d *DynamicSecretRotationDb) Close() error {
	var closeErr error
	d.closeOnce.Do(func() {
		atomic.StoreInt32(&d.closed, 1)
		if d.stopCh != nil {
			close(d.stopCh)
		}
		d.cleanupRetired(true)
		if cache := d.currentConnCache(); cache != nil && cache.Conn != nil {
			closeErr = cache.Conn.Close()
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
		currentUser = cache.UserName
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
		return cache.UserName
	}
	return ""
}

func (d *DynamicSecretRotationDb) currentConnCache() *ConnCache {
	if cache, ok := d.dbConn.Load().(*ConnCache); ok {
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

	err = tmpDbConn.Ping()
	if err != nil {
		return fmt.Errorf("ping cdb error: %s", err)
	}

	curConn := d.currentConnCache()
	d.dbConn.Store(&ConnCache{
		UserName: info.userName,
		Password: info.password,
		Conn:     tmpDbConn,
	})
	if curConn != nil && curConn.Conn != nil {
		d.retireConn(curConn.Conn)
	}
	return nil
}

func (d *DynamicSecretRotationDb) watchSecretChange() {
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

	ticker := time.NewTicker(d.config.WatchChangeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.cleanupRetired(false)
			d.watchChange()
		case <-d.stopCh:
			return
		}
	}
}

func (d *DynamicSecretRotationDb) watchChange() {
	if atomic.LoadInt32(&d.closed) != 0 {
		return
	}

	info, err := d.config.buildConnInfo()
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
		log.Printf("credential rotated: %s -> %s", current.UserName, info.account.UserName)
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
func isCredentialChanged(current *ConnCache, newInfo *connInfo) bool {
	if current == nil || newInfo == nil {
		return true
	}
	// 用户名变化
	if current.UserName != newInfo.userName {
		return true
	}
	// 密码变化（同一用户密码轮转场景）
	if current.Password != newInfo.password {
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
	d.retiredMu.Lock()
	retired := d.retired
	if force {
		d.retired = nil
	} else {
		remaining := make([]retiredConn, 0, len(retired))
		now := time.Now()
		for _, item := range retired {
			if item.db == nil {
				continue
			}
			if now.Before(item.expireAt) {
				remaining = append(remaining, item)
			}
		}
		d.retired = remaining
	}
	d.retiredMu.Unlock()

	now := time.Now()
	for _, item := range retired {
		if item.db == nil {
			continue
		}
		if !force && now.Before(item.expireAt) {
			continue
		}
		if err := item.db.Close(); err != nil {
			log.Println("failed to close retired db handle, err=", err)
		}
	}
}
