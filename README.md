一个测试的小功能，使用go语言实现。
定时同步Nginx配置到apollo、从apollo拉取配置更新到Nginx

1、linux环境下通过 `go build agent.go`下生成编译文件 agent  
2、上传至nginx所在服务器，通过执行`chmod -R 777 agent` 授权脚本执行权限  
3、修改`config.json`文件内容，为必填项  
4、执行 `./agent &` 命令，后台运行脚本  
5、日志信息可通过读取同目录下的`agent.log`文件