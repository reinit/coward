//  Crypto-Obscured Forwarder
//
//  Copyright (C) 2017 Rui NI <ranqus@gmail.com>
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

package aesgcm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"sync"

	"github.com/reinit/coward/common/rw"
	"github.com/reinit/coward/roles/common/codec/key"
	"github.com/reinit/coward/roles/common/codec/marker"
)

const (
	nonceSize        = 12
	maxDataBlockSize = 4096
)

// Errors
var (
	ErrDataBlockTooLarge = errors.New(
		"AES-GCM Data block too large, decode refused")

	ErrInvalidSizeDataLength = errors.New(
		"The length information of size data is invalid")
)

type aesgcm struct {
	rw                   io.ReadWriter
	block                cipher.Block
	decrypter            cipher.AEAD
	decrypterInited      bool
	decryptBuf           []byte
	decryptCipherTextBuf []byte
	decryptReader        *bytes.Reader
	decryptNonceBuf      [nonceSize]byte
	encrypter            cipher.AEAD
	encrypterInited      bool
	encrypterNonceBuf    [nonceSize]byte
	decryptMarker        marker.Marker
	decryptMarkerLock    *sync.Mutex
}

// AESGCM returns a AES-GCM crypter
func AESGCM(
	conn io.ReadWriter,
	kg key.Key,
	keySize int,
	mark marker.Marker,
	markLock *sync.Mutex,
) (io.ReadWriter, error) {
	keyValue, keyErr := kg.Get(keySize)

	if keyErr != nil {
		return nil, keyErr
	}

	blockCipher, blockCipherErr := aes.NewCipher(keyValue)

	if blockCipherErr != nil {
		return nil, blockCipherErr
	}

	gcmEncrypter, gcmEncrypterErr := cipher.NewGCMWithNonceSize(
		blockCipher, nonceSize)

	if gcmEncrypterErr != nil {
		return nil, gcmEncrypterErr
	}

	gcmDecrypter, gcmDecrypterErr := cipher.NewGCMWithNonceSize(
		blockCipher, nonceSize)

	if gcmDecrypterErr != nil {
		return nil, gcmDecrypterErr
	}

	return &aesgcm{
		rw:                   conn,
		block:                blockCipher,
		decrypter:            gcmDecrypter,
		decrypterInited:      false,
		decryptBuf:           nil,
		decryptCipherTextBuf: nil,
		decryptReader:        bytes.NewReader(nil),
		decryptNonceBuf:      [nonceSize]byte{},
		encrypter:            gcmEncrypter,
		encrypterInited:      false,
		encrypterNonceBuf:    [nonceSize]byte{},
		decryptMarker:        mark,
		decryptMarkerLock:    markLock,
	}, nil
}

func (a *aesgcm) Read(b []byte) (int, error) {
	if a.decryptReader.Len() > 0 {
		return a.decryptReader.Read(b)
	} else if !a.decrypterInited {
		_, rErr := io.ReadFull(a.rw, a.decryptNonceBuf[:])

		if rErr != nil {
			return 0, rErr
		}

		a.decryptMarkerLock.Lock()
		markErr := a.decryptMarker.Mark(marker.Mark(a.decryptNonceBuf[:]))
		a.decryptMarkerLock.Unlock()

		if markErr != nil {
			return 0, markErr
		}

		a.decrypterInited = true
	}

	sizeCipherTextReadLen := a.decrypter.Overhead() + 2

	if len(a.decryptCipherTextBuf) < sizeCipherTextReadLen {
		a.decryptCipherTextBuf = make([]byte, sizeCipherTextReadLen)
	}

	_, rErr := io.ReadFull(
		a.rw, a.decryptCipherTextBuf[:sizeCipherTextReadLen])

	if rErr != nil {
		return 0, rErr
	}

	sizeData, sizeDataOpenErr := a.decrypter.Open(
		nil,
		a.decryptNonceBuf[:],
		a.decryptCipherTextBuf[:sizeCipherTextReadLen],
		nil)

	if sizeDataOpenErr != nil {
		return 0, sizeDataOpenErr
	}

	if len(sizeData) != 2 {
		return 0, ErrInvalidSizeDataLength
	}

	size := uint16(0)

	size |= uint16(sizeData[0])
	size <<= 8
	size |= uint16(sizeData[1])

	if size > maxDataBlockSize {
		return 0, ErrDataBlockTooLarge
	}

	actualCipherTextReadLen := a.decrypter.Overhead() + int(size)

	if len(a.decryptCipherTextBuf) < actualCipherTextReadLen {
		a.decryptCipherTextBuf = make([]byte, actualCipherTextReadLen)
	}

	_, rErr = io.ReadFull(
		a.rw, a.decryptCipherTextBuf[:actualCipherTextReadLen])

	if rErr != nil {
		return 0, rErr
	}

	dataData, dataOpenErr := a.decrypter.Open(
		nil,
		a.decryptNonceBuf[:],
		a.decryptCipherTextBuf[:actualCipherTextReadLen],
		nil)

	if dataOpenErr != nil {
		return 0, dataOpenErr
	}

	a.decryptReader = bytes.NewReader(dataData)

	return a.decryptReader.Read(b)
}

func (a *aesgcm) Write(b []byte) (int, error) {
	if !a.encrypterInited {
		_, rErr := rand.Read(a.encrypterNonceBuf[:])

		if rErr != nil {
			return 0, rErr
		}

		_, wErr := rw.WriteFull(a.rw, a.encrypterNonceBuf[:])

		if wErr != nil {
			return 0, wErr
		}

		a.encrypterInited = true
	}

	bLen := len(b)
	start := 0
	sizeBuf := [2]byte{}

	for start < bLen {
		end := start + maxDataBlockSize

		if end > bLen {
			end = bLen
		}

		writeLen := uint16(end - start)
		sizeBuf[0] = byte(uint16(writeLen) >> 8)
		sizeBuf[1] = byte((uint16(writeLen) << 8) >> 8)

		_, rErr := rw.WriteFull(a.rw, a.encrypter.Seal(
			nil, a.encrypterNonceBuf[:], sizeBuf[:], nil))

		if rErr != nil {
			return start, rErr
		}

		_, rErr = rw.WriteFull(a.rw, a.encrypter.Seal(
			nil, a.encrypterNonceBuf[:], b[start:end], nil))

		if rErr != nil {
			return start, rErr
		}

		start = end
	}

	return start, nil
}
