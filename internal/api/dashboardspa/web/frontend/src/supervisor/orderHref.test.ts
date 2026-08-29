import { describe, expect, it } from 'vitest';

import { orderDetailHref } from './orderHref';

describe('orderDetailHref', () => {
  it('encodes rig-scoped names so the colons stay routable', () => {
    expect(orderDetailHref('dolt-health:rig:demo-repo')).toBe(
      '/orders/dolt-health%3Arig%3Ademo-repo',
    );
  });

  it('passes plain city-scoped names through', () => {
    expect(orderDetailHref('mayor-heartbeat')).toBe('/orders/mayor-heartbeat');
  });

  it('round-trips through decodeURIComponent (what useParams hands back)', () => {
    const scoped = 'gate-sweep:rig:ascent';
    const href = orderDetailHref(scoped);
    const segment = href.split('/').pop() ?? '';
    expect(decodeURIComponent(segment)).toBe(scoped);
  });
});
