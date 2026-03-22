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

package ssm

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ssm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923"
)

// sentinel errors，方便调用方通过 errors.Is 做分类处理
var (
	// ErrConfigInvalid 配置参数无效
	ErrConfigInvalid = errors.New("ssm: config invalid")
	// ErrSsmApi SSM API 调用失败
	ErrSsmApi = errors.New("ssm: api error")
	// ErrSecretFormat 凭据格式无效
	ErrSecretFormat = errors.New("ssm: invalid secret format")
)

// CredentialType 凭据类型枚举
//
// 定义 SDK 支持的凭据获取方式
type CredentialType int

const (
	// Permanent 固定 AK/SK 方式
	// 使用长期有效的 SecretId 和 SecretKey
	// 安全性较低，不推荐在生产环境使用
	Permanent CredentialType = iota

	// Temporary 临时凭据方式
	// 使用临时 SecretId、SecretKey 和 Token
	// 凭据有过期时间，需要用户自行管理刷新
	Temporary

	// CamRole CVM 角色绑定方式（推荐用于 CVM）
	// 通过计算实例元数据服务自动获取临时凭据
	// SDK 自动刷新，安全性最高
	// 注意：只有 CVM 支持真正的角色绑定方式
	CamRole
)

// SsmAccount SSM 服务账号配置
//
// 支持三种核心认证方式：
//  1. 角色绑定（CamRole）- 推荐用于 CVM，通过元数据服务自动获取临时凭据
//  2. 临时凭据（Temporary）- 使用临时 AK/SK/Token
//  3. 固定凭据（Permanent）- 使用固定 AK/SK，不推荐在生产环境使用
type SsmAccount struct {
	// CredentialType 凭据类型，默认为 Permanent（向后兼容）
	CredentialType CredentialType `yaml:"credentialType"`

	// SecretId 腾讯云 SecretId，适用于 Permanent 和 Temporary 类型
	SecretId string `yaml:"secretId"`

	// SecretKey 腾讯云 SecretKey，适用于 Permanent 和 Temporary 类型
	SecretKey string `yaml:"secretKey"`

	// Token 临时凭据 Token，仅适用于 Temporary 类型
	Token string `yaml:"token"`

	// RoleName CAM 角色名称，仅适用于 CamRole 类型
	RoleName string `yaml:"roleName"`

	// Url SSM 服务的自定义接入点 URL（可选）
	Url string `yaml:"url"`

	// Region 地域信息，如：ap-guangzhou, ap-beijing 等
	Region string `yaml:"region"`
}

// WithCamRole 创建角色绑定方式的凭据配置（推荐用于 CVM）
//
// SDK 会通过计算实例元数据服务自动获取临时凭据并在过期前自动刷新。
// 注意：只有 CVM 支持真正的角色绑定方式。
func WithCamRole(roleName string, region string) *SsmAccount {
	return &SsmAccount{
		CredentialType: CamRole,
		RoleName:       roleName,
		Region:         region,
	}
}

// WithTemporaryCredential 创建临时凭据方式的配置
//
// 用户自行获取临时凭据后传入 SDK。
// 注意：临时凭据有过期时间，SDK 不会自动刷新此方式的凭据。
func WithTemporaryCredential(secretId, secretKey, token, region string) *SsmAccount {
	return &SsmAccount{
		CredentialType: Temporary,
		SecretId:       secretId,
		SecretKey:      secretKey,
		Token:          token,
		Region:         region,
	}
}

// WithPermanentCredential 创建固定 AK/SK 方式的凭据配置（不推荐）
//
// 安全性较低，不推荐在生产环境使用。
// 生产环境请使用 WithCamRole() 或 WithTemporaryCredential()。
func WithPermanentCredential(secretId, secretKey, region string) *SsmAccount {
	return &SsmAccount{
		CredentialType: Permanent,
		SecretId:       secretId,
		SecretKey:      secretKey,
		Region:         region,
	}
}

// WithEndpoint 设置自定义接入点（链式调用）
func (a *SsmAccount) WithEndpoint(url string) *SsmAccount {
	a.Url = url
	return a
}

// Validate 验证配置参数的有效性
func (a *SsmAccount) Validate() error {
	if a == nil {
		return fmt.Errorf("ssmAccount cannot be nil")
	}
	if a.Region == "" {
		return fmt.Errorf("region cannot be empty")
	}

	switch a.CredentialType {
	case Permanent:
		if a.SecretId == "" {
			return fmt.Errorf("secretId cannot be empty for PERMANENT credential type")
		}
		if a.SecretKey == "" {
			return fmt.Errorf("secretKey cannot be empty for PERMANENT credential type")
		}
	case Temporary:
		if a.SecretId == "" {
			return fmt.Errorf("secretId cannot be empty for TEMPORARY credential type")
		}
		if a.SecretKey == "" {
			return fmt.Errorf("secretKey cannot be empty for TEMPORARY credential type")
		}
		if a.Token == "" {
			return fmt.Errorf("token cannot be empty for TEMPORARY credential type")
		}
	case CamRole:
		if a.RoleName == "" {
			return fmt.Errorf("roleName cannot be empty for CAM_ROLE credential type")
		}
	default:
		return fmt.Errorf("unknown credential type: %d", a.CredentialType)
	}
	return nil
}

// String 自定义字符串输出，对敏感信息进行脱敏处理
func (a *SsmAccount) String() string {
	if a == nil {
		return "SsmAccount{nil}"
	}

	result := fmt.Sprintf("SsmAccount{credentialType=%d", a.CredentialType)

	if a.CredentialType == CamRole {
		result += fmt.Sprintf(", roleName='%s'", a.RoleName)
	} else {
		maskedId := "null"
		if a.SecretId != "" {
			end := 4
			if len(a.SecretId) < 4 {
				end = len(a.SecretId)
			}
			maskedId = a.SecretId[:end] + "****"
		}
		result += fmt.Sprintf(", secretId='%s', secretKey='****'", maskedId)
		if a.CredentialType == Temporary {
			result += ", token='****'"
		}
	}

	result += fmt.Sprintf(", region='%s'", a.Region)
	if a.Url != "" {
		result += fmt.Sprintf(", endpoint='%s'", a.Url)
	}
	result += "}"
	return result
}

// DbAccount 数据库账号信息
type DbAccount struct {
	UserName string `json:"UserName"`
	Password string `json:"Password"`
}

// CreateSsmClient 创建 SSM 客户端（公开方法，供外部缓存复用）
func CreateSsmClient(ssmAcc *SsmAccount) (*ssm.Client, error) {
	return createSsmClient(ssmAcc)
}

// GetCurrentAccount 获取当前数据库账号信息
//
// 注意：此方法每次调用都会创建新的 SSM Client。
// 如果需要频繁调用（如周期性轮询），建议使用 CreateSsmClient 创建客户端后，
// 通过 GetCurrentAccountWithClient 复用，以避免重复创建 TCP/TLS 连接的开销。
func GetCurrentAccount(secretName string, ssmAcc *SsmAccount) (*DbAccount, error) {
	client, err := createSsmClient(ssmAcc)
	if err != nil {
		return nil, fmt.Errorf("create ssm HTTP client error: %w", err)
	}
	return GetCurrentAccountWithClient(secretName, client)
}

// GetCurrentAccountWithClient 使用已有的 SSM 客户端获取当前数据库账号信息
//
// 推荐在需要频繁调用的场景（如 watcher 轮询）中复用 Client，
// 以避免重复创建 TCP/TLS 连接的开销。
func GetCurrentAccountWithClient(secretName string, client *ssm.Client) (*DbAccount, error) {
	secretValue, err := getSecretValueWithClient(secretName, client)
	if err != nil {
		return nil, err
	}
	if len(secretValue) == 0 {
		return nil, fmt.Errorf("%w: secret value is empty", ErrSecretFormat)
	}
	account := &DbAccount{}
	if err := json.Unmarshal([]byte(secretValue), account); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON format", ErrSecretFormat)
	}
	if account.UserName == "" || account.Password == "" {
		return nil, fmt.Errorf("%w: missing userName or password", ErrSecretFormat)
	}
	return account, nil
}

// getSecretValueWithClient 使用指定的 SSM 客户端获取凭据值
func getSecretValueWithClient(secretName string, client *ssm.Client) (string, error) {
	if client == nil {
		return "", fmt.Errorf("%w: ssm client cannot be nil", ErrConfigInvalid)
	}
	request := ssm.NewGetSecretValueRequest()
	request.SecretName = &secretName
	request.VersionId = common.StringPtr("SSM_Current")
	rsp, err := client.GetSecretValue(request)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrSsmApi, err.Error())
	}
	if rsp.Response.SecretString == nil {
		log.Println("Secret Value is nil")
		return "", nil
	}
	return *rsp.Response.SecretString, nil
}

// createSsmClient 根据凭据类型创建 SSM 客户端
func createSsmClient(ssmAcc *SsmAccount) (*ssm.Client, error) {
	if ssmAcc == nil {
		return nil, fmt.Errorf("%w: ssm account cannot be nil", ErrConfigInvalid)
	}
	if ssmAcc.Region == "" {
		return nil, fmt.Errorf("%w: region is required", ErrConfigInvalid)
	}

	credential, err := createCredential(ssmAcc)
	if err != nil {
		return nil, err
	}

	httpProfile := profile.NewHttpProfile()
	httpProfile.ReqMethod = "POST"
	if len(ssmAcc.Url) != 0 {
		httpProfile.Endpoint = ssmAcc.Url
	}
	cpf := profile.NewClientProfile()
	cpf.HttpProfile = httpProfile
	return ssm.NewClient(credential, ssmAcc.Region, cpf)
}

// createCredential 根据凭据类型创建对应的 Credential 对象
//
// 注意：调用前应先通过 SsmAccount.Validate() 校验参数，此处不再重复校验。
func createCredential(ssmAcc *SsmAccount) (common.CredentialIface, error) {
	switch ssmAcc.CredentialType {
	case Temporary:
		return common.NewTokenCredential(ssmAcc.SecretId, ssmAcc.SecretKey, ssmAcc.Token), nil

	case CamRole:
		provider := common.NewCvmRoleProvider(ssmAcc.RoleName)
		return provider.GetCredential()

	case Permanent:
		fallthrough
	default:
		// 默认使用固定 AK/SK（向后兼容）
		return common.NewCredential(ssmAcc.SecretId, ssmAcc.SecretKey), nil
	}
}
