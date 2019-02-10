package main

import (
	"net"
)

type Datum interface {
	BuildHeaders() ([]byte, error)
}

type Command struct {
	Cmd     string   `msgpack:"cmd"`
	Payload []string `msgpack:"payload"`
}

type Message struct {
	To    string   `msgpack:"to"`
	From  string   `msgpack:"from"`
	Flags []string `msgpack:"flags"`
	Date  int64    `msgpack:"date"`
	Msg   string   `msgpack:"msg"`
}

type Status struct {
	Status  int8
	Payload string
}

type User struct {
	Aliases   []string `msgpack:"Aliases"`
	Key       []byte   `msgpack:"key"`
	Last_seen int64    `msgpack:"Last_seen"`
	authed    bool     // This is used internally to track that state.
	name      string   // TODO: REMOVE. Used for testing until we discuss how to treat non authed.
	conn      net.Conn
}

func (o *User) Cleanup() {
	srv.Online.Delete(*o)
}
