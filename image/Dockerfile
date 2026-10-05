FROM debian:13-slim

ARG RUNNER_VERSION=2.337.0
ARG RUNNER_SHA256=70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613
ARG GO_VERSION=1.26.4
ARG GO_SHA256=1153d3d50e0ac764b447adfe05c2bcf08e889d42a02e0fe0259bd47f6733ad7f

ENV DEBIAN_FRONTEND=noninteractive
# actions/setup-* look for preinstalled tools here before they download them.
ENV RUNNER_TOOL_CACHE=/opt/hostedtoolcache

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

RUN groupadd --gid 1001 runner \
 && useradd --uid 1001 --gid runner --create-home --shell /bin/bash runner \
 && echo "runner ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/runner \
 && chmod 0440 /etc/sudoers.d/runner

WORKDIR /home/runner

RUN curl -fsSL -o runner.tar.gz \
      "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" \
 && echo "${RUNNER_SHA256}  runner.tar.gz" | sha256sum -c - \
 && tar xzf runner.tar.gz \
 && rm runner.tar.gz \
 && rm -rf externals/node20 externals/node20_alpine externals/node24_alpine \
 && ./bin/installdependencies.sh \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir _work \
 && chown -R runner:runner /home/runner

RUN mkdir -p "${RUNNER_TOOL_CACHE}/go/${GO_VERSION}" \
 && curl -fsSL -o go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
 && echo "${GO_SHA256}  go.tar.gz" | sha256sum -c - \
 && tar xzf go.tar.gz -C "${RUNNER_TOOL_CACHE}/go/${GO_VERSION}" \
 && mv "${RUNNER_TOOL_CACHE}/go/${GO_VERSION}/go" "${RUNNER_TOOL_CACHE}/go/${GO_VERSION}/x64" \
 && touch "${RUNNER_TOOL_CACHE}/go/${GO_VERSION}/x64.complete" \
 && rm go.tar.gz \
 && chown -R runner:runner "${RUNNER_TOOL_CACHE}"

COPY --chmod=0755 entrypoint.sh /usr/local/bin/entrypoint.sh
