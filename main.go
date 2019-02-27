package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/vmihailenco/msgpack"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"
)

// Was const, now just bytes.
var helo = []byte("\xA4FLEX")

const redis_prefix = "fleximd:offlines:"

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

// TODO: Refactor this out. Only used by users now. Shouldn't be too hard.
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
	_, err := io.ReadFull(c, protocheck)
	if err != nil {
		log.Println("Couldn't read bytes for header..", err)
		return
	}

	if !bytes.Equal(protocheck, helo) {
		log.Println("DEBUG| BAD HEADER:", string(protocheck))
		resp := Status{Status: -1, Payload: "invalid flexim header"}
		out, err := msgpack.Marshal(resp)
		if err != nil {
			log.Println("Couldn't marshal bad header response.")
		}
		srv.Respond(eStatus, out)
		return
	}

	// TODO: Should this be a pointer rather than value for conn?
	self := User{name: "guest#" + hex.EncodeToString([]byte(c.RemoteAddr().String())), conn: c} // hahahah
	defer self.Cleanup()
	// DatumProcessing:
	for {
		// log.Println("DEBUG|", srv.Online)
		headers := make([]byte, 3)
		_, err := io.ReadFull(c, headers)
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
					// self.authed = true
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

				// Stop right here if the user is already online.
				if _, ok := srv.Online.Exists(self.HexifyKey()); ok {
					status := Status{Status: -1, Payload: "user already logged in"}
					srv.Respond(eStatus, status)
					continue
				}

				c, err := exec.Command("uuidgen").Output()
				if err != nil {
					log.Fatal("Couldn't generate uuid")
				}
				// Send the Auth datum
				self.challenge = Auth{Date: time.Now().Unix(), Challenge: strings.TrimSuffix(string(c), "\n")}
				srv.Respond(eAuth, self.challenge)

			case "ROSTER":
				var roster []User
				for _, v := range srv.Online {
					roster = append(roster, v)
				}
				srv.Respond(eRoster, roster)
			case "REGISTER":
				// TODO: replace with not a dummy. This currently only
				// lasts as long as their connection.
				srv.Online.Delete(self)
				self.name = cmd.Payload[0] + "#" + hex.EncodeToString(self.Key)
				srv.Online.Update(self)
			}

		case eAuthResponse:
			var resp AuthResponse
			err = msgpack.Unmarshal(datum, &resp)
			if err != nil {
				log.Println("Something went wrong unmarshalling AuthResp", err)
			}

			// TODO: It'll do for now, but pretty it up. challenge.Challenge? Please.
			if resp.Challenge == self.challenge.Challenge && !self.authed {
				// They seem to be who they claim to be..
				self.authed = true
				// TODO: Temporary. We need to actually store these between starts.
				// TODO: We should also make this happen on register.
				// TODO: remove duplicates bug
				self.Aliases = append(self.Aliases, self.name)
				srv.Online.Update(self)
				srv.Respond(eUser, self)
			}

		case eMessage:
			var msg Message
			err = msgpack.Unmarshal(datum, &msg)
			if err != nil {
				log.Println("Error processing command datum: ", err)
			}

			if msg.From != self.HexifyKey() {
				status := Status{Payload: "spoof detected, please refrain", Status: -1}
				srv.Respond(eStatus, status)
				continue
			}

			if user, ok := srv.Online.Exists(msg.To); ok {
				if user.authed && !self.authed {
					status := Status{Status: -1, Payload: "spim blocker: please auth to msg authed users"}
					srv.Respond(eStatus, status)
				}
				// Send it as we get it vs remarshalling
				log.Printf("DEBUG| MSG: %s -> %s", msg.From, msg.To)
				user.conn.Write(BuildHeaders(eMessage, len(datum)))
				user.conn.Write(datum)
			} else {
				srv.db.Append(redis_prefix+msg.To, string(datum))
				status := Status{Payload: "user not available; storing offline", Status: 1}
				srv.Respond(eStatus, status)
				continue
			}
		}

	}
}

var srv = fleximd{
	Online:      make(FleximRoster),
	BindAddress: ":4321",
	mutex:       &sync.Mutex{},
}

func main() {
	// For cleanup of active sessions
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		srv.Shutdown()
	}()

	srv.Init()
}
