package udp

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
)

func TestHubFullDatagramsOptInPreservesLargeAndEmpty(t *testing.T) {
	for _, full := range []bool{false, true} {
		name := "default"
		if full {
			name = "native-opt-in"
		}
		t.Run(name, func(t *testing.T) {
			var options []HubOption
			if full {
				options = append(options, HubFullDatagrams())
			}
			hub, err := ListenUDP(context.Background(), X.LocalHostIP, 0, &internet.MemoryStreamConfig{}, options...)
			if err != nil {
				t.Fatal(err)
			}
			defer hub.Close()
			client, err := net.Dial("udp", hub.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			large := bytes.Repeat([]byte{0x71}, 13000)
			if _, err = client.Write(large); err != nil {
				t.Fatal(err)
			}
			select {
			case packet := <-hub.Receive():
				defer packet.Payload.Release()
				expected := int(buf.Size)
				if full {
					expected = len(large)
				}
				if len(packet.Payload.Bytes()) != expected || !bytes.Equal(packet.Payload.Bytes(), large[:expected]) {
					t.Errorf("full=%v datagram lost bytes: got=%d want=%d", full, packet.Payload.Len(), expected)
				}
				if packet.Source != X.DestinationFromAddr(client.LocalAddr()) {
					t.Fatal("hub changed authenticated association source address")
				}
			case <-time.After(time.Second):
				t.Fatal("hub did not receive large datagram")
			}
			if _, err = client.Write(nil); err != nil {
				t.Fatal(err)
			}
			select {
			case packet := <-hub.Receive():
				defer packet.Payload.Release()
				if !full {
					t.Fatal("default hub empty-packet behavior changed")
				}
				if packet.Payload.Len() != 0 || (buf.MultiBuffer{packet.Payload}).IsEmpty() {
					t.Fatal("native hub lost empty datagram metadata")
				}
			case <-time.After(150 * time.Millisecond):
				if full {
					t.Fatal("native hub dropped empty datagram")
				}
			}
		})
	}
}
