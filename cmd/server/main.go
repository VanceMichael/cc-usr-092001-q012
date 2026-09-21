package main

import (
	"log"
	"net/http"
	"os"

	"example.com/batch-092001-q012/internal/api"
)

func main() {
	token := os.Getenv("GATEWAY_TOKEN")
	if token == "" {
		log.Println("警告：未设置 GATEWAY_TOKEN，联合体管理接口将不可用")
	}
	server := api.NewServer(token, nil)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("畜禽表型标准接入网关监听 :%s", port)
	if err := http.ListenAndServe("0.0.0.0:"+port, server.Handler()); err != nil {
		panic(err)
	}
}
