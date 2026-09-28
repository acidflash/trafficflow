FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /trafficflow .

FROM alpine:3.21
RUN adduser -D -u 10001 app && mkdir /data && chown app:app /data
USER app
COPY --from=api /trafficflow /trafficflow
EXPOSE 8080
CMD ["/trafficflow"]
