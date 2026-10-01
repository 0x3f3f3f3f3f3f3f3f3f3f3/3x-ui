package snell

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/apernet/quic-go/quicvarint"
	B "github.com/sagernet/sing/common/buf"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/singbridge"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

// isQUICInitial checks the public v1/v2 long-header structure before choosing
// native UDP. Payload authentication remains with the actual QUIC endpoints.
func isQUICInitial(packet []byte) bool {
	if len(packet) < 1200 || packet[0]&0xc0 != 0xc0 {
		return false
	}
	version := binary.BigEndian.Uint32(packet[1:5])
	if version == 1 && packet[0]&0x30 != 0 || version == 0x6b3343cf && packet[0]&0x30 != 0x10 || version != 1 && version != 0x6b3343cf {
		return false
	}
	offset := 5
	for range 2 {
		if offset >= len(packet) || packet[offset] > 20 {
			return false
		}
		offset += 1 + int(packet[offset])
	}
	if offset >= len(packet) {
		return false
	}
	token, used, err := quicvarint.Parse(packet[offset:])
	offset += used
	if err != nil || token > uint64(len(packet)-offset) {
		return false
	}
	offset += int(token)
	length, used, err := quicvarint.Parse(packet[offset:])
	offset += used
	return err == nil && length >= 17 && length <= uint64(len(packet)-offset)
}

type v5PacketFlow struct {
	raw    net.Conn
	packet N.PacketConn
	quic   bool
}

// Each target gets a distinct server source socket. Official QUIC associates a
// source with its first target, so sharing it across targets would misroute raw
// traffic. These connections never enter a cross-request reuse pool.
func (o *Outbound) processV5Packets(ctx context.Context, link *transport.Link, dialer internet.Dialer, destination X.Destination) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var ownedMu sync.Mutex
	var owned []net.Conn
	closeOwned := func() {
		ownedMu.Lock()
		for _, c := range owned {
			_ = c.Close()
		}
		ownedMu.Unlock()
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
	}
	stop := context.AfterFunc(ctx, closeOwned)
	defer stop()
	defer closeOwned()
	// Own the request before reading: an idle link has no physical socket yet.
	request := &packetRequest{cancel: cancel}
	o.mu.Lock()
	if o.closed || len(o.requests) >= 128 {
		o.mu.Unlock()
		return errors.New("Snell outbound packet request closed or limit exceeded")
	}
	var untrack func()
	if in := session.InboundFromContext(ctx); in != nil && in.User != nil {
		var err error
		untrack, err = in.User.TrackSession(cancel)
		if err != nil {
			o.mu.Unlock()
			return err
		}
	}
	o.requests[request] = struct{}{}
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.requests, request)
		o.mu.Unlock()
		if untrack != nil {
			untrack()
		}
	}()
	established := make(chan struct{})
	updates := make(activity, 1)
	done := make(chan copyResult, 2)
	uploadFinished := make(chan struct{})
	replyError := make(chan error, 1)
	var replies sync.WaitGroup
	var writeMu sync.Mutex
	go func() {
		defer close(uploadFinished)
		started := false
		flows := make(map[X.Destination]*v5PacketFlow)
		for {
			mb, readErr := link.Reader.ReadMultiBuffer()
			for j, b := range mb {
				if b == nil {
					continue
				}
				target := destination
				if b.UDP != nil {
					target = *b.UDP
				}
				if !validQUICTarget(target) {
					buf.ReleaseMulti(mb[j:])
					done <- copyResult{upload: true, err: errors.New("invalid Snell UDP target")}
					return
				}
				flow := flows[target]
				first := flow == nil
				if flow == nil {
					if len(flows) >= maxPacketDestinations {
						buf.ReleaseMulti(mb[j:])
						done <- copyResult{upload: true, err: errors.New("Snell UDP destination limit exceeded")}
						return
					}
					flow = &v5PacketFlow{quic: isQUICInitial(b.Bytes())}
					server := o.server
					if flow.quic {
						server.Network = X.Network_UDP
					}
					var err error
					dialCtx, cancelDial := context.WithTimeout(ctx, timeouts(ctx).Handshake)
					flow.raw, err = o.openTo(dialCtx, dialer, server)
					cancelDial()
					if err == nil {
						ownedMu.Lock()
						if ctx.Err() != nil {
							err = ctx.Err()
							_ = flow.raw.Close()
						} else {
							owned = append(owned, flow.raw)
						}
						ownedMu.Unlock()
					}
					if err == nil && !flow.quic {
						flow.packet, err = o.method.DialPacketConn(flow.raw)
					}
					if err == nil {
						err = flow.raw.SetWriteDeadline(time.Now().Add(timeouts(ctx).Handshake))
					}
					if err != nil {
						buf.ReleaseMulti(mb[j:])
						done <- copyResult{upload: true, err: err}
						return
					}
					flows[target] = flow
					replies.Add(1)
					go func(flow *v5PacketFlow, target X.Destination) {
						defer replies.Done()
						for {
							b := buf.NewWithSize(buf.MaxDatagramSize)
							var err error
							if flow.quic {
								var n int
								n, err = flow.raw.Read(b.Extend(buf.MaxDatagramSize))
								b.Resize(0, int32(n))
								// Raw QUIC replies contain no target-address field.
								// Retain the verified request target; never invent a
								// resolved IP for a remotely resolved domain.
								b.UDP = &target
							} else {
								p := B.NewSize(maxDatagramSize)
								var response X.Destination
								addr, readErr := flow.packet.ReadPacket(p)
								err = readErr
								if err == nil {
									response, err = singbridge.ToDestination(addr, X.Network_UDP)
									_, _ = b.Write(p.Bytes())
									b.UDP = &response
								}
								p.Release()
							}
							if err != nil {
								b.Release()
								select {
								case replyError <- err:
								default:
								}
								cancel()
								return
							}
							writeMu.Lock()
							err = link.Writer.WriteMultiBuffer(buf.MultiBuffer{b})
							writeMu.Unlock()
							if err != nil {
								select {
								case replyError <- err:
								default:
								}
								cancel()
								return
							}
							updates.Update()
						}
					}(flow, target)
				}
				var err error
				if flow.quic {
					packet := b.Bytes()
					if isQUICInitial(packet) {
						packet, err = encodeQUICEnvelope([]byte(o.config.Psk), target, packet)
					} else if len(packet) == 0 || packet[0]&quicFixedBit == 0 {
						err = errors.New("Snell QUIC raw datagram requires the fixed bit")
					}
					if err == nil {
						var n int
						n, err = flow.raw.Write(packet)
						if err == nil && n != len(packet) {
							err = io.ErrShortWrite
						}
					}
				} else {
					p := packetBuffer(b.Bytes(), flow.packet)
					err = flow.packet.WritePacket(p, singbridge.ToSocksaddr(target))
				}
				b.Release()
				mb[j] = nil
				if err == nil && first {
					err = flow.raw.SetWriteDeadline(time.Time{})
				}
				if err != nil {
					buf.ReleaseMulti(mb[j+1:])
					done <- copyResult{upload: true, err: err}
					return
				}
				if !started {
					started = true
					close(established)
				}
				updates.Update()
			}
			if readErr != nil {
				if readErr == io.EOF {
					readErr = nil
				}
				done <- copyResult{upload: true, err: readErr}
				return
			}
		}
	}()
	go func() {
		<-uploadFinished
		replies.Wait()
		var err error
		select {
		case err = <-replyError:
		default:
		}
		done <- copyResult{err: err}
	}()
	return awaitCopiesEstablished(ctx, cancel, done, updates, established)
}
