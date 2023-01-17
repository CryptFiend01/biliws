package gate

import (
	"bili-ws/bili"
	"container/list"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/wonderivan/logger"
)

type ClientMsg struct {
	RoomId int64 `json:"room_id"`
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	connections = map[int]*websocket.Conn{}
	connIds     = list.New()
	lock        sync.Mutex
)

func Check(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "success")
}

func Session(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Upgrade error: %v", err)
		return
	}

	// 为每个连接分配一个连接号
	lock.Lock()
	if connIds.Len() == 0 {
		logger.Warn("Connection is all used.")
		lock.Unlock()
		ws.Close()
		return
	}
	e := connIds.Front()
	connIds.Remove(e)
	connectId := e.Value.(int)
	connections[connectId] = ws
	lock.Unlock()

	logger.Debug("%d connect.", connectId)

	errCh := make(chan bool)
	go func() {
		isFirst := true
		var c *bili.BiliWsClient
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				logger.Error("ReadMessage error: %v", err)
				errCh <- true
				if c != nil {
					c.CloseFlag <- struct{}{}
				}
				return
			}

			if !isFirst {
				continue
			}

			if mt == websocket.TextMessage {
				logger.Debug("recv message: %s", string(data))
				var msg ClientMsg
				err := json.Unmarshal(data, &msg)
				if err != nil {
					logger.Debug("parse client msg error: %v", err)
					errCh <- true
					return
				}

				host, port, authBody := "broadcastlv.chat.bilibili.com", int64(2245), bili.MakeAuth(msg.RoomId)
				c = bili.NewBiliWsClient(&bili.BiliWsClientConfig{
					Host:     host,
					Port:     port,
					AuthBody: authBody,
					RoomId:   msg.RoomId,
					Ws:       ws,
				})

				go func() {
					c.Run()
				}()

				isFirst = false
			} else {
				logger.Debug(`client message only one {"room_id":xxxxx}`)
				errCh <- true
				return
			}
		}
	}()

	<-errCh
	close(errCh)

	logger.Debug("close connect %d", connectId)
	lock.Lock()
	delete(connections, connectId)
	connIds.PushBack(connectId)
	lock.Unlock()
	ws.Close()
}

func Init() {
	for i := 1; i <= 60000; i++ {
		connIds.PushBack(i)
	}

	http.HandleFunc("/", Session)
	http.ListenAndServe("0.0.0.0:9009", nil)
}
