package main

import (
	"encoding/binary"
	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
	"net"
	"os"
	"sync"
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

// TODO: Remove super genric "interface{}" for something more specific.
func (o *fleximd) Respond(t datum, d interface{}) {
	o.logger.Debugf("SENDING RESPONSE (TYPE:%s): %+v", t, d)
	// Go ahead and attempt to marshal the datum
	out, err := msgpack.Marshal(d)
	if err != nil {
		o.logger.Error("Couldn't marshal datum: ", err)
	}
	// All datum transmissions begin with metadata
	metadata := make([]byte, 3)
	metadata[0] = byte(t)

	// We need a uint16 for the last 2 bytes of the metadata.
	binary.BigEndian.PutUint16(metadata[1:], uint16(len(out)))
	o.conn.Write(metadata)
	o.conn.Write(out)
}

// TODO: We need to make sure the string is case insensitive.
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
	for _, user := range srv.Online {
		status, err := msgpack.Marshal(Status{Payload: u.HexifyKey(), Status: -10})
		if err != nil {
			srv.logger.Error("Couldn't marshal status for roster update: ", err)
		}
		user.conn.Write(BuildHeaders(eStatus, len(status)))
		user.conn.Write(status)
	}
}

func (o *FleximRoster) Exists(n string) (User, bool) {
	srv.mutex.Lock()
	defer srv.mutex.Unlock()
	user, ok := srv.Online[n]
	return user, ok
}
