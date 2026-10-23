/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-07-12 09:48:56
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-09-02 22:10:05
 * @FilePath: /CasaOS/service/service.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"errors"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/gorilla/websocket"
	"github.com/patrickmn/go-cache"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var Cache *cache.Cache

var (
	MyService Repository
)

var (
	WebSocketConns []*websocket.Conn
	SocketRun      bool
)

type Repository interface {
	Casa() CasaService
	Connections() ConnectionsService
	Gateway() external.ManagementService
	Health() HealthService
	Notify() NotifyServer
	Rely() RelyService
	Shares() SharesService
	System() SystemService
	Storage() StorageService
	MessageBus() *message_bus.ClientWithResponses
	Peer() PeerService
	Other() OtherService
}

func NewService(db *gorm.DB, RuntimePath string) Repository {
	gatewayManagement, err := external.NewManagementService(RuntimePath)
	if err != nil && len(RuntimePath) > 0 {
		// the gateway restarts, or is late at boot: wait for it rather than die
		err = RetryGateway(func() (err error) {
			gatewayManagement, err = external.NewManagementService(RuntimePath)
			return err
		})
		if err != nil {
			panic(err)
		}
	}

	return &store{
		casa:        NewCasaService(),
		connections: NewConnectionsService(db),
		gateway:     gatewayManagement,
		notify:      NewNotifyService(db),
		rely:        NewRelyService(db),
		system:      NewSystemService(),
		health:      NewHealthService(),
		shares:      NewSharesService(db),
		storage:     NewStorageService(),
		other:       NewOtherService(),

		peer: NewPeerService(db),
	}
}

type store struct {
	peer        PeerService
	db          *gorm.DB
	casa        CasaService
	notify      NotifyServer
	rely        RelyService
	system      SystemService
	shares      SharesService
	connections ConnectionsService
	gateway     external.ManagementService
	storage     StorageService
	health      HealthService
	other       OtherService
}

func (c *store) Storage() StorageService {
	return c.storage
}

func (c *store) Peer() PeerService {
	return c.peer
}

func (c *store) Other() OtherService {
	return c.other
}

func (c *store) Gateway() external.ManagementService {
	return c.gateway
}

func (s *store) Connections() ConnectionsService {
	return s.connections
}

func (s *store) Shares() SharesService {
	return s.shares
}

func (c *store) Rely() RelyService {
	return c.rely
}

func (c *store) System() SystemService {
	return c.system
}

func (c *store) Notify() NotifyServer {
	return c.notify
}

func (c *store) Casa() CasaService {
	return c.casa
}

func (c *store) Health() HealthService {
	return c.health
}

func (c *store) MessageBus() *message_bus.ClientWithResponses {
	client, _ := message_bus.NewClientWithResponses("", func(c *message_bus.Client) error {
		// error will never be returned, as we always want to return a client, even with wrong address,
		// in order to avoid panic.
		//
		// If we don't avoid panic, message bus becomes a hard dependency, which is not what we want.

		messageBusAddress, err := external.GetMessageBusAddress(config.CommonInfo.RuntimePath)
		if err != nil {
			c.Server = "message bus address not found"
			return nil
		}

		c.Server = messageBusAddress
		return nil
	}, message_bus.WithRequestEditorFn(external.InternalRequestEditor(config.CommonInfo.RuntimePath)))

	return client
}

// startWait is how long after the process started the core waits, at start, for the gateway (its first
// look, its routes) and the message bus (the event types), all of them together.
const startWait = 2 * time.Minute

// StartDeadline is when those waits end: nothing starts after it, so that the start stays inside the
// unit's TimeoutStartSec (service_test.go adds up what may still end after it). A var, so that tests
// can move it.
var StartDeadline = time.Now().Add(startWait)

// RetryGateway calls try until it succeeds, waiting 1 s, then twice as long each time up to 15 s, and
// gives up with try's last error rather than begin a try after StartDeadline; then systemd restarts the
// core.
func RetryGateway(try func() error) error {
	err := errors.New("the start's deadline for the gateway has passed")
	for wait := time.Second; time.Now().Before(StartDeadline); wait = min(2*wait, 15*time.Second) {
		if err = try(); err == nil || time.Now().Add(wait).After(StartDeadline) {
			return err
		}
		logger.Info("the gateway does not answer, trying again", zap.Error(err), zap.Duration("in", wait))
		time.Sleep(wait)
	}
	return err
}
