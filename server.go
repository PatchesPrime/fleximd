package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
)

type fleximd struct {
	Online      FleximRoster
	BindAddress string
	mutex       *sync.Mutex
	conn        net.Conn
	db          *redis.Client
	logger      *logrus.Logger
}

func (o *fleximd) Init(redisClient *redis.Client) {
	o.db = redisClient
	ln, err := net.Listen("tcp", o.BindAddress)
	if err != nil {
		o.logger.Error("Couldn't opening listening socket: ", err)
	}

	o.logger.Info("fleximd started!")
	for {
		o.logger.Debug("Listening..")
		conn, err := ln.Accept()
		if err != nil {
			o.logger.Error("Couldn't accept connection: ", err)
		}

		o.logger.Debug("CLIENT: ", conn.RemoteAddr())
		o.conn = conn
		go handleConnection(conn)
	}
}

func (o *fleximd) Shutdown() {
	var cursor uint64
	for {
		keys, cursor, err := srv.db.Scan(cursor, "fleximd:sessions:*", 10).Result()
		if err != nil {
			o.logger.Debug("Couldn't get scan of redis:", err)
		}

		for _, k := range keys {
			_, err := srv.db.Del(k).Result()
			if err != nil {
				o.logger.Fatal("Couldn't get redis key:", err)
			}
		}

		// End when we're done.
		if cursor == 0 {
			break
		}
	}
	os.Exit(0)
}

func (o *fleximd) BuildAuth(n int) Auth {
	c := make([]byte, n)
	_, err := rand.Read(c)
	if err != nil {
		log.Fatal("Couldn't generate random bytes")
	}
	challenge := Auth{
		Date:      time.Now().Unix(),
		Challenge: hex.EncodeToString(c),
	}
	return challenge
}

type FleximRoster map[string]User

func (o *FleximRoster) Append(u User) {
	srv.mutex.Lock()
	defer srv.mutex.Unlock()
	srv.Online[u.HexifyKey()] = u
}
func (o *FleximRoster) Update(u User) {
	if _, ok := o.Exists(u.HexifyKey()); ok {
		srv.mutex.Lock()
		defer srv.mutex.Unlock()
		srv.Online[u.HexifyKey()] = u
	} else {
		o.Append(u)
	}
}
func (o *FleximRoster) Delete(u User) {
	srv.mutex.Lock()
	defer srv.mutex.Unlock()
	delete(srv.Online, u.HexifyKey())

	// Notify the users
	go func() {
		for _, user := range srv.Online {
			user.Respond(eStatus, Status{Payload: u.HexifyKey(), Status: -10})
		}
	}()
}

func (o *FleximRoster) Exists(n string) (User, bool) {
	srv.mutex.Lock()
	defer srv.mutex.Unlock()
	user, ok := srv.Online[n]
	return user, ok
}
