package ssm

import (
	"encoding/json"
	"fmt"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ssm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923"
	"log"
	"time"
)

type DbAccount struct {
	UserName string `json:"UserName"`
	Password string `json:"Password"`
}

func GetCurrentAccount(secretName string, ssmAcc *SsmAccount) (*DbAccount, error) {
	secretValue, err := getCurrentProductSecretValue(secretName, ssmAcc) // secretValue里面存储了用户名和密码，格式为：userName:password
	if err != nil {
		log.Println("failed to GetCurrentProductSecretValue, err= ", err)
		return nil, err
	}
	if len(secretValue) == 0 {
		return nil, fmt.Errorf("no valid account info found because secret value is empty")
	}
	account := &DbAccount{}
	if err := json.Unmarshal([]byte(secretValue), account); err != nil {
		log.Println("err when parse secretValue in json format, err= ", err)
		return nil, fmt.Errorf("invalid secret value format")
	}
	return account, nil
}

func getCurrentProductSecretValue(secretName string, ssmAcc *SsmAccount) (string, error) {
	log.Printf("get value for secretName=%v", secretName)
	startTime := time.Now()
	client, err := getClient(ssmAcc.SecretId, ssmAcc.SecretKey, ssmAcc.Url, ssmAcc.Region)
	if err != nil {
		log.Fatal(" create ssm client error: ", err)
		return "", fmt.Errorf("create ssm HTTP client error: %s", err)
	}
	request := ssm.NewGetSecretValueRequest()
	request.SecretName = &secretName
	request.VersionId = common.StringPtr("SSM_Current")
	log.Printf("GetSecretValue request=%s", request.ToJsonString())
	rsp, err := client.GetSecretValue(request)
	if err != nil {
		log.Fatal(" ssm GetSecretValue error: ", err.Error())
		return "", fmt.Errorf("ssm GetSecretValue error: %s", err.Error())
	}
	if rsp.Response.SecretString == nil {
		log.Println("Secret Value is nil")
		return "", nil
	}
	log.Printf("GetCurrentProductSecretValue cost time: %d", time.Since(startTime))
	return *rsp.Response.SecretString, nil
}

func getClient(secretId string, secretKey string, url string, region string) (*ssm.Client, error) {
	credential := common.NewCredential(secretId, secretKey)
	httpProfile := profile.NewHttpProfile()
	httpProfile.ReqMethod = "POST"
	if len(url) != 0 {
		httpProfile.Endpoint = url
	}
	cpf := profile.NewClientProfile()
	cpf.HttpProfile = httpProfile
	client, err := ssm.NewClient(credential, region, cpf)
	return client, err
}

type SsmAccount struct {
	SecretId  string `yaml:"secretId"`
	SecretKey string `yaml:"secretKey"`
	Url       string `yaml:"url"`
	Region    string `yaml:"region"`
}
