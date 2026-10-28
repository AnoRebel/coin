# Build the canonical Go node.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /arned ./cmd/arned

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /arned /usr/local/bin/arned
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/arned"]
CMD ["devnet", "--listen", "0.0.0.0:8080", "--data-dir", "/data"]
