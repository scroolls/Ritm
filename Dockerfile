FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY dist ./dist
RUN CGO_ENABLED=0 go build -trimpath -o /pulse .
FROM alpine:3.22
RUN addgroup -S pulse && adduser -S pulse -G pulse && mkdir /data && chown pulse:pulse /data
USER pulse
COPY --from=build /pulse /pulse
ENV ADDR=0.0.0.0:8080 DATA_FILE=/data/state.json
EXPOSE 8080
ENTRYPOINT ["/pulse"]
