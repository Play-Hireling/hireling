# Models and providers

## Today

| Profile `model` | Sent to | Typical use |
| --- | --- | --- |
| `deepseek-v4-flash` | `https://api.deepseek.com/v1`, model `deepseek-v4-flash` | Every worker hireling |
| `deepseek-v4-pro` | same, `deepseek-v4-pro` | The planner (Innkeeper) |

The runtime writes Hermes' `config.yaml` with provider `custom`, the model base URL and `key_env: DEEPSEEK_API_KEY`. The base URL defaults to `https://api.deepseek.com/v1`. Set `HIRELING_MODEL_BASE_URL` to another OpenAI-compatible endpoint (a stub in a test, for example). The key is read by Hermes from the environment and never written to disk.

### Fallback to OpenRouter

Set `OPENROUTER_API_KEY` and every hireling gets `fallback_providers: [openrouter, deepseek/deepseek-v4-<flash|pro>]`. Hermes switches **for that turn only** when DeepSeek answers 429/5xx (after its own retries) or 401/403/404. The next turn starts on DeepSeek again. Override per hireling:

```yaml
fallback:
  model: deepseek/deepseek-v4-pro   # OpenRouter slug
  disabled: false
```

Fallback turns are recorded in usage but not priced.

### Cost accounting

After each turn the runtime reads Hermes' session store and prices it at DeepSeek's public rates (cache hit, cache miss, output; off-peak at half). Totals per hireling per UTC day: `GET /agents/<id>/budget`. Inside a container: `hireling usage 1h`.

**Off-peak routines (D9).** DeepSeek's peak hours are 01:00–04:00 and 06:00–10:00 UTC, Monday to Friday. During them, a hireling on DeepSeek holds its routines (P3 work) until the window ends, so they run at the off-peak rate, which is half price. P0–P2 work runs at once. A hireling on another provider never waits.

### Turn speed

Turns go through the Hermes API server already running in each container (warm), not a fresh `hermes chat` process: a trivial reply takes ~3 s instead of ~20 s. Conversation turns (no task, not the planner) run with low reasoning effort (`runtime.chat_reasoning: low`); set `default` to keep the model's own setting. Force the old path with `HIRELING_TURNS=cli`.

## Next

Any OpenAI-compatible provider per hireling (Anthropic, OpenAI, local models through vLLM or Ollama) is on the [roadmap](../roadmap.md). The plumbing is already provider-agnostic — Hermes takes any OpenAI-compatible base URL — what is missing is the profile field, egress base list and pricing table per provider. Pull requests welcome.
