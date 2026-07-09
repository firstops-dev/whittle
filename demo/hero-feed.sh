#!/bin/sh
# hero-feed.sh — whittle live-watch choreography for the README hero GIF.
#
# Emits a pre-sized, append-only ANSI stream (no scrolling, one clear) that
# replays a real captured session: four agent turns routed to the right model,
# tool outputs carved losslessly, then a `whittle stats`-style session summary.
#
# All color comes from this script's stdout (VHS `Type` can't emit ANSI).
# Reproducible from a clone: `sh demo/hero-feed.sh`.
#
# Palette (flat 16-color): reset \033[0m · bold \033[1m · dim \033[2m ·
# dim-italic \033[2;3m · green \033[32m · cyan \033[36m · blue \033[34m ·
# red \033[31m. Percent signs are doubled (%%) for printf.

# ── stream ──────────────────────────────────────────────────────────────────
sleep 0.6
printf '\033[2m🪓 whittle · live — carving outputs, routing requests · local · fail-open\033[0m\n'

sleep 0.8
printf '\n'

# turn 1 — trivia drops to the cheapest model
sleep 0.3
printf '\033[2m»\033[0m "bigest country in europe?"\n'
sleep 0.5
printf '  \033[32m▸\033[0m casual-easy   \033[2mopus\033[0m\033[1;32m→haiku\033[0m\033[2m   cplx −0.27 · 52,848-tok ctx\033[0m\n'

sleep 1.7
printf '\n'

# turn 2 — hard reasoning stays on the strongest model; its outputs get carved
sleep 0.3
printf '\033[2m»\033[0m "What is in embed.go file?"\n'
sleep 0.6
printf '  \033[34m▸\033[0m requested   \033[1;34mopus→opus\033[0m  \033[1;34mkept\033[0m\033[2m   dom computer-science 0.98\033[0m\n'

sleep 1.8
printf '\033[2m  🪓 \033[0m\033[1membed.go\033[0m\033[2m        source · \033[0m\033[32muntouched\033[0m\033[2m — code is never cut\033[0m\n'
sleep 0.5
printf '\033[2m  🪓 \033[0m\033[1mbuild.log\033[0m\033[2m       144 lines, 2 errors buried\033[0m\n'
sleep 0.4
printf '\033[31m       ERROR migrate failed: relation "users" does not exist\033[0m\n'
sleep 0.4
printf '\033[2;3m       … [118 lines omitted]\033[0m\n'
sleep 0.4
printf '\033[31m       ERROR shutdown: connection reset by peer\033[0m\n'
sleep 0.4
printf '       \033[2m1,904\033[0m \033[1;32m→ 47\033[0m\033[2m tok · \033[0m\033[1;32m−97%%\033[0m\n'
sleep 0.8
printf '\033[2m  🪓 \033[0m\033[1mtest-run.log\033[0m\033[2m     670 lines · 20,595\033[0m \033[1;32m→ 472\033[0m\033[2m tok · \033[0m\033[1;32m−98%%\033[0m\n'

sleep 1.8
printf '\n'

# turn 3 — another trivial ask drops to haiku
sleep 0.2
printf '\033[2m»\033[0m "share command to rename a file"\n'
sleep 0.5
printf '  \033[32m▸\033[0m casual-easy   \033[2mopus\033[0m\033[1;32m→haiku\033[0m\033[2m   cplx −0.45\033[0m\n'

sleep 1.2
printf '\n'

# turn 4 — mid-complexity drafting lands on sonnet
sleep 0.2
printf '\033[2m»\033[0m "draft a slack message for this content"\n'
sleep 0.5
printf '  \033[36m▸\033[0m casual-medium   \033[2mopus\033[0m\033[1;36m→sonnet\033[0m\033[2m   dom other 1.00\033[0m\n'

# hold the full stream, then one clear into the session summary
sleep 1.6
sleep 1.2
clear

# ── closer (session summary; `whittle stats` house style) ────────────────────
sleep 0.3
printf '\033[1m🪓 whittle\033[0m \033[2m· session summary\033[0m\n'
sleep 0.6
printf '\n'
printf '  \033[2mcontext\033[0m     \033[1;32m21,980\033[0m tokens carved      \033[2mlossless or marked · code untouched\033[0m\n'
sleep 0.6
printf '  \033[2mrouting\033[0m     4 requests   \033[32m●\033[0m haiku 2   \033[36m●\033[0m sonnet 1   \033[34m●\033[0m opus 1 held\n'
sleep 0.6
printf '  \033[2msavings\033[0m     ~$0.05 measured           \033[2mon billed tokens · this session\033[0m\n'
sleep 0.6
printf '\n'
printf '  \033[2mnever cuts what doesn'\''t come back · github.com/firstops-dev/whittle\033[0m\n'

# loop-pause hold, then the GIF loops
sleep 4.0
