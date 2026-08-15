import { describe, expect, it, vi } from 'vitest';
import {
  authenticationFailureMessage,
  handleAuthenticationFailure,
  isAuthenticationFailure
} from './authFailure';

describe('authentication failure classification', () => {
  it('recognizes HTTP 401 and delegates reconciliation', () => {
    const reconcile = vi.fn();
    const envelope = { code: 1001, msg: 'Token has expired' };
    expect(isAuthenticationFailure(401, envelope)).toBe(true);
    expect(handleAuthenticationFailure(401, envelope, reconcile)).toBe(true);
    expect(reconcile).toHaveBeenCalledWith('Token has expired');
  });

  it('recognizes nested terminal envelopes', () => {
    const envelope = { data: { status: 'unauthenticated', error: 'session_expired', msg: 'Login again' } };
    expect(isAuthenticationFailure(200, envelope)).toBe(true);
    expect(authenticationFailureMessage(envelope)).toBe('Login again');
  });

  it('does not treat ordinary server failures as session invalidation', () => {
    const reconcile = vi.fn();
    expect(handleAuthenticationFailure(500, { msg: 'temporary failure' }, reconcile)).toBe(false);
    expect(reconcile).not.toHaveBeenCalled();
  });
});
