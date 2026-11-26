FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/publisher ./cmd/publisher

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /out/publisher /app/publisher
COPY config/publisher.example.cfg /app/config/publisher.cfg
EXPOSE 80
ENTRYPOINT ["/app/publisher"]
CMD ["--config", "/app/config/publisher.cfg"]
