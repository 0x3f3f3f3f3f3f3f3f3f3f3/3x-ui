package scenarios

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/log"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common"
	clog "github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/dokodemo"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/xtls/xray-core/proxy/vmess"
	"github.com/xtls/xray-core/proxy/vmess/inbound"
	"github.com/xtls/xray-core/proxy/vmess/outbound"
	"github.com/xtls/xray-core/testing/servers/tcp"
	"github.com/xtls/xray-core/testing/servers/udp"
	"golang.org/x/sync/errgroup"
)

func pickTunnelPortRange(t *testing.T, network string, count uint32) uint32 {
	t.Helper()
	for range 128 {
		var held []io.Closer
		var first uint32
		for offset := uint32(0); offset < count; offset++ {
			address := "127.0.0.1:0"
			if offset != 0 {
				address = fmt.Sprintf("127.0.0.1:%d", first+offset)
			}
			if network == "tcp" {
				listener, err := net.Listen("tcp4", address)
				if err != nil {
					break
				}
				held = append(held, listener)
				if offset == 0 {
					first = uint32(listener.Addr().(*net.TCPAddr).Port)
				}
			} else {
				listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.LocalHostIP.IP(), Port: int(first + offset)})
				if err != nil {
					break
				}
				held = append(held, listener)
				if offset == 0 {
					first = uint32(listener.LocalAddr().(*net.UDPAddr).Port)
				}
			}
			if first+count-1 > 65535 {
				break
			}
		}
		for _, listener := range held {
			listener.Close()
		}
		if uint32(len(held)) == count {
			return first
		}
	}
	t.Fatal("could not reserve the complete Tunnel port range")
	return 0
}

func TestDokodemoTCP(t *testing.T) {
	tcpServer := tcp.Server{
		MsgProcessor: xor,
	}
	dest, err := tcpServer.Start()
	common.Must(err)
	defer tcpServer.Close()

	userID := protocol.NewID(uuid.New())
	serverPort := tcp.PickPort()
	serverConfig := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&log.Config{
				ErrorLogLevel: clog.Severity_Debug,
				ErrorLogType:  log.LogType_Console,
			}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortList: &net.PortList{Range: []*net.PortRange{net.SinglePortRange(serverPort)}},
					Listen:   net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&inbound.Config{
					User: []*protocol.User{
						{
							Account: serial.ToTypedMessage(&vmess.Account{
								Id: userID.String(),
							}),
						},
					},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&freedom.Config{
					FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
				}),
			},
		},
	}
	server, err := InitializeServerConfig(serverConfig)
	common.Must(err)
	defer CloseServer(server)

	clientPortRange := uint32(5)
	retry := 1
	clientPort := pickTunnelPortRange(t, "tcp", clientPortRange+1)
	for {
		clientConfig := &core.Config{
			App: []*serial.TypedMessage{
				serial.ToTypedMessage(&log.Config{
					ErrorLogLevel: clog.Severity_Debug,
					ErrorLogType:  log.LogType_Console,
				}),
			},
			Inbound: []*core.InboundHandlerConfig{
				{
					ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
						PortList: &net.PortList{Range: []*net.PortRange{{From: clientPort, To: clientPort + clientPortRange}}},
						Listen:   net.NewIPOrDomain(net.LocalHostIP),
					}),
					ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
						RewriteAddress:  net.NewIPOrDomain(dest.Address),
						RewritePort:     uint32(dest.Port),
						AllowedNetworks: []net.Network{net.Network_TCP},
					}),
				},
			},
			Outbound: []*core.OutboundHandlerConfig{
				{
					ProxySettings: serial.ToTypedMessage(&outbound.Config{
						Receiver: &protocol.ServerEndpoint{
							Address: net.NewIPOrDomain(net.LocalHostIP),
							Port:    uint32(serverPort),
							User: &protocol.User{
								Account: serial.ToTypedMessage(&vmess.Account{
									Id: userID.String(),
								}),
							},
						},
					}),
				},
			},
		}

		server, _ := InitializeServerConfig(clientConfig)
		if server != nil && WaitConnAvailableWithTest(t, testTCPConn(net.Port(clientPort), 1024, time.Second*2)) {
			defer CloseServer(server)
			break
		}
		if server != nil {
			CloseServer(server)
		}
		retry++
		if retry > 5 {
			t.Fatal("All attempts failed to start client")
		}
		clientPort = pickTunnelPortRange(t, "tcp", clientPortRange+1)
	}

	for port := clientPort; port <= clientPort+clientPortRange; port++ {
		if err := testTCPConn(net.Port(port), 1024, time.Second*2)(); err != nil {
			t.Error(err)
		}
	}
}

func TestDokodemoUDP(t *testing.T) {
	udpServer := udp.Server{
		MsgProcessor: xor,
	}
	dest, err := udpServer.Start()
	common.Must(err)
	defer udpServer.Close()

	userID := protocol.NewID(uuid.New())
	serverPort := tcp.PickPort()
	serverConfig := &core.Config{
		Inbound: []*core.InboundHandlerConfig{
			{
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortList: &net.PortList{Range: []*net.PortRange{net.SinglePortRange(serverPort)}},
					Listen:   net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&inbound.Config{
					User: []*protocol.User{
						{
							Account: serial.ToTypedMessage(&vmess.Account{
								Id: userID.String(),
							}),
						},
					},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&freedom.Config{
					FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
				}),
			},
		},
	}
	server, err := InitializeServerConfig(serverConfig)
	common.Must(err)
	defer CloseServer(server)

	clientPortRange := uint32(3)
	retry := 1
	clientPort := pickTunnelPortRange(t, "udp", clientPortRange+1)
	for {
		clientConfig := &core.Config{
			Inbound: []*core.InboundHandlerConfig{
				{
					ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
						PortList: &net.PortList{Range: []*net.PortRange{{From: clientPort, To: clientPort + clientPortRange}}},
						Listen:   net.NewIPOrDomain(net.LocalHostIP),
					}),
					ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
						RewriteAddress:  net.NewIPOrDomain(dest.Address),
						RewritePort:     uint32(dest.Port),
						AllowedNetworks: []net.Network{net.Network_UDP},
					}),
				},
			},
			Outbound: []*core.OutboundHandlerConfig{
				{
					ProxySettings: serial.ToTypedMessage(&outbound.Config{
						Receiver: &protocol.ServerEndpoint{
							Address: net.NewIPOrDomain(net.LocalHostIP),
							Port:    uint32(serverPort),
							User: &protocol.User{
								Account: serial.ToTypedMessage(&vmess.Account{
									Id: userID.String(),
								}),
							},
						},
					}),
				},
			},
		}

		server, _ := InitializeServerConfig(clientConfig)
		if server != nil && WaitConnAvailableWithTest(t, testUDPConn(net.Port(clientPort), 1024, time.Second*2)) {
			defer CloseServer(server)
			break
		}
		if server != nil {
			CloseServer(server)
		}
		retry++
		if retry > 5 {
			t.Fatal("All attempts failed to start client")
		}
		clientPort = pickTunnelPortRange(t, "udp", clientPortRange+1)
	}

	var errg errgroup.Group
	for port := clientPort; port <= clientPort+clientPortRange; port++ {
		errg.Go(testUDPConn(net.Port(port), 1024, time.Second*5))
	}
	if err := errg.Wait(); err != nil {
		t.Error(err)
	}
}
