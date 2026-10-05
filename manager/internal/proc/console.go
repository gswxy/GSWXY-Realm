package proc

import "sync"

// consolePipe tracks the commands the Manager has sent to a child's stdin
// console (worldserver). The child's stdout/stderr are captured to its log
// file, which the API tails separately — nothing here ever reads from the
// child's stdin pipe, because the child owns the read end.
type consolePipe struct {
	mu     sync.Mutex
	ring   []string
	closed bool
}

func newConsolePipe() *consolePipe { return &consolePipe{} }

// record appends a sent command to the bounded ring.
func (c *consolePipe) record(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ring = append(c.ring, line)
	if len(c.ring) > 500 {
		c.ring = c.ring[len(c.ring)-500:]
	}
}

func (c *consolePipe) tail(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ring) <= n {
		return append([]string(nil), c.ring...)
	}
	return append([]string(nil), c.ring[len(c.ring)-n:]...)
}

func (c *consolePipe) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}
