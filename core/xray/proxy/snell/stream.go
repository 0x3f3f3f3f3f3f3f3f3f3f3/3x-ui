package snell

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tcp"
)

func timeouts(ctx context.Context) policy.Timeout {
	if instance := core.FromContext(ctx); instance != nil {
		if manager, ok := instance.GetFeature(policy.ManagerType()).(policy.Manager); ok {
			var level uint32
			if in := session.InboundFromContext(ctx); in != nil && in.User != nil {
				level = in.User.Level
			}
			return manager.ForLevel(level).Timeouts
		}
	}
	return policy.SessionDefault().Timeouts
}

type (
	copyResult struct {
		upload bool
		err    error
	}
	activity chan struct{}
)

func (a activity) Update() {
	select {
	case a <- struct{}{}:
	default:
	}
}

func awaitCopies(ctx context.Context, cancel context.CancelFunc, done <-chan copyResult, updates activity) error {
	plcy := timeouts(ctx)
	duration := plcy.ConnectionIdle
	timer := time.NewTimer(duration)
	defer timer.Stop()
	var result error
	remaining := 2
	aborting := false
	cancelled := ctx.Done()
	for remaining > 0 {
		select {
		case r := <-done:
			remaining--
			if result == nil {
				result = r.err
			}
			if r.err != nil && !aborting {
				aborting = true
				cancel()
			}
			if remaining > 0 && !aborting {
				if r.upload {
					duration = plcy.DownlinkOnly
				} else {
					duration = plcy.UplinkOnly
				}
				timer.Reset(duration)
			}
		case <-updates:
			if !aborting {
				timer.Reset(duration)
			}
		case <-cancelled:
			cancelled = nil
			if !aborting {
				aborting = true
				if result == nil {
					result = ctx.Err()
				}
				cancel()
			}
		case <-timer.C:
			if !aborting {
				aborting = true
				if result == nil {
					result = context.DeadlineExceeded
				}
				cancel()
			}
		}
	}
	return result
}

func validateTransport(ctx context.Context) error {
	settings, ok := session.StreamSettingsFromContext(ctx).(*internet.MemoryStreamConfig)
	if !ok || settings == nil {
		return nil
	}
	if settings.ProtocolName != "tcp" || settings.SecurityType != "" || settings.TcpmaskManager != nil || settings.UdpmaskManager != nil || settings.DownloadSettings != nil {
		return errors.New("Snell requires native TCP without Xray transport or security wrappers")
	}
	if c, ok := settings.ProtocolSettings.(*tcp.Config); ok && (c.HeaderSettings != nil || c.AcceptProxyProtocol) {
		return errors.New("Snell does not support a TCP header or PROXY protocol wrapper")
	}
	return nil
}

// copyStream waits for both decoded copy directions before releasing a reused
// logical request. Cancellation always interrupts the actual physical socket.
func copyStream(ctx context.Context, link *transport.Link, c, physical net.Conn, inbound bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { physical.Close(); common.Interrupt(link.Reader); common.Interrupt(link.Writer) })
	defer stop()
	done := make(chan copyResult, 2)
	updates := make(activity, 1)
	go func() {
		err := buf.Copy(link.Reader, buf.NewWriter(c), buf.UpdateActivity(updates))
		if w, ok := c.(interface{ CloseWrite() error }); ok {
			_ = w.CloseWrite()
		}
		done <- copyResult{upload: !inbound, err: err}
	}()
	go func() {
		err := buf.Copy(buf.NewReader(c), link.Writer, buf.UpdateActivity(updates))
		_ = common.Close(link.Writer)
		done <- copyResult{upload: inbound, err: err}
	}()
	err := awaitCopies(ctx, cancel, done, updates)
	common.Interrupt(link.Reader)
	common.Interrupt(link.Writer)
	return err
}
