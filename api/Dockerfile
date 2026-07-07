FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/dictum-api ./cmd/dictum-api

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/dictum-api /dictum-api
EXPOSE 8080
ENTRYPOINT ["/dictum-api"]
