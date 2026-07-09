# The `default` routing policy

The calibrated out-of-the-box policy (`whittle policy init` writes it with the
model ids your account actually uses). Design: **conservative** — whittle only
changes your model when a rule affirmatively matches; on any doubt, your request
runs untouched on the model you asked for.

| when | routed to |
|---|---|
| hard reasoning (contrastive complexity > 0.15) **or** confidently quantitative (math/physics/chemistry mass ≥ 0.7) | `smart` (opus) |
| confidently casual (non-academic mass ≥ 0.85) **and** trivially easy | `fast` (haiku) |
| confidently casual **and** medium (drafts, plans, brainstorms) | `main` (sonnet) |
| **everything else** — all coding traffic included | untouched (`default: "requested"`) |

A down-route needs **two signals agreeing**; an up-route needs one — misroute
risk is priced by direction. The thresholds were calibrated against live traffic
(e.g. the 0.85 casual gate tolerates the ~0.05–0.10 confidence loss real typos
cause).

## Customizing (`~/.whittle/router.json`)

- **Your models**: edit the `tiers` — use full dated ids (`claude-sonnet-4-5-20250929`);
  bare ids are often rejected upstream.
- **More savings**: set `"default": "main"` — unmatched traffic then rides sonnet
  instead of your requested model (quality trade, biggest cost lever).
- **Route your own patterns**: add a route with whole-word `keywords`, or extend
  the `hard`/`easy` example banks — the complexity signal scores similarity to
  those phrasings, so add examples *shaped like your real requests*.
- **Tune the gates**: `min_mass` (domain confidence) and `threshold` (complexity
  dead-band). Watch the `"signals"` field in the router log — it shows every
  computed value against its gate (`dom:casual=0.899/0.85`), so you can see
  exactly why a request routed before touching anything.
- **Escape hatch**: send header `x-whittle-route: <tier>` to pin any request.

After editing: `whittle policy validate ~/.whittle/router.json`, then `kill -HUP`
the router (or restart) — a bad edit keeps the running policy.

Full architecture and signal math: [docs/ROUTER.md](../../docs/ROUTER.md).
