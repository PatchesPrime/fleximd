package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
	"io"
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

// Custom aliases.
type datum int

// TODO: Refactor this out. Only used by users now. Shouldn't be too hard.
func BuildHeaders(d datum, l int) []byte {
	// We need a 3 byte array
	out := make([]byte, 3)

	// The first one is always the datum type, follow by 2 bytes representing uint16
	out[0] = byte(d)
	binary.BigEndian.PutUint16(out[1:], uint16(l))

	return out
}

func handleConnection(client net.Conn) {
	defer client.Close()

	// Helper function
	Respond := func(t datum, d interface{}) {
		srv.logger.Debugf("SENDING RESPONSE (TYPE:%s): %+v", t, d)
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
		client.Write(append(metadata, out...))
	}

	var log = *srv.logger // fite me irl

	protocheck := make([]byte, 5)
	_, err := io.ReadFull(client, protocheck)
	if err != nil {
		log.Error("Couldn't read bytes for header..", err)
		return
	}

	if !bytes.Equal(protocheck, helo) {
		log.Debug("BAD HEADER:", string(protocheck))
		resp := Status{Status: -1, Payload: "invalid flexim header"}
		out, err := msgpack.Marshal(resp)
		if err != nil {
			log.Error("Couldn't marshal bad header response.", err)
		}
		Respond(eStatus, out)
		return
	}

	// TODO: Should this be a pointer rather than value for conn?
	self := User{name: "guest#" + hex.EncodeToString([]byte(client.RemoteAddr().String())), conn: client} // hahahah
	defer self.Cleanup()

	for {
		headers := make([]byte, 3)
		_, err := io.ReadFull(client, headers)
		if err != nil {
			log.Debug("Client Hangup: ", client.RemoteAddr())
			return
		}

		// Get datum information from headers.
		dType, dLength := datum(headers[0]), binary.BigEndian.Uint16(headers[1:])
		log.Debug("PARSED HEADERS, TYPE:", dType, " LEN:", dLength)

		// Extract Datum.
		datum := make([]byte, dLength)
		_, err = io.ReadFull(client, datum)
		if err != nil {
			log.Error("Couldn't fill byte buffer, possibly disconnect?")
			break
		}
		log.Debug("RAW DATUM BYTES: ", datum)

		switch dType {
		case eCommand:
			var cmd Command
			err = msgpack.Unmarshal(datum, &cmd)
			if err != nil {
				log.Error("Error processing command datum: ", err)
			}
			log.Debugf("GOT COMMAND (%s)", cmd.Cmd)
			switch cmd.Cmd {
			case "AUTH":
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
						log.Error("Couldn't decode hex of", cmd.Payload[0])
					}
				}

				// Stop right here if the user is already online.
				if _, ok := srv.Online.Exists(self.HexifyKey()); ok {
					status := Status{Status: -1, Payload: "user already logged in"}
					Respond(eStatus, status)
					continue
				}

				c, err := exec.Command("uuidgen").Output()
				if err != nil {
					log.Fatal("Couldn't generate uuid")
				}
				// Send the Auth datum
				self.challenge = Auth{Date: time.Now().Unix(), Challenge: strings.TrimSuffix(string(c), "\n")}
				Respond(eAuth, self.challenge)

			case "ROSTER":
				var roster []User
				for _, v := range srv.Online {
					roster = append(roster, v)
				}
				Respond(eRoster, roster)

			case "GETUSER":
				if len(cmd.Payload) > 0 {
					if _, ok := srv.Online.Exists(cmd.Payload[0]); ok {
						Respond(eUser, srv.Online[cmd.Payload[0]])
					} else {
						Respond(eStatus, Status{Status: -1, Payload: "GETUSER failed; unknown key"})
					}
				}

			case "SEARCH":
				if len(cmd.Payload) >= 1 {
					var result []User
					for _, user := range srv.Online {
						for _, alias := range user.Aliases {
							if cmd.Payload[0] == alias {
								result = append(result, user)
							}
						}
					}
					if len(result) >= 1 {
						Respond(eRoster, result)
					} else {
						Respond(eStatus, Status{Status: -1, Payload: "alias not found; sorry"})
					}
				}

			case "REGISTER":
				// TODO: replace with not a dummy. This currently only
				// lasts as long as their connection.
				srv.Online.Delete(self)
				self.name = cmd.Payload[0] + "#" + hex.EncodeToString(self.Key)
				srv.Online.Update(self)
			case "OFFLINES":
				if !self.authed {
					status := Status{Payload: "permission denied; please auth", Status: -1}
					Respond(eStatus, status)
					continue
				}
				key := redis_prefix + self.HexifyKey()
				l, err := srv.db.LLen(key).Result()
				if err != nil {
					log.Fatal("Couldn't get length of users offline array", err)
				}
				for i := int64(0); i < l; i++ {
					var msg Message
					thing, err := srv.db.LPop(redis_prefix + self.HexifyKey()).Result()
					if err != nil {
						log.Fatal("Couldn't pop off users offlines:", err)
					}
					err = msgpack.Unmarshal([]byte(thing), &msg)
					Respond(eMessage, msg)
				}

			}

		case eAuthResponse:
			var resp AuthResponse
			err = msgpack.Unmarshal(datum, &resp)
			if err != nil {
				log.Error("Something went wrong unmarshalling AuthResp", err)
			}

			// TODO: It'll do for now, but pretty it up. challenge.Challenge? Please.
			if resp.Challenge == self.challenge.Challenge && !self.authed {
				// They seem to be who they claim to be..
				self.authed = true
				self.challenge = Auth{} // Empty the value to not store old key in memory longer than needed.
				// TODO: Temporary. We need to actually store these between starts.
				// TODO: We should also make this happen on register.
				// TODO: remove duplicates bug
				self.Aliases = append(self.Aliases, self.name)
				srv.Online.Update(self)
				Respond(eUser, self) // TODO: Should this be removed
				for _, u := range srv.Online {
					if self.HexifyKey() == u.HexifyKey() {
						continue
					}
					status, err := msgpack.Marshal(Status{Payload: self.HexifyKey(), Status: 10})
					if err != nil {
						srv.logger.Error("Couldn't marshal status for roster update: ", err)
					}
					u.conn.Write(BuildHeaders(eStatus, len(status)))
					u.conn.Write(status) // TODO: all these things need to have some sort of logging.
				}
			} else {
				status := Status{Payload: "challenge failed; bye", Status: -1}
				Respond(eStatus, status)
				return
			}

		case eMessage:
			var msg Message
			err = msgpack.Unmarshal(datum, &msg)
			if err != nil {
				log.Error("Error processing command datum: ", err)
			}

			if msg.From != self.HexifyKey() {
				status := Status{Payload: "spoof detected, please refrain", Status: -1}
				Respond(eStatus, status)
				continue
			}

			if user, ok := srv.Online.Exists(msg.To); ok {
				if user.authed && !self.authed {
					status := Status{Status: -1, Payload: "spim blocker: if one user is authed both must be"}
					Respond(eStatus, status)
					continue
				}
				// Send it as we get it vs remarshalling
				log.Debugf("MSG: %s -> %s", msg.From, msg.To)
				user.conn.Write(BuildHeaders(eMessage, len(datum)))
				user.conn.Write(datum)
			} else {
				srv.db.RPush(redis_prefix+msg.To, datum)
				status := Status{Payload: fmt.Sprintf("user \"%+v\" not available; storing offline", msg.To), Status: 1}
				Respond(eStatus, status)
				continue
			}
		}

	}
}

var srv = fleximd{
	Online:      make(FleximRoster),
	BindAddress: ":4321",
	mutex:       &sync.Mutex{},
	logger:      logrus.New(),
}

func main() {
	// For cleanup of active sessions
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		srv.Shutdown()
	}()

	addr := flag.String("addr", ":4321", "The binding address the server should use.")
	redis_addr := flag.String("redis", "localhost", "The address of the redis server required for function.q")
	debug := flag.Bool("debug", false, "Set the log level to debug")
	flag.Parse()

	// Ensure server is setup how we like.
	srv.BindAddress = *addr
	srv.logger.SetFormatter(&logrus.JSONFormatter{})

	// What level logging?
	if *debug {
		srv.logger.Level = logrus.DebugLevel
		// Report the calling function.
		srv.logger.SetReportCaller(true)
		srv.logger.SetFormatter(&logrus.JSONFormatter{PrettyPrint: true})
	}

	// Let's do this here.
	// TODO: This should be in a config.
	srv.Init(redis.NewClient(&redis.Options{
		Addr:     *redis_addr,
		Password: "", // no password set
		DB:       0,  // use default DB
	}))
}
