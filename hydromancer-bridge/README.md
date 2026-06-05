# Hydromancer Bridge

A small, dependency-free service a trader runs locally that subscribes to one or
more [DoubleZero Top-of-Book](../top-of-book/spec.md) multicast feeds, arbitrates
them (first received copy of each quote wins), and republishes the result over a
**local websocket that speaks the Hyperliquid / Hydromancer `bbo` schema**.

The point: a trader who already integrates Hydromancer's BBO websocket can point
their existing client at `ws://localhost:8080/ws` and consume the DoubleZero feed
**without integrating our binary wire format at all**.

```
 ┌─ feed A (multicast) ─┐
 │                      ├─► arbiter ─► convert ─► ws server ─► trader's existing
 └─ feed B (multicast) ─┘  (1st wins)  (ref→HL)   (HL bbo)      Hydromancer client
```

## The target format (Hydromancer / Hyperliquid `bbo`)

Hydromancer's BBO stream is schema-compatible with the native Hyperliquid
`WsBbo`. A client subscribes with:

```json
{"method":"subscribe","subscription":{"type":"bbo","coin":"ETH"}}
```

and receives, wrapped in a channel envelope:

```json
{
  "channel": "bbo",
  "data": {
    "coin": "BTC",
    "time": 1754450974231,
    "bbo": [
      {"px":"113377.0","sz":"7.6699","n":17},
      {"px":"113397.0","sz":"0.11543","n":3}
    ]
  }
}
```

- `coin` — symbol string
- `time` — **milliseconds** since epoch
- `bbo` — `[bid, ask]`, each a `WsLevel` (`px`, `sz` strings; `n` order count) or `null`

Sources: [Hyperliquid WS subscriptions](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/websocket/subscriptions),
[Hydromancer websocket docs](https://docs.hydromancer.xyz/hydromancer-better-hyperliquid-apis/websocket).

## How hard is the conversion? Low-to-moderate — the ref spec is a superset

Nothing is lost going DoubleZero `Quote` → Hydromancer `bbo`. The mapping:

| Hydromancer field | DoubleZero source | Transform |
|---|---|---|
| `coin` | `InstrumentDefinition.Symbol` (looked up by `Instrument ID`) | string; **requires the refdata port** since quotes carry only numeric IDs |
| `time` | `Quote.Source Timestamp` (ns) | ÷ 1,000,000 → ms |
| `bbo[0].px` / `.sz` | `Bid Price` / `Bid Quantity` | × 10^`PriceExp` / 10^`QtyExp`, exact integer→decimal string |
| `bbo[1].px` / `.sz` | `Ask Price` / `Ask Quantity` | same |
| `.n` | `Bid` / `Ask Source Count` | direct (0 if unavailable) |
| `null` level | `Update Flags` bit 2/3 (bid/ask gone) or price 0 | side omitted as `null` |

The only real engineering is:

1. **Reference-data state.** Quotes are numeric; you must consume
   `InstrumentDefinition` messages off the refdata port to resolve `coin` and the
   price/qty exponents. Implemented in [`internal/refdata`](internal/refdata).
2. **Exact decimal scaling.** Scaled `i64`/`u64` → decimal strings with no
   floating-point error. Implemented in [`internal/wire/decimal.go`](internal/wire/decimal.go).
3. **Transport inversion.** DoubleZero is a binary UDP-multicast *push*;
   Hyperliquid is a websocket *subscribe* handshake + `{channel,data}` envelope.
   The bridge emulates the Hyperliquid server side
   ([`internal/hlbbo`](internal/hlbbo)).

Everything else is a direct field copy.

## Arbitration ("take the first received quote")

The same venue update is typically published over several redundant feeds for
resilience and latency. The [arbiter](internal/arbiter/arbiter.go) forwards the
**first** copy of each distinct update per instrument and drops every later
duplicate. It keys on the venue's `Source Timestamp` (the common denominator
across heterogeneous feeds), breaking ties on BBO content so that feeds which
report a `0` timestamp still de-duplicate correctly.

## Quick start

```sh
# Terminal 1 — publish a synthetic feed (BTC, ETH, SOL) over multicast
go run ./cmd/simulator

# Terminal 2 — run the bridge (defaults match the simulator)
go run ./cmd/bridge

# Terminal 3 — connect any Hyperliquid/Hydromancer bbo client to:
#   ws://localhost:8080/ws
# subscribe: {"method":"subscribe","subscription":{"type":"bbo","coin":"BTC"}}
```

Or with the Makefile: `make test`, `make build`, `make run-sim`, `make run-bridge`.

## Configuration (bridge flags)

| Flag | Default | Meaning |
|---|---|---|
| `-feed` | `sim@239.255.0.1:5000:5001` | Feed to subscribe to, `[name@]group:mktPort:refPort`. Repeat for multiple feeds to arbitrate. |
| `-listen` | `:8080` | Websocket listen address |
| `-path` | `/ws` | Websocket path |
| `-iface` | (OS default) | Multicast interface name |

Arbitrate two redundant feeds:

```sh
go run ./cmd/bridge \
  -feed primary@239.255.0.1:5000:5001 \
  -feed backup@239.255.0.2:5000:5001
```

Beyond the native per-coin subscription, a client may subscribe with `coin:"*"`
(or `"all"`) to receive every instrument on one connection — a convenience this
bridge adds on top of the Hyperliquid protocol.

## Notes & limitations

- `coin` is taken from `InstrumentDefinition.Symbol` (falling back to `Leg1`).
  Publishers should set `Symbol` to the Hyperliquid coin name (`BTC`, `ETH`, …).
- A quote whose `InstrumentDefinition` has not yet been received is dropped; it
  resolves on the next definition cycle (recommended ≤30 s; the simulator uses 5 s).
- Only the `bbo` channel is served. Trades and L2 are out of scope.
- The websocket layer is a minimal RFC 6455 server (stdlib only, no external
  dependencies) sufficient for serving small JSON frames.
