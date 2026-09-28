package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	statsservice "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func sshCoreAPIAddress(cfg *xray.Config) (string, error) {
	var api struct {
		Tag    string `json:"tag"`
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal(cfg.API, &api); err != nil || api.Tag == "" {
		return "", errors.New("SSH upstream requires the core API for startup verification")
	}
	if api.Listen != "" {
		return api.Listen, nil
	}
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag != api.Tag || inbound.Port < 1 || inbound.Port > 65535 {
			continue
		}
		var host string
		if err := json.Unmarshal(inbound.Listen, &host); err != nil {
			return "", errors.New("SSH upstream requires a TCP core API listener")
		}
		switch host {
		case "", "0.0.0.0":
			host = "127.0.0.1"
		case "::":
			host = "::1"
		}
		return net.JoinHostPort(host, strconv.Itoa(inbound.Port)), nil
	}
	return "", errors.New("SSH upstream requires a core API listener for startup verification")
}

func waitSSHCoreReady(process *xray.Process) error {
	address, err := sshCoreAPIAddress(process.GetConfig())
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return errors.New("xray startup API client failed")
	}
	defer conn.Close()
	client := statsservice.NewStatsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if !process.IsRunning() {
			return errors.New("xray startup exited before readiness")
		}
		attempt, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		_, err := client.GetSysStats(attempt, &statsservice.SysStatsRequest{})
		stop()
		if err == nil && process.IsRunning() {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("xray startup API readiness timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
