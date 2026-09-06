package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const discoveryMethod = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

type rpcMeta struct {
	Method         string         `json:"method"`
	HTTPStatus     int            `json:"http_status"`
	ResponseBytes  int            `json:"response_bytes"`
	TopLevelFields map[string]int `json:"top_level_wire_fields"`
}

func discoverModels(ctx context.Context, client *http.Client, c credentials) (rpcMeta, error) {
	u := *c.server
	u.Path = strings.TrimRight(u.Path, "/") + discoveryMethod
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(""))
	if err != nil {
		return rpcMeta{}, errors.New("cannot construct discovery request")
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+c.apiKey.value)
	resp, err := client.Do(req)
	if err != nil {
		return rpcMeta{}, errors.New("discovery transport failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return rpcMeta{}, errors.New("discovery response unreadable")
	}
	if resp.StatusCode/100 != 2 {
		return rpcMeta{}, fmt.Errorf("discovery returned HTTP %d", resp.StatusCode)
	}
	if len(b) > 8<<20 {
		return rpcMeta{}, errors.New("discovery response was too large")
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/proto" && mediaType != "application/protobuf" {
		return rpcMeta{}, errors.New("discovery response had an unexpected content type")
	}
	fields, err := scanWire(b)
	if err != nil {
		return rpcMeta{}, errors.New("discovery response was not valid protobuf")
	}
	return rpcMeta{Method: discoveryMethod, HTTPStatus: resp.StatusCode, ResponseBytes: len(b), TopLevelFields: fields}, nil
}

func hardenedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
	}
}

func scanWire(b []byte) (map[string]int, error) {
	out := map[string]int{}
	for len(b) > 0 {
		tag, n := consumeVarint(b)
		if n <= 0 || tag == 0 {
			return nil, errors.New("bad tag")
		}
		b = b[n:]
		field, wire := tag>>3, tag&7
		out[strconv.FormatUint(field, 10)+":"+strconv.FormatUint(wire, 10)]++
		switch wire {
		case 0:
			_, n = consumeVarint(b)
		case 1:
			n = 8
		case 2:
			var l uint64
			l, n = consumeVarint(b)
			if n <= 0 {
				return nil, errors.New("bad length")
			}
			b = b[n:]
			n = int(l)
		case 5:
			n = 4
		default:
			return nil, errors.New("unsupported wire type")
		}
		if n < 0 || n > len(b) {
			return nil, errors.New("truncated field")
		}
		b = b[n:]
	}
	return out, nil
}

func consumeVarint(b []byte) (uint64, int) {
	var x uint64
	for i, c := range b {
		if i == 10 {
			return 0, -1
		}
		if c < 0x80 {
			if i == 9 && c > 1 {
				return 0, -1
			}
			return x | uint64(c)<<(7*i), i + 1
		}
		x |= uint64(c&0x7f) << (7 * i)
	}
	return 0, 0
}
