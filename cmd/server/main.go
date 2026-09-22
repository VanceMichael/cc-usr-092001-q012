package main

import (
	"log"
	"net/http"
	"os"

	"example.com/batch-092001-q012/internal/api"
	"example.com/batch-092001-q012/internal/service"
	"example.com/batch-092001-q012/internal/store"
)

func main() {
	dataDir := os.Getenv("DATABASE_PATH")
	if dataDir == "" {
		dataDir = "./data"
	}
	st, err := store.Open(dataDir)
	if err != nil {
		log.Fatalf("打开数据目录失败: %v", err)
	}
	svc := service.New(st, nil)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("接入网关监听 :%s，数据目录 %s", port, dataDir)
	if err := http.ListenAndServe("0.0.0.0:"+port, api.NewMux(svc)); err != nil {
		log.Fatal(err)
	}
}
