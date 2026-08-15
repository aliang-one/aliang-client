function objectValue(value) {
  return value && typeof value === 'object' ? value : null;
}

function envelopeLayers(envelope) {
  const layers = [];
  let current = objectValue(envelope);
  for (let depth = 0; current && depth < 3; depth += 1) {
    layers.push(current);
    current = objectValue(current.data);
  }
  return layers;
}

function normalized(value) {
  return typeof value === 'string' ? value.trim().toLowerCase() : '';
}

export function isAuthenticationFailure(responseStatus, envelope) {
  if (Number(responseStatus) === 401) {
    return true;
  }

  return envelopeLayers(envelope).some((layer) => {
    const status = normalized(layer.status);
    const error = normalized(layer.error);
    const code = normalized(layer.code);
    const message = normalized(layer.message || layer.msg);
    return status === 'unauthenticated'
      || error === 'session_expired'
      || error === 'token_expired'
      || code === 'token_expired'
      || message.includes('local session is no longer valid');
  });
}

export function authenticationFailureMessage(envelope) {
  for (const layer of envelopeLayers(envelope)) {
    for (const field of ['msg', 'message', 'error']) {
      if (typeof layer[field] === 'string' && layer[field].trim()) {
        return layer[field].trim();
      }
    }
  }
  return '';
}

export function handleAuthenticationFailure(responseStatus, envelope, onFailure) {
  if (!isAuthenticationFailure(responseStatus, envelope)) {
    return false;
  }
  if (typeof onFailure === 'function') {
    onFailure(authenticationFailureMessage(envelope));
  }
  return true;
}
