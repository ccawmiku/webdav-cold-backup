package backup

import (
	"context"
	"sync"
)

type Control struct {
	mutex  sync.Mutex
	paused bool
	wakeup chan struct{}
}

func NewControl() *Control {
	return &Control{wakeup: make(chan struct{})}
}

func (c *Control) Pause() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if !c.paused {
		c.paused = true
		c.wakeup = make(chan struct{})
	}
}

func (c *Control) Resume() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.paused {
		c.paused = false
		close(c.wakeup)
	}
}

func (c *Control) Paused() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.paused
}

func (c *Control) Wait(ctx context.Context) error {
	for {
		c.mutex.Lock()
		paused := c.paused
		wakeup := c.wakeup
		c.mutex.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wakeup:
		}
	}
}
