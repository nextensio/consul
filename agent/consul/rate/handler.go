// package rate implements server-side RPC rate limiting.
package rate

import (
	"context"
	"errors"
	"net"
	"sync/atomic"

	"github.com/hashicorp/consul/agent/consul/multilimiter"
)

var (
	// ErrRetryElsewhere indicates that the operation was not allowed because the
	// rate limit was exhausted, but may succeed on a different server.
	//
	// Results in a RESOURCE_EXHAUSTED or "429 Too Many Requests" response.
	ErrRetryElsewhere = errors.New("rate limit exceeded, try a different server")

	// ErrRetryLater indicates that the operation was not allowed because the rate
	// limit was exhausted, and trying a different server won't help (e.g. because
	// the operation can only be performed on the leader).
	//
	// Results in an UNAVAILABLE or "503 Service Unavailable" response.
	ErrRetryLater = errors.New("rate limit exceeded, try again later")
)

// Mode determines the action that will be taken when a rate limit has been
// exhausted (e.g. log and allow, or reject).
type Mode int

const (
	// ModeDisabled causes rate limiting to be bypassed.
	ModeDisabled Mode = iota

	// ModePermissive causes the handler to log the rate-limited operation but
	// still allow it to proceed.
	ModePermissive

	// ModeEnforcing causes the handler to reject the rate-limited operation.
	ModeEnforcing
)

var modeToName = map[Mode]string{
	ModeDisabled:   "disabled",
	ModeEnforcing:  "enforcing",
	ModePermissive: "permissive",
}
var modeFromName = func() map[string]Mode {
	vals := map[string]Mode{
		"": ModeDisabled,
	}
	for k, v := range modeToName {
		vals[v] = k
	}
	return vals
}()

func (m Mode) String() string {
	return modeToName[m]
}

// RequestLimitsModeFromName will unmarshal the string form of a configMode.
func RequestLimitsModeFromName(name string) (Mode, bool) {
	s, ok := modeFromName[name]
	return s, ok
}

// RequestLimitsModeFromNameWithDefault will unmarshal the string form of a configMode.
func RequestLimitsModeFromNameWithDefault(name string) Mode {
	s, ok := modeFromName[name]
	if !ok {
		return ModePermissive
	}
	return s
}

// OperationType is the type of operation the client is attempting to perform.
type OperationType int

const (
	// OperationTypeRead represents a read operation.
	OperationTypeRead OperationType = iota

	// OperationTypeWrite represents a write operation.
	OperationTypeWrite
)

// Operation the client is attempting to perform.
type Operation struct {
	// Name of the RPC endpoint (e.g. "Foo.Bar" for net/rpc and "/foo.service/Bar" for gRPC).
	Name string

	// SourceAddr is the client's (or forwarding server's) IP address.
	SourceAddr net.Addr

	// Type of operation to be performed (e.g. read or write).
	Type OperationType
}

//go:generate mockery --name RequestLimitsHandler --inpackage --filename mock_RequestLimitsHandler_test.go
type RequestLimitsHandler interface {
	Run(ctx context.Context)
	Allow(op Operation) error
	UpdateConfig(cfg HandlerConfig)
}

// Handler enforces rate limits for incoming RPCs.
type Handler struct {
	cfg      *atomic.Pointer[HandlerConfig]
	delegate HandlerDelegate

	limiter multilimiter.RateLimiter
}

type HandlerConfig struct {
	multilimiter.Config

	// GlobalMode configures the action that will be taken when a global rate-limit
	// has been exhausted.
	//
	// Note: in the future there'll be a separate Mode for IP-based limits.
	GlobalMode Mode

	// GlobalWriteConfig configures the global rate limiter for write operations.
	GlobalWriteConfig multilimiter.LimiterConfig

	// GlobalReadConfig configures the global rate limiter for read operations.
	GlobalReadConfig multilimiter.LimiterConfig
}

type HandlerDelegate interface {
	// IsLeader is used to determine whether the operation is being performed
	// against the cluster leader, such that if it can _only_ be performed by
	// the leader (e.g. write operations) we don't tell clients to retry against
	// a different server.
	IsLeader() bool
}

// NewHandler creates a new RPC rate limit handler.
func NewHandler(cfg HandlerConfig, delegate HandlerDelegate) *Handler {
	limiter := multilimiter.NewMultiLimiter(cfg.Config)
	limiter.UpdateConfig(cfg.GlobalWriteConfig, globalWrite)
	limiter.UpdateConfig(cfg.GlobalReadConfig, globalRead)

	h := &Handler{
		cfg:      new(atomic.Pointer[HandlerConfig]),
		delegate: delegate,
		limiter:  limiter,
	}
	h.cfg.Store(&cfg)

	return h
}

// Run the limiter cleanup routine until the given context is canceled.
//
// Note: this starts a goroutine.
func (h *Handler) Run(ctx context.Context) {
	h.limiter.Run(ctx)
}

// Allow returns an error if the given operation is not allowed to proceed
// because of an exhausted rate-limit.
func (h *Handler) Allow(op Operation) error {
	// TODO(NET-1383): actually implement the rate limiting logic.
	//
	// Example:
	//	if !h.limiter.Allow(globalWrite) {
	//	}
	return nil
}

func (h *Handler) UpdateConfig(cfg HandlerConfig) {
	h.cfg.Store(&cfg)
	h.limiter.UpdateConfig(cfg.GlobalWriteConfig, globalWrite)
	h.limiter.UpdateConfig(cfg.GlobalReadConfig, globalRead)
}

var (
	// globalWrite identifies the global rate limit applied to write operations.
	globalWrite = globalLimit("global.write")

	// globalRead identifies the global rate limit applied to read operations.
	globalRead = globalLimit("global.read")
)

// globalLimit represents a limit that applies to all writes or reads.
type globalLimit []byte

// Key satisfies the multilimiter.LimitedEntity interface.
func (prefix globalLimit) Key() multilimiter.KeyType {
	return multilimiter.Key(prefix, nil)
}

// NullRateLimiter returns a RateLimiter that allows every operation.
func NullRateLimiter() RequestLimitsHandler {
	return nullRateLimiter{}
}

type nullRateLimiter struct{}

func (nullRateLimiter) Allow(Operation) error { return nil }

func (nullRateLimiter) Run(ctx context.Context) {}

func (nullRateLimiter) UpdateConfig(cfg HandlerConfig) {}
