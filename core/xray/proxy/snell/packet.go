package snell

import (
	"context"
	"errors"
	"sync"
	"time"

	B "github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/singbridge"
	"github.com/xtls/xray-core/transport"
)

const (
	maxDatagramSize       = int(buf.MaxDatagramSize)
	maxPacketDestinations = 64
)

func packetBuffer(payload []byte, writer any) *B.Buffer {
	front, rear := N.CalculateFrontHeadroom(writer), N.CalculateRearHeadroom(writer)
	p := B.NewSize(front + len(payload) + rear)
	p.Resize(front, 0)
	_, _ = p.Write(payload)
	return p
}

func (i *Inbound) NewPacketConnectionEx(ctx context.Context, c N.PacketConn, _ M.Socksaddr, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	err := i.servePackets(ctx, c)
	if onClose != nil {
		onClose(err)
	}
}

func (i *Inbound) servePackets(ctx context.Context, c N.PacketConn) (result error) {
	a := ctx.Value(acceptedKey{}).(accepted)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { a.physical.Close() })
	defer stop()
	links := make(map[X.Destination]*transport.Link)
	var replies sync.WaitGroup
	var writeMu sync.Mutex
	defer func() {
		cancel()
		for _, link := range links {
			common.Interrupt(link.Reader)
			common.Interrupt(link.Writer)
		}
		replies.Wait()
		c.Close()
	}()
	for {
		if err := c.SetReadDeadline(time.Now().Add(timeouts(ctx).ConnectionIdle)); err != nil {
			return err
		}
		packet := B.NewSize(maxDatagramSize)
		dest, err := c.ReadPacket(packet)
		if err != nil {
			packet.Release()
			return err
		}
		destination, err := singbridge.ToDestination(dest, X.Network_UDP)
		if err != nil {
			packet.Release()
			return err
		}
		link := links[destination]
		if link == nil {
			if len(links) >= maxPacketDestinations {
				packet.Release()
				return errors.New("Snell UDP destination limit exceeded")
			}
			flowCtx := requestContext(ctx)
			content := session.ContentFromContext(flowCtx)
			if content == nil {
				content = &session.Content{}
			}
			content.PreserveUDPPacketSource = true
			flowCtx = session.ContextWithContent(flowCtx, content)
			link, err = a.dispatcher.Dispatch(flowCtx, destination)
			if err != nil {
				packet.Release()
				return err
			}
			links[destination] = link
			replies.Add(1)
			go func(link *transport.Link, destination X.Destination) {
				defer replies.Done()
				for {
					mb, readErr := link.Reader.ReadMultiBuffer()
					for j, b := range mb {
						response := destination
						if b.UDP != nil {
							response = *b.UDP
						}
						p := packetBuffer(b.Bytes(), c)
						b.Release()
						mb[j] = nil
						writeMu.Lock()
						writeErr := c.WritePacket(p, singbridge.ToSocksaddr(response))
						writeMu.Unlock()
						if writeErr != nil {
							buf.ReleaseMulti(mb[j+1:])
							cancel()
							return
						}
						if err := c.SetReadDeadline(time.Now().Add(timeouts(ctx).ConnectionIdle)); err != nil {
							buf.ReleaseMulti(mb[j+1:])
							cancel()
							return
						}
					}
					if readErr != nil {
						return
					}
				}
			}(link, destination)
		}
		payload := buf.NewWithSize(int32(packet.Len()))
		_, _ = payload.Write(packet.Bytes())
		packet.Release()
		payload.UDP = &destination
		if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{payload}); err != nil {
			return err
		}
	}
}
