FROM debian:13-slim

ARG RUNNER_VERSION=2.337.0
ARG RUNNER_SHA256=70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bzip2 \
      ca-certificates \
      curl \
      g++ \
      gcc \
      git \
      gnupg \
      jq \
      libc6-dev \
      lsb-release \
      make \
      openssh-client \
      pkg-config \
      sudo \
      unzip \
      xz-utils \
      zip \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /home/runner

RUN curl -fsSL -o runner.tar.gz \
      "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" \
 && echo "${RUNNER_SHA256}  runner.tar.gz" | sha256sum -c - \
 && tar xzf runner.tar.gz \
 && rm runner.tar.gz \
 && rm -rf externals/node20 externals/node20_alpine externals/node24_alpine \
 && ./bin/installdependencies.sh \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir _work

COPY --chmod=0755 entrypoint.sh /usr/local/bin/entrypoint.sh
