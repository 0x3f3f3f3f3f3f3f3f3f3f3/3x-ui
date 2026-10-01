package snell

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// processQUIC owns one authenticated target per UDP source, matching the
// official v5 listener. The worker supplies whole datagrams and source isolation;
// target dial, DNS, route selection and payload policy belong to Dispatcher.
func (i *Inbound) processQUIC(ctx context.Context, c stat.Connection, dispatcher routing.Dispatcher) error {
	reader, ok := c.(buf.Reader)
	if !ok {
		return errors.New("Snell QUIC requires a native datagram reader")
	}
	i.mu.Lock()
	u := i.user
	if u == nil || i.closed {
		i.mu.Unlock()
		return protocol.ErrCredentialRevoked
	}
	if len(i.physical) >= 128 {
		i.mu.Unlock()
		return errors.New("Snell inbound physical connection limit exceeded")
	}
	physical := &acceptedConnection{Conn: c, owner: i}
	i.physical[physical] = struct{}{}
	i.mu.Unlock()
	defer physical.Close()
	untrack, err := u.TrackSession(func() { physical.Close() })
	if err != nil {
		return err
	}
	defer untrack()
	in := session.Inbound{User: u, Conn: physical, Name: "snell", CanSpliceCopy: 3}
	if inherited := session.InboundFromContext(ctx); inherited != nil {
		in = *inherited
		in.User, in.Conn, in.Name, in.CanSpliceCopy = u, physical, "snell", 3
	}
	ctx = session.ContextWithInbound(ctx, &in)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var linkMu sync.Mutex
	var owned *transport.Link
	closeAssociation := func() {
		// Release physical capacity before the transport exposes closure.
		_ = physical.Close()
		linkMu.Lock()
		link := owned
		linkMu.Unlock()
		if link != nil {
			common.Interrupt(link.Reader)
			common.Interrupt(link.Writer)
		}
	}
	stop := context.AfterFunc(ctx, closeAssociation)
	defer stop()
	defer closeAssociation()
	updates := make(activity, 1)
	authenticated := make(chan struct{}, 1)
	results := make(chan error, 2)
	readDone := make(chan struct{})
	var replies sync.WaitGroup
	psk := []byte(u.Account.(*MemoryAccount).PSK)
	go func() {
		defer close(readDone)
		var target X.Destination
		var link *transport.Link
		for {
			mb, readErr := reader.ReadMultiBuffer()
			for j, b := range mb {
				if b == nil {
					continue
				}
				packet := b.Bytes()
				if err := ctx.Err(); err != nil {
					buf.ReleaseMulti(mb[j:])
					results <- err
					return
				}
				var decoded X.Destination
				inner := packet
				var decodeErr error
				if len(packet) == 0 {
					decodeErr = errors.New("empty Snell QUIC datagram")
				} else if packet[0]&quicFixedBit == 0 {
					decoded, inner, decodeErr = decodeQUICEnvelope(psk, packet)
				} else if link == nil {
					decodeErr = errors.New("Snell QUIC requires an authenticated opening envelope")
				}
				if decodeErr != nil {
					b.Release()
					mb[j] = nil
					if link == nil {
						buf.ReleaseMulti(mb[j+1:])
						results <- decodeErr
						return
					}
					// A malformed retry cannot change a verified binding or renew
					// its idle timer. It also never enters the payload ledger.
					continue
				}
				if link == nil {
					target = decoded
					flowCtx := requestContext(ctx)
					content := session.ContentFromContext(flowCtx)
					if content == nil {
						content = new(session.Content)
					}
					content.PreserveUDPPacketSource = true
					flowCtx = session.ContextWithContent(flowCtx, content)
					link, err = dispatcher.Dispatch(flowCtx, target)
					if err != nil {
						buf.ReleaseMulti(mb[j:])
						results <- err
						return
					}
					linkMu.Lock()
					owned = link
					linkMu.Unlock()
					if err := ctx.Err(); err != nil {
						buf.ReleaseMulti(mb[j:])
						common.Interrupt(link.Reader)
						common.Interrupt(link.Writer)
						results <- err
						return
					}
					authenticated <- struct{}{}
					replies.Add(1)
					go func() {
						defer replies.Done()
						for {
							mb, err := link.Reader.ReadMultiBuffer()
							for j, b := range mb {
								if b == nil {
									continue
								}
								n, writeErr := physical.Write(b.Bytes())
								complete := n == int(b.Len())
								b.Release()
								mb[j] = nil
								if writeErr == nil && !complete {
									writeErr = io.ErrShortWrite
								}
								if writeErr != nil {
									buf.ReleaseMulti(mb[j+1:])
									results <- writeErr
									return
								}
								updates.Update()
							}
							if err != nil {
								results <- err
								return
							}
						}
					}()
				}
				payload := buf.NewWithSize(int32(len(inner)))
				_, _ = payload.Write(inner)
				payload.UDP = &target
				b.Release()
				mb[j] = nil
				if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{payload}); err != nil {
					buf.ReleaseMulti(mb[j+1:])
					results <- err
					return
				}
				updates.Update()
			}
			if readErr != nil {
				results <- readErr
				return
			}
		}
	}()
	plcy := timeouts(ctx)
	duration := plcy.Handshake
	timer := time.NewTimer(duration)
	defer timer.Stop()
	var result error
wait:
	for {
		select {
		case result = <-results:
			break wait
		case <-ctx.Done():
			result = ctx.Err()
			break wait
		case <-timer.C:
			result = context.DeadlineExceeded
			break wait
		case <-authenticated:
			duration = plcy.ConnectionIdle
			timer.Reset(duration)
		case <-updates:
			timer.Reset(duration)
		}
	}
	cancel()
	closeAssociation()
	<-readDone
	replies.Wait()
	return result
}
