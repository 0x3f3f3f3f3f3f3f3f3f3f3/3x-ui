package conf

import "github.com/xtls/xray-core/app/commander"

// TrafficControlConfig keeps final accounting independent of the legacy routed API.
type TrafficControlConfig struct {
	Listen string `json:"listen"`
}

func (c *TrafficControlConfig) Build() (*commander.Config, error) {
	return (&APIConfig{Tag: "traffic-control", Listen: c.Listen, Services: []string{"TrafficControlServiceV1"}}).Build()
}
