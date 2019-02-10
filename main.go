package main

import (
	"encoding/binary"
	"encoding/hex"
	"github.com/vmihailenco/msgpack"
	"io"
	"log"
	"net"
	"sync"
)

const helo = "\xA4FLEX"

var mutex = &sync.Mutex{}
var srv = fleximd{Online: make(FleximRoster), BindAddress: ":4321"}

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

type fleximd struct {
	Online      FleximRoster
	BindAddress string
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
		go handleConnection(conn)
	}
}

// TODO: We need to make sure the string is case insensitive.
type FleximRoster map[string]User

func (o *FleximRoster) Append(u User) {
	mutex.Lock()
	defer mutex.Unlock()
	srv.Online[u.HexifyKey()] = u
}
func (o *FleximRoster) Update(u User) {
	if _, ok := o.Exists(u.HexifyKey()); ok {
		mutex.Lock()
		defer mutex.Unlock()
		srv.Online[u.HexifyKey()] = u
	} else {
		o.Append(u)
	}
}
func (o *FleximRoster) Delete(u User) {
	mutex.Lock()
	defer mutex.Unlock()
	delete(srv.Online, u.HexifyKey())
}
func (o *FleximRoster) Exists(n string) (User, bool) {
	mutex.Lock()
	defer mutex.Unlock()
	user, ok := srv.Online[n]
	return user, ok
}

func BuildHeaders(d datum, l int) []byte {
	// We need a 3 byte array
	out := make([]byte, 3)

	// The first one is always the datum type, follow by 2 bytes representing uint16
	out[0] = byte(d)
	binary.BigEndian.PutUint16(out[1:], uint16(l))

	return out
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
		c.Write(BuildHeaders(eStatus, len(out)))
		c.Write(out)
		return
	}

	// TODO: Should this be a pointer rather than value for conn?
	self := User{name: "guest#" + hex.EncodeToString([]byte(c.RemoteAddr().String())), conn: c} // hahahah
	defer self.Cleanup()
	// DatumProcessing:
	for {
		// log.Println("DEBUG|", srv.Online)
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
		_, err = io.ReadFull(c, datum)
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
				// log.Printf("DEBUG| %+v", cmd)
				// Clearly we need to handle authentication, it's in the TODO
				// For now, just let it go through with nothing in play until we
				// plug in encryption.
				if cmd.Payload[0] != "guest" {
					self.authed = true
					if len(cmd.Payload) >= 2 {
						self.name = cmd.Payload[1]
					} else {
						self.name = cmd.Payload[0]
					}
					// TODO: Bit of a thing, thought I'd mention it: MAKE SURE THEY HAVE THE KEY.
					self.Key, err = hex.DecodeString(cmd.Payload[0])
					if err != nil {
						log.Println("Couldn't decode hex of", cmd.Payload[0])
					}
				}

				// TODO: Temporary. We need to actually store these between starts.
				// TODO: We should also make this happen on register.
				self.Aliases = append(self.Aliases, self.name)

				// Add them to our online. They'll clean themselves up.
				srv.Online.Update(self)

			case "ROSTER":
				var roster []User
				for _, v := range srv.Online {
					roster = append(roster, v)
				}
				payload, err := msgpack.Marshal(roster)
				if err != nil {
					log.Println("Couldn't marshal roster:", err)
				}
				c.Write(BuildHeaders(eRoster, len(payload)))
				c.Write(payload)
			case "REGISTER":
				// TODO: replace with not a dummy. This currently only
				// lasts as long as their connection.
				srv.Online.Delete(self)
				self.name = cmd.Payload[0] + "#" + hex.EncodeToString(self.Key)
				srv.Online.Update(self)
			}
		case eMessage:
			var msg Message
			err = msgpack.Unmarshal(datum, &msg)
			if err != nil {
				log.Println("Error processing command datum: ", err)
			}

			if msg.From != self.HexifyKey() {
				status := Status{Payload: "spoof detected, please refrain", Status: -1}
				out, err := msgpack.Marshal(status)
				if err != nil {
					log.Println("Couldn't marhsal status for spoofer")
				}
				c.Write(BuildHeaders(eStatus, len(out)))
				c.Write(out)
				continue
			}

			if user, ok := srv.Online.Exists(msg.To); ok {
				// Send it as we get it vs remarshalling
				log.Printf("DEBUG| Message: %+v", msg)
				user.conn.Write(BuildHeaders(eMessage, len(datum)))
				user.conn.Write(datum)

			} else {
				status := Status{Payload: "user not available", Status: -1}
				out, err := msgpack.Marshal(status)
				if err != nil {
					log.Println("Couldn't marhsal status for unavailable user")
				}
				c.Write(BuildHeaders(eStatus, len(out)))
				c.Write(out)
			}
		}

	}
}

func main() {
	srv.Init()
}
