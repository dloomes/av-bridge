package cloudpoll

import (
	"context"
	"sync"
)

// dispatcher runs cloud commands in parallel across devices, and in order
// for each device: a device never has two commands running at once, and
// gets them in the order the cloud queued them. Up to `limit` commands run
// at any moment.
//
// Commands used to run one after another for the whole collector, which at
// estate scale is far too slow: a Room Readiness power-off for 2,000
// displays at ~0.3 s each took 10 minutes, long enough for the last
// commands to expire.
type dispatcher struct {
	run func(context.Context, bridgeCommand)
	sem chan struct{} // one slot per running command

	mu      sync.Mutex
	queued  map[string][]bridgeCommand // per device, waiting their turn
	active  map[string]bool            // device has a drain goroutine
	pending int                        // queued + running, all devices
	idle    chan struct{}              // closed and replaced when pending hits 0
}

func newDispatcher(limit int, run func(context.Context, bridgeCommand)) *dispatcher {
	if limit <= 0 {
		limit = 1
	}
	return &dispatcher{
		run:    run,
		sem:    make(chan struct{}, limit),
		queued: map[string][]bridgeCommand{},
		active: map[string]bool{},
		idle:   make(chan struct{}),
	}
}

// submit queues a command behind any others for the same device and
// returns at once.
func (d *dispatcher) submit(ctx context.Context, c bridgeCommand) {
	d.mu.Lock()
	d.pending++
	d.queued[c.ReportedID] = append(d.queued[c.ReportedID], c)
	start := !d.active[c.ReportedID]
	d.active[c.ReportedID] = true
	d.mu.Unlock()
	if start {
		go d.drain(ctx, c.ReportedID)
	}
}

// drain runs one device's commands in order until its queue is empty.
func (d *dispatcher) drain(ctx context.Context, device string) {
	for {
		d.mu.Lock()
		q := d.queued[device]
		if len(q) == 0 {
			delete(d.queued, device)
			delete(d.active, device)
			d.mu.Unlock()
			return
		}
		c := q[0]
		d.queued[device] = q[1:]
		d.mu.Unlock()

		select {
		case d.sem <- struct{}{}:
			d.run(ctx, c)
			<-d.sem
		case <-ctx.Done():
			// Shutting down: the cloud requeues claimed commands that
			// never report (stale claim), so nothing is lost.
		}
		d.done()
	}
}

func (d *dispatcher) done() {
	d.mu.Lock()
	d.pending--
	if d.pending == 0 {
		close(d.idle)
		d.idle = make(chan struct{})
	}
	d.mu.Unlock()
}

// load is how many commands are queued or running.
func (d *dispatcher) load() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending
}

// wait blocks until no command is queued or running, or ctx ends.
func (d *dispatcher) wait(ctx context.Context) {
	for {
		d.mu.Lock()
		if d.pending == 0 {
			d.mu.Unlock()
			return
		}
		idle := d.idle
		d.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return
		}
	}
}
