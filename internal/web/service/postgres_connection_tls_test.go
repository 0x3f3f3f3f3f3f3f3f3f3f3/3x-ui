package service

import (
	"context"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func TestPostgresToolTLSAliasRejectsPlaintextServer(t *testing.T) {
	setupPrivatePostgresRestoreDatabase(t)
	t.Setenv("PGSSLNEGOTIATION", "")
	cfg, err := pgx.ParseConfig(config.GetDBDSN())
	if err != nil {
		t.Fatal("private database configuration rejected")
	}
	if !strings.HasPrefix(cfg.Host, "/") {
		t.Fatal("plaintext TLS boundary fixture requires a private Unix socket")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				peer, err := net.DialTimeout("unix", cfg.Host+"/.s.PGSQL."+strconv.Itoa(int(cfg.Port)), time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				go func() { _, _ = io.Copy(peer, client); _ = peer.Close() }()
				_, _ = io.Copy(client, peer)
			}()
		}
	}()
	base := (&url.URL{Scheme: "postgresql", User: url.UserPassword(cfg.User, cfg.Password), Host: listener.Addr().String(), Path: "/" + cfg.Database}).String()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env, name, err := pgConnEnv(base + "?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	control := exec.CommandContext(ctx, "psql", "-X", "-At", "--dbname", postgresToolDatabaseArgument(name), "-c", "SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()")
	control.Env = env
	if out, err := control.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "f" {
		t.Fatal("private proxy did not establish a real plaintext PostgreSQL control connection")
	}
	dsn := base + "?sslmode=disable&ssl=true"
	conn, err := pgx.Connect(ctx, dsn)
	if conn != nil {
		_ = conn.Close(ctx)
	}
	if err == nil {
		t.Fatal("panel driver accepted the plaintext TLS boundary fixture")
	}
	env, name, err = pgConnEnv(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tool := exec.CommandContext(ctx, "psql", "-X", "-At", "--dbname", postgresToolDatabaseArgument(name), "-c", "SELECT 1")
	tool.Env = env
	if _, err := tool.CombinedOutput(); err == nil {
		t.Fatal("actual PostgreSQL tool weakened the final URI TLS requirement")
	}
}
