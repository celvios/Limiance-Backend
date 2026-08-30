# Matching engine

This directory contains the isolated C++20 spot matching engine. It has no
PostgreSQL client or database credentials. The Go application remains the sole
owner of PostgreSQL and communicates with this process through ZeroMQ and the
frozen FlatBuffers trading contract.

## Build and test

```sh
docker build --target build -f engine/Dockerfile -t limiance-engine-build:local .
```

The build runs the matching, cancellation, journal recovery, corruption,
randomized replay, and Go/C++ golden-wire tests. To run with sanitizers:

```sh
docker build --target build --build-arg ENABLE_SANITIZERS=ON \
  -f engine/Dockerfile -t limiance-engine-sanitized:local .
```

The matching-loop coverage image can be produced with
`--build-arg ENABLE_COVERAGE=ON`; its build directory retains GCC coverage
data for inspection with `gcov`.

Start the engine with its durable journal volume:

```sh
docker compose -f engine/compose.yaml up --build
```

Endpoints default to `tcp://127.0.0.1:5555` for orders, `:5556` for control,
and `:5557` for published events. Each symbol is assigned one deterministic
worker thread and one checksummed append-only journal. A journal is recovered
and its generated outputs are verified before that symbol accepts new work.

Never remove or edit a journal to resolve a sequence problem. Stop the engine,
copy the affected journal for investigation, restore a known-good journal or
snapshot, and then use the replay control command to refill downstream gaps.
