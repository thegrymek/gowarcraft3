// Author:  Niels A.D.
// Project: gowarcraft3 (https://github.com/nielsAD/gowarcraft3)
// License: Mozilla Public License, v2.0

// Package network implements common utilities for higher-level (emulated) Warcraft III network components.
package network

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"math"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nielsAD/gowarcraft3/protocol"
	"github.com/nielsAD/gowarcraft3/protocol/bncs"
	"github.com/nielsAD/gowarcraft3/protocol/capi"
	"github.com/nielsAD/gowarcraft3/protocol/w3gs"
)

// RunStart event
type RunStart struct{}

// RunStop event
type RunStop struct{}

// W3GSBroadcastAddr is used to broadcast W3GS packets to LAN
var W3GSBroadcastAddr = net.UDPAddr{IP: net.IPv4bcast, Port: 6112}

// W3GSPacketConn manages a UDP connection that transfers W3GS packets.
// Public methods/fields are thread-safe unless explicitly stated otherwise
type W3GSPacketConn struct {
	cmut RWMutex
	conn net.PacketConn

	smut sync.Mutex
	sbuf w3gs.SerializationBuffer

	bbuf [2048]byte
	rbuf w3gs.DeserializationBuffer
}

// NewW3GSPacketConn returns conn wrapped in W3GSPacketConn
func NewW3GSPacketConn(conn net.PacketConn) *W3GSPacketConn {
	return &W3GSPacketConn{conn: conn}
}

// Conn returns the underlying net.PacketConn
func (c *W3GSPacketConn) Conn() net.PacketConn {
	c.cmut.RLock()
	var conn = c.conn
	c.cmut.RUnlock()
	return conn
}

// SetConn closes the old connection and starts using the new net.PacketConn
func (c *W3GSPacketConn) SetConn(conn net.PacketConn) {
	c.Close()
	c.cmut.Lock()
	c.conn = conn
	c.cmut.Unlock()
}

// Close closes the connection
func (c *W3GSPacketConn) Close() error {
	c.cmut.RLock()

	var err error
	if c.conn != nil {
		err = c.conn.Close()
	}

	c.cmut.RUnlock()

	return err
}

// Send pkt to addr over net.PacketConn
func (c *W3GSPacketConn) Send(addr net.Addr, pkt w3gs.Packet) (int, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return 0, io.EOF
	}

	var n int
	var e error

	c.smut.Lock()
	c.sbuf.Truncate()
	if e = pkt.Serialize(&c.sbuf); e == nil {
		n, e = c.conn.WriteTo(c.sbuf.Bytes, addr)
	}
	c.smut.Unlock()
	c.cmut.RUnlock()

	return n, e
}

// Broadcast a packet over LAN
func (c *W3GSPacketConn) Broadcast(pkt w3gs.Packet) (int, error) {
	return c.Send(&W3GSBroadcastAddr, pkt)
}

// NextPacket waits for the next packet (with given timeout) and returns its deserialized representation
// Not safe for concurrent invocation
func (c *W3GSPacketConn) NextPacket(timeout time.Duration) (w3gs.Packet, net.Addr, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return nil, nil, io.EOF
	}

	if timeout != 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			c.cmut.RUnlock()
			return nil, nil, err
		}
	}

	size, addr, err := c.conn.ReadFrom(c.bbuf[:])
	if err != nil {
		c.cmut.RUnlock()
		return nil, nil, err
	}

	pkt, _, err := w3gs.DeserializePacketWithBuffer(&protocol.Buffer{Bytes: c.bbuf[:size]}, &c.rbuf)
	c.cmut.RUnlock()

	if err != nil {
		return nil, nil, err
	}

	return pkt, addr, err
}

// Run reads packets (with given max time between packets) from Conn and emits an event for each received packet
// Not safe for concurrent invocation
func (c *W3GSPacketConn) Run(f Emitter, timeout time.Duration) error {
	c.cmut.RLock()
	f.Fire(RunStart{})
	for {
		pkt, addr, err := c.NextPacket(timeout)

		if err != nil {
			switch err {
			// Connection is still valid after these errors, only deserialization failed
			case w3gs.ErrInvalidPacketSize, w3gs.ErrInvalidChecksum, w3gs.ErrUnexpectedConst, w3gs.ErrBufferTooSmall:
				f.Fire(&AsyncError{Src: "Run[NextPacket]", Err: err})
				continue
			default:
				f.Fire(RunStop{})
				c.cmut.RUnlock()
				return err
			}
		}

		f.Fire(pkt, addr)
	}
}

// W3GSConn manages a TCP connection that transfers W3GS packets.
// Public methods/fields are thread-safe unless explicitly stated otherwise
type W3GSConn struct {
	cmut RWMutex
	conn net.Conn

	smut sync.Mutex
	sbuf w3gs.SerializationBuffer
	rbuf w3gs.DeserializationBuffer
}

// NewW3GSConn returns conn wrapped in W3GSConn
func NewW3GSConn(conn net.Conn) *W3GSConn {
	return &W3GSConn{conn: conn}
}

// Conn returns the underlying net.Conn
func (c *W3GSConn) Conn() net.Conn {
	c.cmut.RLock()
	var conn = c.conn
	c.cmut.RUnlock()
	return conn
}

// SetConn closes the old connection and starts using the new net.Conn
func (c *W3GSConn) SetConn(conn net.Conn) {
	c.Close()
	c.cmut.Lock()
	c.conn = conn
	c.cmut.Unlock()
}

// Close closes the connection
func (c *W3GSConn) Close() error {
	c.cmut.RLock()

	var err error
	if c.conn != nil {
		err = c.conn.Close()
	}

	c.cmut.RUnlock()

	return err
}

// Send pkt to addr over net.Conn
func (c *W3GSConn) Send(pkt w3gs.Packet) (int, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return 0, io.EOF
	}

	c.smut.Lock()
	var n, err = w3gs.SerializePacketWithBuffer(c.conn, &c.sbuf, pkt)
	c.smut.Unlock()
	c.cmut.RUnlock()

	return n, err
}

// NextPacket waits for the next packet (with given timeout) and returns its deserialized representation
// Not safe for concurrent invocation
func (c *W3GSConn) NextPacket(timeout time.Duration) (w3gs.Packet, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return nil, io.EOF
	}

	if timeout != 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			c.cmut.RUnlock()
			return nil, err
		}
	}

	pkt, _, err := w3gs.DeserializePacketWithBuffer(c.conn, &c.rbuf)
	c.cmut.RUnlock()

	return pkt, err
}

// Run reads packets (with given max time between packets) from Conn and fires an event through f for each received packet
// Not safe for concurrent invocation
func (c *W3GSConn) Run(f Emitter, timeout time.Duration) error {
	c.cmut.RLock()
	f.Fire(RunStart{})
	for {
		pkt, err := c.NextPacket(timeout)

		if err != nil {
			switch err {
			case w3gs.ErrInvalidPacketSize, w3gs.ErrInvalidChecksum, w3gs.ErrUnexpectedConst, w3gs.ErrBufferTooSmall:
				// Connection is still valid after these errors, only deserialization failed
				f.Fire(&AsyncError{Src: "Run[NextPacket]", Err: err})
				continue
			default:
				f.Fire(RunStop{})
				c.cmut.RUnlock()
				return err
			}
		}

		f.Fire(pkt)
	}
}

// BNCSConn manages a TCP connection that transfers BNCS packets from/to client.
// Public methods/fields are thread-safe unless explicitly stated otherwise
type BNCSConn struct {
	cmut RWMutex
	conn net.Conn

	smut sync.Mutex
	sbuf bncs.SerializationBuffer
	rbuf bncs.DeserializationBuffer

	lmut sync.Mutex
	lnxt time.Time
}

// NewBNCSConn returns conn wrapped in BNCSConn
func NewBNCSConn(conn net.Conn) *BNCSConn {
	return &BNCSConn{conn: conn}
}

// Conn returns the underlying net.Conn
func (c *BNCSConn) Conn() net.Conn {
	c.cmut.RLock()
	var conn = c.conn
	c.cmut.RUnlock()
	return conn
}

// SetConn closes the old connection and starts using the new net.Conn
func (c *BNCSConn) SetConn(conn net.Conn) {
	c.Close()
	c.cmut.Lock()
	c.conn = conn
	c.cmut.Unlock()
}

// Close closes the connection
func (c *BNCSConn) Close() error {
	c.cmut.RLock()

	var err error
	if c.conn != nil {
		err = c.conn.Close()
	}

	c.cmut.RUnlock()

	return err
}

// Send pkt to addr over net.Conn
func (c *BNCSConn) Send(pkt bncs.Packet) (int, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return 0, io.EOF
	}

	c.smut.Lock()
	var n, err = bncs.SerializePacketWithBuffer(c.conn, &c.sbuf, pkt)
	c.smut.Unlock()
	c.cmut.RUnlock()

	return n, err
}

// SendRL pkt to addr over net.Conn with rate limit
func (c *BNCSConn) SendRL(pkt bncs.Packet) (int, error) {
	c.lmut.Lock()

	var t = time.Now()
	if t.Before(c.lnxt) {
		time.Sleep(c.lnxt.Sub(t))
	}

	var n, err = c.Send(pkt)
	if n > 0 {
		// log(packet_size,4)^1.5 × 1300ms
		// ~1.3s for packet size 4
		// ~2.8s for packet size 10
		// ~4.6s for packet size 25
		// ~6.2s for packet size 50
		// ~9.7s for packet size 200
		c.lnxt = time.Now().Add(time.Duration(math.Pow(math.Log(float64(n))/math.Log(4), 1.5)) * (1300 * time.Millisecond))
	}
	c.lmut.Unlock()

	return n, err
}

// NextClientPacket waits for the next client packet (with given timeout) and returns its deserialized representation
// Not safe for concurrent invocation
func (c *BNCSConn) NextClientPacket(timeout time.Duration) (bncs.Packet, error) {
	c.cmut.RLock()
	if c.conn == nil {
		c.cmut.RUnlock()
		return nil, io.EOF
	}

	if timeout != 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			c.cmut.RUnlock()
			return nil, err
		}
	}

	pkt, _, err := bncs.DeserializeClientPacketWithBuffer(c.conn, &c.rbuf)
	c.cmut.RUnlock()

	return pkt, err
}

// NextServerPacket waits for the next server packet (with given timeout) and returns its deserialized representation
// Not safe for concurrent invocation
func (c *BNCSConn) NextServerPacket(timeout time.Duration) (bncs.Packet, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return nil, io.EOF
	}

	if timeout != 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			c.cmut.RUnlock()
			return nil, err
		}
	}

	pkt, _, err := bncs.DeserializeServerPacketWithBuffer(c.conn, &c.rbuf)
	c.cmut.RUnlock()

	return pkt, err
}

// RunServer reads client packets (with given max time between packets) from Conn and emits an event for each received packet
// Not safe for concurrent invocation
func (c *BNCSConn) RunServer(f Emitter, timeout time.Duration) error {
	c.cmut.RLock()
	f.Fire(RunStart{})
	for {
		pkt, err := c.NextClientPacket(timeout)

		if err != nil {
			switch err {
			// Connection is still valid after these errors, only deserialization failed
			case bncs.ErrInvalidPacketSize, bncs.ErrInvalidChecksum, bncs.ErrUnexpectedConst, bncs.ErrBufferTooSmall:
				f.Fire(&AsyncError{Src: "RunServer[NextPacket]", Err: err})
				continue
			default:
				f.Fire(RunStop{})
				c.cmut.RUnlock()
				return err
			}
		}

		f.Fire(pkt)
	}
}

// RunClient reads server packets (with given max time between packets) from Conn and emits an event for each received packet
// Not safe for concurrent invocation
func (c *BNCSConn) RunClient(f Emitter, timeout time.Duration) error {
	c.cmut.RLock()
	f.Fire(RunStart{})
	for {
		pkt, err := c.NextServerPacket(timeout)

		if err != nil {
			switch err {
			// Connection is still valid after these errors, only deserialization failed
			case bncs.ErrInvalidPacketSize, bncs.ErrInvalidChecksum, bncs.ErrUnexpectedConst, bncs.ErrBufferTooSmall:
				f.Fire(&AsyncError{Src: "RunClient[NextPacket]", Err: err})
				continue
			default:
				f.Fire(RunStop{})
				c.cmut.RUnlock()
				return err
			}
		}

		f.Fire(pkt)
	}
}

// CAPIConn manages a websocket connection that processes CAPI requests.
// Public methods/fields are thread-safe unless explicitly stated otherwise
type CAPIConn struct {
	// websocket.Conn supports one concurrent reader and one concurrent writer.
	//
	// Applications are responsible for ensuring that no more than one goroutine calls the write methods
	// (NextWriter, SetWriteDeadline, WriteMessage, WriteJSON, EnableWriteCompression, SetCompressionLevel)
	// concurrently and that no more than one goroutine calls the read methods (NextReader, SetReadDeadline,
	// ReadMessage, ReadJSON, SetPongHandler, SetPingHandler) concurrently.
	//
	// The Close and WriteControl methods can be called concurrently with all other methods.
	conn *websocket.Conn

	cmut RWMutex
	smut sync.Mutex
}

// NewCAPIConn returns conn wrapped in CAPIConn
func NewCAPIConn(conn *websocket.Conn) *CAPIConn {
	return &CAPIConn{conn: conn}
}

// Conn returns the underlying net.Conn
func (c *CAPIConn) Conn() *websocket.Conn {
	c.cmut.RLock()
	var conn = c.conn
	c.cmut.RUnlock()
	return conn
}

// SetConn closes the old connection and starts using the new net.Conn
func (c *CAPIConn) SetConn(conn *websocket.Conn) {
	c.Close()
	c.cmut.Lock()
	c.conn = conn
	c.cmut.Unlock()
}

// Close closes the connection
func (c *CAPIConn) Close() error {
	c.cmut.RLock()

	var err error
	if c.conn != nil {
		err = c.conn.Close()
	}

	c.cmut.RUnlock()

	return err
}

// Send pkt to addr over net.Conn
func (c *CAPIConn) Send(pkt *capi.Packet) error {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return io.EOF
	}

	c.smut.Lock()

	w, err := c.conn.NextWriter(websocket.TextMessage)
	if err == nil {
		err = capi.SerializePacket(w, pkt)
		w.Close()
	}

	c.smut.Unlock()
	c.cmut.RUnlock()

	return err
}

// NextPacket waits for the next packet (with given timeout) and returns its deserialized representation
// Not safe for concurrent invocation
func (c *CAPIConn) NextPacket(timeout time.Duration) (*capi.Packet, error) {
	c.cmut.RLock()

	if c.conn == nil {
		c.cmut.RUnlock()
		return nil, io.EOF
	}

	if timeout != 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			c.cmut.RUnlock()
			return nil, err
		}
	}

	_, r, err := c.conn.NextReader()

	var pkt *capi.Packet
	if err == nil {
		pkt, err = capi.DeserializePacket(r)
		io.Copy(ioutil.Discard, r)
	}

	c.cmut.RUnlock()

	return pkt, err
}

// Run reads packets (with given max time between packets) from Conn and fires an event through f for each received packet
// Not safe for concurrent invocation
func (c *CAPIConn) Run(f Emitter, timeout time.Duration) error {
	c.cmut.RLock()
	f.Fire(RunStart{})
	for {
		pkt, err := c.NextPacket(timeout)

		if err != nil {
			switch err.(type) {
			// Connection is still valid after these errors, only deserialization failed
			case *json.SyntaxError, *json.UnmarshalTypeError:
				f.Fire(&AsyncError{Src: "Run[NextPacket]", Err: err})
				continue
			default:
				f.Fire(RunStop{})
				c.cmut.RUnlock()
				return err
			}
		}

		f.Fire(pkt)
		f.Fire(pkt.Payload, pkt)
	}
}
