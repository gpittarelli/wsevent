//wsevent implements thread-safe event-driven communication similar to socket.IO,
//on the top of Gorilla's WebSocket implementation.
package wsevent

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"
)

//Client
type Client struct {
	//Session ID
	ID string
	//Rooms the client has been added to
	rooms     []string
	roomsLock *sync.RWMutex

	conn     *ws.Conn
	connLock *sync.RWMutex
}

//Server
type Server struct {
	rooms     map[string]([]*Client)
	roomsLock *sync.RWMutex

	//The extractor function reads the byte array and the message type
	//and returns the event represented by the message.
	Extractor func([]byte, int) string
	//Called when the websocket connection is called. The only argument is
	//the disconnected client's session ID
	OnDisconnect func(string)

	handlers     map[string]func([]byte, int) ([]byte, int)
	handlersLock *sync.RWMutex

	newClient chan *Client
}

func genId(r *http.Request) string {
	hash := fmt.Sprintf("%s%d", r.RemoteAddr, time.Now().UnixNano())
	return fmt.Sprintf("%x", md5.Sum([]byte(hash)))
}

func (s *Server) NewClient(upgrader ws.Upgrader, w http.ResponseWriter, r *http.Request) (*Client, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}

	var arr []string
	client := &Client{
		ID:        genId(r),
		rooms:     arr,
		conn:      conn,
		connLock:  new(sync.RWMutex),
		roomsLock: new(sync.RWMutex)}
	s.newClient <- client

	return client, nil
}

//A thread-safe variant of WriteMessage
func (c *Client) Emit(data []byte, messageType int) error {
	c.connLock.Lock()
	defer c.connLock.Unlock()

	return c.conn.WriteMessage(messageType, data)
}

//Return a new server object
func NewServer() *Server {
	s := &Server{
		rooms:        make(map[string]([]*Client)),
		roomsLock:    new(sync.RWMutex),
		handlers:     make(map[string](func([]byte, int) ([]byte, int))),
		handlersLock: new(sync.RWMutex),
		newClient:    make(chan *Client),
	}

	return s
}

//Add a client c to room r
func (s *Server) AddClient(c *Client, r string) {
	s.roomsLock.Lock()
	defer s.roomsLock.Unlock()
	s.rooms[r] = append(s.rooms[r], c)

	c.roomsLock.Lock()
	defer c.roomsLock.Unlock()
	c.rooms = append(c.rooms, r)
}

//Sends all clients in room data with type messageType
func (s *Server) Broadcast(room string, data []byte, messageType int) {
	wg := new(sync.WaitGroup)

	for _, client := range s.rooms[room] {
		go func(c *Client) {
			wg.Add(1)
			defer wg.Done()
			c.Emit(data, messageType)
		}(client)
	}

	wg.Wait()
}

func (c *Client) cleanup(s *Server) {
	c.conn.Close()
	c.roomsLock.RLock()
	defer c.roomsLock.RUnlock()

	for _, room := range c.rooms {
		s.roomsLock.Lock()
		delete(s.rooms, room)
		s.roomsLock.Unlock()
	}

	if s.OnDisconnect != nil {
		s.OnDisconnect(c.ID)
	}
}

func (s *Server) Listener() {
	for {
		c := <-s.newClient
		go func(c *Client) {
			for {
				messageType, data, err := c.conn.ReadMessage()
				if err != nil {
					c.cleanup(s)
					return
				}

				callName := s.Extractor(data, messageType)

				s.handlersLock.RLock()
				f, ok := s.handlers[callName]
				s.handlersLock.RUnlock()

				if !ok {
					continue
				}
				c.Emit(f(data, messageType))
			}
		}(c)
	}
}

//Registers a callback for the event string. The callback must take two arguments,
//a byte array it's type, and return a byte array and it's type.
func (s *Server) On(event string, f func([]byte, int) ([]byte, int)) {
	s.handlersLock.Lock()
	s.handlers[event] = f
	s.handlersLock.Unlock()
}
