/*
   Copyright 2022 Erigon-Lightclient contributors
   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at
       http://www.apache.org/licenses/LICENSE-2.0
   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package sentinel

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net"

	"github.com/ledgerwatch/erigon/cmd/lightclient/sentinel/peers"
	"github.com/ledgerwatch/erigon/p2p/discover"
	"github.com/ledgerwatch/erigon/p2p/enode"
	"github.com/ledgerwatch/erigon/p2p/enr"
	"github.com/ledgerwatch/log/v3"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/pkg/errors"
)

type Sentinel struct {
	started  bool
	listener *discover.UDPv5 // this is us in the network.
	ctx      context.Context
	host     host.Host
	cfg      SentinelConfig
	peers    *peers.Peers
}

func (s *Sentinel) createLocalNode(
	privKey *ecdsa.PrivateKey,
	ipAddr net.IP,
	udpPort, tcpPort int,
) (*enode.LocalNode, error) {
	db, err := enode.OpenDB("")
	if err != nil {
		return nil, errors.Wrap(err, "could not open node's peer database")
	}
	localNode := enode.NewLocalNode(db, privKey)

	ipEntry := enr.IP(ipAddr)
	udpEntry := enr.UDP(udpPort)
	tcpEntry := enr.TCP(tcpPort)

	localNode.Set(ipEntry)
	localNode.Set(udpEntry)
	localNode.Set(tcpEntry)

	localNode.SetFallbackIP(ipAddr)
	localNode.SetFallbackUDP(udpPort)

	return localNode, nil
}

func (s *Sentinel) createListener() (*discover.UDPv5, error) {
	var (
		ipAddr  = s.cfg.IpAddr
		port    = s.cfg.Port
		discCfg = s.cfg.DiscoverConfig
	)

	ip := net.ParseIP(ipAddr)
	if ip.To4() == nil {
		return nil, fmt.Errorf("IPV4 address not provided instead %s was provided", ipAddr)
	}

	var bindIP net.IP
	var networkVersion string

	// check for our network version
	switch {
	// if we have 16 byte and 4 byte representation then we are in using udp6
	case ip.To16() != nil && ip.To4() == nil:
		bindIP = net.IPv6zero
		networkVersion = "udp6"
		// only 4 bytes then we are using udp4
	case ip.To4() != nil:
		bindIP = net.IPv4zero
		networkVersion = "udp4"
	default:
		return nil, fmt.Errorf("bad ip address provided, %s was provided", ipAddr)
	}

	udpAddr := &net.UDPAddr{
		IP:   bindIP,
		Port: port,
	}
	conn, err := net.ListenUDP(networkVersion, udpAddr)
	if err != nil {
		return nil, err
	}

	localNode, err := s.createLocalNode(discCfg.PrivateKey, ip, port, port+1)
	if err != nil {
		return nil, err
	}

	net, err := discover.ListenV5(s.ctx, conn, localNode, discCfg)
	if err != nil {
		return nil, err
	}

	return net, err
}

// This is just one of the examples from the libp2p repository.
func New(ctx context.Context, cfg SentinelConfig) (*Sentinel, error) {
	s := &Sentinel{
		ctx: ctx,
		cfg: cfg,
	}

	opts, err := buildOptions(cfg, s)
	if err != nil {
		return nil, err
	}

	host, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}

	host.RemoveStreamHandler(identify.IDDelta)
	s.host = host
	s.peers = peers.New(s.host)
	return s, nil
}

func (s *Sentinel) Start() error {
	if s.started {
		log.Warn("Sentinel already running")
	}

	var err error
	s.listener, err = s.createListener()
	if err != nil {
		return fmt.Errorf("failed creating sentinel listener err=%s", err)
	}
	if err := s.connectToBootnodes(); err != nil {
		return fmt.Errorf("failed to connect to bootnodes err=%s", err)
	}
	go s.listenForPeers()

	return nil
}

func (s *Sentinel) String() string {
	return s.listener.Self().String()
}

func (s *Sentinel) HasTooManyPeers() bool {
	return s.GetPeersCount() >= peers.DefaultMaxPeers
}

func (s *Sentinel) GetPeersCount() int {
	return len(s.host.Network().Peers())
}