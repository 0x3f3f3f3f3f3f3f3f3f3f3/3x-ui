package dokodemo

import (
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
)

// The full-datagram hub tags an empty raw packet with its source so pipes keep
// it. A fixed-target forwarder must replace that placeholder with the dispatch
// destination. Original destinations from transparent forwarding retain their
// existing metadata and do not use this reader.
type emptyDatagramReader struct {
	buf.Reader
	source      net.Destination
	destination net.Destination
}

func (r *emptyDatagramReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	packets, err := r.Reader.ReadMultiBuffer()
	for _, packet := range packets {
		if packet.IsEmpty() && packet.UDP != nil && *packet.UDP == r.source {
			// Outbounds may resolve or rewrite packet metadata. Give each packet
			// its own destination rather than sharing mutable reader state.
			destination := r.destination
			packet.UDP = &destination
		}
	}
	return packets, err
}

func (r *emptyDatagramReader) Interrupt()   { common.Interrupt(r.Reader) }
func (r *emptyDatagramReader) Close() error { return common.Close(r.Reader) }
