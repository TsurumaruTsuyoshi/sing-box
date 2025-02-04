package main

import (
	"net/netip"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestShadowsocksR(t *testing.T) {
	startDockerContainer(t, DockerOptions{
		Image: ImageShadowsocksR,
		Ports: []uint16{serverPort, testPort},
		Bind: map[string]string{
			"shadowsocksr.json": "/etc/shadowsocks-r/config.json",
		},
	})
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientPort,
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeShadowsocksR,
				Options: &option.ShadowsocksROutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverPort,
					},
					Method:   "aes-256-cfb",
					Password: "password0",
					Obfs:     "plain",
					Protocol: "origin",
				},
			},
		},
	})
	testSuit(t, clientPort, testPort)
}
