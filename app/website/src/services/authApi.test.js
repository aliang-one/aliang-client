import { afterEach, describe, expect, it, vi } from 'vitest';
import { AuthRequestError, restoreSession } from './authApi';

afterEach(() => {
  vi.unstubAllGlobals();
});

function response(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: vi.fn().mockResolvedValue(body)
  };
}

describe('auth API error classification', () => {
  it('classifies a transport failure without treating it as logout', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('connection refused')));

    await expect(restoreSession()).rejects.toMatchObject({
      name: 'AuthRequestError',
      type: 'transport_unavailable',
      status: 0
    });
  });

  it('does not infer terminal session invalidation from a bare HTTP 401', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(401, {
      code: 1001,
      msg: 'request unauthorized'
    })));

    await expect(restoreSession()).rejects.toEqual(expect.objectContaining({
      type: 'server_error',
      status: 401
    }));
  });

  it('classifies only an explicit structured session_invalid response as terminal', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(401, {
      code: 1001,
      data: {
        error: 'session_invalid',
        msg: 'login required'
      }
    })));

    try {
      await restoreSession();
      throw new Error('expected restoreSession to reject');
    } catch (error) {
      expect(error).toBeInstanceOf(AuthRequestError);
      expect(error).toMatchObject({
        type: 'session_invalid',
        status: 401,
        code: 'session_invalid'
      });
    }
  });
});
