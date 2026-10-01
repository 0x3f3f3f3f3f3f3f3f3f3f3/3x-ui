package mieru

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"

	miCommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/transport"
)

const maxPacketDestinations = 64

// Parse framing ourselves to preserve domain targets and complete datagrams.
// The official UDPAssociateWrapper rejects domain replies and truncates to the
// caller's buffer; the transport tunnel still uses the official wire framing.
func decodePacket(packet []byte) (xnet.Destination, []byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return xnet.Destination{}, nil, errors.New("invalid or fragmented mieru UDP packet")
	}
	reader := bytes.NewReader(packet[3:])
	var address model.AddrSpec
	if err := address.ReadFromSocks5(reader); err != nil {
		return xnet.Destination{}, nil, err
	}
	destination, err := requestDestination(address, true)
	if err != nil {
		return xnet.Destination{}, nil, err
	}
	return destination, packet[len(packet)-reader.Len():], nil
}

func encodePacket(destination xnet.Destination, payload []byte) ([]byte, error) {
	address := model.AddrSpec{Port: int(destination.Port)}
	if destination.Address.Family().IsIP() {
		address.IP = destination.Address.IP()
	} else {
		address.FQDN = destination.Address.Domain()
	}
	var packet bytes.Buffer
	packet.Write([]byte{0, 0, 0})
	if err := address.WriteToSocks5(&packet); err != nil {
		return nil, err
	}
	packet.Write(payload)
	if packet.Len() > 65535 {
		return nil, errors.New("mieru UDP frame exceeds maximum length")
	}
	return packet.Bytes(), nil
}

type packetFlow struct {
	link   *transport.Link
	cancel context.CancelFunc
}

func closePacketFlow(flow packetFlow) {
	flow.cancel()
	_ = common.Close(flow.link.Writer)
	common.Interrupt(flow.link.Reader)
}

func (s *Server) servePackets(ctx context.Context, conn net.Conn, user *protocol.MemoryUser) error {
	tunnel := miCommon.NewPacketOverStreamTunnel(conn)
	flows := make(map[string]packetFlow)
	var responses sync.WaitGroup
	var writeMu sync.Mutex
	defer func() {
		for _, flow := range flows {
			closePacketFlow(flow)
		}
		responses.Wait()
	}()
	packet := make([]byte, 65535)
	for {
		n, err := tunnel.Read(packet)
		if err != nil {
			return err
		}
		destination, payload, err := decodePacket(packet[:n])
		if err != nil {
			return err
		}
		key := destination.String()
		flow, exists := flows[key]
		if !exists {
			if len(flows) >= maxPacketDestinations {
				return errors.New("mieru UDP association destination limit reached")
			}
			flowCtx, cancel := context.WithCancel(s.sessionContext(ctx, conn, user))
			link, err := s.dispatcher.Dispatch(flowCtx, destination)
			if err != nil {
				cancel()
				return err
			}
			flow = packetFlow{link: link, cancel: cancel}
			flows[key] = flow
			responses.Add(1)
			go func(link *transport.Link, peer xnet.Destination) {
				defer responses.Done()
				for {
					mb, err := link.Reader.ReadMultiBuffer()
					for _, buffer := range mb {
						source := peer
						if buffer.UDP != nil {
							source = *buffer.UDP
						}
						frame, encodeErr := encodePacket(source, buffer.Bytes())
						if encodeErr != nil {
							buf.ReleaseMulti(mb)
							_ = conn.Close()
							return
						}
						writeMu.Lock()
						_, writeErr := tunnel.Write(frame)
						writeMu.Unlock()
						if writeErr != nil {
							buf.ReleaseMulti(mb)
							_ = conn.Close()
							return
						}
					}
					buf.ReleaseMulti(mb)
					if err != nil {
						return
					}
				}
			}(link, destination)
		}
		buffer := buf.NewWithSize(int32(len(payload)))
		_, _ = buffer.Write(payload)
		buffer.UDP = &destination
		if err := flow.link.Writer.WriteMultiBuffer(buf.MultiBuffer{buffer}); err != nil {
			return err
		}
	}
}

type packetReader struct {
	tunnel *miCommon.PacketOverStreamTunnel
}

func (r *packetReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	frame := make([]byte, 65535)
	n, err := r.tunnel.Read(frame)
	if err != nil {
		return nil, err
	}
	destination, payload, err := decodePacket(frame[:n])
	if err != nil {
		return nil, err
	}
	buffer := buf.NewWithSize(int32(len(payload)))
	_, _ = buffer.Write(payload)
	buffer.UDP = &destination
	return buf.MultiBuffer{buffer}, nil
}

type packetWriter struct {
	tunnel *miCommon.PacketOverStreamTunnel
	target xnet.Destination
}

func (w *packetWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	defer buf.ReleaseMulti(mb)
	for _, buffer := range mb {
		destination := w.target
		if buffer.UDP != nil {
			destination = *buffer.UDP
		}
		packet, err := encodePacket(destination, buffer.Bytes())
		if err != nil {
			return err
		}
		if n, err := w.tunnel.Write(packet); err != nil {
			return err
		} else if n != len(packet) {
			return io.ErrShortWrite
		}
	}
	return nil
}
