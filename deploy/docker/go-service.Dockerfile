ARG GO_VERSION=1.26.8
FROM golang:${GO_VERSION}-alpine AS build
ARG SERVICE
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test -n "${SERVICE}" && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/service "./cmd/${SERVICE}"

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget && addgroup -S fleetrelay && adduser -S -G fleetrelay fleetrelay \
    && mkdir -p /var/lib/fleetrelay && chown fleetrelay:fleetrelay /var/lib/fleetrelay
COPY --from=build /out/service /usr/local/bin/fleetrelay
USER fleetrelay
ENTRYPOINT ["/usr/local/bin/fleetrelay"]
