// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitprotocolio

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

type protocolV1UploadPackResponseState int

const (
	protocolV1UploadPackResponseStateBegin protocolV1UploadPackResponseState = iota
	protocolV1UploadPackResponseStateScanShallows
	protocolV1UploadPackResponseStateScanUnshallows
	protocolV1UploadPackResponseStateBeginAcknowledgements
	protocolV1UploadPackResponseStateScanAcknowledgements
	protocolV1UploadPackResponseStateScanPacks
	protocolV1UploadPackResponseStateEnd
)

// ProtocolV1UploadPackResponseChunk is a chunk of a protocol v1 git-upload-pack
// response.
type ProtocolV1UploadPackResponseChunk struct {
	ShallowObjectID   string
	UnshallowObjectID string
	EndOfShallows     bool
	AckObjectID       string
	AckDetail         string
	Nak               bool
	PackStream        []byte
	EndOfRequest      bool
}

// EncodeToPktLine serializes the chunk.
func (c *ProtocolV1UploadPackResponseChunk) EncodeToPktLine() []byte {
	if c.ShallowObjectID != "" {
		return BytesPacket([]byte(fmt.Sprintf("shallow %s\n", c.ShallowObjectID))).EncodeToPktLine()
	}
	if c.UnshallowObjectID != "" {
		return BytesPacket([]byte(fmt.Sprintf("unshallow %s\n", c.UnshallowObjectID))).EncodeToPktLine()
	}
	if c.EndOfShallows {
		return FlushPacket{}.EncodeToPktLine()
	}
	if c.AckObjectID != "" {
		if c.AckDetail != "" {
			return BytesPacket([]byte(fmt.Sprintf("ACK %s %s\n", c.AckObjectID, c.AckDetail))).EncodeToPktLine()
		}
		return BytesPacket([]byte(fmt.Sprintf("ACK %s\n", c.AckObjectID))).EncodeToPktLine()
	}
	if c.Nak {
		return BytesPacket([]byte("NAK\n")).EncodeToPktLine()
	}
	if len(c.PackStream) != 0 {
		return BytesPacket(c.PackStream).EncodeToPktLine()
	}
	if c.EndOfRequest {
		return FlushPacket{}.EncodeToPktLine()
	}
	panic("impossible chunk")
}

// ProtocolV1UploadPackResponse provides an interface for reading a protocol v1
// git-upload-pack response.
type ProtocolV1UploadPackResponse struct {
	scanner *PacketScanner
	state   protocolV1UploadPackResponseState
	err     error
	curr    *ProtocolV1UploadPackResponseChunk
}

// NewProtocolV1UploadPackResponse returns a new ProtocolV1UploadPackResponse to
// read from rd.
func NewProtocolV1UploadPackResponse(rd io.Reader) *ProtocolV1UploadPackResponse {
	return &ProtocolV1UploadPackResponse{scanner: NewPacketScanner(rd)}
}

// Err returns the first non-EOF error that was encountered by the
// ProtocolV1UploadPackResponse.
func (r *ProtocolV1UploadPackResponse) Err() error {
	return r.err
}

// Chunk returns the most recent chunk generated by a call to Scan.
func (r *ProtocolV1UploadPackResponse) Chunk() *ProtocolV1UploadPackResponseChunk {
	return r.curr
}

// Scan advances the scanner to the next packet. It returns false when the scan
// stops, either by reaching the end of the input or an error. After scan
// returns false, the Err method will return any error that occurred during
// scanning, except that if it was io.EOF, Err will return nil.
func (r *ProtocolV1UploadPackResponse) Scan() bool {
	if r.err != nil || r.state == protocolV1UploadPackResponseStateEnd {
		return false
	}
	if !r.scanner.Scan() {
		r.err = r.scanner.Err()
		if r.err == nil && r.state != protocolV1UploadPackResponseStateBeginAcknowledgements {
			r.err = SyntaxError("early EOF")
		}
		return false
	}
	pkt := r.scanner.Packet()

	switch r.state {
	case protocolV1UploadPackResponseStateBegin, protocolV1UploadPackResponseStateScanShallows:
		if bp, ok := pkt.(BytesPacket); ok {
			if bytes.HasPrefix(bp, []byte("shallow ")) {
				ss := strings.SplitN(strings.TrimSuffix(string(bp), "\n"), " ", 2)
				if len(ss) < 2 {
					r.err = SyntaxError("cannot split shallow: " + string(bp))
					return false
				}
				r.state = protocolV1UploadPackResponseStateScanShallows
				r.curr = &ProtocolV1UploadPackResponseChunk{
					ShallowObjectID: ss[1],
				}
				return true
			}
		}
		fallthrough
	case protocolV1UploadPackResponseStateScanUnshallows:
		if bp, ok := pkt.(BytesPacket); ok {
			if bytes.HasPrefix(bp, []byte("unshallow ")) {
				ss := strings.SplitN(strings.TrimSuffix(string(bp), "\n"), " ", 2)
				if len(ss) < 2 {
					r.err = SyntaxError("cannot split unshallow: " + string(bp))
					return false
				}
				r.state = protocolV1UploadPackResponseStateScanUnshallows
				r.curr = &ProtocolV1UploadPackResponseChunk{
					UnshallowObjectID: ss[1],
				}
				return true
			}
		}
		if _, ok := pkt.(FlushPacket); ok {
			r.state = protocolV1UploadPackResponseStateBeginAcknowledgements
			r.curr = &ProtocolV1UploadPackResponseChunk{
				EndOfShallows: true,
			}
			return true
		}
		fallthrough
	case protocolV1UploadPackResponseStateBeginAcknowledgements, protocolV1UploadPackResponseStateScanAcknowledgements:
		if bp, ok := pkt.(BytesPacket); ok {
			if bytes.HasPrefix(bp, []byte("ACK ")) {
				ss := strings.SplitN(strings.TrimSuffix(string(bp), "\n"), " ", 3)
				if len(ss) < 2 {
					r.err = SyntaxError("cannot split ACK: " + string(bp))
					return false
				}
				detail := ""
				if len(ss) == 3 {
					detail = ss[2]
				}
				r.state = protocolV1UploadPackResponseStateScanAcknowledgements
				r.curr = &ProtocolV1UploadPackResponseChunk{
					AckObjectID: ss[1],
					AckDetail:   detail,
				}
				return true
			}
			if bytes.Equal(bp, []byte("NAK\n")) {
				r.state = protocolV1UploadPackResponseStateScanPacks
				r.curr = &ProtocolV1UploadPackResponseChunk{
					Nak: true,
				}
				return true
			}
		}
		if r.state == protocolV1UploadPackResponseStateBegin {
			r.err = SyntaxError(fmt.Sprintf("unexpected packet: %#v", pkt))
			return false
		}
		fallthrough
	case protocolV1UploadPackResponseStateScanPacks:
		switch p := pkt.(type) {
		case FlushPacket:
			r.state = protocolV1UploadPackResponseStateEnd
			r.curr = &ProtocolV1UploadPackResponseChunk{
				EndOfRequest: true,
			}
			return true
		case BytesPacket:
			r.state = protocolV1UploadPackResponseStateScanPacks
			r.curr = &ProtocolV1UploadPackResponseChunk{
				PackStream: p,
			}
			return true
		default:
			r.err = SyntaxError(fmt.Sprintf("unexpected packet: %#v", p))
			return false
		}
	}
	panic("impossible state")
}
