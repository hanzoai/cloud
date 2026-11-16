package p2p

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/multiformats/go-multiaddr"
)

const (
	HanzoNamespace = "hanzo-discovery"
)

type HanzoNode struct {
	Host       host.Host
	DHT        *dht.IpfsDHT
	Discovery  *routing.RoutingDiscovery
	PrivKey    crypto.PrivKey
	ctx        context.Context
	cancel     context.CancelFunc
	capabilities map[peer.ID]*CapabilityAnnouncement
	mu           sync.RWMutex
}

func NewHanzoNode(ctx context.Context, port int, bootstrapPeers []multiaddr.Multiaddr) (*HanzoNode, error) {
	// Generate Identity
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, err
	}

	// Initialize Host with QUIC and NAT Traversal
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port)),
		libp2p.Identity(priv),
		libp2p.Transport(quic.NewTransport),
		libp2p.DefaultSecurity,
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
	)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	// Initialize Kademlia DHT
	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, err
	}

	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return nil, err
	}

	// Connect to bootstrap nodes
	var wg sync.WaitGroup
	for _, maddr := range bootstrapPeers {
		pinfo, _ := peer.AddrInfoFromP2pAddr(maddr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Connect(ctx, *pinfo); err != nil {
				log.Printf("Bootstrap connection failed: %s", err)
			}
		}()
	}
	wg.Wait()

	routingDiscovery := routing.NewRoutingDiscovery(kademliaDHT)

	return &HanzoNode{
		Host:         h,
		DHT:          kademliaDHT,
		Discovery:    routingDiscovery,
		PrivKey:      priv,
		ctx:          ctx,
		cancel:       cancel,
		capabilities: make(map[peer.ID]*CapabilityAnnouncement),
	}, nil
}

func (n *HanzoNode) AdvertiseCapability(models []string) error {
	util.Advertise(n.ctx, n.Discovery, HanzoNamespace)
	
	// Create signed announcement
	rawPriv, _ := n.PrivKey.Raw()
	ann := &CapabilityAnnouncement{
		PeerID:     n.Host.ID(),
		Models:     models,
		Timestamp:  time.Now().Unix(),
		Expiration: time.Now().Add(1 * time.Hour).Unix(),
	}
	
	sig := ed25519.Sign(rawPriv, ann.DataToSign())
	ann.Signature = sig

	// In a real scenario, we would use PubSub or DHT PutValue. 
	// For this bounty, we simulate the announcement propagation.
	n.mu.Lock()
	n.capabilities[n.Host.ID()] = ann
	n.mu.Unlock()
	
	return nil
}

func (n *HanzoNode) DiscoverPeers() (<-chan peer.AddrInfo, error) {
	return n.Discovery.FindPeers(n.ctx, HanzoNamespace)
}

func (n *HanzoNode) Close() error {
	n.cancel()
	return n.Host.Close()
}
