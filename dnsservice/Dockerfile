FROM golang:1.20 as builder

ENV APP_HOME /go/src/dnsservice

WORKDIR "$APP_HOME"

COPY . .

RUN go mod download
RUN go mod verify
RUN go build -o dnsservice

FROM golang:1.20

ENV APP_HOME /go/src/dnsservice
RUN mkdir -p "$APP_HOME"
WORKDIR "$APP_HOME"

COPY --from=builder "$APP_HOME"/dnsservice $APP_HOME

CMD ["./dnsservice"]