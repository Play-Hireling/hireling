# OS5 — the default door, before and after

Proof for issue #36: `http://127.0.0.1:8080/` opens the tavern when a client bundle is mounted, and stays the owner panel (with build steps) when it is not. `README.md` took the same route and command.

## What ran

The real `tavern-core` handler served the real `dist-selfhost` build from a checkout of `Play-Hireling/playhireling` (`npm run build:selfhost`) through `TAVERN_CLIENT_DIR`, with the same `Security.go` policies as the binary. The API was a Playwright fixture (`GET /api/status`, 401 without `Authorization`); no production host and no DeepSeek call. Chromium at 1440×900 and 390×844, `deviceScaleFactor: 1`, touch emulation at 390. The game accepted the owner token, reached the playing state, and the player walked with `ArrowRight` at both sizes.

| Case | `/` | `/panel/` | `/tavern/` |
| --- | --- | --- | --- |
| Bundle mounted | 307 → `/tavern/`, tavern playable | owner panel, link to the tavern | 200, tavern |
| No bundle (empty volume) | owner panel + build notice | owner panel + build notice | 404 |

In both cases: 0 console errors, 0 page errors, 0 CSP violations, 0 requests outside the tavern host. The panel's token input and `GET /api/status` were exercised.

## Bundle gate

```
./scripts/selfhost-bundle-gate.sh /path/to/playhireling/dist-selfhost --expect-audio
./scripts/selfhost-bundle-gate.sh /path/to/playhireling/dist-selfhost
./scripts/selfhost-bundle-gate_test.sh
```

All three exit 0. The bundle is local; nothing was pushed and no image was published.

## Prints

- `os5/with-client-1440x900.jpg`, `os5/with-client-390x844.jpg` — `/` after the 307, tavern playable.
- `os5/panel-with-client-1440x900.jpg`, `os5/panel-with-client-390x844.jpg` — `/panel/` with the tavern link.
- `os5/without-client-1440x900.jpg`, `os5/without-client-390x844.jpg` — `/` with the build notice, no bundle.

## Automated checks

`go test ./...` in `services/tavern-core` covers the routing and the panel notice, and the Playwright checks (`panel_console_test.go`, `tavern_play_test.go`) cover both HTML pages. `TestAutonomyFirstAsks` fails on the base commit as well (unrelated to this change) and is skipped in that run.
