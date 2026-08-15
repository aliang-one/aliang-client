import { describe, expect, it } from 'vitest';
import {
  createSessionViewState,
  reduceSessionReadFailure,
  reduceSessionSnapshot
} from './sessionSnapshot';

function snapshot(instanceId, revision, state, user) {
  return {
    type: 'session_snapshot',
    instance_id: instanceId,
    revision,
    state,
    ...(user ? { user } : {})
  };
}

describe('session snapshot reducer', () => {
  it('accepts a newer active snapshot and rejects stale revisions', () => {
    const initial = createSessionViewState();
    const active = reduceSessionSnapshot(initial, snapshot('backend-a', 4, 'active', {
      id: 7,
      username: 'alice'
    }));
    expect(active.accepted).toBe(true);
    expect(active.state.isAuthenticated).toBe(true);
    expect(active.state.user.username).toBe('alice');

    const stale = reduceSessionSnapshot(active.state, snapshot('backend-a', 3, 'unauthenticated'));
    expect(stale.accepted).toBe(false);
    expect(stale.state.user.username).toBe('alice');
  });

  it('allows a backend restart once and rejects delayed snapshots from the retired instance', () => {
    const first = reduceSessionSnapshot(
      createSessionViewState(),
      snapshot('backend-a', 9, 'active', { username: 'alice' })
    ).state;
    const restarted = reduceSessionSnapshot(first, snapshot('backend-b', 1, 'restoring'));
    expect(restarted.accepted).toBe(true);
    expect(restarted.state.user.username).toBe('alice');

    const delayed = reduceSessionSnapshot(
      restarted.state,
      snapshot('backend-a', 10, 'unauthenticated')
    );
    expect(delayed.accepted).toBe(false);
    expect(delayed.state.user.username).toBe('alice');
  });

  it('clears identity only for an authoritative terminal snapshot', () => {
    const active = reduceSessionSnapshot(
      createSessionViewState(),
      snapshot('backend-a', 1, 'active', { username: 'alice' })
    ).state;
    const unavailable = reduceSessionReadFailure(active, 'transport_unavailable');
    expect(unavailable.isAuthenticated).toBe(true);
    expect(unavailable.user.username).toBe('alice');

    const recovering = reduceSessionSnapshot(
      unavailable,
      snapshot('backend-a', 2, 'soft_expired', { username: 'alice' })
    ).state;
    expect(recovering.isAuthenticated).toBe(true);

    const terminal = reduceSessionSnapshot(
      recovering,
      snapshot('backend-a', 3, 'hard_invalid')
    ).state;
    expect(terminal.isAuthenticated).toBe(false);
    expect(terminal.user).toBeNull();
  });

  it('rejects malformed active snapshots without a user', () => {
    const result = reduceSessionSnapshot(
      createSessionViewState(),
      snapshot('backend-a', 1, 'active')
    );
    expect(result.accepted).toBe(false);
  });
});
