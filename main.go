package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"

	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
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

func handleConnection(client net.Conn) {
	defer client.Close()

	var log = *srv.logger // fite me irl

	protocheck := make([]byte, 5)
	_, err := io.ReadFull(client, protocheck)
	if err != nil {
		log.Error("Couldn't read bytes for header..", err)
		return
	}

	self := User{
		name:   "guest#" + hex.EncodeToString([]byte(client.RemoteAddr().String())), // Address to Hex
		conn:   client,
		safety: sync.Mutex{},
	}
	defer self.Cleanup()

	if !bytes.Equal(protocheck, helo) {
		log.Debug("BAD HEADER:", string(protocheck))
		resp := Status{Status: -1, Payload: "invalid flexim header"}
		out, err := msgpack.Marshal(resp)
		if err != nil {
			log.Error("Couldn't marshal bad header response.", err)
		}
		go self.Respond(eStatus, out)
		return
	}

	for {
		headers := make([]byte, 3)
		_, err := io.ReadFull(client, headers)
		if err != nil {
			log.Debug("Client Hangup: ", client.RemoteAddr())
			return
		}

		// Get datum information from headers.
		dType, dLength := datum(headers[0]), binary.BigEndian.Uint16(headers[1:])

		// Extract Datum.
		datum := make([]byte, dLength)
		_, err = io.ReadFull(client, datum)
		if err != nil {
			log.Error("Couldn't fill byte buffer, possibly disconnect?")
			break
		}
		log.Debug("INCOMING DATUM! TYPE:", dType, " LEN:", dLength, " BYTES:", datum)

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
				if len(cmd.Payload) < 1 {
					go self.Respond(eStatus, Status{Status: -1, Payload: "AUTH command requires payload"})
					continue
				}
				// Clearly we need to handle authentication, it's in the TODO
				// For now, just let it go through with nothing in play until we
				// plug in encryption.
				if cmd.Payload[0] != "guest" {
					if len(cmd.Payload) >= 2 {
						self.name = cmd.Payload[1]
					} else {
						self.name = cmd.Payload[0]
					}
					self.Key, err = hex.DecodeString(cmd.Payload[0])
					if err != nil {
						log.Error("Couldn't decode hex of", cmd.Payload[0])
					}
				}

				// Stop right here if the user is already online.
				if _, ok := srv.Online.Exists(self.HexifyKey()); ok {
					status := Status{Status: -1, Payload: "user already logged in; " + self.HexifyKey()}
					go self.Respond(eStatus, status)
					continue
				}

				// Build and challenge user
				challenge := srv.BuildAuth(128)
				self.challenge = challenge.Challenge
				go self.Respond(eAuth, challenge)

			case "ROSTER":
				var roster []User
				for _, v := range srv.Online {
					roster = append(roster, v)
				}
				go self.Respond(eRoster, roster)

			case "GETUSER":
				if len(cmd.Payload) > 0 {
					if _, ok := srv.Online.Exists(cmd.Payload[0]); ok {
						go self.Respond(eUser, srv.Online[cmd.Payload[0]])
					} else {
						go self.Respond(eStatus, Status{Status: -1, Payload: "GETUSER failed; unknown key"})
					}
				}

			case "GHOST":
				if len(cmd.Payload) > 0 {
					if user, ok := srv.Online.Exists(cmd.Payload[0]); ok {
						// Notify user of ghost attempt
						status := Status{
							Status:  5,
							Payload: "warning; ghost event triggered by " + self.conn.RemoteAddr().String(),
						}
						user.Respond(eStatus, status)

						// Challenge
						challenge := srv.BuildAuth(128)
						self.challenge = challenge.Challenge
						go self.Respond(eAuth, challenge)
					} else {
						status := Status{
							Status:  -1,
							Payload: "key not online; rejected",
						}
						user.Respond(eStatus, status)
					}
				}

			case "SEARCH":
				if len(cmd.Payload) >= 1 {
					// Because I could? What if there are lots of users?
					go func() {
						var result []User
						for _, user := range srv.Online {
							for _, alias := range user.Aliases {
								if cmd.Payload[0] == alias {
									result = append(result, user)
								}
							}
						}
						if len(result) >= 1 {
							go self.Respond(eRoster, result)
						} else {
							status := Status{
								Status:  -1,
								Payload: fmt.Sprintf("%s not found; unknown alias", cmd.Payload[0]),
							}
							go self.Respond(eStatus, status)
						}
					}()
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
					go self.Respond(eStatus, status)
					continue
				}
				key := redis_prefix + self.HexifyKey()
				l, err := srv.db.LLen(key).Result()
				if err != nil {
					log.Error("Couldn't get length of users offline array", err)
					status := Status{Payload: "error with backend; contact server admin", Status: -1}
					go self.Respond(eStatus, status)
					continue
				}
				for i := int64(0); i < l; i++ {
					var msg Message
					thing, err := srv.db.LPop(redis_prefix + self.HexifyKey()).Result()
					if err != nil {
						log.Fatal("Couldn't pop off users offlines:", err)
					}
					err = msgpack.Unmarshal([]byte(thing), &msg)
					go self.Respond(eMessage, msg)
				}

			}

		case eAuthResponse:
			var resp AuthResponse
			err = msgpack.Unmarshal(datum, &resp)
			if err != nil {
				log.Error("Something went wrong unmarshalling AuthResp", err)
			}

			// TODO: It'll do for now, but pretty it up. challenge.Challenge? Please.
			if resp.Challenge == self.challenge && !self.authed {
				// If we have a valid AuthResponse and that person is online, ghost them.
				if ghost, ok := srv.Online.Exists(self.HexifyKey()); ok {
					ghost.conn.Close()
				}

				// They seem to be who they claim to be..
				self.authed = true
				self.challenge = "" // clear from memory
				// TODO: Temporary. We need to actually store these between starts.
				// TODO: We should also make this happen on register.
				// TODO: remove duplicates bug
				self.Aliases = append(self.Aliases, self.name)
				srv.Online.Update(self)
				for _, u := range srv.Online {
					if self.HexifyKey() == u.HexifyKey() {
						continue
					}
					u.Respond(eStatus, Status{Payload: self.HexifyKey(), Status: 10})
					// u.conn.Write(BuildHeaders(eStatus, len(status)))
					// u.conn.Write(status) // TODO: all these things need to have some sort of logging.
				}
			} else {
				status := Status{Payload: "challenge failed; bye", Status: -1}
				go self.Respond(eStatus, status)
				return
			}

		case eMessage:
			var msg Message
			err = msgpack.Unmarshal(datum, &msg)
			if err != nil {
				log.Error("Error processing command datum: ", err)
			}

			key, err := hex.DecodeString(msg.From)
			if err != nil {
				log.Error("Couldn't decode hex of msg.From")
			}

			if !bytes.Equal(key, self.Key) {
				status := Status{Payload: "spoof detected, please refrain", Status: -1}
				go self.Respond(eStatus, status)
				continue
			}

			if user, ok := srv.Online.Exists(msg.To); ok {
				if user.authed && !self.authed {
					status := Status{Status: -1, Payload: "spim blocker: if one user is authed both must be"}
					go self.Respond(eStatus, status)
					continue
				}
				// Send it as we get it vs remarshalling
				log.Debugf("MSG: %s -> %s", msg.From, msg.To)
				user.Respond(eMessage, msg)
			} else {
				srv.db.RPush(redis_prefix+msg.To, datum)
				status := Status{Payload: fmt.Sprintf("user \"%+v\" not available; storing offline", msg.To), Status: 1}
				go self.Respond(eStatus, status)
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
