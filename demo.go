package main

import (
	"log"

	_ "github.com/go-sql-driver/mysql"
	"github.com/tencentcloud/ssm-rotation-sdk-golang/lib/db"
	"github.com/tencentcloud/ssm-rotation-sdk-golang/lib/ssm"

	"time"
)

var dbConn *db.DynamicSecretRotationDb

func main() {
	// 初始化数据库连接
	dbConn = &db.DynamicSecretRotationDb{}
	err := dbConn.Init(&db.Config{
		DbConfig: &db.DbConfig{
			MaxOpenConns:        100,
			MaxIdleConns:        50,
			IdleTimeoutSeconds:  100,
			ReadTimeoutSeconds:  5,
			WriteTimeoutSeconds: 5,
			SecretName:          "test",          // 凭据名
			IpAddress:           "127.0.0.1",     // 数据库地址
			Port:                58366,           // 数据库端口
			DbName:              "database_name", // 可以为空，或指定具体的数据库名
			ParamStr:            "charset=utf8&loc=Local",
		},
		SsmServiceConfig: &ssm.SsmAccount{
			SecretId:  "SecretId",     // 需填写实际可用的SecretId
			SecretKey: "SecretKey",    // 需填写实际可用的SecretKey
			Region:    "ap-guangzhou", // 选择凭据所存储的地域
		},
		WatchChangeInterval: time.Second * 10, // 多长时间检查一下 凭据是否发生了轮转
	})
	if err != nil {
		log.Fatal("failed to init dbConn, err=", err)
		return
	}
	// 模拟业务处理中，每过一段时间（一般是几毫秒），需要拿到db连接，来操作数据库的场景
	t := time.Tick(time.Second)
	for {
		select {
		case <-t:
			accessDb()
		}
	}
}

func accessDb() {
	log.Println("--- accessDb start")
	c := dbConn.GetConn()
	if err := c.Ping(); err != nil {
		log.Fatal("failed to access db with err: ", err)
		return
	}
	log.Println("--- succeed to access db")
}
