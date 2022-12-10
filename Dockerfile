# syntax=docker/dockerfile:1
FROM golang:1.19 AS builder

WORKDIR /src

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o rarejobctl ./cmd/rarejobctl/main.go


# Selenium webdriver
FROM selenium/standalone-firefox-debug:latest
COPY --from=builder /src/rarejobctl /usr/bin/
CMD ["rarejobctl"]

