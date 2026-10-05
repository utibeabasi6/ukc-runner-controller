FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/utibeabasi6/ukc-runner-controller/internal/cmd.version=${VERSION}" \
      -o /out/ukc-runner-controller ./cmd/ukc-runner-controller

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ukc-runner-controller /usr/bin/ukc-runner-controller

ENTRYPOINT ["/usr/bin/ukc-runner-controller"]
