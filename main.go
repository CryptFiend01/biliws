package main

import (
	"bili-ws/bili"
	"bili-ws/gate"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/wonderivan/logger"
)

const (
	TestRoomId   = 26762232
	TestAkId     = "ZZRlJ5bblpN0EZnU2N6DjkMr"
	TestAkSecret = "cDsGfLfVNr4a6oAnTBsx51Qmjr2TaO"
	TestHttpHost = "http://test-live-open.biliapi.net"
	// TestHttpHost = "https://openplatform.biliapi.com"
)

func test() {
	bili.HttpHost = TestHttpHost
	// host, port, authBody, err := bili.GetWebsocketInfo(TestRoomId, TestAkId, TestAkSecret)
	// if err != nil {
	// 	log.Fatalln("[main | GetWsInfo] err", err)
	// 	return
	// }
	// fmt.Println(host, port, authBody)
	host, port, authBody := "broadcastlv.chat.bilibili.com", int64(2245), bili.MakeAuth(TestRoomId)
	c := bili.NewBiliWsClient(&bili.BiliWsClientConfig{
		Host:     host,
		Port:     port,
		AuthBody: authBody,
		RoomId:   TestRoomId,
	})
	if c == nil {
		log.Fatalln("[main | NewBiliWsClient] client init err")
		return
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		c.Run()
	}()
	wg.Wait()
	log.Println("[main | NewBiliWsClient] exit.....")
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	logger.SetLogger("logger.json")

	gate.Init()

	for {
		time.Sleep(time.Second * 30)
	}
}
