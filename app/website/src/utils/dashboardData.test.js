import { expect, it } from 'vitest';

import { extractUsagePagination, extractUsageRecords } from './dashboardData.js';

it('extractUsageRecords accepts direct array payloads', () => {
	const records = [{ id: 1 }, { id: 2 }];
	expect(extractUsageRecords(records)).toEqual(records);
});

it('extractUsageRecords accepts paginated payloads', () => {
  const records = [{ id: 1001, model: 'claude-sonnet-4-20250514' }];

	expect(extractUsageRecords({
			items: records,
			total: 1,
			page: 1
		})).toEqual(records);

	expect(extractUsageRecords({
			data: {
				items: records,
				total: 1
			}
		})).toEqual(records);
});

it('extractUsagePagination reads pagination metadata from payloads', () => {
	expect(extractUsagePagination({
      items: [{ id: 1 }],
      total: 42,
      page: 2,
      page_size: 20,
      total_pages: 3
		})).toEqual({
      page: 2,
      pageSize: 20,
      total: 42,
      totalPages: 3
		});

	expect(extractUsagePagination({
      data: {
        items: [{ id: 1 }],
        pagination: {
          total: 8,
          page: 1,
          page_size: 5,
          pages: 2
        }
      }
		}, 1, 5)).toEqual({
      page: 1,
      pageSize: 5,
      total: 8,
      totalPages: 2
		});
});
