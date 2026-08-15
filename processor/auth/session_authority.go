package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
	"github.com/google/uuid"
)

// SessionAuthority owns the process-wide authentication snapshot. The snapshot
// is immutable once published: readers get one coherent state/user/revision
// tuple instead of combining independently locked globals.
//
// Authentication operations capture Generation before remote I/O. A successful
// commit, logout, or demotion increments Generation and cancels every peer
// operation. This prevents an old restore/refresh/login response from reviving a
// session after logout or permanent invalidation.

type SessionState int

const (
	StateRestoring SessionState = iota
	StateUnauthenticated
	StateActive
	StateSoftExpired
	StateHardInvalid
)

func (s SessionState) String() string {
	switch s {
	case StateRestoring:
		return "restoring"
	case StateActive:
		return "active"
	case StateSoftExpired:
		return "soft_expired"
	case StateHardInvalid:
		return "hard_invalid"
	default:
		return "unauthenticated"
	}
}

type SessionReason string

const (
	ReasonBoot               SessionReason = "boot"
	ReasonNoSession          SessionReason = "no_session"
	ReasonLogin              SessionReason = "login"
	ReasonRestored           SessionReason = "restored"
	ReasonRefreshed          SessionReason = "refreshed"
	ReasonRestoreUnavailable SessionReason = "restore_unavailable"
	ReasonAccessRejected     SessionReason = "access_rejected"
	ReasonRefreshInvalid     SessionReason = "refresh_invalid"
	ReasonSoftExpiryTimeout  SessionReason = "soft_expiry_timeout"
	ReasonRevoked            SessionReason = "revoked"
	ReasonLogout             SessionReason = "logout"
)

type SessionSnapshot struct {
	InstanceID string
	Revision   uint64
	Generation uint64
	State      SessionState
	Reason     SessionReason
	User       *UserInfo
}

type SessionEvent struct {
	From     SessionState
	To       SessionState
	Reason   SessionReason
	User     *UserInfo
	Snapshot SessionSnapshot
}

type SessionListener func(SessionEvent)

var ErrStaleSessionOperation = errors.New("stale auth session operation")
var ErrProxyAdmissionDenied = errors.New("proxy admission requires an active session")

type sessionOperationEntry struct {
	cancel context.CancelFunc
}

type SessionOperation struct {
	authority  *SessionAuthority
	id         uint64
	generation uint64
	ctx        context.Context
	closeOnce  sync.Once
}

type proxyFlowEntry struct {
	close func()
}

// ProxyLease represents one admitted proxy flow. Admission and session
// demotion are serialized by SessionAuthority.mu, so either the flow is
// registered and subsequently closed by demotion, or admission is rejected.
type ProxyLease struct {
	authority *SessionAuthority
	id        uint64
	closeOnce sync.Once
}

func (l *ProxyLease) Release() {
	if l == nil || l.authority == nil {
		return
	}
	l.closeOnce.Do(func() {
		l.authority.releaseProxyLease(l.id)
	})
}

func (o *SessionOperation) Context() context.Context {
	if o == nil || o.ctx == nil {
		return context.Background()
	}
	return o.ctx
}

func (o *SessionOperation) Generation() uint64 {
	if o == nil {
		return 0
	}
	return o.generation
}

func (o *SessionOperation) Close() {
	if o == nil || o.authority == nil {
		return
	}
	o.closeOnce.Do(func() {
		o.authority.finishOperation(o.id)
	})
}

type SessionAuthority struct {
	initOnce sync.Once
	mu       sync.Mutex

	snapshot atomic.Pointer[SessionSnapshot]

	listeners     []SessionListener
	operations    map[uint64]sessionOperationEntry
	nextOperation uint64
	proxyFlows    map[uint64]proxyFlowEntry
	nextProxyFlow uint64

	rejectedProxyAdmissions atomic.Uint64
	forcedProxyFlowCloses   atomic.Uint64
	staleOperationCommits   atomic.Uint64
	staleSideEffects        atomic.Uint64
}

func newSessionAuthority(initial SessionState) *SessionAuthority {
	a := &SessionAuthority{}
	a.initialize(initial)
	return a
}

func (a *SessionAuthority) initialize(initial SessionState) {
	a.initOnce.Do(func() {
		a.operations = make(map[uint64]sessionOperationEntry)
		a.proxyFlows = make(map[uint64]proxyFlowEntry)
		a.snapshot.Store(&SessionSnapshot{
			InstanceID: uuid.NewString(),
			Revision:   1,
			Generation: 1,
			State:      initial,
			Reason:     ReasonBoot,
		})
	})
}

func (a *SessionAuthority) ensureInitialized() {
	a.initialize(StateRestoring)
}

func cloneUserInfo(user *UserInfo) *UserInfo {
	if user == nil {
		return nil
	}
	clone := *user
	clone.AllowedGroups = append([]int64(nil), user.AllowedGroups...)
	return &clone
}

func cloneSessionSnapshot(snapshot *SessionSnapshot) SessionSnapshot {
	if snapshot == nil {
		return SessionSnapshot{State: StateRestoring, Reason: ReasonBoot}
	}
	clone := *snapshot
	clone.User = cloneUserInfo(snapshot.User)
	return clone
}

func (a *SessionAuthority) currentSnapshot() *SessionSnapshot {
	a.ensureInitialized()
	return a.snapshot.Load()
}

func (a *SessionAuthority) Snapshot() SessionSnapshot {
	return cloneSessionSnapshot(a.currentSnapshot())
}

func (a *SessionAuthority) State() SessionState {
	return a.currentSnapshot().State
}

func (a *SessionAuthority) CanProxy() bool {
	return a.currentSnapshot().State == StateActive
}

func (a *SessionAuthority) AcquireProxyLease(closeFlow func()) (*ProxyLease, error) {
	a.ensureInitialized()
	a.mu.Lock()
	if a.snapshot.Load().State != StateActive {
		a.rejectedProxyAdmissions.Add(1)
		a.mu.Unlock()
		return nil, ErrProxyAdmissionDenied
	}
	a.nextProxyFlow++
	id := a.nextProxyFlow
	a.proxyFlows[id] = proxyFlowEntry{close: closeFlow}
	a.mu.Unlock()
	return &ProxyLease{authority: a, id: id}, nil
}

func (a *SessionAuthority) releaseProxyLease(id uint64) {
	a.ensureInitialized()
	a.mu.Lock()
	delete(a.proxyFlows, id)
	a.mu.Unlock()
}

func (a *SessionAuthority) ProxyAdmissionStats() (rejected, forcedClosed uint64) {
	return a.rejectedProxyAdmissions.Load(), a.forcedProxyFlowCloses.Load()
}

type SessionAuthorityStats struct {
	RejectedProxyAdmissions uint64
	ForcedProxyFlowCloses   uint64
	StaleOperationCommits   uint64
	StaleSideEffects        uint64
	ActiveProxyFlows        int
}

func (a *SessionAuthority) Stats() SessionAuthorityStats {
	a.ensureInitialized()
	a.mu.Lock()
	activeFlows := len(a.proxyFlows)
	a.mu.Unlock()
	return SessionAuthorityStats{
		RejectedProxyAdmissions: a.rejectedProxyAdmissions.Load(),
		ForcedProxyFlowCloses:   a.forcedProxyFlowCloses.Load(),
		StaleOperationCommits:   a.staleOperationCommits.Load(),
		StaleSideEffects:        a.staleSideEffects.Load(),
		ActiveProxyFlows:        activeFlows,
	}
}

func (a *SessionAuthority) Subscribe(listener SessionListener) {
	if listener == nil {
		return
	}
	a.ensureInitialized()
	a.mu.Lock()
	a.listeners = append(a.listeners, listener)
	a.mu.Unlock()
}

var (
	globalSessionListenersMu sync.Mutex
	globalSessionListeners   []SessionListener
)

// SubscribeGlobal registers a process-lifetime listener and attaches it to the
// current authority. Test resets rebuild the singleton and reattach these
// listeners, while ordinary Subscribe calls remain scoped to one authority.
func SubscribeGlobal(listener SessionListener) {
	if listener == nil {
		return
	}
	globalSessionListenersMu.Lock()
	globalSessionListeners = append(globalSessionListeners, listener)
	globalSessionListenersMu.Unlock()
	GetSessionAuthority().Subscribe(listener)
}

func (a *SessionAuthority) BeginOperation(parent ...context.Context) *SessionOperation {
	a.ensureInitialized()
	base := context.Background()
	if len(parent) > 0 && parent[0] != nil {
		base = parent[0]
	}
	ctx, cancel := context.WithCancel(base)

	a.mu.Lock()
	current := a.snapshot.Load()
	a.nextOperation++
	id := a.nextOperation
	a.operations[id] = sessionOperationEntry{cancel: cancel}
	a.mu.Unlock()

	return &SessionOperation{
		authority:  a,
		id:         id,
		generation: current.Generation,
		ctx:        ctx,
	}
}

func (a *SessionAuthority) finishOperation(id uint64) {
	a.ensureInitialized()
	a.mu.Lock()
	entry, ok := a.operations[id]
	if ok {
		delete(a.operations, id)
	}
	a.mu.Unlock()
	if ok {
		entry.cancel()
	}
}

func (a *SessionAuthority) operationCurrentLocked(operation *SessionOperation) bool {
	if operation == nil || operation.authority != a {
		return false
	}
	if _, ok := a.operations[operation.id]; !ok {
		return false
	}
	return a.snapshot.Load().Generation == operation.generation
}

func (a *SessionAuthority) OperationCurrent(operation *SessionOperation) bool {
	a.ensureInitialized()
	a.mu.Lock()
	current := a.operationCurrentLocked(operation)
	a.mu.Unlock()
	return current
}

func (a *SessionAuthority) GenerationActive(generation uint64) bool {
	snapshot := a.currentSnapshot()
	return snapshot.Generation == generation && snapshot.State == StateActive
}

// RunIfGenerationActive serializes a short local side effect with terminal
// transitions. If logout wins first the callback is skipped; if the callback
// wins first logout runs immediately afterwards and tears the side effect down.
func (a *SessionAuthority) RunIfGenerationActive(generation uint64, callback func()) bool {
	if callback == nil {
		return false
	}
	a.ensureInitialized()
	a.mu.Lock()
	snapshot := a.snapshot.Load()
	if snapshot.Generation != generation || snapshot.State != StateActive {
		a.staleSideEffects.Add(1)
		a.mu.Unlock()
		return false
	}
	callback()
	a.mu.Unlock()
	return true
}

func (a *SessionAuthority) cancelOperationsLocked() {
	for id, operation := range a.operations {
		operation.cancel()
		delete(a.operations, id)
	}
}

func (a *SessionAuthority) publishLocked(to SessionState, reason SessionReason, user *UserInfo, invalidateOperations bool) (SessionEvent, []SessionListener, []func()) {
	current := a.snapshot.Load()
	generation := current.Generation
	if invalidateOperations {
		generation++
		a.cancelOperationsLocked()
	}
	next := &SessionSnapshot{
		InstanceID: current.InstanceID,
		Revision:   current.Revision + 1,
		Generation: generation,
		State:      to,
		Reason:     reason,
		User:       cloneUserInfo(user),
	}
	a.snapshot.Store(next)

	var flowClosers []func()
	if current.State == StateActive && to != StateActive {
		flowClosers = make([]func(), 0, len(a.proxyFlows))
		for id, flow := range a.proxyFlows {
			if flow.close != nil {
				flowClosers = append(flowClosers, flow.close)
			}
			delete(a.proxyFlows, id)
		}
		a.forcedProxyFlowCloses.Add(uint64(len(flowClosers)))
	}

	eventSnapshot := cloneSessionSnapshot(next)
	event := SessionEvent{
		From:     current.State,
		To:       to,
		Reason:   reason,
		User:     cloneUserInfo(next.User),
		Snapshot: eventSnapshot,
	}
	return event, append([]SessionListener(nil), a.listeners...), flowClosers
}

func finishSessionTransition(event SessionEvent, listeners []SessionListener, flowClosers []func()) {
	for _, closeFlow := range flowClosers {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Warn(fmt.Sprintf("proxy flow close callback panicked (event %s->%s reason=%s): %v", event.From, event.To, event.Reason, recovered))
				}
			}()
			closeFlow()
		}()
	}
	for _, listener := range listeners {
		func(listener SessionListener) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Warn(fmt.Sprintf("session authority listener panicked (event %s->%s reason=%s): %v", event.From, event.To, event.Reason, recovered))
				}
			}()
			listener(event)
		}(listener)
	}
}

// CommitAuthenticated atomically validates the operation generation, persists
// the user, publishes Active, and invalidates competing operations. The persist
// callback must perform local-only work; remote I/O belongs before this method.
func (a *SessionAuthority) CommitAuthenticated(operation *SessionOperation, user *UserInfo, reason SessionReason, persist func(*UserInfo) error) error {
	_, err := a.CommitAuthenticatedSnapshot(operation, user, reason, persist)
	return err
}

func (a *SessionAuthority) CommitAuthenticatedSnapshot(operation *SessionOperation, user *UserInfo, reason SessionReason, persist func(*UserInfo) error) (SessionSnapshot, error) {
	if user == nil {
		return SessionSnapshot{}, errors.New("authenticated session requires user info")
	}
	if reason == "" {
		reason = ReasonRefreshed
	}
	a.ensureInitialized()
	a.mu.Lock()
	if !a.operationCurrentLocked(operation) {
		a.staleOperationCommits.Add(1)
		a.mu.Unlock()
		return SessionSnapshot{}, ErrStaleSessionOperation
	}
	committedUser := cloneUserInfo(user)
	if persist != nil {
		if err := persist(committedUser); err != nil {
			a.mu.Unlock()
			return SessionSnapshot{}, err
		}
	}
	event, listeners, flowClosers := a.publishLocked(StateActive, reason, committedUser, true)
	a.mu.Unlock()
	finishSessionTransition(event, listeners, flowClosers)
	return cloneSessionSnapshot(&event.Snapshot), nil
}

func (a *SessionAuthority) CommitSoftExpired(operation *SessionOperation, user *UserInfo, reason SessionReason) error {
	if reason == "" {
		reason = ReasonRestoreUnavailable
	}
	a.ensureInitialized()
	a.mu.Lock()
	if !a.operationCurrentLocked(operation) {
		a.staleOperationCommits.Add(1)
		a.mu.Unlock()
		return ErrStaleSessionOperation
	}
	event, listeners, flowClosers := a.publishLocked(StateSoftExpired, reason, user, true)
	a.mu.Unlock()
	finishSessionTransition(event, listeners, flowClosers)
	return nil
}

func (a *SessionAuthority) CommitUnauthenticated(operation *SessionOperation, reason SessionReason) error {
	if reason == "" {
		reason = ReasonNoSession
	}
	a.ensureInitialized()
	a.mu.Lock()
	if !a.operationCurrentLocked(operation) {
		a.staleOperationCommits.Add(1)
		a.mu.Unlock()
		return ErrStaleSessionOperation
	}
	event, listeners, flowClosers := a.publishLocked(StateUnauthenticated, reason, nil, true)
	a.mu.Unlock()
	finishSessionTransition(event, listeners, flowClosers)
	return nil
}

// NotifyLoggedIn remains the synchronous producer API for callers that already
// completed their local commit. New remote-I/O paths should use BeginOperation
// plus CommitAuthenticated.
func (a *SessionAuthority) NotifyLoggedIn(user *UserInfo) bool {
	return a.publishAuthenticated(user, ReasonLogin)
}

func (a *SessionAuthority) NotifyRefreshed(user *UserInfo) bool {
	return a.publishAuthenticated(user, ReasonRefreshed)
}

func (a *SessionAuthority) publishAuthenticated(user *UserInfo, reason SessionReason) bool {
	if user == nil {
		return false
	}
	a.ensureInitialized()
	a.mu.Lock()
	event, listeners, flowClosers := a.publishLocked(StateActive, reason, user, true)
	a.mu.Unlock()
	finishSessionTransition(event, listeners, flowClosers)
	return event.From != event.To
}

func (a *SessionAuthority) NotifyAccessRejected(_ string) bool {
	a.ensureInitialized()
	a.mu.Lock()
	from := a.snapshot.Load().State
	if from != StateActive {
		a.mu.Unlock()
		return false
	}
	event, listeners, flowClosers := a.publishLocked(StateSoftExpired, ReasonAccessRejected, a.snapshot.Load().User, true)
	a.mu.Unlock()
	finishSessionTransition(event, listeners, flowClosers)
	return true
}

func (a *SessionAuthority) NotifyRefreshFailed(permanent bool, reason SessionReason) bool {
	if !permanent {
		if reason == "" {
			reason = ReasonAccessRejected
		}
		return a.NotifyAccessRejected(string(reason))
	}
	if reason == "" {
		reason = ReasonRefreshInvalid
	}
	return a.forceTerminal(StateHardInvalid, reason)
}

func (a *SessionAuthority) NotifyLoggedOut() bool {
	return a.forceTerminal(StateUnauthenticated, ReasonLogout)
}

func (a *SessionAuthority) forceTerminal(to SessionState, reason SessionReason) bool {
	a.ensureInitialized()
	a.mu.Lock()
	event, listeners, flowClosers := a.publishLocked(to, reason, nil, true)
	a.mu.Unlock()
	finishSessionTransition(event, listeners, flowClosers)
	return event.From != event.To
}

func sessionReasonFromWipeReason(reason string) SessionReason {
	switch {
	case strings.Contains(strings.ToLower(reason), "invalid refresh"):
		return ReasonRefreshInvalid
	case strings.Contains(strings.ToLower(reason), "timeout"):
		return ReasonSoftExpiryTimeout
	case strings.Contains(strings.ToLower(reason), "revoke") || strings.Contains(reason, "吊销"):
		return ReasonRevoked
	default:
		return ReasonRefreshInvalid
	}
}

var (
	sessionAuthorityOnce sync.Once
	sessionAuthorityInst *SessionAuthority
)

func GetSessionAuthority() *SessionAuthority {
	sessionAuthorityOnce.Do(func() {
		sessionAuthorityInst = newSessionAuthority(StateRestoring)
	})
	return sessionAuthorityInst
}

func ResetSessionAuthorityForTest() *SessionAuthority {
	sessionAuthorityOnce = sync.Once{}
	sessionAuthorityInst = nil
	authority := GetSessionAuthority()
	globalSessionListenersMu.Lock()
	listeners := append([]SessionListener(nil), globalSessionListeners...)
	globalSessionListenersMu.Unlock()
	for _, listener := range listeners {
		authority.Subscribe(listener)
	}
	return authority
}
