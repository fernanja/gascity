/**
 * The order-detail route href for a scoped order name. Always link by
 * scoped_name, never bare name: rig-scoped orders share a bare name across
 * rigs and the backend 409s a bare-name lookup that matches more than one.
 * encodeURIComponent keeps the `name:rig:<rig>` colons routable.
 */
export function orderDetailHref(scopedName: string): string {
  return `/orders/${encodeURIComponent(scopedName)}`;
}
