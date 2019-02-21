package main

import (
	"encoding/binary"
	"github.com/vmihailenco/msgpack"
	"log"
	"net"
	"sync"
)

type fleximd struct {
	Online      FleximRoster
	BindAddress string
	mutex       *sync.Mutex
	conn        net.Conn
}

func (o *fleximd) Init() {
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
