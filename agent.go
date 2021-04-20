package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	AUTH             = "Authorization"
	TYPE             = "Content-Type"
	TypeValue        = "application/json;charset=UTF-8"
	ConfigPath       = "config.json"
	NginxConf        = "nginx.conf"
	DefaultCluster   = "default"
	DefaultNamespace = "application"
)

type ApolloConfig struct {
	Key                        string `json:"key"`
	Value                      string `json:"value"`
	Comment                    string `json:"comment,omitempty"`
	DataChangeLastModifiedBy   string `json:"dataChangeLastModifiedBy"`
	DataChangeCreatedBy        string `json:"dataChangeCreatedBy,omitempty"`
	DataChangeCreatedTime      string `json:"dataChangeCreatedTime,omitempty"`
	DataChangeLastModifiedTime string `json:"dataChangeLastModifiedTime,omitempty"`
}

type AgentConfig struct {
	Ip            string
	Env           string
	AppId         string
	Token         string
	CreatedBy     string
	NginxConfPath string
}

type Release struct {
	ReleaseTitle string `json:"releaseTitle"`
	ReleasedBy   string `json:"releasedBy"`
}

func main() {

	// 异常处理
	defer func() {
		if err := recover(); err != nil {
			log.Fatalln(err)
		}
	}()

	f, err := os.Open(ConfigPath)
	if err != nil {
		panic(fmt.Errorf("agent配置文件读取异常: %v", err))
	}
	defer f.Close()
	var conf AgentConfig
	if err := json.NewDecoder(f).Decode(&conf); err != nil {
		panic(fmt.Errorf("agent配置文件解析异常: %v", err))
	}

	if err := conf.validate(); err != nil {
		panic(fmt.Errorf("agent配置文件解析异常: %v", err))
	}
	if err = conf.updateConfig(); err != nil {
		panic(fmt.Errorf("同步本地配置至apollo异常: %v", err))
	}
	log.Println("同步本地配置至apollo成功")
	path := conf.NginxConfPath
	bakPath := path + ".bak"
	token := conf.Token

	for {
		time.Sleep(60 * time.Second)
		bakErr := bakOrRec(path, bakPath)
		if bakErr != nil {
			log.Println("备份文件异常: ", bakErr)
			continue
			//panic(bakErr)
		}
		content, httpErr := GetConfig(conf.Format(), token)
		if httpErr != nil {
			log.Println("获取配置信息异常: ", httpErr)
			continue
			//panic(httpErr)
		}
		var config ApolloConfig
		err := json.Unmarshal([]byte(content), &config)
		if err != nil {
			log.Printf("解析配置信息异常: 配置详情: %v\n %v\n", content, err)
			continue
			//panic(err)
		}
		value := config.Value
		if len(value) == 0 {
			log.Println("获取配置文件为空")
			continue
			//panic("获取配置文件为空")
		}
		saveErr := Save(config.Value, path)
		if saveErr != nil {
			log.Println("配置信息保存异常: ", saveErr)
			continue
			//panic(saveErr)
		}
		pathSum, err := getMD5SumString(path)
		if err != nil {
			continue
		}
		bakPathSum, err := getMD5SumString(bakPath)
		if err != nil {
			continue
		}
		if pathSum == bakPathSum {
			log.Println("配置文件未检测到更改")
			continue
		}
		execErr := Exec()
		if execErr != nil {
			log.Println("配置信息reload异常: ", execErr)
			log.Println("从备份文件恢复...")
			bakErr := bakOrRec(bakPath, path)
			if bakErr != nil {
				log.Printf("备份文件恢复异常: %v\n", bakErr)
			} else {
				log.Println("备份文件恢复成功")
			}
			continue
			//panic(execErr)
		}

		log.Println("更新配置文件成功")

	}

}

func bakOrRec(src, dest string) error {
	_, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("nginx配置文件不存在: %v", src)
		} else {
			return err
		}
	}
	source, openErr := os.Open(src)
	if openErr != nil {
		return openErr
	}
	defer source.Close()
	destination, createErr := os.Create(dest)
	if createErr != nil {
		return createErr
	}
	defer destination.Close()
	_, copyErr := io.Copy(destination, source)
	if copyErr != nil {
		return copyErr
	}
	return nil
}

func GetConfig(url, token string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	header := req.Header
	header.Add(AUTH, token)
	header.Add(TYPE, TypeValue)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var buffer [512]byte
	result := bytes.NewBuffer(nil)
	for {
		n, err := resp.Body.Read(buffer[0:])
		result.Write(buffer[0:n])
		if err != nil && err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(result.String())
	}
	return result.String(), nil
}

func (c AgentConfig) updateConfig() error {
	data, readErr := ioutil.ReadFile(c.NginxConfPath)
	if readErr != nil {
		log.Printf("读取配置文件(%v)异常: %v", c.NginxConfPath, readErr)
		return readErr
	}
	client := &http.Client{Timeout: 60 * time.Second}
	url := fmt.Sprintf("%v/openapi/v1/envs/%v/apps/%v/clusters/%v/namespaces/%v/items/%v?createIfNotExists=true",
		c.Ip, c.Env, c.AppId, DefaultCluster, DefaultNamespace, c.AppId)
	body := ApolloConfig{
		Key:                      c.AppId,
		Value:                    byteString(data),
		DataChangeCreatedBy:      c.CreatedBy,
		DataChangeLastModifiedBy: c.CreatedBy,
		Comment:                  NginxConf,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		log.Println("解析json异常: ", err)
		return err
	}
	uploadReq, uploadReqErr := buildRequest(http.MethodPut, url, c.Token, jsonBody)
	if uploadReqErr != nil {
		log.Println("创建同步配置请求异常: ", uploadReqErr)
		return uploadReqErr
	}
	response, doErr := client.Do(uploadReq)
	if doErr != nil {
		log.Println("请求异常: ", doErr)
		return doErr
	}
	if response.StatusCode != http.StatusOK {
		str, _ := readBody(response.Body)
		return errors.New("同步配置至Apollo失败: " + str)
	}

	releaseUrl := fmt.Sprintf("%v/openapi/v1/envs/%v/apps/%v/clusters/%v/namespaces/%v/releases",
		c.Ip, c.Env, c.AppId, DefaultCluster, DefaultNamespace)
	release := Release{
		ReleaseTitle: time.Now().Format("20060102150405") + "-release",
		ReleasedBy:   c.CreatedBy,
	}
	releaseJson, releaseErr := json.Marshal(release)
	if releaseErr != nil {
		log.Println("解析发布信息json异常: ", releaseErr)
		return releaseErr
	}
	releaseReq, releaseReqErr := buildRequest(http.MethodPost, releaseUrl, c.Token, releaseJson)
	if releaseReqErr != nil {
		log.Println("创建发布配置请求异常: ", releaseReqErr)
		return releaseReqErr
	}
	releaseDoRes, releaseDoErr := client.Do(releaseReq)
	if releaseDoErr != nil {
		log.Println("发布请求异常: ", releaseDoErr)
		return releaseDoErr
	}
	if releaseDoRes.StatusCode != http.StatusOK {
		str, _ := readBody(releaseDoRes.Body)
		return errors.New("发布配置失败: " + str)
	}
	return nil
}

func Save(content, path string) error {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			createErr := Create(path)
			if err != nil {
				return createErr
			}
		} else {
			return err
		}
	}
	writeErr := ioutil.WriteFile(path, []byte(content), os.ModePerm)
	if writeErr != nil {
		return writeErr
	}
	return nil
}

func Create(path string) error {
	newFile, err := os.Create(path)
	if err != nil {
		return err
	}
	defer newFile.Close()
	return nil
}

func getMD5SumString(path string) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		log.Printf("读取配置文件 (%v) 异常: %v\n", path, err)
		return "", err
	}
	_, _ = src.Seek(0, 0)
	srcFileSum := md5.New()
	if _, err := io.Copy(srcFileSum, src); err != nil {
		log.Printf("获取配置文件 (%v) MD5异常: %v\n", path, err)
		return "", err
	}
	return string(srcFileSum.Sum(nil)), nil
}

func Exec() error {
	cmdTest := exec.Command("nginx", "-t")
	cmdTestOut, err := cmdTest.CombinedOutput()
	cmdTestResult := byteString(cmdTestOut)
	if err != nil {
		log.Println(cmdTestResult)
		return err
	}
	//if !cmdTest.ProcessState.Success() {
	//	return errors.New("命令执行失败")
	//}
	cmdReload := exec.Command("nginx", "-s", "reload")
	cmdReloadOut, errReload := cmdReload.CombinedOutput()
	cmdReloadResult := byteString(cmdReloadOut)
	if errReload != nil {
		log.Println(cmdReloadResult)
		return errReload
	}
	return nil
}

func byteString(p []byte) string {
	for i := 0; i < len(p); i++ {
		if p[i] == 0 {
			return string(p[0:i])
		}
	}
	return string(p)
}

func (c AgentConfig) Format() string {
	// http://{portal_address}/openapi/v1/envs/{env}/apps/{appId}/clusters/{clusterName}/namespaces/{namespaceName}/items/{key}
	format := fmt.Sprintf("%v/openapi/v1/envs/%v/apps/%v/clusters/%v/namespaces/%v/items/%v",
		c.Ip, c.Env, c.AppId, DefaultCluster, DefaultNamespace, c.AppId)
	return format
}

func (c AgentConfig) validate() error {
	if len(strings.TrimSpace(c.Ip)) == 0 {
		return errors.New("未定义字段: Ip (Apollo配置服务的地址)")
	}
	if len(strings.TrimSpace(c.Env)) == 0 {
		return errors.New("未定义字段: Env (管理的配置环境)")
	}
	if len(strings.TrimSpace(c.AppId)) == 0 {
		return errors.New("未定义字段: AppId (管理的配置AppId)")
	}
	if len(strings.TrimSpace(c.Token)) == 0 {
		return errors.New("未定义字段: Token")
	}
	if len(strings.TrimSpace(c.NginxConfPath)) == 0 {
		return errors.New("未定义字段: Token (nginx配置文件路径)")
	}
	return nil
}

func buildRequest(method, url, headerAuth string, body []byte) (*http.Request, error) {
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequest(method, url, nil)
	} else {
		req, err = http.NewRequest(method, url, strings.NewReader(byteString(body)))
	}
	if err != nil {
		log.Println("创建请求异常: ", err)
		return nil, err
	}
	header := req.Header
	header.Add(AUTH, headerAuth)
	header.Add(TYPE, TypeValue)
	return req, nil
}

func readBody(body io.ReadCloser) (string, error) {
	defer body.Close()
	var buffer [512]byte
	result := bytes.NewBuffer(nil)
	for {
		n, err := body.Read(buffer[0:])
		result.Write(buffer[0:n])
		if err != nil && err == io.EOF {
			break
		} else if err != nil {
			log.Println("响应内容读取异常: ", err)
		}
	}
	return result.String(), nil
}

func init() {
	file := "./agent.log"
	logFile, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags)
}
