package policyflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func flowDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "flow.db")+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=1000&_txlock=immediate"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}, &model.ClientUsageAccount{}, &model.ClientUsageMeter{}} {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	handle, _ := db.DB()
	t.Cleanup(func() { _ = handle.Close() })
	return db
}

func flowClient(t *testing.T, db *gorm.DB, quota int64) model.ClientRecord {
	t.Helper()
	c := model.ClientRecord{Email: uuid.NewString(), Enable: true, TotalGB: quota}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: c.Email, Enable: true, Total: quota}).Error; err != nil {
		t.Fatal(err)
	}
	return c
}

func flowController(t *testing.T, ledger *database.ClientUsageLedger) *Controller {
	t.Helper()
	c := NewController(ledger, "node-a/shared-dispatch")
	t.Cleanup(c.Close)
	return c
}

func flowListener(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				handle(conn)
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); wg.Wait() })
	return listener.Addr().String()
}

func flowProxy(t *testing.T, controller *Controller, policyID, target string) string {
	t.Helper()
	return flowListener(t, func(conn net.Conn) {
		err := controller.Proxy(context.Background(), policyID, conn, func(ctx context.Context) (io.ReadWriteCloser, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", target)
		})
		if err != nil && !errors.Is(err, database.ErrUsageQuota) {
			t.Logf("proxy stopped: %v", err)
		}
	})
}

func TestControllerQuotaStopsConcurrentTCPFlowsAndSurvivesRestart(t *testing.T) {
	db := flowDB(t)
	client := flowClient(t, db, 16<<20)
	ledger := database.NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(context.Background(), client.PolicyID, 1, 2000, nil); err != nil {
		t.Fatal(err)
	}
	controller := flowController(t, ledger)
	if err := controller.Configure(context.Background(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	var received atomic.Int64
	target := flowListener(t, func(conn net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := conn.Read(buf)
			received.Add(int64(n))
			if err != nil {
				return
			}
		}
	})
	listeners := []string{flowProxy(t, controller, client.PolicyID, target), flowProxy(t, controller, client.PolicyID, target)}
	var wg sync.WaitGroup
	start := time.Now()
	for n := range 4 {
		conn, err := net.Dial("tcp", listeners[n%2])
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			buf := make([]byte, 32<<10)
			for {
				if _, err := conn.Write(buf); err != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	if time.Since(start) >= 10*time.Second {
		t.Fatal("quota did not terminate existing transfers before socket deadline")
	}
	account, err := ledger.Read(context.Background(), client.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Up != 8<<20 || account.Down != 0 || account.Billed != 16<<20 {
		t.Fatalf("actual forwarding did not stop at the billed allowance: %+v", account)
	}
	time.Sleep(50 * time.Millisecond)
	got := received.Load()
	if got > 8<<20 || got < (8<<20)-4*2*BufferSize {
		t.Fatalf("target observed %d bytes; expected 8MiB minus at most bounded in-flight payload", got)
	}
	t.Logf("4 connections / 2 bindings: raw admitted=%d target=%d billed=%d elapsed=%v", account.Up, got, account.Billed, time.Since(start))
	if flow, err := controller.Open(context.Background(), client.PolicyID); !errors.Is(err, database.ErrUsageQuota) {
		if flow != nil {
			flow.Close()
		}
		t.Fatalf("exhausted client opened a new flow: %v", err)
	}
	controller.Close()
	restarted := flowController(t, database.NewClientUsageLedger(db))
	if err := restarted.Configure(context.Background(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	if flow, err := restarted.Open(context.Background(), client.PolicyID); !errors.Is(err, database.ErrUsageQuota) {
		if flow != nil {
			flow.Close()
		}
		t.Fatalf("controller restart restored exhausted access: %v", err)
	}
}

func TestControllerDisableClosesIdleFlowWithoutTouchingOtherClient(t *testing.T) {
	db := flowDB(t)
	a, b := flowClient(t, db, 0), flowClient(t, db, 0)
	controller := flowController(t, database.NewClientUsageLedger(db))
	for _, client := range []model.ClientRecord{a, b} {
		if err := controller.Configure(context.Background(), client.PolicyID, Rates{}); err != nil {
			t.Fatal(err)
		}
	}
	echo := flowListener(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	pa, pb := flowProxy(t, controller, a.PolicyID, echo), flowProxy(t, controller, b.PolicyID, echo)
	ca, err := net.Dial("tcp", pa)
	if err != nil {
		t.Fatal(err)
	}
	defer ca.Close()
	cb, err := net.Dial("tcp", pb)
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	for _, conn := range []net.Conn{ca, cb} {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte("ok")); err != nil {
			t.Fatal(err)
		}
		var reply [2]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil || string(reply[:]) != "ok" {
			t.Fatalf("initial echo: %q %v", reply, err)
		}
	}
	start := time.Now()
	if err := db.Model(&model.ClientRecord{}).Where("id = ?", a.Id).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	_ = ca.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	var one [1]byte
	_, err = ca.Read(one[:])
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) || time.Since(start) > 1250*time.Millisecond {
		t.Fatalf("idle connection was not revoked within 1.25s: %v, %v", err, time.Since(start))
	}
	if _, err := cb.Write([]byte("still-live")); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(cb, reply[:]); err != nil || string(reply[:]) != "still-live" {
		t.Fatalf("unrelated client was interrupted: %q %v", reply, err)
	}
	t.Logf("idle disable cutoff=%v; unrelated same-IP client remained live", time.Since(start))
}

func TestControllerStreamCancellationDoesNotPoisonSharedCursor(t *testing.T) {
	db := flowDB(t)
	client := flowClient(t, db, 0)
	ledger := database.NewClientUsageLedger(db)
	controller := flowController(t, ledger)
	if err := controller.Configure(context.Background(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := controller.Open(ctx, client.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := controller.Open(context.Background(), client.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	handle, _ := db.DB()
	handle.SetMaxOpenConns(1)
	conn, err := handle.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := handle.Stats().WaitCount
	done := make(chan error, 1)
	go func() { _, err := a.Writer(Upload, io.Discard).Write([]byte("first")); done <- err }()
	deadline := time.Now().Add(200 * time.Millisecond)
	for handle.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if handle.Stats().WaitCount == before {
		t.Fatal("writer did not enter admission while the database connection was occupied")
	}
	cancel()
	_ = conn.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stream did not stop: %v", err)
	}
	if n, err := b.Writer(Download, io.Discard).Write([]byte("second")); n != 6 || err != nil {
		t.Fatalf("one canceled stream poisoned another stream: %d, %v", n, err)
	}
	account, err := ledger.Read(context.Background(), client.PolicyID)
	if err != nil || account.Up != 5 || account.Down != 6 || account.Billed != 11 {
		t.Fatalf("committed cursor was lost or repeated after stream cancellation: %+v, %v", account, err)
	}
}

func TestControllerCutsIdleTCPOnExpiryQuotaReductionAndDatabaseStall(t *testing.T) {
	for _, change := range []string{"expiry", "quota-reduction", "database-stall", "source-replacement"} {
		t.Run(change, func(t *testing.T) {
			db := flowDB(t)
			client := flowClient(t, db, 100)
			ledger := database.NewClientUsageLedger(db)
			controller := flowController(t, ledger)
			if err := controller.Configure(context.Background(), client.PolicyID, Rates{}); err != nil {
				t.Fatal(err)
			}
			echo := flowListener(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
			proxy := flowProxy(t, controller, client.PolicyID, echo)
			conn, err := net.Dial("tcp", proxy)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := conn.Write([]byte("ok")); err != nil {
				t.Fatal(err)
			}
			var reply [2]byte
			if _, err := io.ReadFull(conn, reply[:]); err != nil || string(reply[:]) != "ok" {
				t.Fatalf("initial echo: %q, %v", reply, err)
			}
			start := time.Now()
			var expected error
			var stalled *gorm.DB
			switch change {
			case "expiry":
				err = db.Model(&client).Update("expiry_time", time.Now().Add(-time.Second).UnixMilli()).Error
				expected = database.ErrUsageExpired
			case "quota-reduction":
				err = db.Model(&client).Update("total_gb", 3).Error
				expected = database.ErrUsageQuota
			case "database-stall":
				stalled = db.Begin()
				err = stalled.Error
				defer stalled.Rollback()
			case "source-replacement":
				replacement := flowController(t, ledger)
				err = replacement.Configure(context.Background(), client.PolicyID, Rates{})
				expected = database.ErrUsageClosed
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			_, err = conn.Read(reply[:])
			var timeout net.Error
			if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) || time.Since(start) > 1250*time.Millisecond {
				t.Fatalf("idle connection survived %s beyond 1.25s: %v, %v", change, time.Since(start), err)
			}
			t.Logf("%s cutoff=%v", change, time.Since(start))
			if stalled != nil {
				if err := stalled.Rollback().Error; err != nil {
					t.Fatal(err)
				}
				return
			}
			flow, err := controller.Open(context.Background(), client.PolicyID)
			if flow != nil {
				flow.Close()
			}
			if !errors.Is(err, expected) {
				t.Fatalf("new flow ignored %s: expected %v, got %v", change, expected, err)
			}
		})
	}
}

type duplexGroup struct {
	up, down atomic.Int64
	conns    []net.Conn
	wg       sync.WaitGroup
}

func flowDuplexGroup(t *testing.T, c *Controller, id string) *duplexGroup {
	t.Helper()
	g := &duplexGroup{}
	target := flowListener(t, func(conn net.Conn) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, BufferSize)
			for {
				if _, err := conn.Write(buf); err != nil {
					return
				}
			}
		}()
		buf := make([]byte, BufferSize)
		for {
			n, err := conn.Read(buf)
			g.up.Add(int64(n))
			if err != nil {
				break
			}
		}
		_ = conn.Close()
		<-done
	})
	bindings := []string{flowProxy(t, c, id, target), flowProxy(t, c, id, target)}
	for n := range 4 {
		conn, err := net.Dial("tcp", bindings[n%2])
		if err != nil {
			t.Fatal(err)
		}
		g.conns = append(g.conns, conn)
		g.wg.Go(func() {
			buf := make([]byte, BufferSize)
			for {
				if _, err := conn.Write(buf); err != nil {
					return
				}
			}
		})
		g.wg.Go(func() {
			buf := make([]byte, BufferSize)
			for {
				n, err := conn.Read(buf)
				g.down.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
	}
	t.Cleanup(g.close)
	return g
}

func (g *duplexGroup) close() {
	for _, conn := range g.conns {
		_ = conn.Close()
	}
	g.wg.Wait()
}

func TestControllerTCPDuplexAggregateRatesAndLiveUpdates(t *testing.T) {
	db := flowDB(t)
	ledger := database.NewClientUsageLedger(db)
	c := flowController(t, ledger)
	baseline := flowClient(t, db, 0)
	if err := c.Configure(context.Background(), baseline.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	u := flowDuplexGroup(t, c, baseline.PolicyID)
	time.Sleep(100 * time.Millisecond)
	u0, d0, start := u.up.Load(), u.down.Load(), time.Now()
	time.Sleep(300 * time.Millisecond)
	seconds := time.Since(start).Seconds()
	for name, bps := range map[string]float64{"up": float64(u.up.Load()-u0) / seconds, "down": float64(u.down.Load()-d0) / seconds} {
		if bps < 8*131072 {
			t.Fatalf("unlimited %s baseline %.0f B/s is too slow for rate acceptance", name, bps)
		}
		t.Logf("unlimited %s %.0f B/s", name, bps)
	}
	u.close()
	a, b := flowClient(t, db, 0), flowClient(t, db, 0)
	if _, err := ledger.ChangeMultiplier(context.Background(), a.PolicyID, 1, 2000, nil); err != nil {
		t.Fatal(err)
	}
	rates := []Rates{{65536, 131072}, {131072, 65536}}
	clients := []model.ClientRecord{a, b}
	groups := make([]*duplexGroup, 2)
	for n, client := range clients {
		if err := c.Configure(context.Background(), client.PolicyID, rates[n]); err != nil {
			t.Fatal(err)
		}
		groups[n] = flowDuplexGroup(t, c, client.PolicyID)
	}
	measure := func(label string, duration time.Duration) {
		t.Helper()
		before := [2][2]int64{}
		for n, g := range groups {
			before[n] = [2]int64{g.up.Load(), g.down.Load()}
		}
		start := time.Now()
		time.Sleep(duration)
		elapsed := time.Since(start).Seconds()
		for n, g := range groups {
			for direction, actual := range []int64{g.up.Load() - before[n][0], g.down.Load() - before[n][1]} {
				rate := []int64{rates[n].Upload, rates[n].Download}[direction]
				burst := min(int64(65536), max(int64(1), rate/10))
				upper, lower := float64(rate)*elapsed*1.06+float64(burst), float64(rate)*elapsed*0.80
				if float64(actual) < lower || float64(actual) > upper {
					t.Fatalf("%s client %d direction %d: %d bytes outside [%.0f,%.0f] in %.3fs", label, n, direction, actual, lower, upper, elapsed)
				}
				t.Logf("%s client %d direction %d: %.0f B/s", label, n, direction, float64(actual)/elapsed)
			}
		}
	}
	time.Sleep(300 * time.Millisecond)
	measure("initial", 1800*time.Millisecond)
	for _, updated := range []Rates{{32768, 65536}, {131072, 131072}} {
		start := time.Now()
		rates[0] = updated
		if err := c.Configure(context.Background(), a.PolicyID, updated); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
		measure(fmt.Sprintf("live %d/%d", updated.Upload, updated.Download), 1500*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("live-update measurement exceeded the 2s acceptance target: %v", elapsed)
		}
	}
	account, err := ledger.Read(context.Background(), a.PolicyID)
	if err != nil || account.Up == 0 || account.Down == 0 || account.Billed != 2*(account.Up+account.Down) {
		t.Fatalf("duplex payload was not billed exactly at 2x: %+v, %v", account, err)
	}
}
