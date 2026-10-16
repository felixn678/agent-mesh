# Morning report

Window: 2026-10-15 16:30 UTC → 2026-10-16 08:30 UTC (16h00m)

Totals: 3 done, 2 stuck, 1 running, 3 queued, 0 cancelled. Cost: $0.90 (client-side estimate, not a bill).

## Needs attention

Stuck (2):

| # | repo | host | attempts | reason | session | worktree |
|---|---|---|---|---|---|---|
| #4 | api | host-b | 1/1 | lease expired on host-b at attempt cap | - | - |
| #5 | web | host-a | 1/3 | secret scan: possible token in config/app.env \| line 3 publish blocked | - | /srv/agent-mesh/worktrees/web-5 |

- #6 lease expired 15m ago on host-a
- #3 leftover_commit on host-b: agent left uncommitted changes; the daemon committed them — review before merging (agentmesh/3-bump-deps)

## Done (3)

| # | repo | host | branch | PR | cost | attempts | note |
|---|---|---|---|---|---|---|---|
| #1 | web | host-a | agentmesh/1-fix-login | https://github.com/example/web/pull/12 | $0.42 | 1 | - |
| #2 | web | host-a | - | - | $0.05 | 1 | no_changes |
| #3 | api | host-b | agentmesh/3-bump-deps | https://github.com/example/api/pull/7 | $0.31 | 1 | leftover_commit |

## Running (1)

| # | host | lease |
|---|---|---|
| #6 | host-a | expired 15m ago |

## Queued (3)

api: 1, web: 2

## Hosts

- host-a: 8c/8GB → 1 slot, throttled: cpu 85% · seen 10m ago · Example CPU 8-core · cpu 85% · mem avail 41% · worktrees 2 · running 1 · claude 2.1.0 · agent 0.1.0
- host-b: 16c/64GB → 3 slots (override) · seen 1m ago · cpu 12% · mem avail 70% · worktrees 0 · running 0 · claude 2.0.9 · agent 0.1.0
- host-c: 4c/16GB → 1 slot, throttled: unhealthy: gh auth failed · seen 30m ago · cpu 3% · mem avail 80% · worktrees 0 · running 0 · claude 2.1.0 · agent 0.1.0
