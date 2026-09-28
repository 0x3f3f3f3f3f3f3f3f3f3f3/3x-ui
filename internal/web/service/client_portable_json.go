package service

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type ClientPortableClient struct {
	TotalGB   string `json:"totalGB" example:"1073741824"`
	LimitHwid int    `json:"limitHwid" example:"0"`
	model.Client
}

type ClientPortableTrafficView struct {
	Up           string `json:"up" example:"3"`
	Down         string `json:"down" example:"3"`
	ResetCount   int    `json:"resetCount" example:"0"`
	LastOnline   int64  `json:"lastOnline,omitempty" example:"0"`
	LastSubFetch int64  `json:"lastSubFetch,omitempty" example:"0"`
}

type ClientPortableExport struct {
	Client     ClientPortableClient       `json:"client"`
	InboundIds []int                      `json:"inboundIds" example:"[1]"`
	Traffic    *ClientPortableTrafficView `json:"traffic,omitempty"`
	Policy     *ClientPortablePolicy      `json:"policy,omitempty"`
}

func portableJSONInt(data json.RawMessage) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}
	value := string(data)
	if data[0] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return 0, err
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || strconv.FormatInt(number, 10) != value {
		return 0, fmt.Errorf("invalid portable integer")
	}
	return number, nil
}

func (p ClientPortableTraffic) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.portableView())
}

func (p ClientPortableTraffic) portableView() *ClientPortableTrafficView {
	return &ClientPortableTrafficView{
		Up: strconv.FormatInt(p.Up, 10), Down: strconv.FormatInt(p.Down, 10),
		ResetCount: p.ResetCount, LastOnline: p.LastOnline, LastSubFetch: p.LastSubFetch,
	}
}

func (p *ClientPortableTraffic) UnmarshalJSON(data []byte) error {
	type fields ClientPortableTraffic
	var raw struct {
		fields
		Up   json.RawMessage `json:"up"`
		Down json.RawMessage `json:"down"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	up, err := portableJSONInt(raw.Up)
	if err != nil {
		return err
	}
	down, err := portableJSONInt(raw.Down)
	if err != nil {
		return err
	}
	*p = ClientPortableTraffic(raw.fields)
	p.Up, p.Down = up, down
	return nil
}
