# Third-party notices for the self-host client

This is the notice for the files in the `hireling-client` image. It is not a legal opinion. The image license is [LICENSE-CLIENT.md](LICENSE-CLIENT.md). Provenance files are not in this repository and not in the image. The public record is this file and [NOTICE](NOTICE).

## Phaser

The game client is built with Phaser 4.2.1 (MIT License, as published with that package).

## Typefaces

The client image ships the typefaces the game build copies. Those files keep the license embedded in them. This notice does not relicense them.

## Sound

The eight files below are the whole audio allowlist. They travel only inside the game. Extracting a file, sublicensing it on its own, or using it to train an AI model is not allowed. Nothing here is a permissive license over the mp3.

### Six effects

Files:

- `assets/audio/sfx/ui_click.mp3`
- `assets/audio/sfx/reward_coins.mp3`
- `assets/audio/sfx/notify_bell.mp3`
- `assets/audio/sfx/parchment.mp3`
- `assets/audio/sfx/broom_sweep.mp3`
- `assets/audio/sfx/amb_fireplace.mp3`

ElevenLabs Sound Effects, plano Creator, 19/10–19/11/2026. C2PA in the files: Eleven Labs Inc., 04/11/2026, 12:19 (America/Sao_Paulo). Credit: elevenlabs.io.

### Door

Files:

- `assets/audio/sfx/door_open.mp3`
- `assets/audio/sfx/door_close.mp3`

ElevenLabs Sound Effects, plano Creator, 10/11/2026. Credit: elevenlabs.io.

Size and duration match the generation report: `door_open.mp3` is 19687 bytes and 1.201633 s; `door_close.mp3` is 8820 bytes and 0.522449 s. SHA-256 of the files in the game repository: `bbe2a245df66883d8620fc872a586ebe52bebf0edf6dc9fb4879dd284a3ed9d5` and `6b86f126d974cc62209847520dacf6dfc540317d982915efa73dbaced78c3388`. The raw-to-final link was checked by those metadata, not by reproducing the ffmpeg hash. The holder named in LICENSE-CLIENT.md can still veto the door files.

The provenance directory `docs/provenance/audio/doors-2026-11-10/` is not published in this repository and is not copied into the image.

## Not in the image

No music is in any dist this image is built from. Faixas de música do jogo are out. `guild_working` and `tavern_idle` are out. `packages/assets/withheld/` is out.
