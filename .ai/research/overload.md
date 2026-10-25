# Overload control: CoDel for request queues

Legend: ✅ verified (with a source link) · ❓ unverified.

What kest's `pool.overload` (M5, K-144, plan `plans/M5-pools.md` D4) borrows, checked on 2026-10-25.

- **CoDel** (controlled delay) is an active queue management algorithm for network queues (Nichols and Jacobson,
  "Controlling Queue Delay", [ACM Queue, 2012](http://queue.acm.org/detail.cfm?id=2209336), the reference folly
  gives): it watches the minimum time packets spend in the queue over an interval, and drops when that minimum
  stays above a target, since a queue that never drains is a standing queue that only adds delay ❓ (the paper was
  not fetched; folly's summary below agrees).
- **Facebook's adaptation for RPC servers** ✅:
  [`folly/executors/Codel.h`](https://github.com/facebook/folly/blob/e3df75d53cada2640b5bba1e9f9d016d0eaf1500/folly/executors/Codel.h#L24-L51):
  "if every request has experienced queueing delay greater than the target (5ms) during the past interval (100ms),
  then we shed load", and "when overloaded slough off requests that we dequeue which have exceeded an alternate
  timeout (2 * target_delay)". Ben Maurer's "Fail at Scale" (ACM Queue, October 2015) describes Facebook's use of
  it, with adaptive LIFO ❓: the article answers 403 to fetches, and only its listing and reference to CoDel were
  seen.

## kest's variant

- **Overloaded** means requests have waited in a lane without a break for `interval`: only a worker taking the last
  waiting request ends the backlog. That is CoDel's "the minimum delay stayed above zero" with a target of zero, and
  it can't flap when requests leave the queue by timing out or being shed.
- **In an overloaded lane** new requests wait at most `queue_timeout`, and a free worker sheds the requests at the
  head that waited longer (folly's "slough off at dequeue"). Lanes with `max_workers` or a `queue_timeout` of their
  own are left out.
- **Defaults:** 500 ms after 5 s, not folly's 5 ms after 100 ms: PHP requests take tens to hundreds of milliseconds,
  and smaller values turned away requests at 95 % load in the measurement (`docs/benchmarks.md` § Overload control).
- No adaptive LIFO: lanes and priorities order the queue instead.
