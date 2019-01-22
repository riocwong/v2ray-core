package router

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"v2ray.com/core/common/dice"
	"v2ray.com/core/common/net"
	"v2ray.com/core/features/outbound"
	"v2ray.com/core/proxy"
)

type Server struct {
	latency time.Duration
	tag     string
}

type By func(p1, p2 *Server) bool

func (by By) Sort(servers []Server) {
	ss := &serverSorter{
		servers: servers,
		by:      by,
	}
	sort.Sort(ss)
}

type serverSorter struct {
	servers []Server
	by      By
}

func (s *serverSorter) Len() int {
	return len(s.servers)
}

func (s *serverSorter) Swap(i, j int) {
	s.servers[i], s.servers[j] = s.servers[j], s.servers[i]
}

func (s *serverSorter) Less(i, j int) bool {
	return s.by(&s.servers[i], &s.servers[j])
}

type LatencyStrategy struct {
	sync.Mutex

	servers []Server
}

func NewLatencyStrategy(ohm outbound.Manager, selectors []string) BalancingStrategy {
	s := &LatencyStrategy{
		servers: make([]Server, 0),
	}
	go s.measure(ohm, selectors)
	return s
}

func (s *LatencyStrategy) PickOutbound(tags []string) string {
	s.Lock()
	defer s.Unlock()

	n := len(tags)
	if n == 0 {
		panic("0 tags")
	}
	if len(s.servers) == 0 {
		return tags[dice.Roll(n)]
	}

	return s.servers[0].tag
}

func measureTCPLatency(addr net.Destination) time.Duration {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}
	start := time.Now()
	conn, err := dialer.Dial("tcp", addr.NetAddr())
	if err != nil {
		return 5 * time.Second
	}
	elasped := time.Since(start)
	conn.Close()
	return elasped
}

func (s *LatencyStrategy) measure(ohm outbound.Manager, selectors []string) {
	for {
		servers := make([]Server, 0)

		select {
		case <-time.After(10 * time.Second):
			hs, ok := ohm.(outbound.HandlerSelector)
			if !ok {
				panic("not selecter")
			}
			tags := hs.Select(selectors)
			if len(tags) == 0 {
				panic("no stags")
			}
			for _, tag := range tags {
				h := ohm.GetHandler(tag)
				if gh, ok := h.(proxy.GetOutbound); ok {
					ob := gh.GetOutbound()
					if aob, ok := ob.(proxy.GetServerAddresses); ok {
						addrs := aob.GetServerAddresses()
						if len(addrs) > 0 {
							latency := measureTCPLatency(addrs[0])
							server := Server{
								latency: latency,
								tag:     tag,
							}
							servers = append(servers, server)
						} else {
							panic(fmt.Sprintf("no address for tag %v", tag))
						}
					}
				}
			}
		}

		latency := func(p1, p2 *Server) bool {
			return p1.latency.Nanoseconds() < p2.latency.Nanoseconds()
		}
		By(latency).Sort(servers)

		s.Lock()
		s.servers = servers
		s.Unlock()
	}
}
