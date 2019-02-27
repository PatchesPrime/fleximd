package main

import (
	"encoding/binary"
	"github.com/go-redis/redis"
	"github.com/vmihailenco/msgpack"
	"log"
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
}

func (o *fleximd) Init() {
	// Set our DB.
	o.db = redis.NewClient(&redis.Options{
		Addr:     "192.168.1.14:32768",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	ln, err := net.Listen("tcp", ":4321")
	if err != nil {
		log.Println("Couldn't opening listening socket: ", err)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("Couldn't accept connection: ", err)
		}

		log.Println("DEBUG| <-", conn.RemoteAddr())
		o.conn = conn
		go handleConnection(conn)
	}
}

func (o *fleximd) Shutdown() {
	var cursor uint64
	for {
		keys, cursor, err := srv.db.Scan(cursor, "fleximd:sessions:*", 10).Result()
		if err != nil {
			log.Println("DEBUG| Couldn't get scan of redis:", err)
		}

		for _, k := range keys {
			_, err := srv.db.Del(k).Result()
			if err != nil {
				log.Fatal("Couldn't get redis key:", err)
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
	// Go ahead and attempt to marshal the datum
	out, err := msgpack.Marshal(d)
	if err != nil {
		log.Println("Couldn't marshal datum: ", err)
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
}

func (o *FleximRoster) Exists(n string) (User, bool) {
	srv.mutex.Lock()
	defer srv.mutex.Unlock()
	user, ok := srv.Online[n]
	return user, ok
}
