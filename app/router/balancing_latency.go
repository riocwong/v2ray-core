package router

import (
	"context"
	"sort"
	"sync"
	"time"

	"v2ray.com/core/common"
	"v2ray.com/core/common/buf"
	"v2ray.com/core/common/dice"
	"v2ray.com/core/common/net"
	"v2ray.com/core/common/session"
	"v2ray.com/core/features/outbound"
	"v2ray.com/core/transport"
	"v2ray.com/core/transport/pipe"
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

type probeWriter struct {
	probe chan struct{}
}

func (w *probeWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		select {
		case w.probe <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func measureLatency(handler outbound.Handler) time.Duration {
	ctx := context.Background()
	destination, err := net.ParseDestination("tcp:www.google.com:80")
	if err != nil {
		panic(err)
	}
	ob := &session.Outbound{
		Target: destination,
	}
	ctx = session.ContextWithOutbound(ctx, ob)

	reqReader, reqWriter := pipe.New()
	payload := buf.New()
	reqBytes := []byte("HEAD / HTTP/1.1\r\n\r\n")
	if _, err := payload.Write(reqBytes); err != nil {
		panic(err)
	}
	reqWriter.WriteMultiBuffer(buf.MultiBuffer{payload})

	probe := make(chan struct{}, 1)
	respWriter := &probeWriter{probe: probe}

	link := &transport.Link{
		Reader: reqReader,
		Writer: buf.NewWriter(respWriter),
	}
	defer func() {
		common.Must(common.Close(link.Writer))
		common.Interrupt(link.Reader)
	}()

	start := time.Now()
	go handler.Dispatch(ctx, link)
	select {
	case <-probe:
	case <-time.After(10 * time.Second):
		return 10 * time.Second
	}
	elasped := time.Since(start)
	return elasped
}

func (s *LatencyStrategy) measure(ohm outbound.Manager, selectors []string) {
	for {
		servers := make([]Server, 0)

		select {
		// Measures every 30 seconds.
		case <-time.After(30 * time.Second):
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
				var totalLatency int64 = 0
				// Total 5 measures for each server.
				for i := 0; i < 5; i++ {
					latency := measureLatency(h)
					totalLatency += latency.Nanoseconds()
					// Waits 1 second between each measure.
					time.Sleep(1 * time.Second)
				}
				avgLatency := time.Duration(int64(float64(totalLatency) / 5))
				server := Server{
					latency: avgLatency,
					tag:     tag,
				}
				servers = append(servers, server)
				newError("tag:", server.tag, ", latency:", server.latency.String()).AtDebug().WriteToLog()
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
