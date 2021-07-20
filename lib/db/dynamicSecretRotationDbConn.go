package db

import (
	"database/sql"
	"fmt"
	"github.com/tencentcloud/ssm-rotation-sdk-golang/lib/ssm"
	"log"

	"sync/atomic"
	"time"
)

type DynamicSecretRotationDb struct {
	config *Config      // 初始化配置
	dbConn atomic.Value // 存储的是 ConnCache 结构体
}

type ConnCache struct {
	ConnStr string // 缓存的当前正在使用的连接信息
	Conn    *sql.DB
}

type Config struct {
	DbConfig            *DbConfig
	SsmServiceConfig    *ssm.SsmAccount
	WatchChangeInterval time.Duration
}

type DbConfig struct {
	MaxOpenConns        int
	MaxIdleConns        int
	IdleTimeoutSeconds  int
	ReadTimeoutSeconds  int
	WriteTimeoutSeconds int
	SecretName          string
	IpAddress           string
	Port                uint64
	DbName              string
	ParamStr            string // 例如：charset=utf8&loc=Local
}

func (c *Config) BuildConnStr() (string, error) {
	account, err := ssm.GetCurrentAccount(c.DbConfig.SecretName, c.SsmServiceConfig) // secretValue里面存储了用户名和密码，格式为：userName:password
	if err != nil {
		return "", err
	}
	// connection string 的格式： {user}:{password}@tcp({ip}:{port})/{dbName}?charset=utf8&loc=Local
	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", account.UserName, account.Password, c.DbConfig.IpAddress, c.DbConfig.Port, c.DbConfig.DbName)
	if len(c.DbConfig.ParamStr) != 0 {
		connStr = fmt.Sprintf("%s?%s", connStr, c.DbConfig.ParamStr)
	}
	return connStr, nil
}

/**
在服务初始化的时候，可调用本方法来完成数据库连接的初始化。
本方法根据提供的凭据相关的信息（服务账号，凭据名），获得真实的数据库用户名和密码信息，然后生成数据库连接
*/
func (d *DynamicSecretRotationDb) Init(config *Config) error {
	d.config = config
	err := d.initDbConn()
	if err != nil {
		return err
	}
	log.Println("succeed to init dbConn: ", d.GetConn())
	go d.watchSecretChange()
	return nil
}

/**
调用方每次访问db时，需通过调用本方法获取db连接。
注意：请不要在调用端缓存获取到的 *sql.DB, 以便确保在凭据发生轮换后，能及时的获得到最新的用户名和密码，防止由于用户名密码过期，而造成数据库连接失败！
*/
func (d *DynamicSecretRotationDb) GetConn() *sql.DB {
	if conn, ok := d.dbConn.Load().(*ConnCache); ok {
		log.Println("GetConn, connStr=", conn.ConnStr)
		return conn.Conn
	}
	return nil
}

func (d *DynamicSecretRotationDb) getCurrentConnStr() string {
	conn := d.dbConn.Load().(*ConnCache)
	log.Println("GetConn, connStr=", conn.ConnStr)
	return conn.ConnStr
}

func (d *DynamicSecretRotationDb) initDbConn() error {
	var err error
	connStr, err := d.config.BuildConnStr()
	if err != nil {
		return err
	}
	tmpDbConn, err := sql.Open("mysql", connStr)
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
		return fmt.Errorf("ping cdb error: %s ", err)
	}
	// 将有效的 connStr 缓存起来
	curConn := d.GetConn()
	d.dbConn.Store(&ConnCache{
		ConnStr: connStr,
		Conn:    tmpDbConn,
	})
	if curConn != nil {
		if err := curConn.Close(); err != nil {
			log.Println("failed to close connection, err= ", err)
			return err
		}
	}
	return nil
}

func (d *DynamicSecretRotationDb) watchSecretChange() {
	t := time.Tick(d.config.WatchChangeInterval)
	for {
		select {
		case <-t:
			d.watchChange()
		}
	}
}

func (d *DynamicSecretRotationDb) watchChange() {
	connStr, err := d.config.BuildConnStr()
	if err != nil {
		log.Println("failed to build connStr, err= ", err)
		return
	}
	if connStr == d.getCurrentConnStr() {
		log.Println("db connStr is not changed")
		return
	}

	log.Printf("connstr is changing from [%s] to [%s] \n", d.getCurrentConnStr(), connStr)
	err = d.initDbConn()
	if err != nil {
		log.Println("failed to initDbConn, err=", err)
		return
	}
	log.Printf("**** succeed to change dbConn, new connStr=%s  **** \n", d.getCurrentConnStr())

}
