FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /ipc-bench ./

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends strace && rm -rf /var/lib/apt/lists/*
COPY --from=build /ipc-bench /ipc-bench
ENTRYPOINT ["/ipc-bench"]
