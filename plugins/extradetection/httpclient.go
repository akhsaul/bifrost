package extradetection

import (
	"context"
	"errors"
	"time"

	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
)

// countClient returns the plugin's shared counting client, creating it on
// first use. One client per plugin instance keeps the connection to each
// counting endpoint pooled across requests instead of dialing per call.
func (p *Plugin) countClient() *fasthttp.Client {
	if c := p.client.Load(); c != nil {
		return c
	}
	p.clientOnce.Do(func() {
		p.client.CompareAndSwap(nil, &fasthttp.Client{
			MaxConnsPerHost:     100,
			MaxIdleConnDuration: 30 * time.Second,
			ReadTimeout:         10 * time.Second,
			WriteTimeout:        10 * time.Second,
		})
	})
	return p.client.Load()
}

// closeCountClient releases the plugin's pooled counting connections.
func (p *Plugin) closeCountClient() {
	if p == nil {
		return
	}
	if c := p.client.Swap(nil); c != nil {
		c.CloseIdleConnections()
	}
}

var errCountTimeout = errors.New("token counting request timed out")

func isTimeout(err error) bool {
	return errors.Is(err, errCountTimeout) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, fasthttp.ErrTimeout)
}

// doWithContext performs the request, bounded by both timeout and context
// cancellation. The counting call blocks the request before routing, so a
// client-initiated cancellation (caller hung up) must release it immediately
// rather than waiting out the full timeout.
func doWithContext(ctx *schemas.BifrostContext, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultCountTimeoutMS * time.Millisecond
	}
	parent := context.Context(ctx)
	if parent == nil {
		return client.DoTimeout(req, resp, timeout)
	}
	// Run the call in a goroutine so a cancelled parent can abandon it. The
	// buffered channel means the goroutine never leaks on that path, and the
	// request/response are owned solely by the caller's defers — the goroutine
	// only reports an error.
	done := make(chan error, 1)
	go func() { done <- client.DoTimeout(req, resp, timeout) }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-parent.Done():
		return parent.Err()
	case <-timer.C:
		return errCountTimeout
	}
}
