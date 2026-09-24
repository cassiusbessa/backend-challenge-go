FROM golang:1.26.4 AS build
WORKDIR /src
RUN go version | grep -q 'go version go1.26.4 '
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/wager ./cmd/wager

FROM alpine:3.22
RUN adduser -D -u 65532 nonroot
USER nonroot
COPY --from=build /out/wager /wager
EXPOSE 8090
ENTRYPOINT ["/wager"]
