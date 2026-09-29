package dispatcher_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	vlessencoding "github.com/xtls/xray-core/proxy/vless/encoding"
	"github.com/xtls/xray-core/proxy/vmess"
	"github.com/xtls/xray-core/proxy/vmess/aead"
	"github.com/xtls/xray-core/transport"
)

type credentialValidator struct {
	account proto.Message
	add     func(*protocol.MemoryUser) error
	auth    func(*protocol.MemoryUser) *protocol.MemoryUser
	remove  func(string) error
}

func testCredentialValidator(t *testing.T, name string) credentialValidator {
	t.Helper()
	id := uuid.New()
	switch name {
	case "vless":
		v := new(vless.MemoryValidator)
		return credentialValidator{&vless.Account{Id: id.String()}, v.Add, func(u *protocol.MemoryUser) *protocol.MemoryUser {
			return v.Get(u.Account.(*vless.MemoryAccount).ID.UUID())
		}, v.Del}
	case "vmess":
		v := vmess.NewTimedUserValidator()
		return credentialValidator{&vmess.Account{Id: id.String()}, v.Add, func(u *protocol.MemoryUser) *protocol.MemoryUser {
			id := aead.CreateAuthID(u.Account.(*vmess.MemoryAccount).ID.CmdKey(), time.Now().Unix())
			user, valid, err := v.GetAEAD(id[:])
			if err != nil || !valid {
				t.Fatalf("authenticate VMess: valid=%v err=%v", valid, err)
			}
			return user
		}, func(email string) error {
			if !v.Remove(email) {
				return vmess.ErrNotFound
			}
			return nil
		}}
	case "trojan":
		v := new(trojan.Validator)
		return credentialValidator{&trojan.Account{Password: id.String()}, v.Add, func(u *protocol.MemoryUser) *protocol.MemoryUser {
			return v.Get(hex.EncodeToString(u.Account.(*trojan.MemoryAccount).Key))
		}, v.Del}
	case "shadowsocks":
		v := new(shadowsocks.Validator)
		return credentialValidator{&shadowsocks.Account{Password: id.String(), CipherType: shadowsocks.CipherType_AES_128_GCM}, v.Add, func(u *protocol.MemoryUser) *protocol.MemoryUser {
			packet, err := shadowsocks.EncodeUDPPacket(&protocol.RequestHeader{User: u, Address: net.LocalHostIP, Port: 1234}, []byte("auth"))
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Release()
			request, _, err := shadowsocks.DecodeUDPPacket(v, packet)
			if err != nil {
				t.Fatal(err)
			}
			return request.User
		}, v.Del}
	default:
		t.Fatalf("unknown validator %s", name)
		return credentialValidator{}
	}
}

func (v credentialValidator) addAuthenticated(t *testing.T) *protocol.MemoryUser {
	t.Helper()
	u, err := (&protocol.User{Account: serial.ToTypedMessage(v.account), Email: "credential@example.test", ClientId: "shared-client"}).ToMemoryUser()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.add(u); err != nil {
		t.Fatal(err)
	}
	user := v.auth(u)
	if user == nil {
		t.Fatal("credential failed authentication")
	}
	return user
}

type removeDuringAdmission struct {
	clientpolicy.Manager
	remove func()
}

func (m *removeDuringAdmission) Open(ctx context.Context, metadata clientpolicy.Metadata, closeFn func()) (*clientpolicy.Session, error) {
	s, err := m.Manager.Open(ctx, metadata, closeFn)
	if err == nil && m.remove != nil {
		m.remove()
		m.remove = nil
	}
	return s, err
}

func managedCredentialLink(manager clientpolicy.Manager, user *protocol.MemoryUser, tag string) (context.Context, *transport.Link, func(), error) {
	link := &transport.Link{Reader: buf.NewReader(bytes.NewReader(nil)), Writer: buf.Discard}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user, Tag: tag})
	ctx, release, err := dispatcher.ManageLinkForTest(manager, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
	return ctx, link, release, err
}

func TestRemovedCredentialCannotResumeManagedTraffic(t *testing.T) {
	for _, name := range []string{"vless", "vmess", "trojan", "shadowsocks"} {
		for _, timing := range []string{"before-admission", "during-admission", "active"} {
			t.Run(name+"/"+timing, func(t *testing.T) {
				v := testCredentialValidator(t, name)
				user := v.addAuthenticated(t)
				engine := clientpolicy.NewEngine()
				defer engine.Close()
				if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 2_000_000, BurstBytes: 1024}); err != nil {
					t.Fatal(err)
				}
				var manager clientpolicy.Manager = engine
				siblingValidator := testCredentialValidator(t, name)
				_, sibling, releaseSibling, err := managedCredentialLink(manager, siblingValidator.addAuthenticated(t), "sibling")
				if err != nil {
					t.Fatal(err)
				}
				defer releaseSibling()
				remove := func() {
					if err := v.remove(user.Email); err != nil {
						t.Fatal(err)
					}
				}
				if timing == "before-admission" {
					remove()
				} else if timing == "during-admission" {
					manager = &removeDuringAdmission{Manager: engine, remove: remove}
				}
				ctx, old, release, err := managedCredentialLink(manager, user, "rotated")
				if release != nil {
					defer release()
				}
				var admitted uint64
				if timing == "active" {
					if err != nil {
						t.Fatal(err)
					}
					if err := old.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("prior"))); err != nil {
						t.Fatal(err)
					}
					admitted = 5
					remove()
					if err := old.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("stale"))); !errors.Is(err, clientpolicy.ErrSessionClosed) {
						t.Fatalf("removed credential admitted stale payload: %v", err)
					}
					if !errors.Is(ctx.Err(), context.Canceled) {
						t.Fatalf("old connection context: %v", ctx.Err())
					}
				} else if err == nil || err.Error() != "credential revoked" {
					t.Fatalf("removed authenticated credential reached dispatch: %v", err)
				}
				if err := sibling.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("sibling"))); err != nil {
					t.Fatalf("sibling credential stopped: %v", err)
				}
				_, replacement, releaseReplacement, err := managedCredentialLink(manager, v.addAuthenticated(t), "rotated")
				if err != nil {
					t.Fatalf("replacement credential: %v", err)
				}
				defer releaseReplacement()
				if err := replacement.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("fresh"))); err != nil {
					t.Fatal(err)
				}
				snapshot, err := engine.Snapshot(user.ClientID)
				want := clientpolicy.Usage{RawDownload: admitted + 12, BilledBytes: (admitted + 12) * 2}
				if err != nil || snapshot.Usage != want || snapshot.ActiveSessions != 2 {
					t.Fatalf("credential rotation ledger: %+v, %v; want %+v with 2 sessions", snapshot, err, want)
				}
			})
		}
	}
}

type afterUUIDReader struct {
	*bytes.Reader
	onAuthenticated func()
	read            int
}

func (r *afterUUIDReader) Read(p []byte) (int, error) {
	if r.read == 17 && r.onAuthenticated != nil {
		r.onAuthenticated()
		r.onAuthenticated = nil
	}
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestVLESSCredentialRemovedDuringHeaderDecodeCannotDispatch(t *testing.T) {
	v := new(vless.MemoryValidator)
	id := uuid.New()
	user, err := (&protocol.User{Account: serial.ToTypedMessage(&vless.Account{Id: id.String()}), Email: "partial-header", ClientId: "shared-client"}).ToMemoryUser()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Add(user); err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := vlessencoding.EncodeRequestHeader(&wire, &protocol.RequestHeader{User: user, Address: net.LocalHostIP, Port: 1234, Command: protocol.RequestCommandTCP}, &vlessencoding.Addons{}); err != nil {
		t.Fatal(err)
	}
	reader := &afterUUIDReader{Reader: bytes.NewReader(wire.Bytes()), onAuthenticated: func() {
		if err := v.Del(user.Email); err != nil {
			t.Fatal(err)
		}
	}}
	_, request, _, _, err := vlessencoding.DecodeRequestHeader(false, nil, reader, v)
	if err != nil {
		t.Fatal(err)
	}
	if reader.onAuthenticated != nil || request.User != user {
		t.Fatal("test did not remove the authenticated user during header decoding")
	}
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 1_000_000, BurstBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	var manager clientpolicy.Manager = engine
	_, _, release, err := managedCredentialLink(manager, request.User, "vless")
	if release != nil {
		defer release()
	}
	if err == nil || err.Error() != "credential revoked" {
		t.Fatalf("deleted UUID finished header and reached dispatch: %v", err)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("header not fully decoded: %v", err)
	}
}

func TestCredentialRevocationRacesWithManagedAdmission(t *testing.T) {
	v := testCredentialValidator(t, "vless")
	user := v.addAuthenticated(t)
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 1_000_000, BurstBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 16)
	var admitted atomic.Uint64
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			<-start
			for range 64 {
				_, link, release, err := managedCredentialLink(engine, user, "rotated")
				if err != nil {
					if err.Error() != "credential revoked" {
						results <- err
						return
					}
					continue
				}
				err = link.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("race")))
				release()
				if err == nil {
					admitted.Add(4)
				} else if !errors.Is(err, clientpolicy.ErrSessionClosed) && !errors.Is(err, context.Canceled) {
					results <- err
					return
				}
			}
		})
	}
	close(start)
	if err := v.remove(user.Email); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	close(results)
	for err := range results {
		t.Errorf("concurrent removal: %v", err)
	}
	snapshot, err := engine.Snapshot(user.ClientID)
	if err != nil || snapshot.ActiveSessions != 0 || snapshot.Usage != (clientpolicy.Usage{RawDownload: admitted.Load(), BilledBytes: admitted.Load()}) {
		t.Fatalf("concurrent removal leaked sessions or changed admitted usage: %+v %v; admitted %d", snapshot, err, admitted.Load())
	}
	_, _, release, err := managedCredentialLink(engine, user, "rotated")
	if release != nil {
		release()
	}
	if err == nil || err.Error() != "credential revoked" {
		t.Fatalf("credential became valid again after racing removal: %v", err)
	}
}
