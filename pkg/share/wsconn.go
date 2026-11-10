package share

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn adapts a gorilla WebSocket to net.Conn: binary messages carry a byte stream. It is safe for one
// concurrent reader, one concurrent writer and Close from anywhere, which is what yamux does.
type wsConn struct {
	ws *websocket.Conn

	cur io.Reader // the message being read; only the reader touches it

	wmu sync.Mutex // one writer at a time
	dmu sync.Mutex // guards wdl
	wdl time.Time  // write deadline, applied at each Write

	emu sync.Mutex
	err error // first read error: yamux swallows it, and it carries the peer's close code

	readDone chan struct{} // closed when Read first fails, after the error is recorded

	closeOnce sync.Once
	closeErr  error
}

func newWSConn(ws *websocket.Conn) *wsConn { return &wsConn{ws: ws, readDone: make(chan struct{})} }

func (c *wsConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if c.cur == nil {
			mt, r, err := c.ws.NextReader()
			if err != nil {
				c.record(err)
				return 0, err
			}
			if mt != websocket.BinaryMessage {
				err := errors.New("share tunnel: unexpected text message")
				c.record(err)
				return 0, err
			}
			c.cur = r
		}
		n, err := c.cur.Read(p)
		if err == io.EOF {
			c.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			c.record(err)
		}
		return n, err
	}
}

func (c *wsConn) record(err error) {
	c.emu.Lock()
	first := c.err == nil
	if first {
		c.err = err
	}
	c.emu.Unlock()
	if first {
		close(c.readDone)
	}
}

// CloseCode is the close code the peer sent (4403, 4409, 1001...), 0 when there was none: a network drop,
// a local close or a normal closure.
func (c *wsConn) CloseCode() int {
	c.emu.Lock()
	err := c.err
	c.emu.Unlock()
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.CloseNormalClosure, websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure:
			return 0
		}
		return ce.Code
	}
	return 0
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.dmu.Lock()
	dl := c.wdl
	c.dmu.Unlock()
	if err := c.ws.SetWriteDeadline(dl); err != nil {
		return 0, err
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close sends a normal close, then drops the connection. WriteControl is safe beside a blocked Write.
func (c *wsConn) Close() error {
	c.closeOnce.Do(func() {
		c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		c.closeErr = c.ws.Close()
	})
	return c.closeErr
}

func (c *wsConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }

func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *wsConn) SetReadDeadline(t time.Time) error { return c.ws.SetReadDeadline(t) }

func (c *wsConn) SetWriteDeadline(t time.Time) error {
	c.dmu.Lock()
	c.wdl = t
	c.dmu.Unlock()
	return nil
}
