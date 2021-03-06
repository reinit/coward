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

package worker

import (
	"testing"
	"time"

	"github.com/reinit/coward/common/logger"
	"github.com/reinit/coward/common/ticker"
)

func TestJobServeClose(t *testing.T) {
	tk, tkErr := ticker.New(300*time.Millisecond, 16).Serve()

	if tkErr != nil {
		t.Errorf("Failed to create ticker due to error: %s", tkErr)

		return
	}

	c := New(logger.NewDitch(), tk, Config{
		MaxWorkers:        64,
		MinWorkers:        16,
		MaxWorkerIdle:     1 * time.Second,
		JobReceiveTimeout: 1 * time.Second,
	})

	for i := 0; i < 100; i++ {
		serving, serveErr := c.Serve()

		if serveErr != nil {
			t.Error("Failed to serve due to error:", serveErr)

			return
		}

		_, serveErr = c.Serve()

		if serveErr == nil {
			t.Error("Re-Serve should resulting an error")

			return
		}

		closeErr := serving.Close()

		if closeErr != nil {
			t.Error("Failed to close due to error:", closeErr)

			return
		}

		closeErr = serving.Close()

		if closeErr == nil {
			t.Error("Re-Close should resulting an error")

			return
		}
	}
}

func TestJobServeRunWaitClose(t *testing.T) {
	tk, tkErr := ticker.New(300*time.Millisecond, 16).Serve()

	if tkErr != nil {
		t.Errorf("Failed to create ticker due to error: %s", tkErr)

		return
	}

	c := New(logger.NewDitch(), tk, Config{
		MaxWorkers:        64,
		MinWorkers:        16,
		MaxWorkerIdle:     1 * time.Second,
		JobReceiveTimeout: 1 * time.Second,
	})

	serving, serveErr := c.Serve()

	if serveErr != nil {
		t.Error("Failed to serve due to error:", serveErr)

		return
	}

	doneWait := make(chan struct{})
	closeWait := make(chan struct{})
	results := [65]error{}
	log := logger.NewDitch()

	go func() {
		for i := 0; i < 65; i++ {
			go func(ind int) {
				results[ind] = serving.RunWait(log, func(logger.Logger) error {
					<-doneWait

					return nil
				}, nil)
			}(i)

			doneWait <- struct{}{}
		}

		closeWait <- struct{}{}
	}()

	<-closeWait

	closeErr := serving.Close()

	if closeErr != nil {
		t.Error("Failed to close due to error:", closeErr)

		return
	}
}
