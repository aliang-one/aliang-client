package tls

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"aliang.one/nursorgate/common/logger"
)

func parseSNIFromBuffer(buf []byte) (string, []byte, error) {
	// Ensure this looks like a TLS ClientHello record.
	if len(buf) < 5 || buf[0] != 0x16 || buf[1] != 0x03 {
		return "", nil, fmt.Errorf("not a valid TLS ClientHello")
	}

	handshakeLength := int(binary.BigEndian.Uint16(buf[3:5])) + 5
	if handshakeLength > len(buf) {
		return "", nil, fmt.Errorf("incomplete TLS handshake")
	}

	// Skip TLS record header (5 bytes) and handshake type (1 byte).
	pos := 6
	if pos >= len(buf) {
		return "", nil, fmt.Errorf("invalid handshake length")
	}

	// Skip handshake length (3 bytes), TLS version (2 bytes), and random (32 bytes).
	pos += 3 + 2 + 32
	if pos >= len(buf) {
		return "", nil, fmt.Errorf("invalid handshake structure")
	}

	sessionIDLength := int(buf[pos])
	pos += 1 + sessionIDLength
	if pos >= len(buf) {
		return "", nil, fmt.Errorf("invalid session ID")
	}

	if pos+2 > len(buf) {
		return "", nil, fmt.Errorf("invalid cipher suites length")
	}
	cipherSuitesLength := int(binary.BigEndian.Uint16(buf[pos:]))
	pos += 2 + cipherSuitesLength
	if pos >= len(buf) {
		return "", nil, fmt.Errorf("invalid cipher suites")
	}

	compressionMethodsLength := int(buf[pos])
	pos += 1 + compressionMethodsLength
	if pos >= len(buf) {
		return "", nil, fmt.Errorf("invalid compression methods")
	}

	if pos+2 > len(buf) {
		return "", nil, fmt.Errorf("invalid extension length")
	}
	extensionsLength := int(binary.BigEndian.Uint16(buf[pos:]))
	pos += 2
	if pos+extensionsLength > len(buf) {
		return "", nil, fmt.Errorf("invalid extensions")
	}

	for pos+4 <= len(buf) {
		extType := binary.BigEndian.Uint16(buf[pos:])
		extLen := int(binary.BigEndian.Uint16(buf[pos+2:]))
		pos += 4
		if pos+extLen > len(buf) {
			return "", nil, fmt.Errorf("invalid extension data")
		}

		if extType == 0x0000 {
			if pos+2 > len(buf) {
				return "", nil, fmt.Errorf("invalid SNI list length")
			}
			pos += 2 // Skip SNI list length.

			if pos+1 > len(buf) {
				return "", nil, fmt.Errorf("invalid SNI type")
			}
			nameType := buf[pos]
			pos++
			if nameType != 0x00 {
				return "", nil, fmt.Errorf("unsupported SNI type")
			}

			if pos+2 > len(buf) {
				return "", nil, fmt.Errorf("invalid SNI host length")
			}
			nameLen := int(binary.BigEndian.Uint16(buf[pos:]))
			pos += 2
			if pos+nameLen > len(buf) {
				return "", nil, fmt.Errorf("invalid SNI hostname")
			}

			serverName := string(buf[pos : pos+nameLen])
			return serverName, buf, nil
		}

		pos += extLen
	}

	return "", nil, fmt.Errorf("SNI not found")
}

func ExtractSNI(conn net.Conn) (string, []byte, error) {
	var totalBuf []byte
	buf := make([]byte, 4096)

	logger.Debug("[TLS SNI] Waiting for ClientHello...")

	for {
		n, err := conn.Read(buf)
		if n > 0 {
			totalBuf = append(totalBuf, buf[:n]...)
			logger.Debug(fmt.Sprintf("[TLS SNI] Read %d bytes, total: %d bytes", n, len(totalBuf)))
		}

		if len(totalBuf) >= 5 {
			if totalBuf[0] != 0x16 {
				logger.Warn(fmt.Sprintf("[TLS SNI] Not a TLS handshake: first byte is 0x%x (expected 0x16)", totalBuf[0]))
				return "", totalBuf, fmt.Errorf("not a TLS handshake: first byte is 0x%x", totalBuf[0])
			}
			recordLen := int(binary.BigEndian.Uint16(totalBuf[3:5]))
			expectedLen := recordLen + 5
			if len(totalBuf) >= expectedLen {
				logger.Debug(fmt.Sprintf("[TLS SNI] Got complete TLS record (%d bytes), parsing...", expectedLen))
				break
			}
		}

		if err != nil {
			if err == io.EOF {
				if len(totalBuf) < 5 {
					logger.Warn(fmt.Sprintf("[TLS SNI] EOF before complete TLS ClientHello (received %d bytes)", len(totalBuf)))
					return "", nil, fmt.Errorf("EOF before complete TLS ClientHello len: %d", len(totalBuf))
				}
				logger.Debug("[TLS SNI] EOF received, attempting partial parse")
				break
			}
			logger.Warn(fmt.Sprintf("[TLS SNI] Read error while waiting for ClientHello: %v", err))
			return "", nil, err
		}
	}

	sni, _, err := parseSNIFromBuffer(totalBuf)
	if err != nil {
		logger.Warn(fmt.Sprintf("[TLS SNI] Failed to parse SNI from ClientHello: %v (buffer size: %d bytes)", err, len(totalBuf)))
		return "", totalBuf, err
	}

	logger.Debug(fmt.Sprintf("[TLS SNI] Successfully extracted SNI: %s", sni))
	return sni, totalBuf, nil
}
