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

package request

import (
	"io"
	"net"
	"time"

	"github.com/reinit/coward/common/fsm"
	"github.com/reinit/coward/common/logger"
	"github.com/reinit/coward/common/rw"
	"github.com/reinit/coward/roles/common/command"
	tcpconn "github.com/reinit/coward/roles/common/network/connection/tcp"
	tcpdial "github.com/reinit/coward/roles/common/network/dialer/tcp"
	"github.com/reinit/coward/roles/common/relay"
)

// TCPIPv4 IPv4 Connect request
type TCPIPv4 struct {
	TCP
}

type tcpIPv4 struct {
	tcp
}

// ID returns the request ID
func (c TCPIPv4) ID() command.ID {
	return TCPCommandIPv4
}

// New creates a new context
func (c TCPIPv4) New(rw rw.ReadWriteDepleteDoner, l logger.Logger) fsm.Machine {
	return &tcpIPv4{
		tcp: tcp{
			logger:            l,
			buf:               c.Buffer,
			dialTimeout:       c.DialTimeout,
			connectionTimeout: c.ConnectionTimeout,
			runner:            c.Runner,
			cancel:            c.Cancel,
			noLocalAccess:     c.NoLocalAccess,
			rw:                rw,
			relay:             nil,
		},
	}
}

func (c *tcpIPv4) Bootup() (fsm.State, error) {
	_, rErr := io.ReadFull(c.rw, c.buf[:7])

	if rErr != nil {
		c.rw.Done()

		return nil, rErr
	}

	c.rw.Done()

	ipv4 := net.IPv4(c.buf[0], c.buf[1], c.buf[2], c.buf[3])

	port := uint16(0)
	port |= uint16(c.buf[4])
	port <<= 8
	port |= uint16(c.buf[5])

	timeout := time.Duration(c.buf[6]) * time.Second

	if timeout <= 0 {
		rw.WriteFull(c.rw, []byte{TCPRespondBadRequest})

		return nil, ErrTCPInvalidTimeout
	}

	if timeout > c.dialTimeout {
		timeout = c.dialTimeout
	}

	c.relay = relay.New(c.logger, c.runner, c.rw, c.buf, tcpRelay{
		noLocalAccess:     c.noLocalAccess,
		dialTimeout:       c.dialTimeout,
		connectionTimeout: c.connectionTimeout,
		dial: tcpdial.New(
			ipv4.String(), port, timeout, tcpconn.Wrap).Dialer(),
	}, make([]byte, 4096))

	bootErr := c.relay.Bootup(c.cancel)

	if bootErr != nil {
		return nil, bootErr
	}

	return c.tick, nil
}
