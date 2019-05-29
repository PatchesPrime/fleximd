package main

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"sync"

	"github.com/vmihailenco/msgpack"
)

type Datum interface {
	BuildHeaders() ([]byte, error)
}

type Auth struct {
	Date      int64  `msgpack:"date"`
	Challenge string `msgpack:"challenge"`
	LastSeen  int64  `msgpack:"last_seen"`
}

type AuthResponse struct {
	Challenge string `msgpack:"challenge"`
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
	Status  int8   `msgpack:"status"`
	Payload string `msgpack:"payload"`
}

type User struct {
	Aliases   []string `msgpack:"aliases"`
	Key       []byte   `msgpack:"key"`
	Last_seen int64    `msgpack:"last_seen"`
	authed    bool     // This is used internally to track that state.
	name      string   // TODO: REMOVE. Used for testing until we discuss how to treat non authed.
	conn      net.Conn
	challenge string
	safety    *sync.Mutex
}

func (o *User) Cleanup() {
	srv.Online.Delete(*o)
}

func (o *User) HexifyKey() string {
	return hex.EncodeToString(o.Key)
}

func (o *User) Respond(t datum, d interface{}) {
	o.safety.Lock()
	defer o.safety.Unlock()
	srv.logger.Debugf("SENDING RESPONSE (TYPE: %d): %+v", t, d)
	// Go ahead and attempt to marshal the datum
	out, err := msgpack.Marshal(d)
	if err != nil {
		srv.logger.Error("Couldn't marshal datum: ", err)
	}
	// All datum transmissions begin with metadata
	metadata := make([]byte, 3)
	metadata[0] = byte(t)

	// We need a uint16 for the last 2 bytes of the metadata.
	binary.BigEndian.PutUint16(metadata[1:], uint16(len(out)))
	o.conn.Write(append(metadata, out...))
}
