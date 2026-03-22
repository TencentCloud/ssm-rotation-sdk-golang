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

package main

import (
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/tencentcloud/ssm-rotation-sdk-golang/lib/db"
	"github.com/tencentcloud/ssm-rotation-sdk-golang/lib/ssm"
)

var dbConn *db.DynamicSecretRotationDb

func main() {
	// ==================== 选择认证方式（三选一）====================

	// 方式一：CVM 角色绑定（推荐，仅限 CVM 环境）
	// SDK 通过元数据服务自动获取和刷新临时凭据，安全性最高
	ssmAccount := ssm.WithCamRole(
		"your-cam-role-name", // CVM 实例绑定的 CAM 角色名称
		"ap-guangzhou",       // 选择凭据所存储的地域
	)

	// 方式二：临时凭据
	// 注意：临时凭据有过期时间，SDK 不会自动刷新此方式的凭据
	// ssmAccount := ssm.WithTemporaryCredential(
	// 	"TmpSecretId",  // 临时 SecretId
	// 	"TmpSecretKey", // 临时 SecretKey
	// 	"Token",        // 临时 Token
	// 	"ap-guangzhou", // 选择凭据所存储的地域
	// )

	// 方式三：固定 AK/SK（向后兼容，不推荐在生产环境使用）
	// ssmAccount := ssm.WithPermanentCredential(
	// 	"SecretId",     // 需填写实际可用的 SecretId
	// 	"SecretKey",    // 需填写实际可用的 SecretKey
	// 	"ap-guangzhou", // 选择凭据所存储的地域
	// )

	// 旧写法仍然兼容（等同于方式三）：
	// ssmAccount := &ssm.SsmAccount{
	// 	SecretId:  "SecretId",
	// 	SecretKey: "SecretKey",
	// 	Region:    "ap-guangzhou",
	// }

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
			Port:                3306,            // 数据库端口
			DbName:              "database_name", // 可以为空，或指定具体的数据库名
			ParamStr:            "charset=utf8&loc=Local",
		},
		SsmServiceConfig:    ssmAccount,
		WatchChangeInterval: time.Second * 10, // 多长时间检查一下凭据是否发生了轮转（范围 1-60 秒）
		RotationGracePeriod: time.Second * 30, // 可选：轮转后旧连接的延迟退休时间，降低并发请求被切断的风险
	})
	if err != nil {
		log.Fatal("failed to init dbConn, err=", err)
		return
	}
	defer func() {
		if err := dbConn.Close(); err != nil {
			log.Println("failed to close dbConn, err=", err)
		}
	}()

	// 健康检查
	log.Printf("isHealthy: %v", dbConn.IsHealthy())
	result := dbConn.GetHealthCheckResult()
	log.Printf("healthCheck: healthy=%v, currentUser=%s, watchFailures=%d",
		result.Healthy, result.CurrentUser, result.WatchFailures)

	// 获取当前凭据用户名（可用于监控轮转是否生效）
	log.Printf("currentUser: %s", dbConn.GetCurrentUser())

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
	if c == nil {
		log.Println("failed to get db handle")
		return
	}
	if err := c.Ping(); err != nil {
		log.Println("failed to access db with err: ", err)
		return
	}
	log.Println("--- succeed to access db")
}
