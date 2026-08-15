function asObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : null;
}

export function extractUsageRecords(payload) {
  if (Array.isArray(payload)) {
    return payload;
  }

  const queue = [payload];
  const seen = new Set();

  while (queue.length > 0) {
    const current = queue.shift();
    if (current == null || seen.has(current)) {
      continue;
    }
    seen.add(current);

    if (Array.isArray(current)) {
      return current;
    }

    const record = asObject(current);
    if (!record) {
      continue;
    }

    for (const key of ['items', 'records', 'list', 'results', 'data']) {
      if (Array.isArray(record[key])) {
        return record[key];
      }
    }

    for (const key of ['pagination', 'meta']) {
      const nested = asObject(record[key]);
      if (nested) {
        queue.push(nested);
      }
    }

    for (const key of ['items', 'records', 'list', 'results', 'data']) {
      const nested = asObject(record[key]);
      if (nested) {
        queue.push(nested);
      }
    }
  }

  return [];
}

export function extractUsagePagination(payload, fallbackPage = 1, fallbackPageSize = 20) {
  const defaults = {
    page: fallbackPage,
    pageSize: fallbackPageSize,
    total: extractUsageRecords(payload).length,
    totalPages: 1
  };

  const queue = [payload];
  const seen = new Set();

  while (queue.length > 0) {
    const current = queue.shift();
    if (current == null || seen.has(current)) {
      continue;
    }
    seen.add(current);

    const record = asObject(current);
    if (!record) {
      continue;
    }

    const hasPaginationField = [
      'page',
      'current_page',
      'page_index',
      'page_size',
      'per_page',
      'limit',
      'total',
      'count',
      'total_count',
      'total_pages',
      'pages',
      'last_page'
    ].some((key) => typeof record[key] !== 'undefined');

    if (hasPaginationField) {
      const page = Number(record.page || record.current_page || record.page_index || defaults.page);
      const pageSize = Number(record.page_size || record.per_page || record.limit || defaults.pageSize);
      const total = Number(record.total || record.count || record.total_count || defaults.total);
      const totalPages = Number(record.total_pages || record.pages || record.last_page || 0);
      const resolvedPage = Number.isFinite(page) && page > 0 ? page : defaults.page;
      const resolvedPageSize = Number.isFinite(pageSize) && pageSize > 0 ? pageSize : defaults.pageSize;
      const resolvedTotal = Number.isFinite(total) && total >= 0 ? total : defaults.total;
      const derivedTotalPages = resolvedPageSize > 0 ? Math.max(1, Math.ceil(resolvedTotal / resolvedPageSize)) : 1;

      return {
        page: resolvedPage,
        pageSize: resolvedPageSize,
        total: resolvedTotal,
        totalPages: Number.isFinite(totalPages) && totalPages > 0 ? totalPages : derivedTotalPages
      };
    }

    for (const key of ['pagination', 'meta', 'data']) {
      const nested = asObject(record[key]);
      if (nested) {
        queue.push(nested);
      }
    }
  }

  return defaults;
}
