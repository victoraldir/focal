package sony

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Live-view packet layout (Camera Remote API reference, "Liveview data
// format"): every packet is an 8-byte common header, a 128-byte payload header,
// the payload, and padding.
const (
	commonHeaderSize  = 8
	payloadHeaderSize = 128
	startByte         = 0xFF
	payloadTypeImage  = 0x01
	maxFrameBytes     = 16 << 20
)

var payloadStartCode = []byte{0x24, 0x35, 0x68, 0x79}

// LiveviewReader decodes the camera's live-view stream into JPEG frames.
type LiveviewReader struct {
	r      io.ReadCloser
	header [commonHeaderSize + payloadHeaderSize]byte
}

// NewLiveviewReader wraps a live-view HTTP body.
func NewLiveviewReader(r io.ReadCloser) *LiveviewReader {
	return &LiveviewReader{r: r}
}

// Next returns the next JPEG frame, skipping frame-information packets (focus
// boxes and the like). Each call returns a fresh slice the caller may keep.
func (l *LiveviewReader) Next() ([]byte, error) {
	for {
		if _, err := io.ReadFull(l.r, l.header[:]); err != nil {
			return nil, err
		}
		common, payload := l.header[:commonHeaderSize], l.header[commonHeaderSize:]
		if common[0] != startByte {
			return nil, fmt.Errorf("live view: bad start byte 0x%02x", common[0])
		}
		if !bytes.Equal(payload[:4], payloadStartCode) {
			return nil, fmt.Errorf("live view: bad payload start code % x", payload[:4])
		}
		size := int(binary.BigEndian.Uint32(append([]byte{0}, payload[4:7]...)))
		padding := int(payload[7])
		if size > maxFrameBytes {
			return nil, fmt.Errorf("live view: implausible payload size %d", size)
		}

		if common[1] != payloadTypeImage {
			if _, err := io.CopyN(io.Discard, l.r, int64(size+padding)); err != nil {
				return nil, err
			}
			continue
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(l.r, frame); err != nil {
			return nil, err
		}
		if padding > 0 {
			if _, err := io.CopyN(io.Discard, l.r, int64(padding)); err != nil {
				return nil, err
			}
		}
		return frame, nil
	}
}

// Close closes the underlying stream.
func (l *LiveviewReader) Close() error { return l.r.Close() }

// EncodeLiveviewPacket builds one live-view packet. It is the inverse of
// LiveviewReader.Next and exists for simulators and tests.
func EncodeLiveviewPacket(w io.Writer, payloadType byte, seq uint16, timestamp uint32, data []byte) error {
	var hdr [commonHeaderSize + payloadHeaderSize]byte
	hdr[0] = startByte
	hdr[1] = payloadType
	binary.BigEndian.PutUint16(hdr[2:4], seq)
	binary.BigEndian.PutUint32(hdr[4:8], timestamp)
	p := hdr[commonHeaderSize:]
	copy(p, payloadStartCode)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	copy(p[4:7], size[1:])
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}
