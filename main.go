package main

import (
	"encoding/binary"
	"encoding/hex"
	"github.com/vmihailenco/msgpack"
	"log"
	"net"
)

const helo = "\xA4FLEX"

type datum int

// My version of a golang enum.
const (
	eAuth datum = iota
	eAuthResponse
	eCommand
	eMessage
	eRoster
	eUser
	eStatus
)

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
	delete(onlineUsers, o.name)
}

var onlineUsers map[string]User

func main() {
	onlineUsers = make(map[string]User)
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
		go handleConnection(conn)
	}
}

func handleConnection(c net.Conn) {
	defer c.Close()
	protocheck := make([]byte, 5)
	// header, datum, len := b[:5], b[5], binary.BigEndian.Uint16(b[6:8])
	_, err := c.Read(protocheck)
	if err != nil {
		log.Println("Couldn't read bytes for header..", err)
		return
	}

	if string(protocheck) != helo {
		log.Println("DEBUG| BAD HEADER:", string(protocheck))
		resp := Status{Status: -1, Payload: "invalid flexim header"}
		out, err := msgpack.Marshal(resp)
		if err != nil {
			log.Println("Couldn't marshal bad header response.")
		}
		c.Write([]byte{byte(eStatus), byte(len(out))})
		c.Write(out)
		return
	}

	// TODO: Should this be a pointer rather than value for conn?
	self := User{name: "guest#" + hex.EncodeToString([]byte(c.RemoteAddr().String())), conn: c} // hahahah
	defer self.Cleanup()
	// DatumProcessing:
	for {
		headers := make([]byte, 3)
		_, err := c.Read(headers)
		if err != nil {
			log.Println("DEBUG| ->", c.RemoteAddr())
			return
		}

		// Get datum information from headers.
		dType, dLength := datum(headers[0]), binary.BigEndian.Uint16(headers[1:])

		// Extract Datum.
		datum := make([]byte, dLength)
		_, err = c.Read(datum)
		if err != nil {
			log.Println("Couldn't fill byte buffer, possibly disconnect?")
			break
		}

		switch dType {
		case eCommand:
			var cmd Command
			err = msgpack.Unmarshal(datum, &cmd)
			if err != nil {
				log.Println("Error processing command datum: ", err)
			}
			switch cmd.Cmd {
			case "AUTH":
				// Clearly we need to handle authentication, it's in the TODO
				// For now, just let it go through with nothing in play until we
				// plug in encryption.
				if cmd.Payload[0] != "guest" {
					self.authed = true
					self.name = cmd.Payload[0] // TODO: Use [1] for name
					self.Key, err = hex.DecodeString(cmd.Payload[0])
					if err != nil {
						log.Println("Couldn't decode hex of", cmd.Payload[0])
					}
				}

				onlineUsers[self.name] = self
			case "ROSTER":
				var roster []User
				for _, v := range onlineUsers {
					roster = append(roster, v)
				}
				payload, err := msgpack.Marshal(roster)
				if err != nil {
					log.Println("Couldn't marshal roster:", err)
				}
				size := uint16(len(payload))
				c.Write([]byte{byte(eRoster), byte(size)})
				c.Write(payload)
			case "REGISTER":
				// TODO: replace with not a dummy. This currently only
				// lasts as long as their connection.
				self.name = cmd.Payload[0]
			}
		case eMessage:
			var msg Message
			err = msgpack.Unmarshal(datum, &msg)
			if err != nil {
				log.Println("Error processing command datum: ", err)
			}

			if user, ok := onlineUsers[msg.To]; ok {
				// Send it as we get it vs remarshalling
				// TODO: Make this not silly.
				t := make([]byte, 3)
				t[0] = byte(dType)
				binary.BigEndian.PutUint16(t[1:], dLength)
				user.conn.Write(t)
				user.conn.Write(datum)

			} else {
				status := Status{Payload: "user not available", Status: -1}
				out, err := msgpack.Marshal(status)
				if err != nil {
					log.Println("Couldn't marhsal status for unavailable user")
				}
				c.Write([]byte{byte(eStatus), byte(len(out))})
				c.Write(out)
			}
		}

	}
}
