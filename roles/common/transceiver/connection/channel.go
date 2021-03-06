//  Crypto-Obscured Forwarder
//
//  Copyright (C) 2018 Rui NI <ranqus@gmail.com>
//
//  This file is part of Crypto-Obscured Forwarder.
//
//  Crypto-Obscured Forwarder is free software: you can redistribute it
//  and/or modify it under the terms of the GNU General Public License
//  as published by the Free Software Foundation, either version 3 of
//  the License, or (at your option) any later version.
//
//  Crypto-Obscured Forwarder is distributed in the hope that it will be
//  useful, but WITHOUT ANY WARRANTY; without even the implied warranty
//  of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU General Public License for more details.
//
//  You should have received a copy of the GNU General Public License
//  along with Crypto-Obscured Forwarder. If not, see
//  <http://www.gnu.org/licenses/>.

package connection

import (
	"io"
	"math"
	"sync"
	"time"

	"github.com/reinit/coward/common/fsm"
	"github.com/reinit/coward/common/rw"
	"github.com/reinit/coward/common/ticker"
	ch "github.com/reinit/coward/roles/common/channel"
	"github.com/reinit/coward/roles/common/network"
)

// Errors
var (
	ErrChannelShuttedDown = NewError(
		"Connection Channel already closed")

	ErrChannelInitializationFailed = NewError(
		"Failed to read initial information of Connection Channel")

	ErrChannelWriteIncomplete = NewError(
		"Incomplete write")

	ErrChannelDispatchChannelUnavailable = NewError(
		"Selected Channel is unavailable")

	ErrChannelDispatchChannelInactive = NewError(
		"Selected Channel is inactive")

	ErrChannelVirtualConnectionTimedout = NewError(
		"Virtual Connection has timed out")

	ErrChannelVirtualConnectionSegmentDepleted = NewError(
		"Data Segment of current Virtual Connection are depleted")

	ErrChannelConnectionDropped = NewError(
		"Channel Connection is lost")

	ErrChannelVirtualConnectionNotAssigned = NewError(
		"Virtual Connection not assigned")

	ErrChannelAlreadyClosing = NewError(
		"Channel already closing")
)

// Consts
const (
	maxDepleteBufSize = 256
)

// Channelizer represents a Channel Connection Manager
type Channelizer interface {
	Dispatch(ch.Channels) (ch.ID, fsm.FSM, error)
	Timeout(time.Duration)
	For(ch.ID) Virtual
	Shutdown() error
	Closed() <-chan struct{}
}

// Virtual means represents a Virtual Channel
type Virtual interface {
	rw.ReadWriteDepleteDoner

	Timeout(time.Duration)
}

// channelize implements Channelizer
type channelize struct {
	conn              network.Connection
	codec             rw.Codec
	timeout           time.Duration
	timeoutTicker     ticker.Requester
	buf               [3]byte
	channels          [ch.MaxChannels]*channel
	dispatchCompleted chan struct{}
	downSignal        chan struct{}
	downed            bool
	writeLock         sync.Mutex
}

// channelReader is the dispatched channel reader data
type channelReader struct {
	Reader   network.Connection
	Length   uint16
	Complete chan struct{}
}

// channel implements Channel
type channel struct {
	network.Connection

	codec         rw.Codec
	id            ch.ID
	parent        Channelizer
	timeout       time.Duration
	timeoutTicker ticker.Requester
	connClosed    <-chan struct{}
	connReader    chan channelReader
	currentReader channelReader
	downSignal    chan struct{}
	writeLock     *sync.Mutex
}

// Channelize creates a Connection Channel for mulit-channel dispatch
func Channelize(
	c network.Connection,
	codec rw.Codec,
	timeoutTicker ticker.Requester,
) Channelizer {
	return &channelize{
		conn:              newBuffered(errorconn{Connection: c}, 4096),
		codec:             codec,
		timeout:           0,
		timeoutTicker:     timeoutTicker,
		buf:               [3]byte{},
		channels:          [ch.MaxChannels]*channel{},
		dispatchCompleted: make(chan struct{}, 1),
		downSignal:        make(chan struct{}),
		downed:            false,
		writeLock:         sync.Mutex{},
	}
}

// Closed returns a channel that will be closed when current Channelizer
// is down
func (c *channelize) Closed() <-chan struct{} {
	return c.downSignal
}

// Timeout set the read timeout
func (c *channelize) Timeout(t time.Duration) {
	c.timeout = t

	c.conn.SetTimeout(t)
}

// Initialize reads initialization data from Connection
func (c *channelize) Dispatch(channels ch.Channels) (ch.ID, fsm.FSM, error) {
	// Write will be blocked until someone released the lock
	select {
	case c.dispatchCompleted <- struct{}{}:

	case <-c.conn.Closed():
		if c.dispatchCompleted != nil {
			close(c.dispatchCompleted)

			c.dispatchCompleted = nil
		}

		return 0, nil, ErrChannelConnectionDropped

	case <-c.downSignal: // Unblock dispatchCompleted with a Channel
		// Forget about dispatchCompleted, we're downing
		if c.dispatchCompleted != nil {
			close(c.dispatchCompleted)

			c.dispatchCompleted = nil
		}

		return 0, nil, ErrChannelShuttedDown
	}

	_, rErr := io.ReadFull(c.codec.Decode(c.conn), c.buf[:3])

	if rErr != nil {
		<-c.dispatchCompleted

		return 0, nil, rErr
	}

	machine, fsmErr := channels.Get(ch.ID(c.buf[0]))

	if fsmErr != nil {
		<-c.dispatchCompleted

		return 0, nil, fsmErr
	}

	// Deliever the Read Connection to Virtual Channel
	if uint8(c.buf[0]) >= channels.Size() || c.channels[c.buf[0]] == nil {
		<-c.dispatchCompleted

		return 0, nil, ErrChannelDispatchChannelUnavailable
	}

	segDataLen := uint16(0)
	segDataLen |= uint16(c.buf[1])
	segDataLen <<= 8
	segDataLen |= uint16(c.buf[2])

	select {
	case c.channels[c.buf[0]].connReader <- channelReader{
		Reader:   c.conn,
		Length:   segDataLen,
		Complete: c.dispatchCompleted}:
		return ch.ID(c.buf[0]), machine, nil

	case <-c.conn.Closed():
		if c.dispatchCompleted != nil {
			<-c.dispatchCompleted

			close(c.dispatchCompleted)
			c.dispatchCompleted = nil
		}

		return 0, nil, ErrChannelConnectionDropped

	case <-c.downSignal:
		if c.dispatchCompleted != nil {
			<-c.dispatchCompleted

			close(c.dispatchCompleted)
			c.dispatchCompleted = nil
		}

		return 0, nil, ErrChannelShuttedDown
	}
}

// For creates a Virtual Channel Connection reader for specified Channel
func (c *channelize) For(id ch.ID) Virtual {
	if c.channels[id] != nil {
		return c.channels[id]
	}

	c.channels[id] = &channel{
		Connection:    c.conn,
		codec:         c.codec,
		id:            id,
		parent:        c,
		timeout:       c.timeout,
		timeoutTicker: c.timeoutTicker,
		connClosed:    c.conn.Closed(),
		connReader:    make(chan channelReader, 1),
		currentReader: channelReader{
			Reader:   nil,
			Complete: nil,
			Length:   0,
		},
		downSignal: c.downSignal,
		writeLock:  &c.writeLock,
	}

	return c.channels[id]
}

// Shutdown closes all underlaying Virtual Channels
func (c *channelize) Shutdown() error {
	if c.downed {
		return ErrChannelShuttedDown
	}

	c.downed = true

	close(c.downSignal)

	for cIdx := range c.channels {
		c.channels[cIdx] = nil
	}

	return nil
}

// Timeout set the read timeout
func (c *channel) Timeout(t time.Duration) {
	c.timeout = t
}

// Depleted returns whether or not there are still remaining data
// in the current Virtual Channel to read
func (c *channel) Depleted() bool {
	return c.currentReader.Length <= 0
}

// Deplete ditchs all remaining data of current segment
func (c *channel) Deplete() error {
	if c.Depleted() {
		return nil
	}

	maxBufSize := c.currentReader.Length

	if maxBufSize > maxDepleteBufSize {
		maxBufSize = maxDepleteBufSize
	}

	dBuf := make([]byte, maxBufSize)

	for {
		_, rErr := c.Read(dBuf)

		if rErr != nil {
			return rErr
		}

		if !c.Depleted() {
			continue
		}

		return nil
	}
}

// Done finish use of current Virtual Channel
func (c *channel) Done() error {
	err := c.Deplete()

	if c.currentReader.Reader != nil {
		c.currentReader.Reader = nil
		<-c.currentReader.Complete
	}

	return err
}

// Read reads data from Virtual Channel
func (c *channel) Read(b []byte) (int, error) {
	if c.currentReader.Reader == nil {
		var timeoutTicker ticker.Wait

		if c.timeout > 0 && c.timeoutTicker != nil {
			tWait, tWaitErr := c.timeoutTicker.Request(
				time.Now().Add(c.timeout))

			if tWaitErr != nil {
				return 0, tWaitErr
			}

			defer tWait.Close()

			timeoutTicker = tWait.Wait()
		}

		select {
		case reader := <-c.connReader:
			c.currentReader.Reader = reader.Reader
			c.currentReader.Length = reader.Length
			c.currentReader.Complete = reader.Complete

		case <-c.connClosed:
			return 0, ErrChannelConnectionDropped

		case <-c.downSignal:
			return 0, ErrChannelShuttedDown

		case <-timeoutTicker:
			return 0, ErrChannelVirtualConnectionTimedout
		}
	} else if c.Depleted() {
		return 0, ErrChannelVirtualConnectionSegmentDepleted
	}

	bufLen := len(b)

	if bufLen > math.MaxUint16 {
		bufLen = math.MaxUint16
	}

	maxBufLen := uint16(bufLen)
	maxReadLen := c.currentReader.Length

	if maxReadLen > maxBufLen {
		maxReadLen = maxBufLen
	}

	rLen, rErr := c.codec.Decode(c.currentReader.Reader).Read(b[:maxReadLen])

	c.currentReader.Length -= uint16(rLen)

	return rLen, rErr
}

// Write writes data to a Connection Channel
func (c *channel) Write(b []byte) (int, error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	startPos := 0
	bLen := len(b)
	segLen := bLen

	if segLen > math.MaxUint16 {
		segLen = math.MaxUint16
	}

	headBuf := [3]byte{}

	for bLen > startPos {
		headBuf[0] = c.id.Byte()
		headBuf[1] = 0 | byte(segLen>>8)
		headBuf[2] = 0 | byte(segLen<<8>>8)

		_, wErr := c.codec.Encode(c.Connection).
			WriteAll(headBuf[:], b[startPos:startPos+segLen])

		if wErr != nil {
			return startPos, wErr
		}

		startPos += segLen
		segLen = bLen - startPos

		if segLen > math.MaxUint16 {
			segLen = math.MaxUint16
		}
	}

	return startPos, nil
}
