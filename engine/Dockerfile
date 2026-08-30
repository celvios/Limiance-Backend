FROM ubuntu:24.04@sha256:33ceb71981b602c1a7443a53469e4dba065f7503eab3078a2d7a57a2ab987517 AS toolchain

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential=12.10ubuntu1 \
      ca-certificates=20260601~24.04.1 \
      cmake=3.28.3-1build7 \
      git=1:2.43.0-1ubuntu7.3 \
      libgtest-dev=1.14.0-1 \
      libzmq3-dev=4.3.5-1build2 \
      ninja-build=1.11.1-2 \
      pkgconf=1.8.1-2build1 \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

FROM toolchain AS dependencies
RUN git clone --branch v25.12.19 --depth 1 https://github.com/google/flatbuffers.git /src/flatbuffers \
 && cmake -S /src/flatbuffers -B /src/flatbuffers-build -G Ninja \
      -DCMAKE_BUILD_TYPE=Release \
      -DFLATBUFFERS_BUILD_TESTS=OFF \
      -DFLATBUFFERS_BUILD_FLATC=OFF \
      -DFLATBUFFERS_BUILD_SHAREDLIB=OFF \
 && cmake --build /src/flatbuffers-build --parallel \
 && cmake --install /src/flatbuffers-build

FROM toolchain AS build
ARG ENABLE_SANITIZERS=OFF
ARG ENABLE_COVERAGE=OFF
COPY --from=dependencies /usr/local /usr/local
COPY engine /src/engine
COPY schema/generated/cpp /src/schema/generated/cpp
COPY schema/tests /src/schema/tests

RUN cmake -S engine -B build -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo -DBUILD_TESTING=ON -DLIMIANCE_ENABLE_SANITIZERS=${ENABLE_SANITIZERS} -DLIMIANCE_ENABLE_COVERAGE=${ENABLE_COVERAGE} \
 && cmake --build build --parallel \
 && ENGINE_TEST_JOURNAL_DIR=/dev/shm ctest --test-dir build --output-on-failure

FROM ubuntu:24.04@sha256:33ceb71981b602c1a7443a53469e4dba065f7503eab3078a2d7a57a2ab987517 AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libstdc++6=14.2.0-4ubuntu2~24.04.1 \
      libzmq5=4.3.5-1build2 \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home /var/lib/limiance-engine limiance-engine \
 && install -d -o limiance-engine -g limiance-engine /var/lib/limiance-engine
COPY --from=build /src/build/matching_engine /usr/local/bin/matching_engine
USER 10001
VOLUME ["/var/lib/limiance-engine"]
ENTRYPOINT ["/usr/local/bin/matching_engine"]
