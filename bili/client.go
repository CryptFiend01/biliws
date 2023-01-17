package bili

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"github.com/wonderivan/logger"
)

type BiliWsClientConfig struct {
	Name     string
	Host     string
	Port     int64
	RoomId   int64
	AuthBody string
	Ws       *websocket.Conn
}

type BiliWsClient struct {
	*websocket.Conn
	conf       *BiliWsClientConfig
	dispather  *protoDispather
	decoder    *DecodeManager
	bufPool    sync.Pool // TODO
	sequenceId int32
	CloseFlag  chan struct{}
	authed     bool
	retryCnt   int64
	disconnecd bool
	msgBuf     chan *Proto
	isClose    bool
}

func MakeAuth(roomId int64) string {
	return fmt.Sprintf(`{"uid":0, "roomid":%d, "protover":1, "platform":"web", "clientver": "1.4.0"}`, roomId)
}

func NewBiliWsClient(conf *BiliWsClientConfig) *BiliWsClient {
	if conf == nil {
		panic("[BiliWsClient | NewBiliWsClient] conf == nil")
	}
	c := &BiliWsClient{
		conf:      conf,
		dispather: newMessageDispather(),
		CloseFlag: make(chan struct{}),
		decoder:   NewDecodeManager(),
		msgBuf:    make(chan *Proto, 1024),
		isClose:   false,
	}
	var err error
	wsAddr := fmt.Sprintf("wss://%s:%d/sub", c.conf.Host, c.conf.Port)
	c.Conn, _, err = websocket.DefaultDialer.Dial(wsAddr, nil)
	if err != nil {
		logger.Fatal("[BiliWsClient | NewBiliWsClient] connect err")
		return nil
	}
	logger.Info("[BiliWsClient | NewBiliWsClient] connect success")

	c.registerProtoHandler(OP_AUTH_REPLY, c.authResp)
	c.registerProtoHandler(OP_HEARTBEAT_REPLY, c.heartBeatResp)
	c.registerProtoHandler(OP_SEND_SMS_REPLY, c.msgResp)

	err = c.sendAuth(c.conf.AuthBody)
	if err != nil {
		logger.Fatal("[BiliWsClient | NewBiliWsClient] sendAuth err:", err)
		return nil
	}
	return c
}

func (c *BiliWsClient) Run() {
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.doReadLoop()
	}()
	go func() {
		defer wg.Done()
		c.doEventLoop()
	}()
	wg.Wait()
}

func (c *BiliWsClient) sendAuth(authBody string) (err error) {
	p := &Proto{
		Operation: OP_AUTH,
		Body:      []byte(authBody),
	}
	return c.sendMsg(p)
}

func (c *BiliWsClient) sendHeartBeat() {
	if !c.authed {
		return
	}
	msg := &Proto{}
	msg.Operation = OP_HEARTBEAT
	msg.SequenceId = c.sequenceId
	c.sequenceId++
	err := c.sendMsg(msg)
	if err != nil {
		logger.Fatal("[BiliWsClient | sendHeartBeat] err", err)
		return
	}
	logger.Info("[BiliWsClient | sendHeartBeat] seq:", msg.SequenceId)
}

func (c *BiliWsClient) registerProtoHandler(cmd int32, logic protoLogic) {
	c.dispather.register(cmd, logic)
}

func (c *BiliWsClient) Close() {
	c.Conn.Close()
	c.isClose = true
}

func (c *BiliWsClient) sendMsg(msg *Proto) (err error) {
	dataBuff := &bytes.Buffer{}
	packLen := int32(RawHeaderSize + len(msg.Body))
	msg.HeaderLength = RawHeaderSize
	binary.Write(dataBuff, binary.BigEndian, packLen)
	binary.Write(dataBuff, binary.BigEndian, int16(RawHeaderSize))
	binary.Write(dataBuff, binary.BigEndian, msg.Version)
	binary.Write(dataBuff, binary.BigEndian, msg.Operation)
	binary.Write(dataBuff, binary.BigEndian, msg.SequenceId)
	binary.Write(dataBuff, binary.BigEndian, msg.Body)
	err = c.Conn.WriteMessage(websocket.BinaryMessage, dataBuff.Bytes())
	if err != nil {
		err = errors.Wrapf(err, "[BiliWsClient | SendMsg] WriteMessage err")
		return
	}
	return
}

func (c *BiliWsClient) readMsg() {
	retProto := &Proto{}
	mt, buf, err := c.Conn.ReadMessage()
	if err != nil {
		retProto.ErrMsg = errors.Wrapf(err, "[BiliWsClient | ReadMsg] conn err")
		return
	}
	if len(buf) < RawHeaderSize {
		retProto.ErrMsg = errors.Wrapf(err, "[BiliWsClient | ReadMsg] buf:%d less", len(buf))
		return
	}
	retProto.PacketLength = int32(binary.BigEndian.Uint32(buf[PackOffset:HeaderOffset]))
	retProto.HeaderLength = int16(binary.BigEndian.Uint16(buf[HeaderOffset:VerOffset]))
	retProto.Version = int16(binary.BigEndian.Uint16(buf[VerOffset:OperationOffset]))
	retProto.Operation = int32(binary.BigEndian.Uint32(buf[OperationOffset:SeqIdOffset]))
	retProto.SequenceId = int32(binary.BigEndian.Uint32(buf[SeqIdOffset:]))
	if retProto.PacketLength < 0 || retProto.PacketLength > MaxPackSize {
		retProto.ErrMsg = errors.Wrapf(err, "[BiliWsClient | ReadMsg] PacketLength:%d err", retProto.PacketLength)
		return
	}
	if retProto.HeaderLength != RawHeaderSize {
		retProto.ErrMsg = errors.Wrapf(err, "[BiliWsClient | ReadMsg] HeaderLength:%d err", retProto.PacketLength)
		return
	}
	if bodyLen := int(retProto.PacketLength - int32(retProto.HeaderLength)); bodyLen > 0 {
		retProto.Body = buf[retProto.HeaderLength:retProto.PacketLength]
	} else {
		retProto.ErrMsg = errors.Wrapf(err, "[BiliWsClient | ReadMsg] BodyLength:%d err", bodyLen)
		return
	}
	retProto.BodyMuti, err = c.decoder.Decode(int64(retProto.Version), retProto.Body)
	if len(retProto.BodyMuti) > 0 {
		retProto.Body = retProto.BodyMuti[0]
	}
	c.msgBuf <- retProto

	c.conf.Ws.WriteMessage(mt, buf)
}

func (c *BiliWsClient) doEventLoop() {
	ticker := time.NewTicker(time.Second * 5)
	for {
		select {
		case p := <-c.msgBuf:
			if p == nil {
				logger.Fatal("[BiliWsClient | ReadMsg] p == nil")
				continue
			}
			if p.ErrMsg != nil {
				logger.Fatal("[BiliWsClient | ReadMsg] err:", p.ErrMsg)
				continue
			}
			err := c.dispather.do(p)
			if err != nil {
				logger.Fatal("[BiliWsClient | ReadMsg] dispather err:", err)
				continue
			}
		case <-c.CloseFlag:
			logger.Debug("client close, disconnect danmu")
			goto exit
		case <-ticker.C:
			c.sendHeartBeat()
		}
	}
exit:
	c.Close()
}

func (c *BiliWsClient) doReadLoop() {
	for !c.isClose {
		c.readMsg()
	}
}

func (c *BiliWsClient) authResp(msg *Proto) (err error) {
	resp := &AuthRespParam{}
	err = json.Unmarshal(msg.Body, resp)
	if err != nil {
		err = errors.Wrapf(err, "[BiliWsClient | AuthResp] Unmarshal err")
		return
	}
	if resp.Code != 0 {
		err = fmt.Errorf("[BiliWsClient | AuthResp] code:%d", resp.Code)
		return
	}
	c.authed = true
	logger.Info("[BiliWsClient | AuthResp] auth success")
	return
}

func (c *BiliWsClient) heartBeatResp(msg *Proto) (err error) {
	logger.Info("[BiliWsClient | HeartBeatResp] recv HeartBeat resp", msg.Body)
	return
}

//MsgResp 可以这里做回调
func (c *BiliWsClient) msgResp(msg *Proto) (err error) {
	for index, cmd := range msg.BodyMuti {
		logger.Info("[BiliWsClient | HeartBeatResp] recv MsgResp index:%d ver:%d cmd:%s", index, msg.Version, string(cmd))
	}
	return
}

type protoLogic func(p *Proto) (err error)

type protoDispather struct {
	dispather map[int32]protoLogic
}

func newMessageDispather() *protoDispather {
	return &protoDispather{
		dispather: map[int32]protoLogic{},
	}
}

func (m *protoDispather) register(Op int32, f protoLogic) {
	if m.dispather[Op] != nil {
		panic(fmt.Sprintf("[MessageDispather | Register] Op:%d repeated", Op))
	}
	m.dispather[Op] = f
}

func (m *protoDispather) do(p *Proto) (err error) {
	f, exist := m.dispather[p.Operation]
	if exist {
		fmt.Println("proto:", p.Version)
		err = f(p)
		if err != nil {
			errors.Wrapf(err, "[MessageDispather | Do] process err")
		}
		return
	}
	return fmt.Errorf("[MessageDispather | Do] Op:%d not found", p.Operation)
}
