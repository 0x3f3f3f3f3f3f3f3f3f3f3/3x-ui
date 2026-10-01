package mieru

import (
	"errors"

	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tcp"
)

func ValidateNativeStream(stream *internet.MemoryStreamConfig) error {
	if stream == nil {
		return nil
	}
	if stream.ProtocolName != "tcp" || stream.SecurityType != "" || stream.TcpmaskManager != nil || stream.UdpmaskManager != nil || stream.QuicParams != nil || stream.DownloadSettings != nil || stream.Destination != nil {
		return errors.New("mieru native transport does not support Xray stream wrappers")
	}
	config, ok := stream.ProtocolSettings.(*tcp.Config)
	if !ok || config.HeaderSettings != nil || config.AcceptProxyProtocol || stream.SocketSettings != nil && stream.SocketSettings.AcceptProxyProtocol {
		return errors.New("mieru native transport does not support TCP headers or PROXY framing")
	}
	return nil
}
