package ratelimit

type ConcurrencyLimiter struct {
	semaphore chan struct{}
}

func NewConcurrencyLimiter(maxConcurrent int) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

func (c *ConcurrencyLimiter) TryAcquire() bool {
	select {
	case c.semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *ConcurrencyLimiter) Release() {
	<-c.semaphore
}
