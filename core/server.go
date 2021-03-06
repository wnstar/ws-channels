package core

import (
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"sync"
	"time"
	"ws-channels/common"
	"ws-channels/config"
	"ws-channels/layer/redis"
)

type Server struct {
	Layer                common.LayerInterface
	Clients              map[string]*Client
	OnConnect            func(resp http.ResponseWriter, req *http.Request, client *Client, next func(channelName string) error)
	OnDisconnect         func(code int, reason string, client *Client)
	OnMessage            func(messageType int, data []byte, From int, client *Client)
	Ctx                  context.Context
	receiverLayerMessage chan common.ReceiverLayerMessage
	receiverGroupMessage chan common.ReceiverLayerMessage
	upgrader             websocket.Upgrader
	localLayer           MemoryLayer
}

func NewServer(
	c *config.Config,
	ctx context.Context,
	onConnect func(resp http.ResponseWriter, req *http.Request, client *Client, next func(channelName string) error),
	OnDisconnect func(code int, reason string, client *Client),
	onMessage func(messageType int, data []byte, From int, client *Client),
) *Server {
	receiverMessage := make(chan common.ReceiverLayerMessage, 500)

	server := &Server{
		Clients:              make(map[string]*Client),
		OnConnect:            onConnect,
		OnDisconnect:         OnDisconnect,
		OnMessage:            onMessage,
		Ctx:                  ctx,
		receiverLayerMessage: receiverMessage,
		upgrader:             DefaultUpgrader,
		localLayer:           MemoryLayer{groups: new(sync.Map)},
	}
	switch c.Layer {
	case config.RedisLayer:
		server.Layer = redis.NewLayer(receiverMessage, c.RedisConfig)
	default:
		return nil
	}

	return server
}

func (s *Server) SetUpgrade(Upgrader websocket.Upgrader) {
	s.upgrader = Upgrader
}

func (s *Server) Handler(resp http.ResponseWriter, req *http.Request) {

	ctx, cancel := context.WithCancel(s.Ctx)

	client := &Client{
		Channel:  "",
		Req:      req,
		cancel:   cancel,
		inChan:   nil,
		outChan:  nil,
		wsSocket: nil,
		server:   s,
	}

	next := func(channelName string) error {
		if channelName == "" {
			client.Channel = s.Layer.NewChannel(channelName)
		} else {
			client.Channel = s.Layer.NewChannel("")
		}
		wsSocket, err := s.upgrader.Upgrade(resp, req, nil)
		if err != nil {
			log.Println("升级为websocket失败", err.Error())
			return err
		}
		wsSocket.SetCloseHandler(func(code int, reason string) error {
			if client.isClose == false {
				client.isClose = true
				client.cancel()
				_ = wsSocket.Close()
				if s.OnDisconnect != nil {
					s.OnDisconnect(code, reason, client)
				}
				if _, ok := s.Clients[client.Channel]; ok {
					delete(s.Clients, client.Channel)
				}
			}
			return nil
		})
		client.wsSocket = wsSocket
		client.inChan = make(chan string, 1000)
		client.outChan = make(chan common.Message, 1000)
		s.Clients[client.Channel] = client
		go client.readLoop(ctx)
		go client.writeLoop(ctx)

		return err
	}

	if s.OnConnect != nil {
		s.OnConnect(resp, req, client, next)
	} else {
		_ = next("")
	}

}

func (s Server) sendToChannel(channel string, msg common.ReceiverLayerMessage) error {
	if client, ok := s.Clients[channel]; ok {
		s.OnMessage(msg.Message.MessageType, msg.Message.Data, FromServer, client)

	}
	return nil
}

func (s Server) receiverLayerTask(ctx context.Context) {
	for {
		select {
		case msg := <-s.receiverLayerMessage:
			if len(msg.Channels) > 0 {
				for _, channel := range msg.Channels {
					if err := s.sendToChannel(channel, msg); err != nil {
						fmt.Println(err)
					}

				}
			}
			if len(msg.Groups) > 0 {
				// todo
				panic("not implement")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s Server) Run() {
	go s.receiverLayerTask(s.Ctx)
	go s.Layer.Run(s.Ctx)
}

func (c *Client) Close(code int, reason string) {
	if c.isClose {
		return
	}
	c.wsSocket.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(3*time.Second))
}

func (s Server) Send(messageType int, data []byte, channels ...string) error {
	return s.Layer.Send(common.Message{MessageType: messageType, Data: data,}, channels...)
}
func (s Server) GroupAdd(channel string, groups ...string) error {
	if err := s.Layer.GroupAdd(channel, groups...); err != nil {
		return err
	}
	s.localLayer.GroupAdd(channel, groups...)
	return nil
}
func (s Server) GroupDiscard(channel string, groups ...string) error {
	if err := s.Layer.GroupDiscard(channel, groups...); err != nil {
		return err
	}
	s.localLayer.GroupDiscard(channel, groups...)
	return nil

}
func (s Server) GroupSend(messageType int, data []byte, groups ...string) error {
	return s.Layer.GroupSend(common.Message{
		MessageType: messageType,
		Data:        data,
	}, groups...)
}
